package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// fakeService records what the endpoint asked of apps and answers what a test
// set, so these tests are about the MCP surface alone.
type fakeService struct {
	connections []domain.Connection
	tools       []application.ToolView
	run         application.ToolRun
	err         error

	credentials  authclient.Credentials
	organization string
	toolkit      string
	query        string
	limit        int
	input        application.RunToolInput
}

func (fake *fakeService) Connections(_ context.Context, credentials authclient.Credentials, organizationID, toolkit string, organization bool) ([]domain.Connection, error) {
	fake.credentials, fake.organization, fake.toolkit = credentials, organizationID, toolkit
	if organization {
		return nil, errors.New("the endpoint asked for the whole organization")
	}
	return fake.connections, fake.err
}

func (fake *fakeService) SearchTools(_ context.Context, credentials authclient.Credentials, organizationID, toolkit, query string, limit int) ([]application.ToolView, error) {
	fake.credentials, fake.organization, fake.toolkit, fake.query, fake.limit = credentials, organizationID, toolkit, query, limit
	return fake.tools, fake.err
}

func (fake *fakeService) RunTool(_ context.Context, credentials authclient.Credentials, organizationID string, input application.RunToolInput) (application.ToolRun, error) {
	fake.credentials, fake.organization, fake.input = credentials, organizationID, input
	if fake.err != nil {
		return application.ToolRun{}, fake.err
	}
	return fake.run, nil
}

// headers adds a member's credentials and organization to every request the
// MCP client makes, the way the agents toolbox does.
type headers struct {
	values map[string]string
	next   http.RoundTripper
}

func (transport headers) RoundTrip(request *http.Request) (*http.Response, error) {
	for name, value := range transport.values {
		request.Header.Set(name, value)
	}
	return transport.next.RoundTrip(request)
}

// session connects a real MCP client to the handler, so the tools' schemas and
// the protocol are exercised rather than the handler's methods alone.
func session(t *testing.T, handler http.Handler, path string, values map[string]string) *mcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	connected, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             server.URL + path,
		HTTPClient:           &http.Client{Transport: headers{values: values, next: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = connected.Close() })
	return connected
}

// memberSession is the usual caller: a member's token, the organization in a
// header.
func memberSession(t *testing.T, service Service) *mcp.ClientSession {
	t.Helper()
	return session(t, New(service, nil), "", map[string]string{
		"Authorization": "Bearer member", OrganizationHeader: "org_1",
	})
}

// callTool calls one tool and answers its result.
func callTool(t *testing.T, connected *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := connected.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return result
}

// answer reads a result's structured content.
func answer(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("the result has no content for a model to read")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T", result.Content[0])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
		t.Fatalf("content %q: %v", text.Text, err)
	}
	return decoded
}

func TestEndpointNeedsAMembersToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(New(&fakeService{}, nil))
	t.Cleanup(server.Close)

	request, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set(OrganizationHeader, "org_1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
	// An MCP client is told how to authenticate, not merely refused.
	if !strings.Contains(response.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("WWW-Authenticate = %q", response.Header.Get("WWW-Authenticate"))
	}
}

func TestEndpointNeedsAnOrganization(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(New(&fakeService{}, nil))
	t.Cleanup(server.Close)

	request, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer member")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
}

func TestEndpointOffersFourTools(t *testing.T) {
	t.Parallel()
	connected := memberSession(t, &fakeService{})

	tools, err := connected.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		byName[tool.Name] = tool
	}
	slices.Sort(names)
	want := []string{ToolConnections, ToolRunRead, ToolRunWrite, ToolSearchTools}
	slices.Sort(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	// The catalog runs to thousands of tools; none of them is listed here.
	for _, tool := range tools.Tools {
		if strings.HasPrefix(tool.Name, "GITHUB_") {
			t.Fatalf("the endpoint listed a Composio tool: %s", tool.Name)
		}
	}
	// A model can tell which of the two run tools is safe without running it.
	if annotations := byName[ToolRunRead].Annotations; annotations == nil || !annotations.ReadOnlyHint {
		t.Fatalf("%s annotations = %+v", ToolRunRead, annotations)
	}
	if annotations := byName[ToolRunWrite].Annotations; annotations == nil || annotations.ReadOnlyHint ||
		annotations.DestructiveHint == nil || !*annotations.DestructiveHint {
		t.Fatalf("%s annotations = %+v", ToolRunWrite, annotations)
	}
	// A write's key is part of the contract, not an afterthought.
	write := schemaOf(t, byName[ToolRunWrite])
	if !strings.Contains(write, "idempotency_key") || !strings.Contains(write, `"required"`) {
		t.Fatalf("schema = %s", write)
	}
	if read := schemaOf(t, byName[ToolRunRead]); strings.Contains(read, "idempotency_key") {
		t.Fatalf("a read asks for an idempotency key: %s", read)
	}
	for _, name := range []string{ToolRunRead, ToolRunWrite} {
		if schema := schemaOf(t, byName[name]); strings.Contains(schema, "connection_id") {
			t.Fatalf("%s lets an agent choose an account: %s", name, schema)
		}
	}
}

