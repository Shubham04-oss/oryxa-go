package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/application"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
)

const skill = "# Hooks\n\nUse these carefully.\n"

// fakeService is integrations as an MCP endpoint asks of it.
type fakeService struct {
	err          error
	ran          []application.RunActionInput
	credentials  []authclient.Credentials
	organization string
}

func (service *fakeService) Catalog() []providers.Description {
	return []providers.Description{
		{
			Name: "hooks", Delivery: providers.DeliveryWebhook, Skill: skill,
			Actions: []providers.ActionDescription{
				{Name: "read_thing", Summary: "Read a thing.", Input: json.RawMessage(`{"type":"object","properties":{"thing":{"type":"string","description":"Which thing."}},"required":["thing"],"additionalProperties":false}`)},
				{Name: "write_thing", Summary: "Write a thing.", Writes: true, Input: json.RawMessage(`{"type":"object","properties":{"thing":{"type":"string","description":"Which thing."}},"required":["thing"],"additionalProperties":false}`)},
			},
		},
		// A provider with no actions offers no endpoint at all.
		{Name: "quiet", Delivery: providers.DeliveryApp, Skill: "# Quiet\n"},
	}
}

func (service *fakeService) RunAction(_ context.Context, credentials authclient.Credentials, organizationID string, input application.RunActionInput) (domain.Action, error) {
	service.ran = append(service.ran, input)
	service.credentials = append(service.credentials, credentials)
	service.organization = organizationID
	if service.err != nil {
		return domain.Action{}, service.err
	}
	ended := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	return domain.Action{
		ID: "act_1", Status: domain.ActionCompleted, Name: input.Action, Provider: "hooks",
		Resource: &domain.ActionResource{Type: "issue", ID: "12"}, Result: json.RawMessage(`{"number":12}`), EndedAt: &ended,
	}, nil
}

func (service *fakeService) ProviderConnections(_ context.Context, _ authclient.Credentials, _, provider string) ([]domain.Connection, error) {
	if service.err != nil {
		return nil, service.err
	}
	return []domain.Connection{
		{ID: "conn_1", Provider: provider, Scope: "member", Status: "active", ProviderAccount: domain.ProviderAccount{Name: "octocat"}},
		{ID: "conn_2", Provider: provider, Scope: "team", Status: "active", ProviderAccount: domain.ProviderAccount{Name: "acme"}},
	}, nil
}

// serve mounts the MCP endpoint on the path the service serves it at.
func serve(t *testing.T, service Service) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("POST /v1/organizations/{organizationID}/mcp/{provider}", New(service, nil))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// bearer sends the member's token on every MCP request, as an agent's
// credential provider does.
type bearer struct{ token string }

