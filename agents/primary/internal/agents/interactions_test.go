package agents

import (
	"context"
	"slices"
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
	// The member is watching with their own token, and it is still the agent
	// acting: a run never presents itself as the member's own session.
	if service.actor.Session != SessionAgent {
		t.Fatalf("a tool call acted in session %q, want %q", service.actor.Session, SessionAgent)
	}
}

func TestInteractionToolboxNeedsAService(t *testing.T) {
	if _, err := InteractionToolbox(InteractionConfig{}); err == nil {
		t.Fatal("InteractionToolbox() accepted no service")
	}
}

// TestAnAnsweringToolboxCannotStartNewRequests: the toolbox a woken agent
// gets answers what another member asked; it does not let that member's words
// turn into fresh requests on everyone else.
func TestAnAnsweringToolboxCannotStartNewRequests(t *testing.T) {
	names := func(answering bool) []string {
		t.Helper()
		toolbox, err := InteractionToolbox(InteractionConfig{Service: &fakeInteractions{}, Answering: answering})
		if err != nil {
			t.Fatal(err)
		}
		tools, err := toolbox.Toolset.Tools(nil)
		if err != nil {
			t.Fatal(err)
		}
		var offered []string
		for _, one := range tools {
			offered = append(offered, one.Name())
		}
		return offered
	}
	if !slices.Contains(names(false), "interactions_create") {
		t.Fatal("a member's own agent cannot start a request")
	}
	answering := names(true)
	if slices.Contains(answering, "interactions_create") {
		t.Fatalf("a run nobody is watching was offered interactions_create: %v", answering)
	}
	for _, needed := range []string{"interactions_get", "interactions_decide", "interactions_respond"} {
		if !slices.Contains(answering, needed) {
			t.Fatalf("a woken agent cannot %s: %v", needed, answering)
		}
	}
}
