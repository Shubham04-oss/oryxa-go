package agents

import (
	"context"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

// teamspaceRequests is fakeInteractions keeping the last request a tool
// asked it to create.
type teamspaceRequests struct {
	fakeInteractions
	input InteractionCreateInput
}

func (service *teamspaceRequests) Create(ctx context.Context, actor InteractionActor, token, key string, input InteractionCreateInput) (Interaction, error) {
	service.input = input
	return service.fakeInteractions.Create(ctx, actor, token, key, input)
}

// TestAToolRequestCarriesTheTeamspaceTheModelNamed: an agent asking a
// teammate's agent inside a teamspace says which one, and the ledger — not
// the model — then works out who approves it.
func TestAToolRequestCarriesTheTeamspaceTheModelNamed(t *testing.T) {
	service := &teamspaceRequests{}
	box, err := InteractionToolbox(InteractionConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "interactions_create") != "":
			return genai.NewContentFromText("Asked.", genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "ask_in_room", Name: "interactions_create", Args: map[string]any{
				"target_agent_id": "agt_grace", "teamspace_id": "tsp_launch", "purpose": "Launch", "task": "Review the checklist.",
			}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load_interactions", Name: "load_tools", Args: map[string]any{"names": []any{"interactions"}}})
		}
	})
	h := newHarness(t, llm, box)
	ctx := memberauth.WithAccessToken(context.Background(), "member-token")
	h.turnWith(ctx, "user_a", map[string]any{
		OrganizationIDKey: "org_1", AgentIDKey: "agt_a", AgentNameKey: "Crew", AgentKindKey: "primary",
	}, "Ask Grace's agent in the launch room")
	if service.input.TeamspaceID != "tsp_launch" || service.input.TargetAgentID != "agt_grace" || service.actor.AgentID != "agt_a" {
		t.Fatalf("input=%+v actor=%+v, want the teamspace the model named, asked as agt_a", service.input, service.actor)
	}
}
