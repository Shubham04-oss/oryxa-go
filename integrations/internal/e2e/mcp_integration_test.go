package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// memberBearer sends the member's token and organization on every MCP
// request, as an agent's credential provider does.
type memberBearer struct{ token, organization string }

func (bearer memberBearer) RoundTrip(request *http.Request) (*http.Response, error) {
	carried := request.Clone(request.Context())
	carried.Header.Set("Authorization", "Bearer "+bearer.token)
	carried.Header.Set("X-Oryxa-Organization", bearer.organization)
	return http.DefaultTransport.RoundTrip(carried)
}

// agentSession connects to a provider's fixed MCP URL the way an agent's
// toolset does, naming the organization in a header.
func (s *stack) agentSession(provider string) *mcp.ClientSession {
	s.t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "oryxa-agent", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   s.apiURL + "/v1/mcp/" + provider,
		HTTPClient: &http.Client{Transport: memberBearer{token: memberToken, organization: organization}, Timeout: 15 * time.Second},
	}, nil)
	if err != nil {
		s.t.Fatalf("Connect() to the %s endpoint error = %v", provider, err)
	}
	s.t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestAgentCallsGitHubThroughMCP(t *testing.T) {
	github := newFakeGitHub(t)
	s := newStack(t, github.server)
	s.connections.add(domain.Connection{
		ID: "conn_github", AppID: "app_github", Provider: "github", CredentialType: "bearer", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "583231", Name: "octocat"}, Status: "active",
	})
	session := s.agentSession("github")

	// The agent discovers the provider's tools and its skill.
	if instructions := session.InitializeResult().Instructions; !strings.HasPrefix(instructions, "# GitHub") {
		t.Fatalf("instructions = %q, want GitHub's skill", first(instructions, 40))
	}
	if version := session.InitializeResult().ProtocolVersion; version != "2026-07-28" {
		t.Fatalf("protocol = %s, want the stateless one", version)
	}
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	for _, want := range []string{"github_connections", "github_create_issue", "github_get_issue"} {
		if !slices.Contains(names, want) {
			t.Fatalf("ListTools() = %v, want %s among them", names, want)
		}
	}

	// It asks which connection it would act as, then opens an issue.
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "github_connections", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("github_connections = %+v, %v", result, err)
	}
	if !strings.Contains(textOf(t, result), "conn_github") {
		t.Fatalf("github_connections answered %s", textOf(t, result))
	}

	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "github_create_issue",
		Arguments: map[string]any{
			"repository": "acme/api", "title": "Webhook deliveries are late", "connection_id": "conn_github",
			"idempotency_key": "agent-turn-1",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("github_create_issue = %s, %v", textOf(t, result), err)
	}
	var answered struct {
		ActionID string          `json:"action_id"`
		Status   string          `json:"status"`
		Result   json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(textOf(t, result)), &answered); err != nil {
		t.Fatal(err)
	}
	if answered.Status != "completed" || !strings.HasPrefix(answered.ActionID, "act_") {
		t.Fatalf("github_create_issue answered %+v", answered)
	}
	if created := github.created(); len(created) != 1 || created[0].title != "Webhook deliveries are late" {
		t.Fatalf("GitHub was asked for %+v", created)
	}

	// The same key again answers the same action without opening a second.
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "github_create_issue",
		Arguments: map[string]any{
			"repository": "acme/api", "title": "Webhook deliveries are late", "connection_id": "conn_github",
			"idempotency_key": "agent-turn-1",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if len(github.created()) != 1 {
		t.Fatalf("the same key opened %d issues", len(github.created()))
	}
	if published := completed(s); len(published) != 1 || published[0].Action != "github.create_issue" {
		t.Fatalf("action.completed = %+v", published)
	}

	// A tool the member cannot use fails as a result the model can act on,
	// not as a protocol error.
	result, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "github_get_issue",
		Arguments: map[string]any{"repository": "acme/api", "number": 4041, "connection_id": "conn_missing"},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v, want the failure as a result", err)
	}
	if !result.IsError || !strings.Contains(textOf(t, result), "not_found") {
		t.Fatalf("calling with a connection the member does not have answered %s", textOf(t, result))
	}
	s.noSecrets(providerToken)
}

func TestMCPEndpointRefusesAStranger(t *testing.T) {
	s := newStack(t, nil)

	response, err := s.server.Client().Post(s.apiURL+"/v1/organizations/"+organization+"/mcp/github", "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("MCP without credentials = %d, want 401", response.StatusCode)
	}
}

func TestMCPToolsAreTheCatalogsActions(t *testing.T) {
	s := newStack(t, nil)
	s.connections.add(domain.Connection{
		ID: "conn_slack", AppID: "app_slack", Provider: "slack", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "T1:U1"}, Status: "active",
	})
	session := s.agentSession("slack")

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	// Every action in the catalogue is a tool, named after its provider.
	for _, described := range s.service.Catalog() {
		if described.Name != "slack" {
			continue
		}
		for _, action := range described.Actions {
			tool, ok := tools["slack_"+action.Name]
			if !ok {
				t.Fatalf("the catalogue has slack %s but MCP does not offer it", action.Name)
			}
			if tool.Description != action.Summary {
				t.Fatalf("slack_%s describes itself as %q", action.Name, tool.Description)
			}
			if action.Writes && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint) {
				t.Fatalf("slack_%s writes but is annotated read-only", action.Name)
			}
		}
	}
}

func textOf(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("the tool answered nothing")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("the tool answered %T, want text", result.Content[0])
	}
	return text.Text
}

// first shortens text for a failure message.
func first(text string, size int) string {
	if len(text) <= size {
		return text
	}
	return text[:size] + "…"
}

var _ = integrationsv1.ActionCompletedDetailType
