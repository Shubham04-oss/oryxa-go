package agents

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
)

// deliveringHarness runs Krude as a run delivering one interaction runs it.
func deliveringHarness(t *testing.T, llm model.LLM, toolboxes ...Toolbox) *harness {
	t.Helper()
	return thinkingHarness(t, llm, "", toolboxes...)
}

// thinkingHarness is deliveringHarness with its model asked to think at
// thinking.
func thinkingHarness(t *testing.T, llm model.LLM, thinking genai.ThinkingLevel, toolboxes ...Toolbox) *harness {
	t.Helper()
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	root, err := DeliveringRoot(router, toolboxes, thinking)
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

// approvedReach is what approved work may reach, as the assembly gives it:
// the member's apps and knowledge, read-only, and the one request.
func approvedReach(t *testing.T) []Toolbox {
	t.Helper()
	interactions, err := InteractionToolbox(InteractionConfig{Service: &fakeInteractions{}, Delivering: true})
	if err != nil {
		t.Fatal(err)
	}
	return []Toolbox{
		{Name: "apps", Description: "The member's apps.", Instructions: "APPS SKILL: read before you answer.", Toolset: staticToolset{echoTool(t, "apps_run_read_tool")}},
		{Name: "knowledge", Description: "Documents.", Instructions: "KNOWLEDGE SKILL: cite passages.", Toolset: staticToolset{echoTool(t, "knowledge_search")}},
		interactions,
	}
}

// discovery is every tool that finds or loads toolboxes, and lookups every
// tool a delivery run is not offered for looking up what it is already told.
var (
	discovery = []string{"list_tools", "load_tools"}
	lookups   = append([]string{"get_user_details"}, discovery...)
)

// memberState is the session of the member whose agent is woken, as it is
// seeded from their auth record.
func memberState(agentID, agentName, kind string) map[string]any {
	return map[string]any{
		UserNameKey: "Org Admin", UserAboutKey: "an admin of their organization", UserEmailKey: "admin@sim.oryxa",
		UserRoleKey: "admin", OrgNameKey: "Sim Org", OrganizationIDKey: "org_1",
		AgentIDKey: agentID, AgentNameKey: agentName, AgentKindKey: kind,
	}
}

// memberLines are what get_user_details would answer, as the instruction
// carries them.
var memberLines = []string{"- Name: Org Admin", "- About: an admin of their organization", "- Email: admin@sim.oryxa", "- Role: admin", "- Organization: Sim Org", "- Organization ID: org_1"}

// TestADeliveryRunStartsWithAllItMayReach: the model delivering an approved
// request is offered every tool of its reach, with every skill, on its very
// first call, whatever its session loaded, and has nothing to list or load,
// so no model call goes to finding its tools. Nor is it offered
// get_user_details: everything that would answer is in its instruction.
func TestADeliveryRunStartsWithAllItMayReach(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Done.", genai.RoleModel)
	})
	// Nothing is loaded in the session: the run's reach is its own.
	state := memberState("agt_b", "Carl", "primary")
	maps.Copy(state, DeliveryState(Delivery{RequestID: "irq_1", Purpose: DeliveryWorks}))
	deliveringHarness(t, llm, approvedReach(t)...).turn("user_b", state, "Deliver irq_1.")

	first := llm.requestsFor(RootName)[0]
	declared := declaredFunctions(first)
	for _, want := range []string{"apps_run_read_tool", "knowledge_search", "interactions_get", "interactions_respond"} {
		if !slices.Contains(declared, want) {
			t.Fatalf("the first call declared %v, want %s among them", declared, want)
		}
	}
	if slices.Contains(declared, "interactions_decide") {
		t.Fatalf("approved work was offered interactions_decide: %v", declared)
	}
	for _, looking := range lookups {
		if slices.Contains(declared, looking) {
			t.Fatalf("a delivery run was offered %s: %v", looking, declared)
		}
	}
	instruction := text(first.Config.SystemInstruction)
	for _, want := range append([]string{"APPS SKILL", "KNOWLEDGE SKILL", "# Agent interactions", "already loaded"}, memberLines...) {
		if !strings.Contains(instruction, want) {
			t.Fatalf("the first call's instruction lacks %q:\n%s", want, instruction)
		}
	}
	for _, looking := range lookups {
		if strings.Contains(instruction, looking) {
			t.Fatalf("the instruction still sends the model to %s:\n%s", looking, instruction)
		}
	}
}

