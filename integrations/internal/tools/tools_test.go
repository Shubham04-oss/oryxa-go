package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/integrations/egress"
	"github.com/Shubham04-oss/oryxa-go/integrations/host"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
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

var keys = egress.Keys{Current: "k1", Keys: map[string]secrets.Secret{"k1": secrets.Secret(strings.Repeat("k", 32))}}

type rig struct {
	*Service
	pool *pgxpool.Pool
	host *host.Host
	org  string
	// handles the fake broker saw, and how many writes reached the app.
	mu      sync.Mutex
	handles []egress.Claims
	posts   int
}

// The tests need a Postgres with the schema's migrations applied; see
// integrations/vault_integration_test.go. The rows they make are removed after.
func newRig(t *testing.T) *rig {
	t.Helper()
	url := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: url, ApplicationName: "oryxa-tools-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	r := &rig{pool: database.Pool, org: fmt.Sprintf("test_org_%d", time.Now().UnixNano())}

	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		claims, err := keys.Verify(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "), time.Now())
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.mu.Lock()
		r.handles = append(r.handles, claims)
		r.posts++
		r.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"n1"}`))
	}))
	t.Cleanup(broker.Close)

	h, err := host.New(host.Config{StartTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	if err := h.Start(ctx, host.Spec{ID: "fakeapp", Path: fakeappBin}); err != nil {
		t.Fatal(err)
	}
	r.host = h
	r.Service = New(database.Pool, h, Config{Keys: keys, BrokerOrigin: broker.URL})
	t.Cleanup(func() {
		database.Pool.Exec(context.Background(), `DELETE FROM integrations.tool_calls WHERE org_id = $1`, r.org)
		database.Pool.Exec(context.Background(), `DELETE FROM integrations.credentials WHERE connection_id IN (SELECT id FROM integrations.connections WHERE org_id = $1)`, r.org)
		database.Pool.Exec(context.Background(), `DELETE FROM integrations.connections WHERE org_id = $1`, r.org)
		database.Pool.Exec(context.Background(), `DELETE FROM integrations.org_apps WHERE org_id = $1`, r.org)
	})
	return r
}

// connection makes a personal connection of member m1 to fakeapp, with status.
func (r *rig) connection(t *testing.T, status string) string {
	t.Helper()
	ctx := context.Background()
	appID, id := "app_"+r.org, "conn_"+r.org+status
	now := time.Now().UTC()
	if _, err := r.pool.Exec(ctx, `INSERT INTO integrations.org_apps (id, org_id, app, kind, status, created_at, updated_at)
		VALUES ($1, $2, 'fakeapp', 'oauth_client', 'ok', $3, $3) ON CONFLICT DO NOTHING`, appID, r.org, now); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `INSERT INTO integrations.connections (id, org_id, member_id, level, app, org_app_id, mode, status, created_at, updated_at)
		VALUES ($1, $2, 'm1', 'personal', 'fakeapp', $3, 'byo', $4, $5, $5)`, id, r.org, appID, status, now); err != nil {
		t.Fatal(err)
	}
	return id
}

func (r *rig) req(connection, tool, input string) Request {
	return Request{Org: r.org, Member: "m1", Agent: "agent_7", Role: "member", Connection: connection, Tool: tool, Input: json.RawMessage(input)}
}

type auditEntry struct{ agent, role, tool, effect, decision, status string }

func (r *rig) audit(t *testing.T) []auditEntry {
	t.Helper()
	rows, err := r.pool.Query(context.Background(), `SELECT agent_id, agent_role, tool, effect, decision, status
		FROM integrations.tool_calls WHERE org_id = $1 ORDER BY at, id`, r.org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditEntry
	for rows.Next() {
		var e auditEntry
		if err := rows.Scan(&e.agent, &e.role, &e.tool, &e.effect, &e.decision, &e.status); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestFakeappToolRunsAndTheAuditNamesTheAgent(t *testing.T) {
	r := newRig(t)
	conn := r.connection(t, "ok")
	res, err := r.Call(context.Background(), r.req(conn, "fakeapp.echo", `{"text":"hi"}`))
	if err != nil || string(res.Output) != `{"text":"hi"}` {
		t.Fatalf("call = %v, %v", res, err)
	}
	want := auditEntry{"agent_7", "member", "fakeapp.echo", "read", "allowed", "ok"}
	if got := r.audit(t); len(got) != 1 || got[0] != want {
		t.Fatalf("audit = %+v, want %+v", got, want)
	}
}

func TestUnconfirmedWriteIsRefusedAndConfirmedRunsWithAHandle(t *testing.T) {
	r := newRig(t)
	conn := r.connection(t, "ok")
	req := r.req(conn, "fakeapp.add_note", `{"text":"x"}`)
	if _, err := r.Call(context.Background(), req); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("unconfirmed write: err = %v, want ErrConfirmation", err)
	}
	if r.posts != 0 {
		t.Fatal("the connector reached the app without confirmation")
	}
	req.Confirmed = true
	res, err := r.Call(context.Background(), req)
	if err != nil || string(res.Output) != `{"id":"n1"}` {
		t.Fatalf("confirmed write = %v, %v", res, err)
	}
	if len(r.handles) != 1 || r.handles[0].Connection != conn || r.handles[0].Agent != "agent_7" || r.handles[0].Member != "m1" {
		t.Fatalf("handle claims = %+v", r.handles)
	}
	got := r.audit(t)
	if len(got) != 2 || got[0].decision != "needs_confirmation" || got[0].status != "refused" || got[1].decision != "allowed" || got[1].status != "ok" {
		t.Fatalf("audit = %+v", got)
	}
}

func TestToolErrorIsTheAnswerNotAFailure(t *testing.T) {
	r := newRig(t)
	conn := r.connection(t, "ok")
	res, err := r.Call(context.Background(), r.req(conn, "fakeapp.echo", `{"text":""}`))
	if err != nil || res.ToolError == nil || res.ToolError.Code != "invalid_input" {
		t.Fatalf("call = %+v, %v", res, err)
	}
	if got := r.audit(t); len(got) != 1 || got[0].status != "tool_error" {
		t.Fatalf("audit = %+v", got)
	}
}

func TestWrongOrgMemberToolOrInputIsRefused(t *testing.T) {
	r := newRig(t)
	conn := r.connection(t, "ok")
	for name, mutate := range map[string]func(*Request){
		"other org":    func(q *Request) { q.Org = r.org + "x" },
		"other member": func(q *Request) { q.Member = "m2" },
		"unknown tool": func(q *Request) { q.Tool = "fakeapp.nope" },
		"another app":  func(q *Request) { q.Tool = "other.echo" },
		"unknown conn": func(q *Request) { q.Connection = "conn_nope" },
	} {
		q := r.req(conn, "fakeapp.echo", `{"text":"hi"}`)
		mutate(&q)
		if _, err := r.Call(context.Background(), q); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
	q := r.req(conn, "fakeapp.echo", `{"text":1}`)
	if _, err := r.Call(context.Background(), q); !errors.Is(err, host.ErrInvalidMessage) {
		t.Errorf("input off schema: err = %v, want ErrInvalidMessage", err)
	}
	q = r.req(conn, "fakeapp.echo", `{"text":"hi"}`)
	q.Agent = "bad agent"
	if _, err := r.Call(context.Background(), q); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad agent id: err = %v, want ErrInvalid", err)
	}
}

func TestConnectionNeedingReauthIsRefusedAndAudited(t *testing.T) {
	r := newRig(t)
	conn := r.connection(t, "needs_reauth")
	if _, err := r.Call(context.Background(), r.req(conn, "fakeapp.echo", `{"text":"hi"}`)); !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("err = %v, want ErrNeedsReauth", err)
	}
	if got := r.audit(t); len(got) != 1 || got[0].status != "refused" {
		t.Fatalf("audit = %+v", got)
	}
	conn = r.connection(t, "revoked")
	if _, err := r.Call(context.Background(), r.req(conn, "fakeapp.echo", `{"text":"hi"}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("revoked: err = %v, want ErrUnavailable", err)
	}
}