// schemaOf is a tool's input schema as JSON, whatever shape the SDK keeps it in.
func schemaOf(t *testing.T, tool *mcp.Tool) string {
	t.Helper()
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestConnectionsListsWhatTheMemberCanActThrough(t *testing.T) {
	t.Parallel()
	fake := &fakeService{connections: []domain.Connection{
		{ID: "apc_1", Toolkit: "github", Label: "work", Status: domain.ConnectionActive, ComposioAccountID: "ca_1", ComposioUserID: "oryxa.org_1.user-1"},
		{ID: "apc_2", Toolkit: "gmail", Status: domain.ConnectionExpired, ComposioAccountID: "ca_2"},
		{ID: "apc_3", Toolkit: "slack", Status: domain.ConnectionPending, ComposioAccountID: "ca_3"},
		{ID: "apc_4", Toolkit: "notion", Status: domain.ConnectionRemoved, ComposioAccountID: "ca_4"},
	}}
	connected := memberSession(t, fake)

	result := callTool(t, connected, ToolConnections, map[string]any{})
	if result.IsError {
		t.Fatalf("result = %+v", answer(t, result))
	}
	listed := answer(t, result)["connections"].([]any)
	var toolkits []string
	for _, item := range listed {
		view := item.(map[string]any)
		toolkits = append(toolkits, view["toolkit"].(string))
		if _, exposed := view["connection_id"]; exposed {
			t.Fatalf("agent-facing connection=%v exposes routing id", view)
		}
	}
	// A pending or removed connection can do nothing; an expired one is
	// listed so the agent can ask the member to reconnect it.
	if strings.Join(toolkits, ",") != "github,gmail" {
		t.Fatalf("listed = %v", toolkits)
	}
	// Nothing of Composio's own bookkeeping reaches the model.
	for _, forbidden := range []string{"ca_1", "ca_2", "oryxa.org_1.user-1", "composio"} {
		if strings.Contains(strings.ToLower(answerText(t, result)), strings.ToLower(forbidden)) {
			t.Fatalf("the answer carries %q: %s", forbidden, answerText(t, result))
		}
	}
	if fake.credentials.Authorization != "Bearer member" || fake.organization != "org_1" {
		t.Fatalf("caller = %+v in %q", fake.credentials, fake.organization)
	}
}

func answerText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T", result.Content[0])
	}
	return text.Text
}

func TestSearchToolsPassesTheAskOn(t *testing.T) {
	t.Parallel()
	fake := &fakeService{tools: []application.ToolView{
		{Tool: "GITHUB_CREATE_AN_ISSUE", Name: "Create an issue", Toolkit: "github", Destructive: true,
			InputSchema: json.RawMessage(`{"type":"object"}`)},
	}}
	connected := memberSession(t, fake)

	result := callTool(t, connected, ToolSearchTools, map[string]any{"toolkit": "github", "query": "create issue", "limit": 5})
	if result.IsError {
		t.Fatalf("result = %+v", answer(t, result))
	}
	if fake.toolkit != "github" || fake.query != "create issue" || fake.limit != 5 {
		t.Fatalf("ask = %q, %q, %d", fake.toolkit, fake.query, fake.limit)
	}
	// The model is told what each tool takes and whether it is safe.
	tools := answer(t, result)["tools"].([]any)
	first := tools[0].(map[string]any)
	if first["tool"] != "GITHUB_CREATE_AN_ISSUE" || first["read_only"] != false || first["destructive"] != true {
		t.Fatalf("tool = %v", first)
	}
	if _, ok := first["input_schema"]; !ok {
		t.Fatalf("tool = %v", first)
	}
}