func (b bearer) RoundTrip(request *http.Request) (*http.Response, error) {
	carried := request.Clone(request.Context())
	if b.token != "" {
		carried.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(carried)
}

// connect opens an MCP session against the endpoint, as an agent's toolset
// does.
func connect(t *testing.T, server *httptest.Server, provider, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + "/v1/organizations/org_1/mcp/" + provider,
		HTTPClient: &http.Client{Transport: bearer{token: token}, Timeout: 10 * time.Second},
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestToolsAreTheProvidersActions(t *testing.T) {
	server := serve(t, &fakeService{})
	session := connect(t, server, "hooks", "member-token")

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	var names []string
	byName := map[string]*mcp.Tool{}
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		byName[tool.Name] = tool
	}
	slices.Sort(names)
	want := []string{"hooks_connections", "hooks_read_thing", "hooks_write_thing"}
	if !slices.Equal(names, want) {
		t.Fatalf("ListTools() = %v, want %v", names, want)
	}
	if byName["hooks_read_thing"].Description != "Read a thing." {
		t.Fatalf("read_thing description = %q", byName["hooks_read_thing"].Description)
	}

	// Every tool takes the connection to act with; only a write takes, and
	// requires, an idempotency key.
	read := schemaOf(t, byName["hooks_read_thing"])
	if _, ok := read.Properties["connection_id"]; !ok {
		t.Fatalf("read_thing takes %v, want connection_id among them", read.Properties)
	}
	if slices.Contains(read.Required, "idempotency_key") {
		t.Fatal("a read requires an idempotency key")
	}
	write := schemaOf(t, byName["hooks_write_thing"])
	if _, ok := write.Properties["idempotency_key"]; !ok || !slices.Contains(write.Required, "idempotency_key") {
		t.Fatalf("write_thing schema = %+v, want it to require an idempotency key", write)
	}
	if !slices.Contains(write.Required, "thing") {
		t.Fatalf("write_thing lost its own required fields: %v", write.Required)
	}
}

type toolSchemaShape struct {
	Type       string                     `json:"type"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

func schemaOf(t *testing.T, tool *mcp.Tool) toolSchemaShape {
	t.Helper()
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var shape toolSchemaShape
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("tool %s has no readable schema: %v", tool.Name, err)
	}
	return shape
}

func TestTheSkillIsTheServersInstructions(t *testing.T) {
	server := serve(t, &fakeService{})
	session := connect(t, server, "hooks", "member-token")

	if instructions := session.InitializeResult().Instructions; instructions != skill {
		t.Fatalf("instructions = %q, want the provider's skill", instructions)
	}
	// A stateless server is what speaks the sessionless protocol; a stateful
	// one would quietly negotiate down to an older one that expects a session.
	if version := session.InitializeResult().ProtocolVersion; version != "2026-07-28" {
		t.Fatalf("protocol = %s, want 2026-07-28", version)
	}
}

func TestCallingAToolRunsTheAction(t *testing.T) {
	service := &fakeService{}
	server := serve(t, service)
	session := connect(t, server, "hooks", "member-token")

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "hooks_write_thing",
		Arguments: map[string]any{"thing": "a widget", "connection_id": "conn_1", "idempotency_key": "chat-turn-7788"},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool() = %+v, want it to have run", result.Content)
	}
	if len(service.ran) != 1 {
		t.Fatalf("actions run = %d, want 1", len(service.ran))
	}
	ran := service.ran[0]
	if ran.Action != "write_thing" || ran.Provider != "hooks" || ran.ConnectionID != "conn_1" || ran.IdempotencyKey != "chat-turn-7788" {
		t.Fatalf("RunAction() input = %+v", ran)
	}
	// The fields the tool adds are taken out: the action reads only its own.
	if string(ran.Input) != `{"thing":"a widget"}` {
		t.Fatalf("the action was given %s, want only its own fields", ran.Input)
	}
	if service.organization != "org_1" || service.credentials[0].Authorization != "Bearer member-token" {
		t.Fatalf("the action ran for %q as %+v", service.organization, service.credentials[0])
	}
	var answered map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &answered); err != nil {
		t.Fatal(err)
	}
	if answered["action_id"] != "act_1" || answered["status"] != "completed" {
		t.Fatalf("the tool answered %v", answered)
	}
}

func TestAToolWithoutAConnectionNamesItsProvider(t *testing.T) {
	service := &fakeService{}
	server := serve(t, service)
	session := connect(t, server, "hooks", "member-token")

	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hooks_read_thing", Arguments: map[string]any{"thing": "a widget"},
	}); err != nil {
		t.Fatal(err)
	}
	if ran := service.ran[0]; ran.ConnectionID != "" || ran.Provider != "hooks" {
		t.Fatalf("RunAction() input = %+v, want the provider to find the connection", ran)
	}
}

func TestAFailedActionIsAResultTheModelCanActOn(t *testing.T) {
	service := &fakeService{err: domain.ErrNeedsReconnect}
	server := serve(t, service)
	session := connect(t, server, "hooks", "member-token")

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hooks_read_thing", Arguments: map[string]any{"thing": "a widget", "connection_id": "conn_1"},
	})

	if err != nil {
		t.Fatalf("CallTool() error = %v, want the failure as a result", err)
	}
	if !result.IsError {
		t.Fatal("a failed action answered as success")
	}
	var answered map[string]string
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &answered); err != nil {
		t.Fatal(err)
	}
	if answered["error"] != "needs_reconnect" {
		t.Fatalf("the tool answered %v, want needs_reconnect", answered)
	}
}

func TestAnUnknownFailureTellsTheModelNothingItCouldNotKnow(t *testing.T) {
	service := &fakeService{err: errors.New("dial tcp 10.0.0.1:5432: connection refused")}
	server := serve(t, service)
	session := connect(t, server, "hooks", "member-token")

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hooks_read_thing", Arguments: map[string]any{"thing": "a widget"},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !result.IsError || strings.Contains(text, "10.0.0.1") || !strings.Contains(text, "provider_error") {
		t.Fatalf("the tool answered %s", text)
	}
}

func TestListingConnectionsSaysWhatAnActionWouldActAs(t *testing.T) {
	server := serve(t, &fakeService{})
	session := connect(t, server, "hooks", "member-token")

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "hooks_connections", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var answered struct {
		Connections []map[string]any `json:"connections"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &answered); err != nil {
		t.Fatal(err)
	}
	if len(answered.Connections) != 2 || answered.Connections[0]["connection_id"] != "conn_1" || answered.Connections[0]["account"] != "octocat" {
		t.Fatalf("hooks_connections answered %+v", answered.Connections)
	}
}