// proxyConns is the proxy's view of the rig's one connection: fakeapp's
// manifest from the running connector, and a fixed token.
type proxyConns struct{ h *host.Host }

func (c proxyConns) Manifest(context.Context, string) (*manifest.Manifest, error) {
	m, _ := c.h.Manifest("fakeapp")
	return m, nil
}
func (proxyConns) Token(context.Context, string, string) (secrets.Secret, error) {
	return "app-token", nil
}
func (proxyConns) Refresh(context.Context, string, string, secrets.Secret) (secrets.Secret, error) {
	return "", errors.New("no refresh in this test")
}

// The path the service mounts: the tool call, the connector, the real egress
// proxy as its broker, and the app, which sees the connection's token and
// never the handle.
func TestConfirmedWriteReachesTheAppThroughTheEgressProxy(t *testing.T) {
	r := newRig(t)
	conn := r.connection(t, "ok")
	var gotHost, gotAuth string
	app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotHost, gotAuth = req.Host, req.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"n9"}`))
	}))
	t.Cleanup(app.Close)
	transport := app.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, app.Listener.Addr().String())
	}
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the test app's own certificate
	broker := httptest.NewServer(&egress.Proxy{Keys: keys, Connections: proxyConns{r.host}, Client: &http.Client{Transport: transport}})
	t.Cleanup(broker.Close)
	r.Service = New(r.pool, r.host, Config{Keys: keys, BrokerOrigin: broker.URL})

	req := r.req(conn, "fakeapp.add_note", `{"text":"x"}`)
	req.Confirmed = true
	res, err := r.Call(context.Background(), req)
	if err != nil || string(res.Output) != `{"id":"n9"}` {
		t.Fatalf("call = %+v, %v", res, err)
	}
	if gotHost != "api.fakeapp.example.com" || gotAuth != "Bearer app-token" {
		t.Fatalf("app saw host %q, Authorization %q", gotHost, gotAuth)
	}
}