// TestASpecialistsDeliveryRunStartsWithAllItMayReach: a persistent
// specialist delivering a request starts the same way, and its result
// contract no longer tells it to load toolboxes.
func TestASpecialistsDeliveryRunStartsWithAllItMayReach(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText(`{"status":"completed","summary":"Answered.","tools_used":[],"artifacts":[],"handoffs":[]}`, genai.RoleModel)
	})
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	specialist, err := ManagedDelivering(router, approvedReach(t), ManagedSpec{
		ID: "agt_sales", Name: "Sales", Description: "Reviews pipeline", Instructions: "Answer requests about the pipeline.",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	state := memberState("agt_sales", "Sales", "specialist")
	maps.Copy(state, DeliveryState(Delivery{RequestID: "irq_1", Purpose: DeliveryWorks}))
	runSpecialist(t, specialist, state)

	first := llm.requestsFor("subagent")[0]
	declared := declaredFunctions(first)
	for _, want := range []string{"apps_run_read_tool", "knowledge_search", "interactions_get", "interactions_respond"} {
		if !slices.Contains(declared, want) {
			t.Fatalf("the first call declared %v, want %s among them", declared, want)
		}
	}
	for _, looking := range lookups {
		if slices.Contains(declared, looking) {
			t.Fatalf("a specialist's delivery run was offered %s: %v", looking, declared)
		}
	}
	instruction := text(first.Config.SystemInstruction)
	if !strings.Contains(instruction, "APPS SKILL") || !strings.Contains(instruction, "already loaded") || strings.Contains(instruction, "Load only toolboxes") {
		t.Fatalf("the specialist's instruction does not start it loaded:\n%s", instruction)
	}
	for _, want := range memberLines {
		if !strings.Contains(instruction, want) {
			t.Fatalf("the specialist's instruction lacks %q, which get_user_details would have answered:\n%s", want, instruction)
		}
	}
}

// TestADeliveryRunLeavesOutAToolboxItCannotOfferSafely: a toolbox whose
// tools are not named after it, which load_tools would refuse, is left out
// of a delivery run, and so is one whose tools cannot be listed right now;
// the run goes on with the rest.
func TestADeliveryRunLeavesOutAToolboxItCannotOfferSafely(t *testing.T) {
	var calls int
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Done.", genai.RoleModel)
	})
	answer := deliveringHarness(t, llm,
		Toolbox{Name: "bad", Description: "Misnamed.", Toolset: staticToolset{echoTool(t, "add_thing")}},
		Toolbox{Name: "remote", Description: "Down.", Toolset: failingToolset{calls: &calls}},
		Toolbox{Name: "notes", Description: "Notes.", Toolset: staticToolset{echoTool(t, "notes_add")}},
	).turn("user_b", nil, "Deliver irq_1.")
	if answer != "Done." {
		t.Fatalf("answer = %q", answer)
	}
	declared := declaredFunctions(llm.requestsFor(RootName)[0])
	if slices.Contains(declared, "add_thing") || !slices.Contains(declared, "notes_add") {
		t.Fatalf("declared %v, want notes_add and never the misnamed add_thing", declared)
	}
}

