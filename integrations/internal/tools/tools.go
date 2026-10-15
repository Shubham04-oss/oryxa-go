// Package tools runs one tool of a connector for a member's agent: it checks
// what the tool's effect needs, mints an egress handle, invokes the connector
// through the host, projects the output to the tool's output_schema and keeps
// an audit row (design §12.3, steps 3 to 5).
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/integrations/egress"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

// MaxOutput is the most an agent gets back from one call.
const MaxOutput = 16 << 10

// handleTTL covers the connector's call deadline with room to spare.
const handleTTL = 2 * time.Minute

var (
	// ErrInvalid means the request is malformed; the error says which field.
	ErrInvalid = errors.New("tools: invalid request")
	// ErrNotFound means the connection (in this org, for this member) or the
	// tool does not exist. The two look the same on purpose.
	ErrNotFound = errors.New("tools: no such connection or tool")
	// ErrConfirmation means a tool that changes something was called without
	// confirmed: true. Nothing ran.
	ErrConfirmation = errors.New("tools: this tool changes something and needs confirmation")
	// ErrNeedsReauth means the member must connect the app again.
	ErrNeedsReauth = errors.New("tools: the connection needs to be signed in again")
	// ErrUnavailable means the connection can't be used now (error, degraded or revoked).
	ErrUnavailable = errors.New("tools: the connection is not usable")
	// ErrOutputTooLarge means the projected output is over MaxOutput.
	ErrOutputTooLarge = errors.New("tools: the tool's output is too large")
)

// Connectors is the host's side: the manifest of a running connector, and a
// call to it. *host.Host is one.
type Connectors interface {
	Manifest(id string) (*manifest.Manifest, bool)
	Invoke(ctx context.Context, id string, req *pb.InvokeRequest) (*pb.InvokeResponse, error)
}

