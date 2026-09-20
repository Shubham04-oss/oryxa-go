package agents

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

type fakeTeamspaceCollaboration struct {
	actor      TeamspaceActor
	token      string
	id         string
	input      SharedEntryInput
	spaces     []Teamspace
	entries    []SharedEntry
	listCalls  int
	entryCalls int
}

func (service *fakeTeamspaceCollaboration) CollaborationSpaces(_ context.Context, actor TeamspaceActor, token string) ([]Teamspace, error) {
	service.actor, service.token = actor, token
	service.listCalls++
	return service.spaces, nil
}

func (service *fakeTeamspaceCollaboration) CollaborationEntries(_ context.Context, actor TeamspaceActor, token, id string, _ int) ([]SharedEntry, error) {
	service.actor, service.token, service.id = actor, token, id
	service.entryCalls++
	return service.entries, nil
}

func teamspaceContextLLM() model.LLM {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "teamspaces_context") != "":
			return genai.NewContentFromText("Queue capacity needs an owner.", genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "read_context", Name: "teamspaces_context", Args: map[string]any{"query": "Launch Room"}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load_teamspaces", Name: "load_tools", Args: map[string]any{"names": []any{"teamspaces"}}})
		}
	})
}

func TestTeamspaceContextFindsAndReadsNamedRoomInOneToolCall(t *testing.T) {
	service := &fakeTeamspaceCollaboration{
		spaces:  []Teamspace{{ID: "tsp_launch", Name: "Launch Room", TeamName: "Launch Council", Status: "active"}},
		entries: []SharedEntry{{ID: "tse_1", TeamspaceID: "tsp_launch", Summary: "Capacity remains open."}},
	}
	box, err := TeamspaceToolbox(TeamspaceConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, teamspaceContextLLM(), box)
	ctx := memberauth.WithAccessToken(context.Background(), "member-token")
	answer := h.turnWith(ctx, "user_a", map[string]any{
		OrganizationIDKey: "org_1", AgentIDKey: "agt_a", AgentNameKey: "Carl", AgentKindKey: "primary",
	}, "Review the Launch Room")
	if !strings.Contains(answer, "capacity") || service.listCalls != 1 || service.entryCalls != 1 || service.id != "tsp_launch" {
		t.Fatalf("answer=%q lists=%d reads=%d id=%q", answer, service.listCalls, service.entryCalls, service.id)
	}
}

func TestSelectTeamspaceRequiresOneSemanticMatch(t *testing.T) {
	spaces := []Teamspace{{ID: "tsp_launch", Name: "Launch Room", TeamName: "Launch Council"}, {ID: "tsp_sales", Name: "Sales Room", TeamName: "Sales"}}
	if got, err := selectTeamspace(spaces, "launch"); err != nil || got.ID != "tsp_launch" {
		t.Fatalf("semantic match=%+v error=%v", got, err)
	}
	if _, err := selectTeamspace(spaces, "room"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous error=%v", err)
	}
	if _, err := selectTeamspace(spaces, "missing"); err == nil || !strings.Contains(err.Error(), "no accessible") {
		t.Fatalf("missing error=%v", err)
	}
}

func (service *fakeTeamspaceCollaboration) CollaborationPublish(_ context.Context, actor TeamspaceActor, token, id string, input SharedEntryInput) (SharedEntry, error) {
	service.actor, service.token, service.id, service.input = actor, token, id, input
	return SharedEntry{
		ID: "tse_1", TeamspaceID: id, AuthorUserID: actor.UserID,
		AuthorAgentID: actor.AgentID, AuthorAgentName: actor.AgentName,
		Kind: input.Kind, Title: input.Title, Summary: input.Summary,
	}, nil
}

func teamspaceLLM() model.LLM {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "teamspaces_publish") != "":
			return genai.NewContentFromText("Published "+functionResponse(request, "teamspaces_publish"), genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "publish_result", Name: "teamspaces_publish", Args: map[string]any{
				"teamspace_id": "tsp_1", "kind": "result", "title": "Architecture review",
				"summary": "Ready to proceed.", "tools_used": []any{"github.search"},
			}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load_teamspaces", Name: "load_tools", Args: map[string]any{"names": []any{"teamspaces"}}})
		}
	})
}

func TestTeamspaceToolboxDerivesWhispererAuthorship(t *testing.T) {
	service := &fakeTeamspaceCollaboration{}
	box, err := TeamspaceToolbox(TeamspaceConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, teamspaceLLM(), box)
	ctx := memberauth.WithAccessToken(context.Background(), "member-token")
	answer := h.turnWith(ctx, "user_a", map[string]any{
		OrganizationIDKey: "org_1", AgentIDKey: "agt_a", AgentNameKey: "Carl", AgentKindKey: "primary",
	}, "Publish the completed review")
	if !strings.Contains(answer, "tse_1") || service.actor.UserID != "user_a" || service.actor.AgentID != "agt_a" ||
		service.actor.AgentKind != "primary" || service.token != "member-token" || service.id != "tsp_1" ||
		service.input.Title != "Architecture review" {
		t.Fatalf("answer=%q actor=%+v token=%q id=%q input=%+v", answer, service.actor, service.token, service.id, service.input)
	}
}

func TestTeamspaceToolboxNeedsAService(t *testing.T) {
	if _, err := TeamspaceToolbox(TeamspaceConfig{}); err == nil {
		t.Fatal("TeamspaceToolbox() accepted no service")
	}
}
