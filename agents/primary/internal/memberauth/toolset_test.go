package memberauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

// fakeInvocationContext is a minimal, real agent.InvocationContext: embedding
// agent.ContextMock (ADK's own zero-value test double, exported for exactly
// this) supplies every method this test does not care about, and the four
// overrides below supply the two this test does: a session (so UserID and
// ReadonlyState resolve) and a context.Context to serve Deadline/Done/Err/
// Value, since ContextMock's own Done and Deadline panic.
//
// Building a Context this way, through agent.NewToolContext and
// agent.NewContext exactly as ADK's own request flow does
// (internal/llminternal/base_flow.go), is what proves WrapToolset against the
// real concrete context type a tool's Run receives in production — including
// the fact that its WithAgentContext is stubbed out. A hand-written fake
// satisfying the Context interface directly would not prove that; this does.
type fakeInvocationContext struct {
	agent.ContextMock
	base    context.Context
	session session.Session
}

func (c *fakeInvocationContext) Deadline() (time.Time, bool) { return c.base.Deadline() }
func (c *fakeInvocationContext) Done() <-chan struct{}       { return c.base.Done() }
func (c *fakeInvocationContext) Err() error                  { return c.base.Err() }
func (c *fakeInvocationContext) Value(key any) any           { return c.base.Value(key) }
func (c *fakeInvocationContext) Session() session.Session    { return c.session }

var _ agent.InvocationContext = (*fakeInvocationContext)(nil)

// newTestSession creates a real, in-memory ADK session for userID, seeded
// with state exactly as internal/adksession seeds a member's session.
func newTestSession(t *testing.T, userID string, state map[string]any) session.Session {
	t.Helper()
	sessions := session.InMemoryService()
	created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: "memberauth-test", UserID: userID, State: state})
	if err != nil {
		t.Fatal(err)
	}
	return created.Session
}

// newRealReadonlyContext and newRealToolContext build a real agent.Context
// (respectively usable as a ReadonlyContext, and as the context a tool's Run
// receives) over base, through ADK's own public constructors.
func newRealReadonlyContext(base context.Context, sess session.Session) agent.ReadonlyContext {
	return agent.NewContext(&fakeInvocationContext{base: base, session: sess})
}

func newRealToolContext(base context.Context, sess session.Session) agent.Context {
	return agent.NewToolContext(&fakeInvocationContext{base: base, session: sess}, "call-1", &session.EventActions{StateDelta: map[string]any{}}, nil)
}

// recordingToolset and recordingTool are the toolset WrapToolset wraps in
// this test: they record the context they were called with so the test can
// inspect exactly what WrapToolset attached to it, without needing any real
// MCP or HTTP machinery.
type recordingToolset struct {
	gotToolsCtx context.Context
	tool        *recordingTool
}

func (r *recordingToolset) Name() string { return "inner" }

func (r *recordingToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	r.gotToolsCtx = ctx
	return []tool.Tool{r.tool}, nil
}

type recordingTool struct {
	name      string
	gotRunCtx context.Context
}

func (r *recordingTool) Name() string        { return r.name }
func (r *recordingTool) Description() string { return "records the context it ran with" }
func (r *recordingTool) IsLongRunning() bool { return false }
func (r *recordingTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: r.name}
}

func (r *recordingTool) Run(ctx agent.Context, _ any) (map[string]any, error) {
	r.gotRunCtx = ctx
	return map[string]any{"ok": true}, nil
}

func TestWrapToolsetAttachesMemberToTools(t *testing.T) {
	inner := &recordingToolset{tool: &recordingTool{name: "demo_read"}}
	wrapped := WrapToolset(inner)

	sess := newTestSession(t, "user-1", map[string]any{"organization_id": "org-1"})
	ctx := newRealReadonlyContext(context.Background(), sess)

	if _, err := wrapped.Tools(ctx); err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	if inner.gotToolsCtx == nil {
		t.Fatal("inner toolset's Tools was never called")
	}
	member, ok := memberFromContext(inner.gotToolsCtx)
	if !ok {
		t.Fatal("the context inner.Tools received carries no member")
	}
	if member != (Member{UserID: "user-1", OrganizationID: "org-1"}) {
		t.Fatalf("member = %+v, want user-1/org-1", member)
	}
}

func TestWrapToolsetAttachesMemberToRun(t *testing.T) {
	inner := &recordingToolset{tool: &recordingTool{name: "demo_read"}}
	wrapped := WrapToolset(inner)

	sess := newTestSession(t, "user-1", map[string]any{"organization_id": "org-1"})
	readonlyCtx := newRealReadonlyContext(context.Background(), sess)
	tools, err := wrapped.Tools(readonlyCtx)
	if err != nil || len(tools) != 1 {
		t.Fatalf("Tools() = %v, %v", tools, err)
	}

	runCtx := newRealToolContext(context.Background(), sess)
	if _, err := tools[0].(interface {
		Run(agent.Context, any) (map[string]any, error)
	}).Run(runCtx, map[string]any{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if inner.tool.gotRunCtx == nil {
		t.Fatal("inner tool's Run was never called")
	}
	member, ok := memberFromContext(inner.tool.gotRunCtx)
	if !ok {
		t.Fatal("the context inner tool's Run received carries no member")
	}
	if member != (Member{UserID: "user-1", OrganizationID: "org-1"}) {
		t.Fatalf("member = %+v, want user-1/org-1", member)
	}
}

// TestWrapToolsetCarriesAnyCtxAccessTokenIntoRun proves the other half of the
// contract: a member access token already on the context a run started with
// (WithAccessToken) is still readable from inside the wrapped tool's Run,
// exactly where CredentialProvider looks for it.
func TestWrapToolsetCarriesAnyCtxAccessTokenIntoRun(t *testing.T) {
	inner := &recordingToolset{tool: &recordingTool{name: "demo_read"}}
	wrapped := WrapToolset(inner)

	sess := newTestSession(t, "user-1", map[string]any{"organization_id": "org-1"})
	base := WithAccessToken(context.Background(), "member-own-token")

	readonlyCtx := newRealReadonlyContext(base, sess)
	tools, err := wrapped.Tools(readonlyCtx)
	if err != nil || len(tools) != 1 {
		t.Fatalf("Tools() = %v, %v", tools, err)
	}

	runCtx := newRealToolContext(base, sess)
	if _, err := tools[0].(interface {
		Run(agent.Context, any) (map[string]any, error)
	}).Run(runCtx, map[string]any{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got := AccessToken(inner.tool.gotRunCtx); got != "member-own-token" {
		t.Fatalf("AccessToken(inner run ctx) = %q, want %q", got, "member-own-token")
	}
}

func TestWrapToolsetNoOrganizationIsErrNoMember(t *testing.T) {
	inner := &recordingToolset{tool: &recordingTool{name: "demo_read"}}
	wrapped := WrapToolset(inner)

	// A session with no organization_id state, such as the development UI's
	// bare "user" who belongs to no organization.
	sess := newTestSession(t, "user-1", nil)
	ctx := newRealReadonlyContext(context.Background(), sess)

	if _, err := wrapped.Tools(ctx); !errors.Is(err, ErrNoMember) {
		t.Fatalf("Tools() error = %v, want ErrNoMember", err)
	}
	if inner.gotToolsCtx != nil {
		t.Fatal("the inner toolset was called despite no member")
	}
}
