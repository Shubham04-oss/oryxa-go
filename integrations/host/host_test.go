package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/Shubham04-oss/oryxa-go/integrations/gosdk"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

var fakeappBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakeapp-bin")
	if err != nil {
		panic(err)
	}
	fakeappBin = filepath.Join(dir, "fakeapp")
	out, err := exec.Command("go", "build", "-o", fakeappBin, "github.com/Shubham04-oss/oryxa-go/integrations/connectors/fakeapp").CombinedOutput()
	if err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func newHost(t *testing.T) *Host {
	t.Helper()
	h, err := New(Config{MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond, StartTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func startFakeapp(t *testing.T, h *Host) {
	t.Helper()
	if err := h.Start(context.Background(), Spec{ID: "fakeapp", Path: fakeappBin}); err != nil {
		t.Fatal(err)
	}
}

func TestStartsDescribesInvokesAndSyncs(t *testing.T) {
	h := newHost(t)
	startFakeapp(t, h)

	if m, ok := h.Manifest("fakeapp"); !ok || m.ID != "fakeapp" {
		t.Fatalf("manifest %v %v", m, ok)
	}
	info, err := os.Stat(filepath.Join(h.dir, "fakeapp.sock"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v", info, err)
	}
	resp, err := h.Invoke(context.Background(), "fakeapp", &pb.InvokeRequest{Tool: "fakeapp.echo", InputJson: `{"text":"hi"}`})
	if err != nil || resp.GetOutputJson() != `{"text":"hi"}` {
		t.Fatalf("invoke %v %v", resp, err)
	}

	var upserts int
	var cursor string
	emit := func(ev *pb.SyncEvent) error {
		if ev.GetUpsert() != nil {
			upserts++
		}
		if cp := ev.GetCheckpoint(); cp != nil {
			cursor = string(cp.Cursor)
		}
		return nil
	}
	req := &pb.SyncRequest{Stream: "notes", Mode: pb.SyncMode_SYNC_MODE_FULL}
	if err := h.Sync(context.Background(), "fakeapp", req, emit); err != nil || upserts != 3 || cursor != "3" {
		t.Fatalf("full sync: %v, %d upserts, cursor %q", err, upserts, cursor)
	}
	upserts = 0
	req = &pb.SyncRequest{Stream: "notes", Mode: pb.SyncMode_SYNC_MODE_INCREMENTAL, Checkpoint: []byte(cursor)}
	if err := h.Sync(context.Background(), "fakeapp", req, emit); err != nil || upserts != 0 {
		t.Fatalf("incremental sync: %v, %d upserts", err, upserts)
	}
}

func TestRestartsACrashedConnector(t *testing.T) {
	h := newHost(t)
	startFakeapp(t, h)
	c, _ := h.get("fakeapp")
	c.mu.Lock()
	pid := c.pid
	c.mu.Unlock()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		c.mu.Lock()
		restarted := c.client != nil && c.pid != pid
		c.mu.Unlock()
		if restarted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connector was not restarted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := h.Invoke(context.Background(), "fakeapp", &pb.InvokeRequest{Tool: "fakeapp.echo", InputJson: `{"text":"back"}`}); err != nil {
		t.Fatal(err)
	}
}

func TestStartFailsForABrokenConnector(t *testing.T) {
	h := newHost(t)
	err := h.Start(context.Background(), Spec{ID: "nothere", Path: filepath.Join(h.dir, "missing")})
	if err == nil {
		t.Fatal("started a missing binary")
	}
	if _, ok := h.Manifest("nothere"); ok {
		t.Fatal("failed connector stayed registered")
	}
	// A connector whose manifest names another id is refused.
	err = h.Start(context.Background(), Spec{ID: "other", Path: fakeappBin})
	if err == nil || !strings.Contains(err.Error(), "manifest id") {
		t.Fatalf("got %v", err)
	}
}

func TestInvalidInvokeIsRejected(t *testing.T) {
	h := newHost(t)
	startFakeapp(t, h)
	for name, req := range map[string]*pb.InvokeRequest{
		"unknown tool":      {Tool: "fakeapp.nope", InputJson: `{}`},
		"input not JSON":    {Tool: "fakeapp.echo", InputJson: `nope`},
		"input off schema":  {Tool: "fakeapp.echo", InputJson: `{"text":1}`},
		"extra input field": {Tool: "fakeapp.echo", InputJson: `{"text":"a","x":1}`},
	} {
		if _, err := h.Invoke(context.Background(), "fakeapp", req); !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := h.Invoke(context.Background(), "ghost", &pb.InvokeRequest{}); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown connector: got %v", err)
	}
}

func TestInvalidSyncRequestIsRejected(t *testing.T) {
	h := newHost(t)
	startFakeapp(t, h)
	noop := func(*pb.SyncEvent) error { return nil }
	for name, req := range map[string]*pb.SyncRequest{
		"unknown stream":       {Stream: "nope", Mode: pb.SyncMode_SYNC_MODE_FULL},
		"mode not declared":    {Stream: "notes", Mode: pb.SyncMode_SYNC_MODE_WEBHOOK},
		"mode unspecified":     {Stream: "notes"},
		"full with checkpoint": {Stream: "notes", Mode: pb.SyncMode_SYNC_MODE_FULL, Checkpoint: []byte("1")},
	} {
		if err := h.Sync(context.Background(), "fakeapp", req, noop); !errors.Is(err, ErrInvalidMessage) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// rogue is a connector that sends whatever a test hands it.
type rogue struct {
	pb.UnimplementedConnectorServer
	events []*pb.SyncEvent
	invoke *pb.InvokeResponse
	hang   bool
}

func (r *rogue) Invoke(context.Context, *pb.InvokeRequest) (*pb.InvokeResponse, error) {
	return r.invoke, nil
}

func (r *rogue) Sync(_ *pb.SyncRequest, s pb.Connector_SyncServer) error {
	for _, ev := range r.events {
		if err := s.Send(ev); err != nil {
			return err
		}
	}
	if r.hang {
		<-s.Context().Done()
	}
	return nil
}

// attachRogue registers fakeapp's manifest on a host whose connector is an in-process server.
func attachRogue(t *testing.T, h *Host, r *rogue) {
	t.Helper()
	raw, err := os.ReadFile("../connectors/fakeapp/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := compileTools(m)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(h.dir, "rogue.sock")
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { gosdk.Serve(ctx, r, sock); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	// Serve chmods the socket once it listens, so mode 0600 means ready.
	for range 500 {
		if info, err := os.Stat(sock); err == nil && info.Mode().Perm() == 0o600 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := &connector{m: m, tools: tools, streams: map[string]manifest.Stream{}, client: pb.NewConnectorClient(conn)}
	for _, s := range m.Streams {
		c.streams[s.Name] = s
	}
	h.list["fakeapp"] = c
}

func upsert(id string) *pb.SyncEvent {
	return &pb.SyncEvent{Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{
		Item: &pb.Item{Id: id, Kind: "note", Title: "t"}, Acl: &pb.Acl{AnyoneInTenant: true},
	}}}
}

func checkpoint() *pb.SyncEvent {
	return &pb.SyncEvent{Event: &pb.SyncEvent_Checkpoint{Checkpoint: &pb.Checkpoint{Cursor: []byte("1")}}}
}

func TestInvalidSyncEventsAreRejected(t *testing.T) {
	big := strings.Repeat("x", maxTitle+1)
	cases := map[string]*pb.SyncEvent{
		"empty event":        {},
		"upsert no item":     {Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Acl: &pb.Acl{}}}},
		"upsert no ACL":      {Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Item: &pb.Item{Id: "1", Kind: "note"}}}},
		"path-like id":       upsert("a/../b"),
		"empty id":           upsert(""),
		"long title":         {Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Item: &pb.Item{Id: "1", Kind: "note", Title: big}, Acl: &pb.Acl{}}}},
		"bad kind":           {Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Item: &pb.Item{Id: "1", Kind: "Note"}, Acl: &pb.Acl{}}}},
		"http url":           {Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Item: &pb.Item{Id: "1", Kind: "note", Url: "http://x.example.com"}, Acl: &pb.Acl{}}}},
		"empty principal":    {Event: &pb.SyncEvent_Upsert{Upsert: &pb.Upsert{Item: &pb.Item{Id: "1", Kind: "note"}, Acl: &pb.Acl{Users: []string{""}}}}},
		"delete bad id":      {Event: &pb.SyncEvent_Delete{Delete: &pb.Delete{Id: "a b"}}},
		"set_acl no ACL":     {Event: &pb.SyncEvent_SetAcl{SetAcl: &pb.SetAcl{Id: "1"}}},
		"checkpoint too big": {Event: &pb.SyncEvent_Checkpoint{Checkpoint: &pb.Checkpoint{Cursor: make([]byte, maxCheckpoint+1)}}},
	}
	for name, ev := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHost(t)
			attachRogue(t, h, &rogue{events: []*pb.SyncEvent{ev, checkpoint()}})
			err := h.Sync(context.Background(), "fakeapp", &pb.SyncRequest{Stream: "notes", Mode: pb.SyncMode_SYNC_MODE_FULL}, func(*pb.SyncEvent) error { return nil })
			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestSyncMustEndOnACheckpoint(t *testing.T) {
	h := newHost(t)
	attachRogue(t, h, &rogue{events: []*pb.SyncEvent{upsert("1")}})
	err := h.Sync(context.Background(), "fakeapp", &pb.SyncRequest{Stream: "notes", Mode: pb.SyncMode_SYNC_MODE_FULL}, func(*pb.SyncEvent) error { return nil })
	if !errors.Is(err, ErrInvalidMessage) || !strings.Contains(err.Error(), "checkpoint") {
		t.Fatalf("got %v", err)
	}
}

