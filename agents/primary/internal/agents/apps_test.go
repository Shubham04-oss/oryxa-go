package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

// --- a fake apps service: its stateless MCP endpoint ------------------------

// appsToolNames are the four tools the real apps MCP contract always serves,
// named exactly as it names them.
var appsToolNames = []string{"apps_connections", "apps_search_tools", "apps_run_read_tool", "apps_run_write_tool"}

// appsRecordedRequest is one HTTP request the fake apps service answered,
// kept so a test can inspect exactly what reached the wire.
type appsRecordedRequest struct {
	path   string
	header http.Header
	// method is the JSON-RPC method a request carried, such as tools/list or
	// tools/call.
	method string
}

// appsHeaderRecorder wraps the fake service's handler to capture every
// request's path, headers and JSON-RPC method before the real handler runs.
type appsHeaderRecorder struct {
	mu       sync.Mutex
	requests []appsRecordedRequest
}

func (h *appsHeaderRecorder) middleware(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var message struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &message)
		h.mu.Lock()
		h.requests = append(h.requests, appsRecordedRequest{path: r.URL.Path, header: r.Header.Clone(), method: message.Method})
		h.mu.Unlock()
		inner.ServeHTTP(w, r)
	})
}

func (h *appsHeaderRecorder) all() []appsRecordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]appsRecordedRequest{}, h.requests...)
}

func (h *appsHeaderRecorder) count(method string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, req := range h.requests {
		if req.method == method {
			count++
		}
	}
	return count
}