// Config wires the service.
type Config struct {
	// Keys mints the egress handle the connector calls the app with.
	Keys egress.Keys
	// BrokerOrigin is where the egress proxy listens, as the connector sees it.
	BrokerOrigin string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Service runs tools.
type Service struct {
	pool       *pgxpool.Pool
	connectors Connectors
	config     Config
}

// New returns the service.
func New(pool *pgxpool.Pool, connectors Connectors, config Config) *Service {
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Service{pool: pool, connectors: connectors, config: config}
}

// Request is one tool call. The caller (apps) has already authenticated the
// member and agent; Confirmed says the member agreed to a change.
type Request struct {
	Org        string
	Member     string
	Agent      string
	Role       string
	Connection string
	Tool       string
	Input      json.RawMessage
	Confirmed  bool
}

// Result is the tool's answer: Output, or the connector's own ToolError.
type Result struct {
	Output    json.RawMessage
	ToolError *ToolError
}

// ToolError is a failure the app or connector reported, safe to show.
type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var idRE = regexp.MustCompile(`^[A-Za-z0-9._:@+=-]{1,128}$`)

func (r Request) check() error {
	for _, f := range []struct{ name, value string }{
		{"org", r.Org}, {"member", r.Member}, {"agent", r.Agent}, {"role", r.Role}, {"connection", r.Connection},
	} {
		if !idRE.MatchString(f.value) {
			return fmt.Errorf("%w: %s must be 1 to 128 of letters, digits and . _ : @ + = -", ErrInvalid, f.name)
		}
	}
	if r.Tool == "" || len(r.Tool) > 128 {
		return fmt.Errorf("%w: tool is required", ErrInvalid)
	}
	if len(r.Input) == 0 {
		return fmt.Errorf("%w: input is required", ErrInvalid)
	}
	return nil
}

// Call runs the tool. Every refusal after the connection and tool are known
// is audited as well as every run.
func (s *Service) Call(ctx context.Context, req Request) (*Result, error) {
	if err := req.check(); err != nil {
		return nil, err
	}
	conn, err := s.connection(ctx, req)
	if err != nil {
		return nil, err
	}
	m, ok := s.connectors.Manifest(conn.app)
	if !ok || !strings.HasPrefix(req.Tool, conn.app+".") {
		return nil, ErrNotFound
	}
	tool, ok := toolOf(m, req.Tool)
	if !ok {
		return nil, ErrNotFound
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	call := auditRow{id: id, req: req, effect: tool.Effect, at: s.config.Now().UTC().Truncate(time.Microsecond)}
	switch {
	case conn.status == "needs_reauth":
		return nil, s.refuse(ctx, call, ErrNeedsReauth)
	case conn.status != "ok":
		return nil, s.refuse(ctx, call, ErrUnavailable)
	case tool.Effect != "read" && !req.Confirmed:
		return nil, s.refuse(ctx, call, ErrConfirmation)
	}
	if err := s.record(ctx, call, "allowed", "started"); err != nil {
		return nil, fmt.Errorf("tools: audit: %w", err) // fail closed: an unrecorded call doesn't run
	}

	started := time.Now()
	result, err := s.invoke(ctx, req, m, tool)
	status := "ok"
	switch {
	case err != nil:
		status = "failed"
	case result.ToolError != nil:
		status = "tool_error"
	}
	// ctx may be what ended the call; the row is still owed its outcome.
	done, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = s.pool.Exec(done, `UPDATE integrations.tool_calls SET status = $2, duration_ms = $3 WHERE id = $1`,
		id, status, time.Since(started).Milliseconds())
	return result, err
}

type connection struct{ app, status string }

// connection reads the connection if it is in the org and, when personal, the
// member's own. An org-level connection is any member's of the org.
func (s *Service) connection(ctx context.Context, req Request) (connection, error) {
	var c connection
	err := s.pool.QueryRow(ctx, `SELECT app, status FROM integrations.connections
		WHERE id = $1 AND org_id = $2 AND (level = 'org' OR member_id = $3)`,
		req.Connection, req.Org, req.Member).Scan(&c.app, &c.status)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func toolOf(m *manifest.Manifest, name string) (manifest.Tool, bool) {
	i := slices.IndexFunc(m.Tools, func(t manifest.Tool) bool { return t.Name == name })
	if i < 0 {
		return manifest.Tool{}, false
	}
	return m.Tools[i], true
}

// tokenKind is the token the tool's calls to the app use.
func tokenKind(m *manifest.Manifest, t manifest.Tool) string {
	switch {
	case t.TokenKind != "":
		return t.TokenKind
	case m.Auth.OAuth2 != nil && len(m.Auth.OAuth2.TokenKinds) > 0:
		return m.Auth.OAuth2.TokenKinds[0].Name
	}
	return "api_key"
}

func (s *Service) invoke(ctx context.Context, req Request, m *manifest.Manifest, tool manifest.Tool) (*Result, error) {
	now := s.config.Now()
	kind := tokenKind(m, tool)
	handle, err := s.config.Keys.Mint(egress.Claims{
		Connection: req.Connection, TokenKind: kind, Member: req.Member, Agent: req.Agent, Expires: now.Add(handleTTL),
	}, now)
	if err != nil {
		return nil, fmt.Errorf("tools: mint a handle: %w", err)
	}
	// ponytail: connectors that name no token kind (fakeapp, the gate) read the handle under
	// "default"; the contract has no other name for the connection's only token yet.
	handles := map[string]string{kind: handle, "default": handle}
	resp, err := s.connectors.Invoke(ctx, m.ID, &pb.InvokeRequest{
		Connection: &pb.ConnectionContext{ConnectionId: req.Connection},
		Tool:       req.Tool,
		InputJson:  string(req.Input),
		Egress:     &pb.Egress{BrokerOrigin: s.config.BrokerOrigin, Handles: handles},
		Acting:     &pb.ActingUser{UserId: req.Member},
	})
	if err != nil {
		return nil, err
	}
	if e := resp.GetError(); e != nil {
		return &Result{ToolError: &ToolError{Code: e.GetCode(), Message: e.GetMessage()}}, nil
	}
	out, err := project(tool.OutputSchema, resp.GetOutputJson())
	if err != nil {
		return nil, err
	}
	return &Result{Output: out}, nil
}