func TestSilentSyncIsCutOff(t *testing.T) {
	h := newHost(t)
	h.cfg.SyncIdle = 100 * time.Millisecond
	attachRogue(t, h, &rogue{events: []*pb.SyncEvent{upsert("1"), checkpoint()}, hang: true})
	err := h.Sync(context.Background(), "fakeapp", &pb.SyncRequest{Stream: "notes", Mode: pb.SyncMode_SYNC_MODE_FULL}, func(*pb.SyncEvent) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "silent") {
		t.Fatalf("got %v", err)
	}
}

func TestEmitErrorEndsTheRun(t *testing.T) {
	h := newHost(t)
	attachRogue(t, h, &rogue{events: []*pb.SyncEvent{upsert("1"), checkpoint()}})
	boom := errors.New("store failed")
	err := h.Sync(context.Background(), "fakeapp", &pb.SyncRequest{Stream: "notes", Mode: pb.SyncMode_SYNC_MODE_FULL}, func(*pb.SyncEvent) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestInvalidInvokeResponsesAreRejected(t *testing.T) {
	out := func(s string) *pb.InvokeResponse {
		return &pb.InvokeResponse{Result: &pb.InvokeResponse_OutputJson{OutputJson: s}}
	}
	fail := func(code, msg string) *pb.InvokeResponse {
		return &pb.InvokeResponse{Result: &pb.InvokeResponse_Error{Error: &pb.ToolError{Code: code, Message: msg}}}
	}
	cases := map[string]*pb.InvokeResponse{
		"no result":         {},
		"output not JSON":   out("nope"),
		"output off schema": out(`{"text":1}`),
		"output too big":    out(`{"text":"` + strings.Repeat("x", maxOutput) + `"}`),
		"bad error code":    fail("Not Found", "x"),
		"long error":        fail("not_found", strings.Repeat("x", maxErrMessage+1)),
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHost(t)
			attachRogue(t, h, &rogue{invoke: resp})
			_, err := h.Invoke(context.Background(), "fakeapp", &pb.InvokeRequest{Tool: "fakeapp.echo", InputJson: `{"text":"a"}`})
			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("got %v", err)
			}
		})
	}
	// A well-formed tool error passes through.
	h := newHost(t)
	attachRogue(t, h, &rogue{invoke: fail("not_found", "no such note")})
	resp, err := h.Invoke(context.Background(), "fakeapp", &pb.InvokeRequest{Tool: "fakeapp.echo", InputJson: `{"text":"a"}`})
	if err != nil || resp.GetError().GetCode() != "not_found" {
		t.Fatalf("%v %v", resp, err)
	}
}

func TestConnectorGetsABareEnvironment(t *testing.T) {
	t.Setenv("INTEGRATIONS_SECRET_PROBE", "leak")
	h := newHost(t)
	startFakeapp(t, h)
	c, _ := h.get("fakeapp")
	env, err := os.ReadFile("/proc/" + strconv.Itoa(c.pid) + "/environ")
	if err != nil {
		t.Skip("no /proc:", err)
	}
	if strings.Contains(string(env), "INTEGRATIONS_SECRET_PROBE") {
		t.Fatal("host environment leaked to the connector")
	}
}