// fakeAppsResult is what every fake apps tool answers, regardless of its
// arguments: enough for a model to summarise into a final answer.
func fakeAppsResult(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"action_id":"a1","tool":"demo","successful":true,"data":{}}`}}}, nil
}

// newFakeApps starts an httptest server serving a stateless MCP endpoint at
// POST /v1/mcp/apps, built with the same go-sdk the real apps service uses,
// exactly as the archived integrations test built its fake. It serves the
// four real apps tools plus any extraTools a test wants, standing in for a
// tool the apps service adds later that this toolbox does not yet know about.
func newFakeApps(t *testing.T, extraTools ...string) (string, *http.Client, *appsHeaderRecorder) {
	t.Helper()
	names := append(append([]string{}, appsToolNames...), extraTools...)

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		server := mcp.NewServer(&mcp.Implementation{Name: "fake-apps", Version: "1"}, nil)
		for _, name := range names {
			server.AddTool(&mcp.Tool{
				Name:        name,
				Description: name,
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			}, fakeAppsResult)
		}
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true})

	mux := http.NewServeMux()
	mux.Handle("POST /v1/mcp/apps", mcpHandler)

	recorder := &appsHeaderRecorder{}
	server := httptest.NewServer(recorder.middleware(mux))
	t.Cleanup(server.Close)
	return server.URL, server.Client(), recorder
}

// assertAppsReachedWith fails the test unless at least one recorded request
// reached /v1/mcp/apps and every recorded request carries exactly the given
// Authorization and X-Oryxa-Organization headers.
func assertAppsReachedWith(t *testing.T, recorder *appsHeaderRecorder, authorization, organization string) {
	t.Helper()
	requests := recorder.all()
	if len(requests) == 0 {
		t.Fatal("no request reached the fake apps MCP endpoint")
	}
	for _, req := range requests {
		if req.path != "/v1/mcp/apps" {
			t.Errorf("request path = %q, want /v1/mcp/apps", req.path)
		}
		if req.header.Get("Authorization") != authorization {
			t.Errorf("request to %s: Authorization = %q, want %q", req.path, req.header.Get("Authorization"), authorization)
		}
		if req.header.Get("X-Oryxa-Organization") != organization {
			t.Errorf("request to %s: X-Oryxa-Organization = %q, want %q", req.path, req.header.Get("X-Oryxa-Organization"), organization)
		}
	}
}

// fakeAppsTokens is a memberauth.TokenSource a test can script and inspect.
type fakeAppsTokens struct {
	mu    sync.Mutex
	token string
	calls []string
}

func (f *fakeAppsTokens) AgentToken(_ context.Context, userID string, _ memberauth.Agent) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, userID)
	return f.token, time.Now().Add(time.Hour), nil
}

func (f *fakeAppsTokens) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeAppsTokens) firstCall() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return ""
	}
	return f.calls[0]
}

// --- structural tests: what AppsToolbox builds ------------------------------

func TestAppsToolboxRequiresAURL(t *testing.T) {
	if _, err := AppsToolbox(AppsConfig{}); err == nil {
		t.Fatal("AppsToolbox() error = nil with no URL")
	}
}

func TestAppsToolboxIsNamedAppsAndSatisfiesTheNamingRule(t *testing.T) {
	url, httpClient, _ := newFakeApps(t)
	box, err := AppsToolbox(AppsConfig{URL: url, HTTP: httpClient, Tokens: &fakeAppsTokens{token: "t"}})
	if err != nil {
		t.Fatalf("AppsToolbox() error = %v", err)
	}
	if box.Name != "apps" {
		t.Errorf("Name = %q, want apps", box.Name)
	}
	if box.Description == "" {
		t.Error("Description is empty")
	}
	if box.Instructions == "" {
		t.Error("Instructions is empty")
	}
	if box.Toolset == nil {
		t.Fatal("Toolset is nil")
	}
	// newToolCatalog is what Root feeds every toolbox through; a toolbox that
	// fails it can never be loaded. Its per-tool naming check (every tool
	// prefixed apps_) runs lazily in load_tools, exercised below by a full
	// turn actually loading "apps".
	if _, err := newToolCatalog([]Toolbox{box}); err != nil {
		t.Fatalf("newToolCatalog() error = %v", err)
	}
}

// --- a tool call inside a real ADK invocation --------------------------------

// readsAppsLLM has Krude load the "apps" toolbox and call its
// apps_connections read, exactly what both token-path tests below need.
func readsAppsLLM() model.LLM {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "apps_connections") != "":
			return genai.NewContentFromText("Read done.", genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "call_read", Name: "apps_connections", Args: map[string]any{}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "call_load", Name: "load_tools", Args: map[string]any{"names": []any{"apps"}}})
		}
	})
}

func TestAppsToolboxReachesTheFakeServerWithTheMembersOwnToken(t *testing.T) {
	url, httpClient, recorder := newFakeApps(t)
	tokens := &fakeAppsTokens{token: "should-not-be-minted"}
	box, err := AppsToolbox(AppsConfig{URL: url, HTTP: httpClient, Tokens: tokens})
	if err != nil {
		t.Fatalf("AppsToolbox() error = %v", err)
	}

	h := newHarness(t, readsAppsLLM(), box)
	ctx := memberauth.WithAccessToken(context.Background(), "member-own-token")
	answer := h.turnWith(ctx, "user-1", map[string]any{OrganizationIDKey: "org-1"}, "What apps have I connected?")

	if answer != "Read done." {
		t.Fatalf("answer = %q, want %q", answer, "Read done.")
	}
	assertAppsReachedWith(t, recorder, "Bearer member-own-token", "org-1")
	if tokens.callCount() != 0 {
		t.Fatalf("the agent token source was called %d times, want 0: a member's own token must never mint one", tokens.callCount())
	}
}

func TestAppsToolboxReachesTheFakeServerWithATokenFromTheSource(t *testing.T) {
	url, httpClient, recorder := newFakeApps(t)
	tokens := &fakeAppsTokens{token: "agent-token-abc"}
	box, err := AppsToolbox(AppsConfig{URL: url, HTTP: httpClient, Tokens: tokens})
	if err != nil {
		t.Fatalf("AppsToolbox() error = %v", err)
	}

	h := newHarness(t, readsAppsLLM(), box)
	answer := h.turn("user-1", map[string]any{OrganizationIDKey: "org-1"}, "What apps have I connected?")

	if answer != "Read done." {
		t.Fatalf("answer = %q, want %q", answer, "Read done.")
	}
	assertAppsReachedWith(t, recorder, "Bearer agent-token-abc", "org-1")
	if tokens.callCount() == 0 {
		t.Fatal("the agent token source was never called")
	}
	if tokens.firstCall() != "user-1" {
		t.Fatalf("token source was asked for %q, want user-1", tokens.firstCall())
	}
}

func TestAppsToolboxListsToolsOnceAcrossMembers(t *testing.T) {
	url, httpClient, recorder := newFakeApps(t)
	box, err := AppsToolbox(AppsConfig{URL: url, HTTP: httpClient, Tokens: &fakeAppsTokens{token: "agent-token"}})
	if err != nil {
		t.Fatalf("AppsToolbox() error = %v", err)
	}
	h := newHarness(t, readsAppsLLM(), box)

	if answer := h.turn("user-1", map[string]any{OrganizationIDKey: "org-1"}, "What apps have I connected?"); answer != "Read done." {
		t.Fatalf("first member's answer = %q", answer)
	}
	if answer := h.turn("user-2", map[string]any{OrganizationIDKey: "org-2"}, "What apps have I connected?"); answer != "Read done." {
		t.Fatalf("second member's answer = %q", answer)
	}

	// The apps service serves every member the same tools: they are listed
	// once, as whichever member's turn asks first.
	if lists := recorder.count("tools/list"); lists != 1 {
		t.Fatalf("tools were listed %d times, want once", lists)
	}
	if calls := recorder.count("tools/call"); calls != 2 {
		t.Fatalf("tools were called %d times, want 2 (one read per member)", calls)
	}
}

// --- confirmation gating ------------------------------------------------

// fakeInvocationContext is a minimal, real agent.InvocationContext, built the
// same way internal/memberauth's own tests build one: agent.ContextMock (a
// public ADK test double) supplies everything this test does not touch, and
// the overrides below supply a session and a context.Context, since
// ContextMock's own Done and Deadline panic. Going through
// agent.NewToolContext, as ADK's real tool-call flow does, gives Run the same
// concrete context type production uses.
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

func findAppsTool(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, candidate := range tools {
		if candidate.Name() == name {
			return candidate
		}
	}
	t.Fatalf("tool %q was not offered; got %v", name, tools)
	return nil
}

func TestAppsToolboxWritesAndUnknownToolsRequireConfirmationReadsDoNot(t *testing.T) {
	// apps_future_tool stands in for a tool the apps service adds after this
	// toolbox was built, which this toolbox does not name as a read.
	url, httpClient, recorder := newFakeApps(t, "apps_future_tool")
	box, err := AppsToolbox(AppsConfig{URL: url, HTTP: httpClient, Tokens: &fakeAppsTokens{token: "agent-token"}})
	if err != nil {
		t.Fatalf("AppsToolbox() error = %v", err)
	}

	sessions := session.InMemoryService()
	created, err := sessions.Create(context.Background(), &session.CreateRequest{
		AppName: "test", UserID: "user-1", State: map[string]any{OrganizationIDKey: "org-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readonlyCtx := agent.NewContext(&fakeInvocationContext{base: context.Background(), session: created.Session})
	tools, err := box.Toolset.Tools(readonlyCtx)
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	// Tools() itself already made a protocol-level request (tools/list) to
	// build this list; only a request made after this point can be the tool
	// call the assertions below care about.
	beforeCalls := recorder.count("tools/call")

	runnable := func(name string) interface {
		Run(agent.Context, any) (map[string]any, error)
	} {
		found := findAppsTool(t, tools, name)
		runner, ok := found.(interface {
			Run(agent.Context, any) (map[string]any, error)
		})
		if !ok {
			t.Fatalf("tool %q cannot be run", name)
		}
		return runner
	}
	toolCtx := func() agent.Context {
		return agent.NewToolContext(&fakeInvocationContext{base: context.Background(), session: created.Session}, "call-1", &session.EventActions{StateDelta: map[string]any{}}, nil)
	}

	for _, name := range []string{"apps_run_write_tool", "apps_future_tool"} {
		if _, err := runnable(name).Run(toolCtx(), map[string]any{}); !errors.Is(err, tool.ErrConfirmationRequired) {
			t.Fatalf("%s Run() error = %v, want ErrConfirmationRequired", name, err)
		}
	}
	if got := recorder.count("tools/call"); got != beforeCalls {
		t.Fatalf("a call awaiting confirmation reached the server: %d requests, want %d (no new one)", got, beforeCalls)
	}

	for _, name := range []string{"apps_connections", "apps_search_tools", "apps_run_read_tool"} {
		if _, err := runnable(name).Run(toolCtx(), map[string]any{}); err != nil {
			t.Fatalf("%s Run() error = %v, want none: reads need no confirmation", name, err)
		}
	}
	if got := recorder.count("tools/call"); got != beforeCalls+3 {
		t.Fatalf("the three reads made %d tools/call requests, want %d", got-beforeCalls, 3)
	}
}

func TestAReadOnlyAppsToolboxOffersOnlyTheReads(t *testing.T) {
	url, httpClient, _ := newFakeApps(t, "apps_future_tool")
	box, err := AppsToolbox(AppsConfig{URL: url, HTTP: httpClient, Tokens: &fakeAppsTokens{token: "agent-token"}, ReadOnly: true})
	if err != nil {
		t.Fatalf("AppsToolbox() error = %v", err)
	}
	sessions := session.InMemoryService()
	created, err := sessions.Create(context.Background(), &session.CreateRequest{
		AppName: "test", UserID: "user-1", State: map[string]any{OrganizationIDKey: "org-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := box.Toolset.Tools(agent.NewContext(&fakeInvocationContext{base: context.Background(), session: created.Session}))
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	var names []string
	for _, offered := range tools {
		names = append(names, offered.Name())
	}
	slices.Sort(names)
	if want := []string{"apps_connections", "apps_run_read_tool", "apps_search_tools"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want only %v", names, want)
	}
	if !strings.Contains(box.Description, "Nothing can be changed") {
		t.Fatalf("description = %q", box.Description)
	}
}
