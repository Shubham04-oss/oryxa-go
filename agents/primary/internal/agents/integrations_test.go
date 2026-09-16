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
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
)

// --- a fake integrations service: the catalogue and the MCP endpoint -------

// recordedRequest is one HTTP request the fake integrations service
// answered, kept so a test can inspect exactly what reached the wire.
type recordedRequest struct {
	path   string
	header http.Header
	// method is the JSON-RPC method an MCP request carried, such as
	// tools/list.
	method string
}

// headerRecorder wraps the fake service's handler to capture every request's
// path and headers before the real handler runs.
type headerRecorder struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (h *headerRecorder) middleware(inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var message struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &message)
		h.mu.Lock()
		h.requests = append(h.requests, recordedRequest{path: r.URL.Path, header: r.Header.Clone(), method: message.Method})
		h.mu.Unlock()
		inner.ServeHTTP(w, r)
	})
}

// mcpRequests returns the recorded requests that went to an MCP endpoint,
// the only ones the credential provider authenticates.
func (h *headerRecorder) mcpRequests() []recordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []recordedRequest
	for _, req := range h.requests {
		if strings.HasPrefix(req.path, "/v1/mcp/") {
			out = append(out, req)
		}
	}
	return out
}

// fakeToolResult is what every fake tool answers, regardless of its
// arguments: enough for a model to summarize into a final answer.
func fakeToolResult(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"ok":true}`}}}, nil
}

// newFakeIntegrations starts an httptest server serving GET /v1/catalog from
// catalog and a stateless MCP endpoint per provider at POST /v1/mcp/{provider},
// built with the same go-sdk used by integrations/internal/mcpserver. Every
// tool it serves is named provider_action, exactly as
// integrations/internal/mcpserver names them, and it also serves extraTools,
// by full name, that the catalogue does not describe.
func newFakeIntegrations(t *testing.T, catalog integrationsCatalog, extraTools ...string) (string, *http.Client, *headerRecorder) {
	t.Helper()
	byName := make(map[string]catalogProvider, len(catalog.Providers))
	for _, provider := range catalog.Providers {
		byName[provider.Name] = provider
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/catalog", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(catalog); err != nil {
			t.Errorf("encode catalog: %v", err)
		}
	})

	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		provider := byName[r.PathValue("provider")]
		server := mcp.NewServer(&mcp.Implementation{Name: "fake-" + provider.Name, Version: "1"}, &mcp.ServerOptions{Instructions: provider.Skill})
		names := slices.Clone(extraTools)
		for _, action := range provider.Actions {
			names = append(names, provider.Name+"_"+action.Name)
		}
		for _, name := range names {
			server.AddTool(&mcp.Tool{
				Name:        name,
				Description: name,
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			}, fakeToolResult)
		}
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	mux.Handle("POST /v1/mcp/{provider}", mcpHandler)

	recorder := &headerRecorder{}
	server := httptest.NewServer(recorder.middleware(mux))
	t.Cleanup(server.Close)
	return server.URL, server.Client(), recorder
}

// fakeTokens is a memberauth.TokenSource a test can script and inspect.
type fakeTokens struct {
	mu    sync.Mutex
	token string
	calls []string
}

func (f *fakeTokens) AgentToken(_ context.Context, userID string) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, userID)
	return f.token, time.Now().Add(time.Hour), nil
}

func (f *fakeTokens) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// demoCatalog is the fake catalogue this file's tests build toolboxes from:
// one provider with a read and a write action, and one provider with none.
func demoCatalog() integrationsCatalog {
	return integrationsCatalog{Providers: []catalogProvider{
		{
			Name:  "demo",
			Skill: "Use demo_list_items to read. demo_create_item writes and needs confirmation.",
			Actions: []catalogAction{
				{Name: "list_items", Writes: false},
				{Name: "create_item", Writes: true},
			},
		},
		{
			Name:    "empty",
			Skill:   "Nothing to do here.",
			Actions: []catalogAction{},
		},
	}}
}

// --- structural tests: what IntegrationsToolboxes builds --------------------

func TestIntegrationsToolboxesOnePerProviderWithActions(t *testing.T) {
	url, httpClient, _ := newFakeIntegrations(t, demoCatalog())
	toolboxes, err := IntegrationsToolboxes(context.Background(), IntegrationsConfig{URL: url, HTTP: httpClient, Tokens: &fakeTokens{token: "t"}})
	if err != nil {
		t.Fatalf("IntegrationsToolboxes() error = %v", err)
	}
	if len(toolboxes) != 1 {
		t.Fatalf("len(toolboxes) = %d, want 1 (the provider with no actions is skipped)", len(toolboxes))
	}
	box := toolboxes[0]
	if box.Name != "demo" {
		t.Errorf("Name = %q, want demo", box.Name)
	}
	if box.Instructions != "Use demo_list_items to read. demo_create_item writes and needs confirmation." {
		t.Errorf("Instructions = %q, want the provider's skill verbatim", box.Instructions)
	}
	if box.Description == "" {
		t.Error("Description is empty")
	}
	if box.Toolset == nil {
		t.Fatal("Toolset is nil")
	}
}

func TestIntegrationsToolboxesSatisfyTheNamingRule(t *testing.T) {
	url, httpClient, _ := newFakeIntegrations(t, demoCatalog())
	toolboxes, err := IntegrationsToolboxes(context.Background(), IntegrationsConfig{URL: url, HTTP: httpClient, Tokens: &fakeTokens{token: "t"}})
	if err != nil {
		t.Fatalf("IntegrationsToolboxes() error = %v", err)
	}
	// newToolCatalog is what Root feeds every toolbox through; a toolbox that
	// fails it can never be loaded. Its per-tool naming check (every tool
	// prefixed name_) runs lazily in load_tools, exercised below by the full
	// turn actually loading "demo".
	if _, err := newToolCatalog(toolboxes); err != nil {
		t.Fatalf("newToolCatalog() error = %v", err)
	}
}

func TestIntegrationsToolboxesUnreachableCatalogIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close() // closed before any request reaches it

	toolboxes, err := IntegrationsToolboxes(context.Background(), IntegrationsConfig{URL: server.URL, Tokens: &fakeTokens{}})
	if err == nil {
		t.Fatal("IntegrationsToolboxes() error = nil for an unreachable catalogue")
	}
	if toolboxes != nil {
		t.Fatalf("toolboxes = %v, want nil", toolboxes)
	}
}

func TestIntegrationsToolboxesRequireAURL(t *testing.T) {
	if _, err := IntegrationsToolboxes(context.Background(), IntegrationsConfig{}); err == nil {
		t.Fatal("IntegrationsToolboxes() error = nil with no URL")
	}
}

// --- a tool call inside a real ADK invocation --------------------------------

// runIntegrationsTurn drives one real Krude turn — a real runner, a real
// in-memory session seeded exactly as internal/adksession seeds a member's
// session, and toolboxes built from IntegrationsToolboxes — and returns
// every event it produced. ctx is the caller's, so a test can attach a
// member access token to it before the run starts, the way whatever starts a
// real run would.
func runIntegrationsTurn(t *testing.T, ctx context.Context, llm model.LLM, toolboxes []Toolbox, userID, organizationID, message string) []*session.Event {
	t.Helper()
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatalf("modelrouter.New() error = %v", err)
	}
	root, err := Root(router, toolboxes)
	if err != nil {
		t.Fatalf("Root() error = %v", err)
	}
	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{
		AppName: RootName, UserID: userID,
		State: map[string]any{OrganizationIDKey: organizationID},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	turns, err := runner.New(runner.Config{AppName: RootName, Agent: root, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	var events []*session.Event
	for event, err := range turns.Run(ctx, userID, created.Session.ID(), genai.NewContentFromText(message, genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		events = append(events, event)
	}
	return events
}

// readsDemoLLM has Krude load the "demo" toolbox and call its read action,
// exactly what both token-path tests below need.
func readsDemoLLM() model.LLM {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "demo_list_items") != "":
			return genai.NewContentFromText("Read done.", genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "call_read", Name: "demo_list_items", Args: map[string]any{}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "call_load", Name: "load_tools", Args: map[string]any{"names": []any{"demo"}}})
		}
	})
}

func finalAnswer(events []*session.Event) string {
	var answer strings.Builder
	for _, event := range events {
		if event.Author == RootName && event.Content != nil {
			answer.WriteString(text(event.Content))
		}
	}
	return answer.String()
}

func TestIntegrationsToolboxReachesTheFakeServerWithTheMembersOwnToken(t *testing.T) {
	url, httpClient, recorder := newFakeIntegrations(t, demoCatalog())
	tokens := &fakeTokens{token: "should-not-be-minted"}
	toolboxes, err := IntegrationsToolboxes(context.Background(), IntegrationsConfig{URL: url, HTTP: httpClient, Tokens: tokens})
	if err != nil {
		t.Fatalf("IntegrationsToolboxes() error = %v", err)
	}

	ctx := memberauth.WithAccessToken(context.Background(), "member-own-token")
	events := runIntegrationsTurn(t, ctx, readsDemoLLM(), toolboxes, "user-1", "org-1", "List the demo items.")

	if answer := finalAnswer(events); answer != "Read done." {
		t.Fatalf("answer = %q, want %q", answer, "Read done.")
	}
	assertReachedWith(t, recorder, "Bearer member-own-token", "org-1")
	if tokens.callCount() != 0 {
		t.Fatalf("the agent token source was called %d times, want 0: a member's own token must never mint one", tokens.callCount())
	}
}

func TestIntegrationsToolboxReachesTheFakeServerWithATokenFromTheSource(t *testing.T) {
	url, httpClient, recorder := newFakeIntegrations(t, demoCatalog())
	tokens := &fakeTokens{token: "agent-token-abc"}
	toolboxes, err := IntegrationsToolboxes(context.Background(), IntegrationsConfig{URL: url, HTTP: httpClient, Tokens: tokens})
	if err != nil {
		t.Fatalf("IntegrationsToolboxes() error = %v", err)
	}

	events := runIntegrationsTurn(t, context.Background(), readsDemoLLM(), toolboxes, "user-1", "org-1", "List the demo items.")

	if answer := finalAnswer(events); answer != "Read done." {
		t.Fatalf("answer = %q, want %q", answer, "Read done.")
	}
	assertReachedWith(t, recorder, "Bearer agent-token-abc", "org-1")
	if tokens.callCount() == 0 {
		t.Fatal("the agent token source was never called")
	}
	if tokens.calls[0] != "user-1" {
		t.Fatalf("token source was asked for %q, want user-1", tokens.calls[0])
	}
}

// assertReachedWith fails the test unless at least one recorded MCP request
// carries exactly the given Authorization and X-Oryxa-Organization headers.
func assertReachedWith(t *testing.T, recorder *headerRecorder, authorization, organization string) {
	t.Helper()
	requests := recorder.mcpRequests()
	if len(requests) == 0 {
		t.Fatal("no request reached the fake integrations MCP endpoint")
	}
	for _, req := range requests {
		if req.header.Get("Authorization") != authorization {
			t.Errorf("request to %s: Authorization = %q, want %q", req.path, req.header.Get("Authorization"), authorization)
		}
		if req.header.Get("X-Oryxa-Organization") != organization {
			t.Errorf("request to %s: X-Oryxa-Organization = %q, want %q", req.path, req.header.Get("X-Oryxa-Organization"), organization)
		}
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

func findTool(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, candidate := range tools {
		if candidate.Name() == name {
			return candidate
		}
	}
	t.Fatalf("tool %q was not offered; got %v", name, tools)
	return nil
}

func TestIntegrationsToolboxWritesRequireConfirmationReadsDoNot(t *testing.T) {
	url, httpClient, recorder := newFakeIntegrations(t, demoCatalog())
	toolboxes, err := IntegrationsToolboxes(context.Background(), IntegrationsConfig{URL: url, HTTP: httpClient, Tokens: &fakeTokens{token: "agent-token"}})
	if err != nil {
		t.Fatalf("IntegrationsToolboxes() error = %v", err)
	}
	box := toolboxes[0]

	sessions := session.InMemoryService()
	created, err := sessions.Create(context.Background(), &session.CreateRequest{
		AppName: "test", UserID: "user-1", State: map[string]any{"organization_id": "org-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readonlyCtx := agent.NewContext(&fakeInvocationContext{base: context.Background(), session: created.Session})
	tools, err := box.Toolset.Tools(readonlyCtx)
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	// Tools() itself already made protocol-level requests (initialize,
	// tools/list) to build this list; only a request made after this point
	// can be the tool call the assertions below care about.
	beforeCalls := len(recorder.mcpRequests())

	runnable := func(name string) interface {
		Run(agent.Context, any) (map[string]any, error)
	} {
		found := findTool(t, tools, name)
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

	if _, err := runnable("demo_create_item").Run(toolCtx(), map[string]any{}); !errors.Is(err, tool.ErrConfirmationRequired) {
		t.Fatalf("demo_create_item Run() error = %v, want ErrConfirmationRequired", err)
	}
	if got := len(recorder.mcpRequests()); got != beforeCalls {
		t.Fatalf("a write awaiting confirmation reached the server: %d requests, want %d (no new one)", got, beforeCalls)
	}

	if _, err := runnable("demo_list_items").Run(toolCtx(), map[string]any{}); err != nil {
		t.Fatalf("demo_list_items Run() error = %v, want none: reads need no confirmation", err)
	}
	if got := len(recorder.mcpRequests()); got <= beforeCalls {
		t.Fatalf("the read tool never reached the server: %d requests, want more than %d", got, beforeCalls)
	}
}

// --- one toolset, many members -----------------------------------------------

// memberTokens mints each member a token of their own, or refuses every
// member when refuse is set.
type memberTokens struct {
	mu     sync.Mutex
	refuse error
}

func (m *memberTokens) AgentToken(_ context.Context, userID string) (string, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refuse != nil {
		return "", time.Time{}, m.refuse
	}
	return "agent-token-for-" + userID, time.Now().Add(time.Hour), nil
}

func (h *headerRecorder) count(method string) int {
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

func TestIntegrationsToolboxesKeepEachMembersOwnIdentity(t *testing.T) {
	url, httpClient, recorder := newFakeIntegrations(t, demoCatalog())
	toolboxes, err := IntegrationsToolboxes(context.Background(), IntegrationsConfig{URL: url, HTTP: httpClient, Tokens: &memberTokens{}})
	if err != nil {
		t.Fatalf("IntegrationsToolboxes() error = %v", err)
	}
	h := newHarness(t, readsDemoLLM(), toolboxes...)

	// One member chats with their own token; then, on the same toolboxes and
	// so the same MCP client, another member's run has no token of its own.
	h.turnWith(memberauth.WithAccessToken(context.Background(), "first-members-own-token"), "user-1", map[string]any{OrganizationIDKey: "org-1"}, "List the demo items.")
	firstRequests := len(recorder.mcpRequests())
	if answer := h.turnWith(context.Background(), "user-2", map[string]any{OrganizationIDKey: "org-2"}, "List the demo items."); answer != "Read done." {
		t.Fatalf("second member's answer = %q", answer)
	}

	requests := recorder.mcpRequests()
	if len(requests) <= firstRequests {
		t.Fatal("the second member's call never reached integrations")
	}
	for _, req := range requests[:firstRequests] {
		if req.header.Get("Authorization") != "Bearer first-members-own-token" || req.header.Get("X-Oryxa-Organization") != "org-1" {
			t.Fatalf("first member's %s request carried %q for %q", req.method, req.header.Get("Authorization"), req.header.Get("X-Oryxa-Organization"))
		}
	}
	for _, req := range requests[firstRequests:] {
		if req.header.Get("Authorization") != "Bearer agent-token-for-user-2" || req.header.Get("X-Oryxa-Organization") != "org-2" {
			t.Fatalf("second member's %s request carried %q for %q", req.method, req.header.Get("Authorization"), req.header.Get("X-Oryxa-Organization"))
		}
	}
	// Integrations serves every member the same tools: they are listed once.
	if lists := recorder.count("tools/list"); lists != 1 {
		t.Fatalf("tools were listed %d times, want once", lists)
	}
}

func TestIntegrationsToolsTheCatalogueDoesNotNameAsReadsAskFirst(t *testing.T) {
	url, httpClient, recorder := newFakeIntegrations(t, demoCatalog(), "demo_connections", "demo_delete_everything")
	toolboxes, err := IntegrationsToolboxes(context.Background(), IntegrationsConfig{URL: url, HTTP: httpClient, Tokens: &memberTokens{}})
	if err != nil {
		t.Fatalf("IntegrationsToolboxes() error = %v", err)
	}
	sessions := session.InMemoryService()
	created, err := sessions.Create(context.Background(), &session.CreateRequest{
		AppName: "test", UserID: "user-1", State: map[string]any{OrganizationIDKey: "org-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := toolboxes[0].Toolset.Tools(agent.NewContext(&fakeInvocationContext{base: context.Background(), session: created.Session}))
	if err != nil {
		t.Fatalf("Tools() error = %v", err)
	}
	run := func(name string) error {
		toolCtx := agent.NewToolContext(&fakeInvocationContext{base: context.Background(), session: created.Session}, "call-1", &session.EventActions{StateDelta: map[string]any{}}, nil)
		_, err := findTool(t, tools, name).(interface {
			Run(agent.Context, any) (map[string]any, error)
		}).Run(toolCtx, map[string]any{})
		return err
	}

	// A write integrations gained after the catalogue was read asks first.
	if err := run("demo_delete_everything"); !errors.Is(err, tool.ErrConfirmationRequired) {
		t.Fatalf("demo_delete_everything Run() error = %v, want ErrConfirmationRequired", err)
	}
	if calls := recorder.count("tools/call"); calls != 0 {
		t.Fatalf("%d calls reached integrations before confirmation, want none", calls)
	}
	// Listing the member's connections is a read.
	if err := run("demo_connections"); err != nil {
		t.Fatalf("demo_connections Run() error = %v, want none", err)
	}
}

func TestIntegrationsToolboxWithoutAGrantSaysSo(t *testing.T) {
	url, httpClient, _ := newFakeIntegrations(t, demoCatalog())
	toolboxes, err := IntegrationsToolboxes(context.Background(), IntegrationsConfig{URL: url, HTTP: httpClient, Tokens: &memberTokens{refuse: memberauth.ErrGrantRequired}})
	if err != nil {
		t.Fatalf("IntegrationsToolboxes() error = %v", err)
	}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if functionResponse(request, "load_tools") != "" {
			return genai.NewContentFromText("You need to allow me first.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_load", Name: "load_tools", Args: map[string]any{"names": []any{"demo"}}})
	})
	h := newHarness(t, llm, toolboxes...)
	if answer := h.turn("user-1", map[string]any{OrganizationIDKey: "org-1"}, "List the demo items."); answer != "You need to allow me first." {
		t.Fatalf("answer = %q", answer)
	}

	result := functionResponse(llm.requestsFor(RootName)[1], "load_tools")
	if !strings.Contains(result, "has not allowed agents to act for them") || strings.Contains(result, url) {
		t.Fatalf("load_tools result %s, want the missing grant in words and no request detail", result)
	}
}

func TestIntegrationsDescriptionNamesTheProviderAsItsSkillDoes(t *testing.T) {
	github := catalogProvider{Name: "github", Skill: "# GitHub\n\nA subscription watches one repository.", Actions: []catalogAction{{Name: "get_issue"}, {Name: "create_issue", Writes: true}}}
	if got, want := integrationsDescription(github), "GitHub, through the member's own connection: get_issue, create_issue."; got != want {
		t.Fatalf("integrationsDescription() = %q, want %q", got, want)
	}
	if got := integrationsDescription(catalogProvider{Name: "plain", Actions: []catalogAction{{Name: "read"}}}); !strings.HasPrefix(got, "plain, ") {
		t.Fatalf("integrationsDescription() without a heading = %q", got)
	}
}