func TestRunReadToolRefusesAnythingThatWrites(t *testing.T) {
	t.Parallel()
	fake := &fakeService{run: application.ToolRun{
		Action: domain.Action{ID: "apa_1", Tool: "GITHUB_LIST_ISSUES", ConnectionID: "apc_1", Status: domain.ActionSucceeded},
		Data:   json.RawMessage(`{"issues":[]}`),
	}}
	connected := memberSession(t, fake)

	result := callTool(t, connected, ToolRunRead, map[string]any{
		"tool": "GITHUB_LIST_ISSUES", "arguments": map[string]any{"repo": "oryxa"},
	})
	if result.IsError {
		t.Fatalf("result = %+v", answer(t, result))
	}
	// The read tool asks the application to refuse anything that writes: the
	// rule is the server's, so a misbehaving agent cannot get around it.
	if !fake.input.OnlyReads {
		t.Fatal("the read tool did not ask for reads only")
	}
	if fake.input.IdempotencyKey != "" {
		t.Fatalf("a read carried a key: %q", fake.input.IdempotencyKey)
	}
	if string(fake.input.Arguments) != `{"repo":"oryxa"}` {
		t.Fatalf("arguments = %s", fake.input.Arguments)
	}
}

func TestRunWriteToolCarriesTheKey(t *testing.T) {
	t.Parallel()
	fake := &fakeService{run: application.ToolRun{
		Action: domain.Action{ID: "apa_1", Tool: "GITHUB_CREATE_AN_ISSUE", ConnectionID: "apc_1", Status: domain.ActionSucceeded},
		Data:   json.RawMessage(`{"number":7}`),
	}}
	connected := memberSession(t, fake)

	result := callTool(t, connected, ToolRunWrite, map[string]any{
		"tool": "GITHUB_CREATE_AN_ISSUE", "arguments": map[string]any{"title": "bug"},
		"idempotency_key": "issue-2026-09-17",
	})
	if result.IsError {
		t.Fatalf("result = %+v", answer(t, result))
	}
	if fake.input.OnlyReads {
		t.Fatal("the write tool asked for reads only")
	}
	if fake.input.IdempotencyKey != "issue-2026-09-17" || fake.input.ConnectionID != "" {
		t.Fatalf("input = %+v", fake.input)
	}
	answered := answer(t, result)
	if answered["successful"] != true || answered["action_id"] != "apa_1" {
		t.Fatalf("answer = %v", answered)
	}
}

func TestRunToolSaysWhatTheAppRefused(t *testing.T) {
	t.Parallel()
	fake := &fakeService{run: application.ToolRun{
		Action: domain.Action{ID: "apa_1", Tool: "GITHUB_CREATE_AN_ISSUE", Status: domain.ActionFailed, Error: "Validation failed: title is required"},
	}}
	connected := memberSession(t, fake)

	result := callTool(t, connected, ToolRunWrite, map[string]any{
		"tool": "GITHUB_CREATE_AN_ISSUE", "arguments": map[string]any{}, "idempotency_key": "issue-2026-09-17",
	})
	// A tool the app refused is a failed result, so the model tries again
	// differently rather than reporting success.
	if !result.IsError {
		t.Fatalf("result = %+v", answer(t, result))
	}
	answered := answer(t, result)
	if answered["error"] != "app_error" || answered["message"] != "Validation failed: title is required" {
		t.Fatalf("answer = %v", answered)
	}
	if answered["action_id"] != "apa_1" {
		t.Fatalf("answer = %v", answered)
	}
}

