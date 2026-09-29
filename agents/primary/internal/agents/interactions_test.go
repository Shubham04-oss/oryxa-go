package agents

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
)

type fakeInteractions struct {
	actor InteractionActor
	token string
	key   string
	input InteractionCreateInput
}

func (service *fakeInteractions) Create(_ context.Context, actor InteractionActor, token, key string, input InteractionCreateInput) (Interaction, error) {
	service.actor, service.token, service.key, service.input = actor, token, key, input
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

// TestADeliveringToolboxHoldsOnlyItsOneRequest: a run delivering one
// interaction is offered, from its first model call, exactly the
// interactions tools its purpose uses: reading and deciding its request in
// a run deciding it, reading and answering it in one doing the approved
// work, and reading it alone in a report, or in a run that does not say
// what it is for. It never has a tool that lists the member's other
// requests or starts new ones.
func TestADeliveringToolboxHoldsOnlyItsOneRequest(t *testing.T) {
	toolbox, err := InteractionToolbox(InteractionConfig{Service: &fakeInteractions{}, Delivering: true})
	if err != nil {
		t.Fatal(err)
	}
	if toolbox.Name != InteractionsToolboxName {
		t.Fatalf("the delivering toolbox is named %q, want %q", toolbox.Name, InteractionsToolboxName)
	}
	for purpose, want := range map[string][]string{
		DeliveryDecides: {"interactions_get", "interactions_decide"},
		DeliveryWorks:   {"interactions_get", "interactions_respond"},
		DeliveryReports: {"interactions_get"},
		"":              {"interactions_get"},
	} {
		llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
			return genai.NewContentFromText("Read it.", genai.RoleModel)
		})
		deliveringHarness(t, llm, toolbox).turn("user_b", deliveryStateFor(purpose), "Deliver irq_1.")
		declared := declaredFunctions(llm.requestsFor(RootName)[0])
		slices.Sort(declared)
		slices.Sort(want)
		if !slices.Equal(declared, want) || !slices.Equal(slices.Sorted(slices.Values(DeliveryTools(purpose))), want) {
			t.Errorf("a run delivering for %q was offered %v, want exactly %v", purpose, declared, want)
		}
	}
	listed, err := toolbox.Toolset.Tools(nil)
	if err != nil || len(listed) != 1 || listed[0].Name() != "interactions_get" {
		t.Fatalf("listed with no run = %v, %v; want interactions_get alone", listed, err)
	}
}

// unattendedHarness runs Krude as a run nobody watches runs it.
func unattendedHarness(t *testing.T, llm model.LLM, toolboxes ...Toolbox) *harness {
	t.Helper()
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	root, err := UnattendedRoot(router, toolboxes)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{AppName: RootName, Agent: root, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, sessions: sessions, turns: turns}
}

// deliveryState is a delivery run's session: its agent's identity, with its
// interactions toolbox loaded before the run and irq_1 the request it
// delivers, for doing its approved work, as the waker starts it.
func deliveryState() map[string]any {
	return deliveryStateFor(DeliveryWorks)
}

// deliveryStateFor is deliveryState for a run delivering irq_1 for purpose.
func deliveryStateFor(purpose string) map[string]any {
	state := map[string]any{OrganizationIDKey: "org_1", AgentIDKey: "agt_b", AgentNameKey: "Carl", AgentKindKey: "primary"}
	maps.Copy(state, LoadToolboxes(mapState{}, InteractionsToolboxName))
	maps.Copy(state, DeliveryState(Delivery{RequestID: "irq_1", Purpose: purpose}))
	return state
}

// pinnedInteractions records every request id each of its methods is asked
// about. A model's calls may run at once.
type pinnedInteractions struct {
	fakeInteractions
	mu    sync.Mutex
	asked []string
}

func (service *pinnedInteractions) record(call string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.asked = append(service.asked, call)
}

func (service *pinnedInteractions) Get(_ context.Context, _ InteractionActor, id string) (Interaction, error) {
	service.record("get " + id)
	return Interaction{ID: id, Status: "pending"}, nil
}

func (service *pinnedInteractions) Decide(_ context.Context, _ InteractionActor, id string, input InteractionDecisionInput) (Interaction, error) {
	service.record("decide " + id)
	return Interaction{ID: id, Status: input.Decision}, nil
}

func (service *pinnedInteractions) Respond(_ context.Context, _ InteractionActor, id string, input InteractionResponseInput) (Interaction, error) {
	service.record("respond " + id)
	return Interaction{ID: id, Status: input.Status}, nil
}

