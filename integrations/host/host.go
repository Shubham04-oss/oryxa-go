// Package host runs connectors: it spawns each connector binary on its own
// private Unix socket, supervises it, reads its manifest with Describe and
// checks every message that crosses the contract against that manifest.
// Connectors are never trusted: they get a bare environment, egress handles
// instead of tokens and, with Config.ConnectorUID, a user of their own, so
// they cannot read the host's memory or environment (and with it the task's
// AWS credentials) or reach its sockets.
package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Shubham04-oss/oryxa-go/integrations/gosdk"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

var (
	// ErrUnknown is returned for a connector the host was not asked to run.
	ErrUnknown = errors.New("host: no such connector")
	// ErrUnavailable is returned while a connector is down or restarting.
	ErrUnavailable = errors.New("host: connector is not running")
	// ErrInvalidMessage is returned when a connector sent, or was asked to
	// handle, a message the manifest or the contract does not allow.
	ErrInvalidMessage = errors.New("host: invalid message")
)

// Config tunes the host; the zero value is production-ready.
type Config struct {
	Logger *slog.Logger
	// StartTimeout is how long a spawned connector has to answer Describe.
	StartTimeout time.Duration
	// MinBackoff and MaxBackoff bound the wait before a crashed connector is restarted.
	MinBackoff, MaxBackoff time.Duration
	// StableAfter is how long a connector must stay up for its backoff to reset.
	StableAfter time.Duration
	// InvokeTimeout, SyncIdle and SyncLimit are the contract's 30 s call deadline,
	// 60 s silence limit and 30 minute run limit.
	InvokeTimeout, SyncIdle, SyncLimit time.Duration
	// ConnectorUID, when set, is the user and group connectors run as. The
	// host needs CAP_SETUID and CAP_SETGID for it and CAP_KILL to stop them
	// (the image's /oryxa has the three); without them no connector starts. Zero runs them as the host's
	// own user, as in local development and tests.
	ConnectorUID uint32
}

func (c *Config) defaults() {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	for _, d := range []struct {
		field *time.Duration
		value time.Duration
	}{
		{&c.StartTimeout, 10 * time.Second}, {&c.MinBackoff, time.Second}, {&c.MaxBackoff, time.Minute},
		{&c.StableAfter, time.Minute}, {&c.InvokeTimeout, 30 * time.Second}, {&c.SyncIdle, 60 * time.Second},
		{&c.SyncLimit, 30 * time.Minute},
	} {
		if *d.field == 0 {
			*d.field = d.value
		}
	}
}

// Spec says which binary serves a connector.
type Spec struct {
	ID   string // the manifest id
	Path string // the connector binary
	Args []string
}

// Host owns the connector processes.
type Host struct {
	cfg  Config
	dir  string // holds the sockets; 0700
	mu   sync.Mutex
	list map[string]*connector
	wg   sync.WaitGroup
	stop context.CancelFunc
	ctx  context.Context
}

// New creates a host whose sockets live in a fresh private directory.
func New(cfg Config) (*Host, error) {
	cfg.defaults()
	// No process of the host's user may read its memory or /proc environ.
	if err := undumpable(); err != nil {
		return nil, fmt.Errorf("host: %w", err)
	}
	// os.MkdirTemp creates the directory 0700; the path stays short for the socket limit.
	dir, err := os.MkdirTemp("", "oryxa-conn")
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	return &Host{cfg: cfg, dir: dir, list: map[string]*connector{}, ctx: ctx, stop: stop}, nil
}

// Start spawns the connector, waits for its manifest and keeps it running,
// restarting it with backoff if it exits. It fails if the first start does.
func (h *Host) Start(ctx context.Context, spec Spec) error {
	if !validConnectorID(spec.ID) {
		return fmt.Errorf("host: invalid connector id %q", spec.ID)
	}
	c := &connector{host: h, spec: spec, socket: filepath.Join(h.dir, spec.ID+".sock")}
	h.mu.Lock()
	if h.ctx.Err() != nil {
		h.mu.Unlock()
		return errors.New("host: closed")
	}
	if _, ok := h.list[spec.ID]; ok {
		h.mu.Unlock()
		return fmt.Errorf("host: connector %q is already started", spec.ID)
	}
	h.list[spec.ID] = c
	h.mu.Unlock()

	first := make(chan error, 1)
	var sctx context.Context
	sctx, c.cancel = context.WithCancel(h.ctx)
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		c.supervise(sctx, first)
	}()
	select {
	case err := <-first:
		if err != nil {
			h.remove(spec.ID)
		}
		return err
	case <-ctx.Done():
		h.remove(spec.ID)
		return ctx.Err()
	}
}

// remove stops supervising a connector that never came up.
func (h *Host) remove(id string) {
	h.mu.Lock()
	c := h.list[id]
	delete(h.list, id)
	h.mu.Unlock()
	if c != nil {
		c.halt()
	}
}