func TestFailuresComeBackAsCodesAModelCanAct(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		err  error
		code string
	}{
		"the app is not connected": {err: domain.ErrNotConnected, code: "not_connected"},
		"several could be meant":   {err: domain.ErrChooseConnection, code: "choose_connection"},
		"it needs reconnecting":    {err: domain.ErrNeedsReconnect, code: "needs_reconnect"},
		"the tool writes":          {err: domain.ErrNotReadOnly, code: "not_read_only"},
		"the member is not that":   {err: domain.ErrForbidden, code: "forbidden"},
		"no such tool":             {err: domain.ErrNotFound, code: "not_found"},
		"too many requests":        {err: domain.ErrRateLimited, code: "rate_limited"},
		"apps is broken":           {err: errors.New("the database is on fire"), code: "internal"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeService{err: fmt.Errorf("run GITHUB_LIST_ISSUES: %w", testCase.err)}
			connected := memberSession(t, fake)

			result := callTool(t, connected, ToolRunRead, map[string]any{
				"tool": "GITHUB_LIST_ISSUES", "arguments": map[string]any{},
			})
			if !result.IsError {
				t.Fatalf("result = %+v", answer(t, result))
			}
			answered := answer(t, result)
			if answered["error"] != testCase.code {
				t.Fatalf("error = %v, want %q", answered["error"], testCase.code)
			}
			if testCase.code == "internal" && strings.Contains(answerText(t, result), "on fire") {
				t.Fatalf("the answer leaks the reason: %s", answerText(t, result))
			}
		})
	}
}

func TestArgumentsMustMatchTheSchema(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	connected := memberSession(t, fake)

	// An argument the schema does not allow is refused before anything runs,
	// so a client cannot smuggle in another member or choose an account.
	result := callTool(t, connected, ToolRunWrite, map[string]any{
		"tool": "GITHUB_CREATE_AN_ISSUE", "arguments": map[string]any{},
		"idempotency_key": "issue-2026-09-17", "connection_id": "apc_other",
	})
	if !result.IsError {
		t.Fatalf("an unknown argument was accepted: %+v", answer(t, result))
	}
	if answer(t, result)["error"] != "invalid_request" {
		t.Fatalf("answer = %v", answer(t, result))
	}
	if fake.input.Tool != "" {
		t.Fatalf("the run reached the application: %+v", fake.input)
	}

	// A write whose key is missing is passed on without one, for the
	// application to refuse; the endpoint never invents a key, which would
	// make a retry a second change.
	callTool(t, connected, ToolRunWrite, map[string]any{
		"tool": "GITHUB_CREATE_AN_ISSUE", "arguments": map[string]any{},
	})
	if fake.input.IdempotencyKey != "" || fake.input.OnlyReads {
		t.Fatalf("input = %+v", fake.input)
	}
}

func TestTheOrganizationMayComeFromThePath(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	mux := http.NewServeMux()
	mux.Handle("POST /v1/organizations/{organizationID}/apps/mcp", New(fake, nil))
	connected := session(t, mux, "/v1/organizations/org_7/apps/mcp", map[string]string{
		"Authorization": "Bearer member", OrganizationHeader: "org_1",
	})

	callTool(t, connected, ToolConnections, map[string]any{})
	// The path wins over the header, so a fixed client URL cannot reach
	// another organization by sending a header.
	if fake.organization != "org_7" {
		t.Fatalf("organization = %q", fake.organization)
	}
}

func TestEachRequestCarriesItsOwnCaller(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	handler := New(fake, nil)
	first := session(t, handler, "", map[string]string{"Authorization": "Bearer one", OrganizationHeader: "org_1"})
	second := session(t, handler, "", map[string]string{"Authorization": "Bearer two", OrganizationHeader: "org_2"})

	callTool(t, first, ToolConnections, map[string]any{})
	if fake.credentials.Authorization != "Bearer one" || fake.organization != "org_1" {
		t.Fatalf("first caller = %+v in %q", fake.credentials, fake.organization)
	}
	// The endpoint keeps no session, so the second member's request is not
	// answered as the first.
	callTool(t, second, ToolConnections, map[string]any{})
	if fake.credentials.Authorization != "Bearer two" || fake.organization != "org_2" {
		t.Fatalf("second caller = %+v in %q", fake.credentials, fake.organization)
	}
}

func TestACookieAuthenticatesTooForABrowserClient(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	connected := session(t, New(fake, nil), "", map[string]string{
		"Cookie": authclient.AccessCookieName + "=token-value", OrganizationHeader: "org_1",
	})

	callTool(t, connected, ToolConnections, map[string]any{})
	if fake.credentials.AccessCookie != "token-value" || fake.credentials.Authorization != "" {
		t.Fatalf("credentials = %+v", fake.credentials)
	}
}