// TestADeliveryRunDoesOnlyWhatItIsFor: a run delivering a new request
// decides it and never answers it, so approved work always runs on its own
// with exactly what its approval allows; a run doing approved work answers
// and never decides; a report does neither; and a run that does not say
// what it is for does neither either. Each reads its request.
func TestADeliveryRunDoesOnlyWhatItIsFor(t *testing.T) {
	calls := []*genai.FunctionCall{
		{ID: "read", Name: "interactions_get", Args: map[string]any{"request_id": "irq_1"}},
		{ID: "decide", Name: "interactions_decide", Args: map[string]any{"request_id": "irq_1", "decision": "approved", "message": "Fine."}},
		{ID: "answer", Name: "interactions_respond", Args: map[string]any{"request_id": "irq_1", "status": "completed", "summary": "Done.", "content": "From what I know."}},
	}
	for purpose, want := range map[string][]string{
		DeliveryDecides: {"decide irq_1", "get irq_1"},
		DeliveryWorks:   {"get irq_1", "respond irq_1"},
		DeliveryReports: {"get irq_1"},
		"":              {"get irq_1"},
	} {
		service := &pinnedInteractions{}
		toolbox, err := InteractionToolbox(InteractionConfig{Service: service, Delivering: true})
		if err != nil {
			t.Fatal(err)
		}
		llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
			if hasFunctionResponse(request) {
				return genai.NewContentFromText("Done.", genai.RoleModel)
			}
			return functionCalls(calls...)
		})
		deliveringHarness(t, llm, toolbox).turn("user_b", deliveryStateFor(purpose), "Deliver irq_1.")
		slices.Sort(service.asked)
		if !slices.Equal(service.asked, want) {
			t.Errorf("a run delivering irq_1 for %q asked the ledger %v, want %v", purpose, service.asked, want)
		}
	}
}

// TestADeliveryRunActsOnItsOwnRequestAlone: a run woken to deliver irq_1
// can neither read, decide nor answer any other request of its member's,
// whatever the model is steered to try, and a run with no delivered request
// on record acts on none; its own request it reads as before. A member's own
// run is not limited this way.
func TestADeliveryRunActsOnItsOwnRequestAlone(t *testing.T) {
	calls := []*genai.FunctionCall{
		{ID: "read", Name: "interactions_get", Args: map[string]any{"request_id": "irq_other"}},
		{ID: "decide", Name: "interactions_decide", Args: map[string]any{"request_id": "irq_other", "decision": "rejected", "message": "Ann's phone is 555-0100."}},
		{ID: "answer", Name: "interactions_respond", Args: map[string]any{"request_id": "irq_other", "status": "completed", "summary": "Ann is at ann@example.com.", "content": "Her number is 555-0100."}},
		{ID: "own", Name: "interactions_get", Args: map[string]any{"request_id": "irq_1"}},
	}
	for name, testCase := range map[string]struct {
		delivering bool
		state      map[string]any
		want       []string
	}{
		"a delivery of irq_1":          {true, deliveryState(), []string{"get irq_1"}},
		"a delivery naming no request": {true, map[string]any{OrganizationIDKey: "org_1", AgentIDKey: "agt_b", AgentNameKey: "Carl", AgentKindKey: "primary"}, nil},
		"the member's own run":         {false, deliveryState(), []string{"get irq_other", "decide irq_other", "respond irq_other", "get irq_1"}},
	} {
		t.Run(name, func(t *testing.T) {
			service := &pinnedInteractions{}
			toolbox, err := InteractionToolbox(InteractionConfig{Service: service, Delivering: testCase.delivering})
			if err != nil {
				t.Fatal(err)
			}
			llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
				if hasFunctionResponse(request) {
					return genai.NewContentFromText("Done.", genai.RoleModel)
				}
				return functionCalls(calls...)
			})
			running := unattendedHarness
			if testCase.delivering {
				running = deliveringHarness
			}
			running(t, llm, toolbox).turn("user_b", testCase.state, "Deliver irq_1.")
			slices.Sort(service.asked)
			want := slices.Sorted(slices.Values(testCase.want))
			if !slices.Equal(service.asked, want) {
				t.Fatalf("the ledger was asked %v, want %v", service.asked, want)
			}
		})
	}
}

// TestADeliveryRunCannotBrowseTheInbox: the model delivering one request is
// offered its interactions tools from its first call, and interactions_inbox
// is not among them, whatever it might want to look at.
func TestADeliveryRunCannotBrowseTheInbox(t *testing.T) {
	delivering, err := InteractionToolbox(InteractionConfig{Service: &fakeInteractions{}, Delivering: true})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Read it.", genai.RoleModel)
	})
	deliveringHarness(t, llm, delivering).turn("user_b", deliveryState(), "Deliver irq_1.")
	declared := declaredFunctions(llm.requestsFor(RootName)[0])
	if !slices.Contains(declared, "interactions_get") {
		t.Fatalf("the first call declared %v, want interactions_get among them", declared)
	}
	for _, browsing := range []string{"interactions_inbox", "interactions_sent", "interactions_create"} {
		if slices.Contains(declared, browsing) {
			t.Fatalf("a delivery run was offered %s: %v", browsing, declared)
		}
	}
}