// Manifest returns the validated manifest of a started connector.
func (h *Host) Manifest(id string) (*manifest.Manifest, bool) {
	c, ok := h.get(id)
	if !ok {
		return nil, false
	}
	return c.m, true
}

func (h *Host) get(id string) (*connector, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.list[id]
	return c, ok
}

// Close stops every connector and removes the sockets.
func (h *Host) Close() error {
	h.stop()
	h.wg.Wait()
	return os.RemoveAll(h.dir)
}

// connector is one supervised process.
type connector struct {
	host   *Host
	spec   Spec
	socket string
	cancel context.CancelFunc

	m       *manifest.Manifest // the manifest from the first Describe; later ones must match
	raw     string
	tools   map[string]toolSchemas
	streams map[string]manifest.Stream

	mu     sync.Mutex
	client pb.ConnectorClient // nil while down
	pid    int
}

func (c *connector) halt() { c.cancel() }

func (c *connector) ready() (pb.ConnectorClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil, ErrUnavailable
	}
	return c.client, nil
}

func (c *connector) setClient(client pb.ConnectorClient, pid int) {
	c.mu.Lock()
	c.client, c.pid = client, pid
	c.mu.Unlock()
}

// supervise runs the process until ctx ends, restarting it with backoff.
func (c *connector) supervise(ctx context.Context, first chan<- error) {
	log := c.host.cfg.Logger.With("connector", c.spec.ID)
	backoff := c.host.cfg.MinBackoff
	for attempt := 0; ; attempt++ {
		started := time.Now()
		err := c.runOnce(ctx, func() {
			if attempt == 0 {
				first <- nil
			}
		})
		if attempt == 0 && err != nil && !c.hasManifest() {
			first <- err // never came up: report it rather than retry forever
			return
		}
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= c.host.cfg.StableAfter {
			backoff = c.host.cfg.MinBackoff
		}
		log.Warn("connector exited; restarting", "error", err, "after", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, c.host.cfg.MaxBackoff)
	}
}

func (c *connector) hasManifest() bool { return c.m != nil }

// runOnce starts the process, describes it, calls up once it is ready and
// returns when the process has exited.
func (c *connector) runOnce(ctx context.Context, up func()) error {
	listener, err := c.listen()
	if err != nil {
		return err
	}
	cmd := exec.Command(c.spec.Path, c.spec.Args...)
	// A bare environment: connectors never see the host's secrets. The one
	// variable is the inherited listener's descriptor, fd 3 (ExtraFiles[0]).
	cmd.Env = []string{gosdk.ListenerFDEnv + "=3"}
	cmd.ExtraFiles = []*os.File{listener}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if uid := c.host.cfg.ConnectorUID; uid != 0 {
		// No supplementary groups: Groups is empty, so they are cleared.
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
	}
	err = cmd.Start()
	listener.Close()
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(exited) }()
	defer func() {
		_ = cmd.Process.Kill()
		<-exited
	}()

	conn, err := grpc.NewClient("unix://"+c.socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pb.NewConnectorClient(conn)

	dctx, cancel := context.WithTimeout(ctx, c.host.cfg.StartTimeout)
	go func() { // a process that dies before it answers ends the wait
		select {
		case <-exited:
			cancel()
		case <-dctx.Done():
		}
	}()
	resp, err := client.Describe(dctx, &pb.DescribeRequest{}, grpc.WaitForReady(true))
	cancel()
	if err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	if err := c.accept(resp.GetManifestJson()); err != nil {
		return err
	}
	c.setClient(client, cmd.Process.Pid)
	up()
	select {
	case <-exited:
		c.setClient(nil, 0)
		return fmt.Errorf("exited: %v", waitErr)
	case <-ctx.Done():
		c.setClient(nil, 0)
		return ctx.Err()
	}
}

// listen binds the connector's socket in the host's private directory and
// returns it as a file for the connector to inherit. The connector never
// needs the directory, so it stays the host's alone.
func (c *connector) listen() (*os.File, error) {
	_ = os.Remove(c.socket)
	lis, err := net.ListenUnix("unix", &net.UnixAddr{Name: c.socket, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	lis.SetUnlinkOnClose(false) // the host dials the path after it hands the socket down
	defer lis.Close()
	if err := os.Chmod(c.socket, 0o600); err != nil {
		return nil, err
	}
	return lis.File()
}

// accept validates a Describe answer. The first one is registered; a restarted
// connector must describe the same manifest.
func (c *connector) accept(raw string) error {
	if c.m != nil {
		if raw != c.raw {
			return errors.New("describe: manifest changed since it was registered")
		}
		return nil
	}
	m, err := manifest.Parse([]byte(raw))
	if err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	if m.ID != c.spec.ID {
		return fmt.Errorf("describe: manifest id %q, want %q", m.ID, c.spec.ID)
	}
	tools, err := compileTools(m)
	if err != nil {
		return err
	}
	c.m, c.raw, c.tools = m, raw, tools
	c.streams = map[string]manifest.Stream{}
	for _, s := range m.Streams {
		c.streams[s.Name] = s
	}
	return nil
}