func TestTheEndpointNeedsAMember(t *testing.T) {
	server := serve(t, &fakeService{})

	response, err := server.Client().Post(server.URL+"/v1/organizations/org_1/mcp/hooks", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST without credentials = %d, want 401", response.StatusCode)
	}
	if !strings.Contains(response.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("WWW-Authenticate = %q, want it to say how to authenticate", response.Header.Get("WWW-Authenticate"))
	}
}

func TestAProviderWithNoActionsOffersNoEndpoint(t *testing.T) {
	server := serve(t, &fakeService{})

	for _, provider := range []string{"quiet", "nothing"} {
		response, err := server.Client().Post(server.URL+"/v1/organizations/org_1/mcp/"+provider, "application/json",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("POST to %s = %d, want 404", provider, response.StatusCode)
		}
	}
}

// TestTheEndpointKeepsNoSession checks the statelessness the 2026-07-28
// protocol requires: no session header comes back, and a second request needs
// nothing from the first.
func TestTheEndpointKeepsNoSession(t *testing.T) {
	server := serve(t, &fakeService{})

	for range 2 {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/organizations/org_1/mcp/hooks",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("Authorization", "Bearer member-token")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("tools/list without a session = %d", response.StatusCode)
		}
		if session := response.Header.Get("Mcp-Session-Id"); session != "" {
			t.Fatalf("the endpoint handed out the session %q", session)
		}
	}
}

// serveFixed mounts the endpoint at its fixed per-provider URL, which names
// the organization in a header instead of the path.
func serveFixed(t *testing.T, service Service) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("POST /v1/mcp/{provider}", New(service, nil))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// organizationHeader sends the member's token and an organization on every
// request, as an agent's credential provider does.
type organizationHeader struct{ token, organization string }

func (header organizationHeader) RoundTrip(request *http.Request) (*http.Response, error) {
	carried := request.Clone(request.Context())
	carried.Header.Set("Authorization", "Bearer "+header.token)
	if header.organization != "" {
		carried.Header.Set(OrganizationHeader, header.organization)
	}
	return http.DefaultTransport.RoundTrip(carried)
}

func TestTheFixedURLTakesTheOrganizationFromAHeader(t *testing.T) {
	service := &fakeService{}
	server := serveFixed(t, service)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   server.URL + "/v1/mcp/hooks",
		HTTPClient: &http.Client{Transport: organizationHeader{token: "agent-token", organization: "org_7"}, Timeout: 10 * time.Second},
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "hooks_read_thing", Arguments: map[string]any{"thing": "a widget"},
	}); err != nil {
		t.Fatal(err)
	}
	if service.organization != "org_7" {
		t.Fatalf("the action ran in %q, want the organization the header named", service.organization)
	}
}

func TestTheFixedURLWithoutAnOrganizationIsRefused(t *testing.T) {
	server := serveFixed(t, &fakeService{})
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/mcp/hooks", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer member-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST without an organization = %d, want 400", response.StatusCode)
	}
}
