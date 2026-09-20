package agents

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

type fakeInteractions struct {
	actor InteractionActor
	token string
	key   string
}

func (service *fakeInteractions) Create(_ context.Context, actor InteractionActor, token, key string, input InteractionCreateInput) (Interaction, error) {
	service.actor, service.token, service.key = actor, token, key
	return Interaction{ID: "irq_1", OrganizationID: actor.OrganizationID, Requester: InteractionParty{UserID: actor.UserID, AgentID: actor.AgentID}, Target: InteractionParty{AgentID: input.TargetAgentID}, Purpose: input.Purpose, Task: input.Task, Status: "pending"}, nil
}
func (*fakeInteractions) Inbox(context.Context, InteractionActor, int) ([]Interaction, error) {
	return []Interaction{}, nil
}
func (*fakeInteractions) Sent(context.Context, InteractionActor, int) ([]Interaction, error) {
	return []Interaction{}, nil
}
func (*fakeInteractions) Get(context.Context, InteractionActor, string) (Interaction, error) {
	return Interaction{}, nil
}
func (*fakeInteractions) Decide(context.Context, InteractionActor, string, InteractionDecisionInput) (Interaction, error) {
	return Interaction{}, nil
}
func (*fakeInteractions) Respond(context.Context, InteractionActor, string, InteractionResponseInput) (Interaction, error) {
	return Interaction{}, nil
}

func interactionLLM() model.LLM {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "interactions_create") != "":
			return genai.NewContentFromText("Requested "+functionResponse(request, "interactions_create"), genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "request_engineering", Name: "interactions_create", Args: map[string]any{
				"target_agent_id": "agt_b", "purpose": "Architecture", "task": "Explain the boundary.",
			}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load_interactions", Name: "load_tools", Args: map[string]any{"names": []any{"interactions"}}})
		}
	})
}

func TestInteractionToolboxDerivesTheRequesterIdentity(t *testing.T) {
	service := &fakeInteractions{}
	box, err := InteractionToolbox(InteractionConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, interactionLLM(), box)
	ctx := memberauth.WithAccessToken(context.Background(), "member-token")
	answer := h.turnWith(ctx, "user_a", map[string]any{
		OrganizationIDKey: "org_1", AgentIDKey: "agt_a", AgentNameKey: "Crew", AgentKindKey: "primary",
	}, "Ask engineering")
	if !strings.Contains(answer, "irq_1") || service.actor.UserID != "user_a" || service.actor.AgentID != "agt_a" ||
		service.actor.AgentName != "Crew" || service.token != "member-token" || service.key == "" {
		t.Fatalf("answer=%q actor=%+v token=%q key=%q", answer, service.actor, service.token, service.key)
	}
}

func TestInteractionToolboxNeedsAService(t *testing.T) {
	if _, err := InteractionToolbox(InteractionConfig{}); err == nil {
		t.Fatal("InteractionToolbox() accepted no service")
	}
}