// TestAWakeUpStillFindsItsToolboxes: an app wake-up keeps list_tools,
// load_tools and get_user_details, and loads what it needs as it always has;
// only a delivery starts loaded.
func TestAWakeUpStillFindsItsToolboxes(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Two emails came in.", genai.RoleModel)
	})
	unattendedHarness(t, llm, approvedReach(t)...).turn("member_1", nil, "Events arrived.")
	first := llm.requestsFor(RootName)[0]
	declared := declaredFunctions(first)
	for _, looking := range lookups {
		if !slices.Contains(declared, looking) {
			t.Fatalf("a wake-up lost %s: %v", looking, declared)
		}
	}
	if slices.Contains(declared, "apps_run_read_tool") || strings.Contains(text(first.Config.SystemInstruction), "APPS SKILL") {
		t.Fatalf("a wake-up started with the apps loaded: %v", declared)
	}
}

// runSpecialist runs one turn of a persistent specialist in a new session
// starting with state.
func runSpecialist(t *testing.T, specialist agent.Agent, state map[string]any) {
	t.Helper()
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{AppName: "managed_agt_sales", Agent: specialist, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: "managed_agt_sales", UserID: "user_b", State: state})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range turns.Run(context.Background(), "user_b", created.Session.ID(), genai.NewContentFromText("Deliver irq_1.", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run the specialist: %v", err)
		}
	}
}

// thinkingOf is the thinking level a model request asks for, or none.
func thinkingOf(request *model.LLMRequest) genai.ThinkingLevel {
	if request.Config == nil || request.Config.ThinkingConfig == nil {
		return ""
	}
	return request.Config.ThinkingConfig.ThinkingLevel
}

// TestADeliveryRunThinksAtTheLevelItIsGiven: every model call of a delivery
// run, Krude's or a specialist's, asks for the thinking level the run was
// built with, and a run built with none sends no thinking config at all, so
// the model thinks as it does by default.
func TestADeliveryRunThinksAtTheLevelItIsGiven(t *testing.T) {
	for _, thinking := range []genai.ThinkingLevel{genai.ThinkingLevelLow, ""} {
		// Krude reads the request, then answers: two calls.
		llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
			if hasFunctionResponse(request) {
				return genai.NewContentFromText("Read it.", genai.RoleModel)
			}
			return functionCalls(&genai.FunctionCall{ID: "read", Name: "interactions_get", Args: map[string]any{"request_id": "irq_1"}})
		})
		thinkingHarness(t, llm, thinking, approvedReach(t)...).turn("user_b", memberState("agt_b", "Carl", "primary"), "Deliver irq_1.")
		calls := llm.requestsFor(RootName)
		if len(calls) != 2 {
			t.Fatalf("the model was called %d times, want twice", len(calls))
		}
		for index, call := range calls {
			if got := thinkingOf(call); got != thinking {
				t.Errorf("Krude's call %d asked for thinking %q, want %q", index, got, thinking)
			}
			if thinking == "" && call.Config.ThinkingConfig != nil {
				t.Errorf("Krude's call %d carried a thinking config with none set: %+v", index, call.Config.ThinkingConfig)
			}
		}

		specialistLLM := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
			return genai.NewContentFromText(`{"status":"completed","summary":"Answered.","tools_used":[],"artifacts":[],"handoffs":[]}`, genai.RoleModel)
		})
		router, err := modelrouter.New(specialistLLM, specialistLLM)
		if err != nil {
			t.Fatal(err)
		}
		specialist, err := ManagedDelivering(router, approvedReach(t), ManagedSpec{ID: "agt_sales", Name: "Sales", Instructions: "Answer requests."}, thinking)
		if err != nil {
			t.Fatal(err)
		}
		runSpecialist(t, specialist, memberState("agt_sales", "Sales", "specialist"))
		first := specialistLLM.requestsFor("subagent")[0]
		if got := thinkingOf(first); got != thinking || (thinking == "" && first.Config.ThinkingConfig != nil) {
			t.Errorf("a specialist's delivery asked for thinking %q (%+v), want %q", got, first.Config.ThinkingConfig, thinking)
		}
	}
}