// recordingInteractions records every decision and answer, as the ledger
// does: the request takes the status the decision or answer gives it.
type recordingInteractions struct{ fakeInteractions }

func (*recordingInteractions) Decide(_ context.Context, _ InteractionActor, id string, input InteractionDecisionInput) (Interaction, error) {
	return Interaction{ID: id, Status: input.Decision}, nil
}

func (*recordingInteractions) Respond(_ context.Context, _ InteractionActor, id string, input InteractionResponseInput) (Interaction, error) {
	return Interaction{ID: id, Status: input.Status}, nil
}

// TestADeliveryRunEndsOnceItsAnswerIsRecorded: recording the answer, or a
// decision, is the last thing a delivery run does. No model call follows to
// tell a member who is not there what the ledger already holds, and a
// delivery that approves work leaves it to a run of its own. A member's own
// run carries on to say what it did.
func TestADeliveryRunEndsOnceItsAnswerIsRecorded(t *testing.T) {
	answer := &genai.FunctionCall{ID: "answer", Name: "interactions_respond", Args: map[string]any{
		"request_id": "irq_1", "status": "completed", "summary": "Reviewed.", "content": "The plan is ready.",
	}}
	decision := func(decided string) *genai.FunctionCall {
		return &genai.FunctionCall{ID: "decide", Name: "interactions_decide", Args: map[string]any{
			"request_id": "irq_1", "decision": decided, "message": "Understood.",
		}}
	}
	for name, testCase := range map[string]struct {
		delivering bool
		purpose    string
		call       *genai.FunctionCall
		calls      int
	}{
		"a delivery answering":         {true, DeliveryWorks, answer, 1},
		"a delivery rejecting":         {true, DeliveryDecides, decision("rejected"), 1},
		"a delivery approving":         {true, DeliveryDecides, decision("approved"), 1},
		"the member's own run answers": {false, DeliveryWorks, answer, 2},
	} {
		t.Run(name, func(t *testing.T) {
			toolbox, err := InteractionToolbox(InteractionConfig{Service: &recordingInteractions{}, Delivering: testCase.delivering})
			if err != nil {
				t.Fatal(err)
			}
			llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
				if hasFunctionResponse(request) {
					return genai.NewContentFromText("I answered Crew's request for you.", genai.RoleModel)
				}
				return functionCalls(testCase.call)
			})
			running := unattendedHarness
			if testCase.delivering {
				running = deliveringHarness
			}
			said := running(t, llm, toolbox).turn("user_b", deliveryStateFor(testCase.purpose), "Deliver irq_1.")
			if calls := len(llm.requestsFor(RootName)); calls != testCase.calls {
				t.Fatalf("the model was called %d times, want %d", calls, testCase.calls)
			}
			if ended := testCase.calls == 1; ended != (said == "") {
				t.Fatalf("the run said %q, want a closing message only where the run carries on", said)
			}
		})
	}
}

// TestASpecialistsDeliveryRunEndsOnceItsAnswerIsRecorded: a persistent
// specialist, whose runs otherwise end on their JSON result, ends a delivery
// run on the recorded answer too, with no result written after it.
func TestASpecialistsDeliveryRunEndsOnceItsAnswerIsRecorded(t *testing.T) {
	toolbox, err := InteractionToolbox(InteractionConfig{Service: &recordingInteractions{}, Delivering: true})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if hasFunctionResponse(request) {
			return genai.NewContentFromText(`{"status":"completed","summary":"Answered Crew.","tools_used":["interactions_respond"],"artifacts":[],"handoffs":[]}`, genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "answer", Name: "interactions_respond", Args: map[string]any{
			"request_id": "irq_1", "status": "completed", "summary": "Reviewed.", "content": "The plan is ready.",
		}})
	})
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	specialist, err := ManagedDelivering(router, []Toolbox{toolbox}, ManagedSpec{
		ID: "agt_sales", Name: "Sales", Description: "Reviews pipeline", Instructions: "Answer requests about the pipeline.",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{AppName: "managed_agt_sales", Agent: specialist, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: "managed_agt_sales", UserID: "user_b", State: deliveryState()})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range turns.Run(context.Background(), "user_b", created.Session.ID(), genai.NewContentFromText("Deliver irq_1.", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run the specialist: %v", err)
		}
	}
	if calls := len(llm.requestsFor("subagent")); calls != 1 {
		t.Fatalf("the specialist's model was called %d times, want once", calls)
	}
}
