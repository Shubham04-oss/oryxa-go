package agents

import (
	"context"
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
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	root, err := DeliveringRoot(router, toolboxes)
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
	interactions, err := InteractionToolbox(InteractionConfig{Service: &fakeInteractions{}, Answering: true, Delivering: true})
	if err != nil {
		t.Fatal(err)
	}
	return []Toolbox{
		{Name: "apps", Description: "The member's apps.", Instructions: "APPS SKILL: read before you answer.", Toolset: staticToolset{echoTool(t, "apps_run_read_tool")}},
		{Name: "knowledge", Description: "Documents.", Instructions: "KNOWLEDGE SKILL: cite passages.", Toolset: staticToolset{echoTool(t, "knowledge_search")}},
		interactions,
	}
}

// discovery is every tool that finds or loads toolboxes.
var discovery = []string{"list_tools", "load_tools"}

// TestADeliveryRunStartsWithAllItMayReach: the model delivering an approved
// request is offered every tool of its reach, with every skill, on its very
// first call, whatever its session loaded, and has nothing to list or load,
// so no model call goes to finding its tools.
func TestADeliveryRunStartsWithAllItMayReach(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Done.", genai.RoleModel)
	})
	// Nothing is loaded in the session: the run's reach is its own.
	deliveringHarness(t, llm, approvedReach(t)...).turn("user_b", map[string]any{
		OrganizationIDKey: "org_1", AgentIDKey: "agt_b", AgentNameKey: "Carl", AgentKindKey: "primary",
	}, "Deliver irq_1.")

	first := llm.requestsFor(RootName)[0]
	declared := declaredFunctions(first)
	for _, want := range []string{"apps_run_read_tool", "knowledge_search", "interactions_get", "interactions_decide", "interactions_respond"} {
		if !slices.Contains(declared, want) {
			t.Fatalf("the first call declared %v, want %s among them", declared, want)
		}
	}
	for _, finding := range discovery {
		if slices.Contains(declared, finding) {
			t.Fatalf("a delivery run was offered %s: %v", finding, declared)
		}
	}
	instruction := text(first.Config.SystemInstruction)
	for _, want := range []string{"APPS SKILL", "KNOWLEDGE SKILL", "# Agent interactions", "already loaded"} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("the first call's instruction lacks %q:\n%s", want, instruction)
		}
	}
	if strings.Contains(instruction, "list_tools") {
		t.Fatalf("the instruction still sends the model to list_tools:\n%s", instruction)
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
	})
	if err != nil {
		t.Fatal(err)
	}
	runSpecialist(t, specialist, map[string]any{OrganizationIDKey: "org_1", AgentIDKey: "agt_sales", AgentNameKey: "Sales", AgentKindKey: "specialist"})

	first := llm.requestsFor("subagent")[0]
	declared := declaredFunctions(first)
	for _, want := range []string{"apps_run_read_tool", "knowledge_search", "interactions_get", "interactions_respond"} {
		if !slices.Contains(declared, want) {
			t.Fatalf("the first call declared %v, want %s among them", declared, want)
		}
	}
	for _, finding := range discovery {
		if slices.Contains(declared, finding) {
			t.Fatalf("a specialist's delivery run was offered %s: %v", finding, declared)
		}
	}
	instruction := text(first.Config.SystemInstruction)
	if !strings.Contains(instruction, "APPS SKILL") || !strings.Contains(instruction, "already loaded") || strings.Contains(instruction, "Load only toolboxes") {
		t.Fatalf("the specialist's instruction does not start it loaded:\n%s", instruction)
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

// TestAWakeUpStillFindsItsToolboxes: an app wake-up keeps list_tools and
// load_tools, and loads what it needs as it always has; only a delivery
// starts loaded.
func TestAWakeUpStillFindsItsToolboxes(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Two emails came in.", genai.RoleModel)
	})
	unattendedHarness(t, llm, approvedReach(t)...).turn("member_1", nil, "Events arrived.")
	first := llm.requestsFor(RootName)[0]
	declared := declaredFunctions(first)
	for _, finding := range discovery {
		if !slices.Contains(declared, finding) {
			t.Fatalf("a wake-up lost %s: %v", finding, declared)
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
