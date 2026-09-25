package agents

import (
	"context"
	"errors"
	"strings"
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

// heldForItsOwner is fakeInteractions refusing every decision the way the
// ledger refuses an agent's decision on a request held for its owner, and
// keeping who asked.
type heldForItsOwner struct {
	fakeInteractions
	deciders []InteractionActor
}

func (service *heldForItsOwner) Decide(_ context.Context, actor InteractionActor, _ string, _ InteractionDecisionInput) (Interaction, error) {
	service.deciders = append(service.deciders, actor)
	return Interaction{}, errors.New("agent request is not available to this agent: this request is held for your member's own decision in Oryxa")
}

// TestAnAgentCannotDecideARequestHeldForItsOwner: interactions_decide says a
// request held for its owner is refused, asks the ledger as the agent itself
// — the tools have no way to decide as the member — and hands the refusal
// back to the model rather than a decision.
func TestAnAgentCannotDecideARequestHeldForItsOwner(t *testing.T) {
	service := &heldForItsOwner{}
	box, err := InteractionToolbox(InteractionConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := box.Toolset.Tools(nil)
	if err != nil {
		t.Fatal(err)
	}
	described := false
	for _, one := range tools {
		if one.Name() == "interactions_decide" {
			described = strings.Contains(one.Description(), `approval is "owner"`) && strings.Contains(one.Description(), "refuses it")
		}
	}
	if !described {
		t.Fatal("interactions_decide does not say it refuses a request held for its owner")
	}

	var refusal string
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "interactions_decide") != "":
			refusal = functionResponse(request, "interactions_decide")
			return genai.NewContentFromText("It is waiting for you to decide.", genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "approve_held", Name: "interactions_decide", Args: map[string]any{
				"request_id": "irq_held", "decision": "approved", "message": "Approved on my member's behalf.",
			}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load_interactions", Name: "load_tools", Args: map[string]any{"names": []any{"interactions"}}})
		}
	})
	newHarness(t, llm, box).turn("user_b", map[string]any{
		OrganizationIDKey: "org_1", AgentIDKey: "agt_b", AgentNameKey: "Carl", AgentKindKey: "primary",
	}, "Approve Ada's request")
	if len(service.deciders) != 1 || service.deciders[0].UserID != "user_b" || service.deciders[0].AgentID != "agt_b" ||
		service.deciders[0].Session != SessionAgent {
		t.Fatalf("deciders=%+v, want one decision asked as agt_b, in an agent's session", service.deciders)
	}
	if !strings.Contains(refusal, "error") || !strings.Contains(refusal, "held for your member's own decision") {
		t.Fatalf("the model was told %q, want the refusal", refusal)
	}
}
