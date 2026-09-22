package agents

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"cloud.google.com/go/auth"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
)

// treeModel plays every agent in the tree. It tells the agents apart by their
// instructions, keeps each agent's requests, and answers with respond.
type treeModel struct {
	respond  func(agentName string, request *model.LLMRequest) *genai.Content
	mu       sync.Mutex
	requests map[string][]*model.LLMRequest
}

func newTreeModel(respond func(agentName string, request *model.LLMRequest) *genai.Content) *treeModel {
	return &treeModel{respond: respond, requests: map[string][]*model.LLMRequest{}}
}

func (m *treeModel) Name() string { return "tree" }

func (m *treeModel) GenerateContent(_ context.Context, request *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	instruction := text(request.Config.SystemInstruction)
	name := "subagent"
	switch {
	case strings.Contains(instruction, "You are Krude"):
		name = RootName
	case strings.Contains(instruction, "You are a temporary helper"):
		name = "helper"
	case strings.Contains(instruction, "You are the planner sub-agent"):
		name = plannerName
	case strings.Contains(instruction, "You are the researcher sub-agent"):
		name = researcherName
	}
	m.mu.Lock()
	m.requests[name] = append(m.requests[name], request)
	m.mu.Unlock()
	content := m.respond(name, request)
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: content, TurnComplete: true}, nil)
	}
}

func (m *treeModel) requestsFor(agentName string) []*model.LLMRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[agentName]
}

func TestKrudeAnswersATurn(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Hello from Krude.", genai.RoleModel)
	})
	answer := runTurn(t, llm, "hi")

	if answer != "Hello from Krude." {
		t.Fatalf("answer = %q", answer)
	}
	instruction := text(llm.requestsFor(RootName)[0].Config.SystemInstruction)
	want := "You are Krude. You will be assisting the member you are talking with in their organization."
	if !strings.Contains(instruction, want) {
		t.Fatalf("instruction is missing %q:\n%s", want, instruction)
	}
	for _, guidance := range []string{"load knowledge and search", "relevant connected app through apps", "scheduled snapshot", "private app content to the web researcher"} {
		if !strings.Contains(instruction, guidance) {
			t.Fatalf("primary research guidance is missing %q", guidance)
		}
	}
	declared := declaredFunctions(llm.requestsFor(RootName)[0])
	for _, name := range []string{plannerName, researcherName, "list_tools", "load_tools", "get_user_details", "list_helpers", "spawn_helper", "create_subagent", "list_subagents", "remove_subagent", "load_artifacts"} {
		if !slices.Contains(declared, name) {
			t.Fatalf("Krude can call %v, missing %q", declared, name)
		}
	}
	for _, removed := range []string{"teams", "set_nickname", "list_skills", "load_skill", "load_memory"} {
		if slices.Contains(declared, removed) {
			t.Fatalf("Krude can still call the stripped %q", removed)
		}
	}
}

func TestManagedSpecialistCanRunAsItsPersistentThreadRoot(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText(`{"status":"completed","summary":"done","tools_used":[],"artifacts":[],"handoffs":[]}`, genai.RoleModel)
	})
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	specialist, err := Managed(router, nil, false, ManagedSpec{
		ID: "agt_sales", Name: "Sales", Description: "Reviews pipeline", Instructions: "Complete the assigned sales task.",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{
		AppName: "managed_agt_sales", Agent: specialist, SessionService: sessions,
		ArtifactService: artifact.InMemoryService(),
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: "managed_agt_sales", UserID: "user_1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range turns.Run(context.Background(), "user_1", created.Session.ID(), genai.NewContentFromText("Review pipeline", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run managed root: %v", err)
		}
	}
	if len(llm.requestsFor("subagent")) != 1 {
		t.Fatalf("managed model requests = %d, want 1", len(llm.requestsFor("subagent")))
	}
	instruction := text(llm.requestsFor("subagent")[0].Config.SystemInstruction)
	for _, required := range []string{
		"The summary is the deliverable",
		"Never use a meta-summary",
		"only when the exact action tool returns success",
		"Never simulate an action or tool call, invent an ID or status",
		"including failed calls",
	} {
		if !strings.Contains(instruction, required) {
			t.Fatalf("managed instruction is missing %q:\n%s", required, instruction)
		}
	}
}

// TestKrudeReadsItsMemoryOnlyWhenItHasOne covers the other half of wiring a
// memory service in: without load_memory nothing Krude remembers is ever
// read back, and with no memory service behind it the tool is there to be
// called and fails every time.
func TestKrudeReadsItsMemoryOnlyWhenItHasOne(t *testing.T) {
	for _, remembers := range []bool{true, false} {
		t.Run(map[bool]string{true: "with memory", false: "without"}[remembers], func(t *testing.T) {
			llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
				return genai.NewContentFromText("Hello.", genai.RoleModel)
			})
			router, err := modelrouter.New(llm, llm)
			if err != nil {
				t.Fatal(err)
			}
			root, err := Root(router, nil, remembers)
			if err != nil {
				t.Fatalf("Root() error = %v", err)
			}
			sessions := session.InMemoryService()
			turns, err := runner.New(runner.Config{
				AppName: RootName, Agent: root, SessionService: sessions,
				ArtifactService: artifact.InMemoryService(), MemoryService: memory.InMemoryService(),
			})
			if err != nil {
				t.Fatal(err)
			}
			created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: RootName, UserID: "user"})
			if err != nil {
				t.Fatal(err)
			}
			for _, err := range turns.Run(context.Background(), "user", created.Session.ID(),
				genai.NewContentFromText("hi", genai.RoleUser), agent.RunConfig{}) {
				if err != nil {
					t.Fatalf("run: %v", err)
				}
			}

			declared := declaredFunctions(llm.requestsFor(RootName)[0])
			if got := slices.Contains(declared, "load_memory"); got != remembers {
				t.Fatalf("load_memory declared = %v with remembers = %v; Krude can call %v", got, remembers, declared)
			}
		})
	}
}

func TestKrudeInstructionReadsSessionState(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Hi Asha.", genai.RoleModel)
	})
	runTurn(t, llm, "hi", map[string]any{UserNameKey: "Asha", UserAboutKey: "a nice lady", OrgNameKey: "Acme"})

	instruction := text(llm.requestsFor(RootName)[0].Config.SystemInstruction)
	want := "You are Krude. You will be assisting Asha, a nice lady in Acme."
	if !strings.Contains(instruction, want) {
		t.Fatalf("instruction is missing %q:\n%s", want, instruction)
	}
}

func TestStateTextKeepsMemberValuesToOneShortLine(t *testing.T) {
	state := mapState{
		"name":     "  Asha \n Rao ",
		"blank":    " \n\t ",
		"number":   42,
		"too_long": strings.Repeat("k", 100),
	}
	tests := map[string]string{
		"name":     "Asha Rao",
		"blank":    "fallback",
		"number":   "fallback",
		"missing":  "fallback",
		"too_long": strings.Repeat("k", maxStateTextRunes),
	}
	for key, want := range tests {
		if got := stateText(state, key, "fallback"); got != want {
			t.Errorf("stateText(%q) = %q, want %q", key, got, want)
		}
	}
	if got := stateText(mapState{"name": strings.Repeat("é", 100)}, "name", ""); utf8.RuneCountInString(got) != maxStateTextRunes || !utf8.ValidString(got) {
		t.Errorf("multibyte value was not cut on a rune boundary: %q", got)
	}
}

func TestKrudeLoadsToolboxesWhenATaskNeedsThem(t *testing.T) {
	notes := Toolbox{Name: "notes", Description: "Notes.", Toolset: staticToolset{echoTool(t, "notes_add")}}
	misnamed := Toolbox{Name: "bad", Description: "Misnamed tools.", Toolset: staticToolset{echoTool(t, "add_thing")}}
	orphan := Toolbox{Name: "orphan", Description: "No tools yet."}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "notes_add") != "":
			return genai.NewContentFromText("Noted.", genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "call_note", Name: "notes_add", Args: map[string]any{"text": "buy milk"}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "call_load", Name: "load_tools", Args: map[string]any{"names": []any{"notes", "bad", "orphan", "nope"}}})
		}
	})
	h := newHarness(t, llm, notes, misnamed, orphan)
	if answer := h.turn("member_1", nil, "Add a note."); answer != "Noted." {
		t.Fatalf("answer = %q", answer)
	}

	// Loading, using the loaded tool and answering all happen in one turn.
	krude := llm.requestsFor(RootName)
	if len(krude) != 3 {
		t.Fatalf("Krude made %d model calls, want 3", len(krude))
	}
	if slices.Contains(declaredFunctions(krude[0]), "notes_add") {
		t.Fatal("notes tools were declared before they were loaded")
	}
	if !slices.Contains(declaredFunctions(krude[1]), "notes_add") {
		t.Fatalf("notes tools were not declared right after loading, got %v", declaredFunctions(krude[1]))
	}
	if slices.Contains(declaredFunctions(krude[1]), "add_thing") {
		t.Fatal("a misnamed toolbox was loaded")
	}
	result := functionResponse(krude[1], "load_tools")
	for _, want := range []string{"loaded:[notes]", "bad", "not named after the toolbox", "orphan", "not connected yet", "nope", "no toolbox with this name"} {
		if !strings.Contains(result, want) {
			t.Fatalf("load_tools result %s is missing %q", result, want)
		}
	}
	if note := functionResponse(krude[2], "notes_add"); !strings.Contains(note, "buy milk") {
		t.Fatalf("the loaded tool did not run, got %q", note)
	}
}

func TestKrudeUsesAToolboxLoadedBeforeTheTurn(t *testing.T) {
	notes := Toolbox{Name: "notes", Description: "Notes.", Toolset: staticToolset{echoTool(t, "notes_add")}}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if functionResponse(request, "notes_add") != "" {
			return genai.NewContentFromText("Noted again.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_note", Name: "notes_add", Args: map[string]any{"text": "buy tea"}})
	})
	h := newHarness(t, llm, notes)
	answer := h.turn("member_1", map[string]any{loadedToolsKey: []string{"notes"}}, "Add another note.")

	if answer != "Noted again." {
		t.Fatalf("answer = %q", answer)
	}
	first := declaredFunctions(llm.requestsFor(RootName)[0])
	if count := countName(first, "notes_add"); count != 1 {
		t.Fatalf("notes_add was declared %d times, want once; declarations: %v", count, first)
	}
}

func TestRootRejectsInvalidToolboxes(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Hello.", genai.RoleModel)
	})
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	for _, toolboxes := range [][]Toolbox{
		{{Name: "Bad Name"}},
		{{Name: "notes"}, {Name: "notes"}},
	} {
		if _, err := Root(router, toolboxes, false); err == nil {
			t.Fatalf("Root() error = nil for %+v", toolboxes)
		}
	}
}

func TestKrudeListsItsHelpers(t *testing.T) {
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if hasFunctionResponse(request) {
			return genai.NewContentFromText("Listed.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_list", Name: "list_helpers", Args: map[string]any{}})
	})
	runTurn(t, llm, "Who helps you?")

	result := functionResponse(llm.requestsFor(RootName)[1], "list_helpers")
	for _, want := range []string{plannerName, researcherName, "spawn_helper"} {
		if !strings.Contains(result, want) {
			t.Fatalf("list_helpers result %s is missing %q", result, want)
		}
	}
	if strings.Contains(result, "teams") {
		t.Fatalf("list_helpers still lists teams: %s", result)
	}
}

func TestKrudeSpawnsTemporaryHelpersTogether(t *testing.T) {
	llm := newTreeModel(func(agentName string, request *model.LLMRequest) *genai.Content {
		switch {
		case agentName == "helper" && strings.Contains(text(request.Config.SystemInstruction), "You draft launch notes."):
			return genai.NewContentFromText("Draft ready.", genai.RoleModel)
		case agentName == "helper":
			return genai.NewContentFromText("Facts listed.", genai.RoleModel)
		case hasFunctionResponse(request):
			return genai.NewContentFromText("Both helpers are done.", genai.RoleModel)
		default:
			return functionCalls(
				&genai.FunctionCall{ID: "call_drafter", Name: "spawn_helper", Args: map[string]any{"name": "Drafter", "instruction": "You draft launch notes.", "task": "Draft a launch note for the new planner."}},
				&genai.FunctionCall{ID: "call_checker", Name: "spawn_helper", Args: map[string]any{"name": "fact checker!", "instruction": "You list the facts a note relies on.", "task": "List the facts in the launch note."}},
			)
		}
	})
	answer := runTurn(t, llm, "Write and check a launch note.")

	if answer != "Both helpers are done." {
		t.Fatalf("answer = %q", answer)
	}
	spawned := llm.requestsFor("helper")
	if len(spawned) != 2 {
		t.Fatalf("%d helper model calls, want 2", len(spawned))
	}
	for _, request := range spawned {
		if !strings.Contains(contentsText(request), "launch note") {
			t.Fatalf("helper was not given its task: %q", contentsText(request))
		}
		for _, forbidden := range []string{"spawn_helper", "list_helpers"} {
			if slices.Contains(declaredFunctions(request), forbidden) {
				t.Fatalf("helper was offered %q", forbidden)
			}
		}
	}
	results := functionResponse(llm.requestsFor(RootName)[1], "spawn_helper")
	for _, want := range []string{"Draft ready.", "Facts listed.", "helper_drafter_", "helper_fact_checker_"} {
		if !strings.Contains(results, want) {
			t.Fatalf("spawn_helper results %s are missing %q", results, want)
		}
	}
}

func TestKrudeCreatesASubagentAndCallsIt(t *testing.T) {
	llm := newTreeModel(func(agentName string, request *model.LLMRequest) *genai.Content {
		switch {
		case agentName == "subagent":
			return genai.NewContentFromText("Two claims need sources.", genai.RoleModel)
		case functionResponse(request, "fact_checker") != "":
			return genai.NewContentFromText("The checker is done.", genai.RoleModel)
		case functionResponse(request, "create_subagent") != "":
			return functionCalls(&genai.FunctionCall{ID: "call_facts", Name: "fact_checker", Args: map[string]any{"request": "Check the launch note."}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "call_create", Name: "create_subagent", Args: map[string]any{
				"name":        "fact_checker",
				"description": "Checks the claims in a note.",
				"instruction": "You check claims and name what you cannot verify.",
			}})
		}
	})
	answer := runTurn(t, llm, "Create a fact checker and use it.")

	if answer != "The checker is done." {
		t.Fatalf("answer = %q", answer)
	}
	krude := llm.requestsFor(RootName)
	if len(krude) != 3 {
		t.Fatalf("Krude made %d model calls, want 3", len(krude))
	}
	if slices.Contains(declaredFunctions(krude[0]), "fact_checker") {
		t.Fatal("the sub-agent was declared before it was created")
	}
	if !slices.Contains(declaredFunctions(krude[1]), "fact_checker") {
		t.Fatalf("the sub-agent was not declared right after creation, got %v", declaredFunctions(krude[1]))
	}
	result := functionResponse(krude[1], "create_subagent")
	for _, want := range []string{"fact_checker", "Checks the claims in a note.", "Call fact_checker by name"} {
		if !strings.Contains(result, want) {
			t.Fatalf("create_subagent result %s is missing %q", result, want)
		}
	}

	workers := llm.requestsFor("subagent")
	if len(workers) != 1 {
		t.Fatalf("the sub-agent made %d model calls, want 1", len(workers))
	}
	if got := text(workers[0].Config.SystemInstruction); !strings.HasPrefix(got, "You check claims and name what you cannot verify.") {
		t.Fatalf("sub-agent instruction = %q", got)
	}
	if !strings.Contains(contentsText(workers[0]), "Check the launch note.") {
		t.Fatal("the sub-agent was not given its task")
	}
	if tools := declaredFunctions(workers[0]); len(tools) != 0 {
		t.Fatalf("the sub-agent was offered tools %v", tools)
	}
}

func TestKrudeCallsASubagentFromSessionState(t *testing.T) {
	llm := newTreeModel(func(agentName string, request *model.LLMRequest) *genai.Content {
		if agentName == "subagent" {
			return genai.NewContentFromText("The note is consistent.", genai.RoleModel)
		}
		if functionResponse(request, "note_reviewer") != "" {
			return genai.NewContentFromText("Reviewed.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_review", Name: "note_reviewer", Args: map[string]any{"request": "Review the note."}})
	})
	state := map[string]any{subagentsKey: []subagent{
		{Name: "note_reviewer", Description: "Reviews notes.", Instruction: "You review notes for consistency."},
	}}
	answer := runTurn(t, llm, "Have the reviewer look at it.", state)

	if answer != "Reviewed." {
		t.Fatalf("answer = %q", answer)
	}
	if !slices.Contains(declaredFunctions(llm.requestsFor(RootName)[0]), "note_reviewer") {
		t.Fatal("a sub-agent registered before the turn was not declared")
	}
	workers := llm.requestsFor("subagent")
	if len(workers) != 1 || !strings.Contains(contentsText(workers[0]), "Review the note.") {
		t.Fatalf("the stored sub-agent did not run with its task: %v", workers)
	}
}

func TestKrudeListsAndRemovesSubagents(t *testing.T) {
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "remove_subagent") != "":
			return genai.NewContentFromText("Removed.", genai.RoleModel)
		case functionResponse(request, "list_subagents") != "":
			return functionCalls(&genai.FunctionCall{ID: "call_remove", Name: "remove_subagent", Args: map[string]any{"name": "note_reviewer"}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "call_list", Name: "list_subagents", Args: map[string]any{}})
		}
	})
	state := map[string]any{subagentsKey: []subagent{
		{Name: "note_reviewer", Description: "Reviews notes.", Instruction: "You review notes."},
	}}
	answer := runTurn(t, llm, "Who is on your team, and drop the reviewer.", state)

	if answer != "Removed." {
		t.Fatalf("answer = %q", answer)
	}
	krude := llm.requestsFor(RootName)
	if listed := functionResponse(krude[1], "list_subagents"); !strings.Contains(listed, "note_reviewer") {
		t.Fatalf("list_subagents result %s is missing the sub-agent", listed)
	}
	if removed := functionResponse(krude[2], "remove_subagent"); !strings.Contains(removed, "note_reviewer") {
		t.Fatalf("remove_subagent result %s is missing the name", removed)
	}
	last := declaredFunctions(krude[len(krude)-1])
	if slices.Contains(last, "note_reviewer") {
		t.Fatalf("the removed sub-agent is still declared: %v", last)
	}
}

func TestKrudeRefusesASubagentNamedLikeATool(t *testing.T) {
	notes := Toolbox{Name: "notes", Description: "Notes.", Toolset: staticToolset{echoTool(t, "notes_add")}}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if functionResponse(request, "create_subagent") != "" {
			return genai.NewContentFromText("Could not.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_create", Name: "create_subagent", Args: map[string]any{
			"name": "notes_add", "description": "Adds notes.", "instruction": "You add notes.",
		}})
	})
	h := newHarness(t, llm, notes)
	h.turn("member_1", nil, "Create a notes helper.")

	result := functionResponse(llm.requestsFor(RootName)[1], "create_subagent")
	if !strings.Contains(result, "kept for the notes toolbox") {
		t.Fatalf("create_subagent result %s did not refuse the toolbox name", result)
	}
}

// failingToolset is a toolbox whose tools cannot be listed, such as a remote
// one that is down; calls reports each attempt.
type failingToolset struct{ calls *int }

func (f failingToolset) Name() string { return "failing" }

func (f failingToolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	*f.calls++
	return nil, errors.New("dial tcp: connection refused")
}

func TestKrudeAnswersWhenALoadedToolboxCannotListItsTools(t *testing.T) {
	var calls int
	remote := Toolbox{Name: "remote", Description: "Remote tools.", Toolset: failingToolset{calls: &calls}}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if functionResponse(request, "load_tools") != "" {
			return genai.NewContentFromText("Remote tools are down.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_load", Name: "load_tools", Args: map[string]any{"names": []any{"remote"}}})
	})
	h := newHarness(t, llm, remote)

	// Loaded in an earlier turn: the turn goes on without its tools, and
	// loading it again says why they are missing.
	if answer := h.turn("member_1", map[string]any{loadedToolsKey: []string{"remote"}}, "Use the remote tools."); answer != "Remote tools are down." {
		t.Fatalf("answer = %q", answer)
	}
	result := functionResponse(llm.requestsFor(RootName)[1], "load_tools")
	if !strings.Contains(result, "could not be listed right now") || strings.Contains(result, "dial tcp") {
		t.Fatalf("load_tools result %s, want the toolbox unavailable without transport detail", result)
	}
}

func TestKrudeCreatesASubagentWithoutListingToolboxes(t *testing.T) {
	var calls int
	remote := Toolbox{Name: "remote", Description: "Remote tools.", Toolset: failingToolset{calls: &calls}}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if functionResponse(request, "create_subagent") != "" {
			return genai.NewContentFromText("Created.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_create", Name: "create_subagent", Args: map[string]any{
			"name": "note_reviewer", "description": "Reviews notes.", "instruction": "You review notes.",
		}})
	})
	h := newHarness(t, llm, remote)
	h.turn("member_1", nil, "Create a reviewer.")

	if result := functionResponse(llm.requestsFor(RootName)[1], "create_subagent"); !strings.Contains(result, "note_reviewer") || strings.Contains(result, "error") {
		t.Fatalf("create_subagent result %s, want the sub-agent created", result)
	}
	if calls != 0 {
		t.Fatalf("creating a sub-agent listed a toolbox's tools %d times, want none", calls)
	}
}

func TestSubagentsFromStateDecodesStoredValues(t *testing.T) {
	state := mapState{subagentsKey: []any{
		map[string]any{"name": "fact_checker", "description": "Checks claims.", "instruction": "You check claims."},
		map[string]any{"name": 42},
		"nonsense",
	}}
	specs := subagentsFromState(state)
	if len(specs) != 1 || specs[0].Name != "fact_checker" || specs[0].Instruction != "You check claims." {
		t.Fatalf("subagentsFromState() = %+v", specs)
	}
}

func TestValidateSubagentName(t *testing.T) {
	for _, valid := range []string{"fact_checker", "reviewer2", "a"} {
		if err := validateSubagentName(valid); err != nil {
			t.Errorf("validateSubagentName(%q) = %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "Fact Checker", "planner", "list_tools", "create_subagent", "krude", "user", "9lives", strings.Repeat("a", maxSubagentNameRunes+1)} {
		if err := validateSubagentName(invalid); err == nil {
			t.Errorf("validateSubagentName(%q) = nil", invalid)
		}
	}
}

func TestInstructionCarriesMemberDetails(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Hi Asha.", genai.RoleModel)
	})
	runTurn(t, llm, "hi", map[string]any{
		UserNameKey: "Asha", UserAboutKey: "a product designer", UserEmailKey: "asha@acme.dev",
		UserRoleKey: "admin", OrgNameKey: "Acme", OrganizationIDKey: "org_1",
	})

	instruction := text(llm.requestsFor(RootName)[0].Config.SystemInstruction)
	for _, want := range []string{
		"- Name: Asha",
		"- About: a product designer",
		"- Email: asha@acme.dev",
		"- Role: admin",
		"- Organization: Acme",
		"- Organization ID: org_1",
	} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("instruction is missing %q:\n%s", want, instruction)
		}
	}
}

func TestLoadedToolboxSkillJoinsTheInstruction(t *testing.T) {
	notes := Toolbox{Name: "notes", Description: "Notes.", Instructions: "Use notes_add for every note.", Toolset: staticToolset{echoTool(t, "notes_add")}}
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Noted.", genai.RoleModel)
	})
	h := newHarness(t, llm, notes)

	h.turn("member_1", nil, "hi")
	instruction := text(llm.requestsFor(RootName)[0].Config.SystemInstruction)
	if strings.Contains(instruction, "## Loaded toolboxes") {
		t.Fatal("a skill was in the instruction before its toolbox was loaded")
	}

	h.turn("member_1", map[string]any{loadedToolsKey: []string{"notes"}}, "hi")
	instruction = text(llm.requestsFor(RootName)[1].Config.SystemInstruction)
	for _, want := range []string{"## Loaded toolboxes", "### notes", "Use notes_add for every note."} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("instruction is missing %q:\n%s", want, instruction)
		}
	}
}

func TestGetUserDetailsReturnsSeededState(t *testing.T) {
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if hasFunctionResponse(request) {
			return genai.NewContentFromText("Checked.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_details", Name: "get_user_details", Args: map[string]any{}})
	})
	runTurn(t, llm, "who am I?", map[string]any{
		UserNameKey: "Asha", UserAboutKey: "a product designer", UserEmailKey: "asha@acme.dev",
		UserRoleKey: "admin", OrgNameKey: "Acme", OrganizationIDKey: "org_1",
	})

	result := functionResponse(llm.requestsFor(RootName)[1], "get_user_details")
	for _, want := range []string{"Asha", "a product designer", "asha@acme.dev", "admin", "Acme", "org_1"} {
		if !strings.Contains(result, want) {
			t.Fatalf("get_user_details result %s is missing %q", result, want)
		}
	}
}

func TestToolboxConstructorsCarryTheirSkills(t *testing.T) {
	toolboxes := map[string]Toolbox{
		"sandbox": SandboxToolbox(staticToolset{echoTool(t, "sandbox_execute_code")}),
		"browser": BrowserToolbox(staticToolset{echoTool(t, "browser_navigate")}),
	}
	for name, box := range toolboxes {
		if box.Name != name {
			t.Errorf("toolbox name = %q, want %q", box.Name, name)
		}
		if box.Description == "" || strings.TrimSpace(box.Instructions) == "" {
			t.Errorf("the %s toolbox is missing its description or skill", name)
		}
	}
}

func TestRootRoutesEachRoleToItsTier(t *testing.T) {
	main := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "fact_checker") != "":
			return genai.NewContentFromText("Done on main.", genai.RoleModel)
		case functionResponse(request, "create_subagent") != "":
			return functionCalls(&genai.FunctionCall{ID: "call_facts", Name: "fact_checker", Args: map[string]any{"request": "Check it."}})
		default:
			return functionCalls(
				&genai.FunctionCall{ID: "call_planner", Name: plannerName, Args: map[string]any{"request": "Plan it."}},
				&genai.FunctionCall{ID: "call_create", Name: "create_subagent", Args: map[string]any{
					"name": "fact_checker", "description": "Checks facts.", "instruction": "You check facts.",
				}},
			)
		}
	})
	lite := newTreeModel(func(agentName string, _ *model.LLMRequest) *genai.Content {
		if agentName == plannerName {
			return genai.NewContentFromText(`{"steps":[{"agent":"krude","task":"answer"}]}`, genai.RoleModel)
		}
		return genai.NewContentFromText("Facts checked.", genai.RoleModel)
	})
	router, err := modelrouter.New(main, lite)
	if err != nil {
		t.Fatal(err)
	}
	h := newRouterHarness(t, router)

	if answer := h.turn("member_1", nil, "Plan and check."); answer != "Done on main." {
		t.Fatalf("answer = %q", answer)
	}
	if len(lite.requestsFor(plannerName)) != 1 || len(main.requestsFor(plannerName)) != 0 {
		t.Fatalf("planner ran %d times on lite and %d on main, want 1 and 0", len(lite.requestsFor(plannerName)), len(main.requestsFor(plannerName)))
	}
	if len(lite.requestsFor("subagent")) != 1 || len(main.requestsFor("subagent")) != 0 {
		t.Fatalf("the sub-agent ran %d times on lite and %d on main, want 1 and 0", len(lite.requestsFor("subagent")), len(main.requestsFor("subagent")))
	}
}

func TestKrudeRunsSingleTurnSubAgentsTogether(t *testing.T) {
	llm := newTreeModel(func(agentName string, request *model.LLMRequest) *genai.Content {
		switch {
		case agentName == plannerName:
			return genai.NewContentFromText(`{"steps":[{"agent":"researcher","task":"Find when Oryxa launched.","depends_on":[]}]}`, genai.RoleModel)
		case agentName == researcherName:
			return genai.NewContentFromText("Oryxa launched in 2026.", genai.RoleModel)
		case hasFunctionResponse(request):
			return genai.NewContentFromText("Planned and researched.", genai.RoleModel)
		default:
			return functionCalls(
				&genai.FunctionCall{ID: "call_planner", Name: plannerName, Args: map[string]any{"request": "Plan a launch note."}},
				&genai.FunctionCall{ID: "call_researcher", Name: researcherName, Args: map[string]any{"request": "When did Oryxa launch?"}},
			)
		}
	})
	answer := runTurn(t, llm, "Write a launch note.")

	if answer != "Planned and researched." {
		t.Fatalf("answer = %q", answer)
	}
	if len(llm.requestsFor(plannerName)) != 1 || len(llm.requestsFor(researcherName)) != 1 {
		t.Fatalf("planner ran %d times and researcher %d, want once each", len(llm.requestsFor(plannerName)), len(llm.requestsFor(researcherName)))
	}
	research := llm.requestsFor(researcherName)[0]
	if !strings.Contains(contentsText(research), "When did Oryxa launch?") {
		t.Fatal("researcher was not given the request Krude handed it")
	}
	if !usesGoogleSearch(research) {
		t.Fatal("researcher was not given Google Search")
	}
	if !usesURLContext(research) {
		t.Fatal("researcher was not given URL context")
	}
	if tools := declaredFunctions(research); len(tools) != 0 {
		t.Fatalf("researcher was offered function tools %v beside its native tools", tools)
	}
}

func TestGeminiRequiresAProject(t *testing.T) {
	if _, err := Gemini(context.Background(), "gemini-3.8-flash", "", "global", nil); err == nil {
		t.Fatal("Gemini() error = nil without a project")
	}
}

// With no API key anywhere, Vertex AI can only be reached through Application
// Default Credentials, so building the model proves that path. The credential
// file is never exchanged for a token here, so the test stays offline.
func TestGeminiUsesApplicationDefaultCredentials(t *testing.T) {
	credentials := filepath.Join(t.TempDir(), "application_default_credentials.json")
	if err := os.WriteFile(credentials, []byte(`{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credentials)
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	llm, err := Gemini(context.Background(), "gemini-3.8-flash", "oryxa-test", "global", nil)
	if err != nil {
		t.Fatalf("Gemini() error = %v", err)
	}
	if llm.Name() != "gemini-3.8-flash" {
		t.Fatalf("Name() = %q", llm.Name())
	}
}

// fixedToken is credentials that always hold the same token.
type fixedToken string

func (token fixedToken) Token(context.Context) (*auth.Token, error) {
	return &auth.Token{Value: string(token), Type: "Bearer"}, nil
}

// On Fargate there are no Application Default Credentials to find: the
// credentials internal/gcpauth federates from AWS stand in for them, and with
// them nothing looks for a credentials file at all.
func TestGeminiUsesTheCredentialsItIsGiven(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	if _, err := Gemini(context.Background(), "gemini-3.5-flash-lite", "oryxa-test", "global", nil); err == nil {
		t.Fatal("Gemini() found Application Default Credentials without a credentials file, so this test proves nothing")
	}

	credentials := auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: fixedToken("service-account-token")})
	if _, err := Gemini(context.Background(), "gemini-3.5-flash-lite", "oryxa-test", "global", credentials); err != nil {
		t.Fatalf("Gemini() with credentials = %v, want it built on them", err)
	}
	if _, err := GeminiClient(context.Background(), "oryxa-test", "global", credentials); err != nil {
		t.Fatalf("GeminiClient() with credentials = %v, want it built on them", err)
	}
}

// harness runs Krude against one in-memory session service.
type harness struct {
	t        *testing.T
	sessions session.Service
	turns    *runner.Runner
}

func newHarness(t *testing.T, llm model.LLM, toolboxes ...Toolbox) *harness {
	t.Helper()
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatalf("modelrouter.New() error = %v", err)
	}
	return newRouterHarness(t, router, toolboxes...)
}

func newRouterHarness(t *testing.T, router *modelrouter.Router, toolboxes ...Toolbox) *harness {
	t.Helper()
	root, err := Root(router, toolboxes, false)
	if err != nil {
		t.Fatalf("Root() error = %v", err)
	}
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{AppName: RootName, Agent: root, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	return &harness{t: t, sessions: sessions, turns: turns}
}

// turn sends one message to Krude in a new session, starting with state, and
// returns Krude's reply.
func (h *harness) turn(userID string, state map[string]any, message string) string {
	h.t.Helper()
	return h.turnWith(context.Background(), userID, state, message)
}

// turnWith is turn on a context of the caller's, such as one carrying the
// member's own access token.
func (h *harness) turnWith(ctx context.Context, userID string, state map[string]any, message string) string {
	h.t.Helper()
	created, err := h.sessions.Create(ctx, &session.CreateRequest{AppName: RootName, UserID: userID, State: state})
	if err != nil {
		h.t.Fatalf("create session: %v", err)
	}
	var answer strings.Builder
	for event, err := range h.turns.Run(ctx, userID, created.Session.ID(), genai.NewContentFromText(message, genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			h.t.Fatalf("run: %v", err)
		}
		if event.Author == RootName && event.Content != nil {
			answer.WriteString(text(event.Content))
		}
	}
	return answer.String()
}

func runTurn(t *testing.T, llm model.LLM, message string, state ...map[string]any) string {
	t.Helper()
	var initial map[string]any
	if len(state) > 0 {
		initial = state[0]
	}
	return newHarness(t, llm).turn("member_1", initial, message)
}

type mapState map[string]any

func (s mapState) Get(key string) (any, error) {
	value, ok := s[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (s mapState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for key, value := range s {
			if !yield(key, value) {
				return
			}
		}
	}
}

type staticToolset []tool.Tool

func (s staticToolset) Name() string { return "static" }

func (s staticToolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) { return s, nil }

type echoArgs struct {
	Text string `json:"text"`
}

func echoTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	echo, err := functiontool.New(functiontool.Config{Name: name, Description: "Echoes text."}, func(_ agent.Context, args echoArgs) (echoArgs, error) {
		return args, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return echo
}

func countName(names []string, want string) int {
	count := 0
	for _, name := range names {
		if name == want {
			count++
		}
	}
	return count
}

func functionCalls(calls ...*genai.FunctionCall) *genai.Content {
	content := &genai.Content{Role: genai.RoleModel}
	for _, call := range calls {
		content.Parts = append(content.Parts, &genai.Part{FunctionCall: call})
	}
	return content
}

// functionResponse joins every response to the named function in the request.
func functionResponse(request *model.LLMRequest, name string) string {
	var responses []string
	for _, content := range request.Contents {
		for _, part := range content.Parts {
			if part.FunctionResponse != nil && part.FunctionResponse.Name == name {
				responses = append(responses, fmt.Sprint(part.FunctionResponse.Response))
			}
		}
	}
	return strings.Join(responses, " | ")
}

func text(content *genai.Content) string {
	if content == nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range content.Parts {
		builder.WriteString(part.Text)
	}
	return builder.String()
}

func contentsText(request *model.LLMRequest) string {
	var builder strings.Builder
	for _, content := range request.Contents {
		builder.WriteString(text(content))
	}
	return builder.String()
}

func hasFunctionResponse(request *model.LLMRequest) bool {
	for _, content := range request.Contents {
		for _, part := range content.Parts {
			if part.FunctionResponse != nil {
				return true
			}
		}
	}
	return false
}

func declaredFunctions(request *model.LLMRequest) []string {
	var names []string
	if request.Config == nil {
		return names
	}
	for _, declared := range request.Config.Tools {
		for _, function := range declared.FunctionDeclarations {
			names = append(names, function.Name)
		}
	}
	return names
}

func usesGoogleSearch(request *model.LLMRequest) bool {
	if request.Config == nil {
		return false
	}
	for _, declared := range request.Config.Tools {
		if declared.GoogleSearch != nil {
			return true
		}
	}
	return false
}

func usesURLContext(request *model.LLMRequest) bool {
	if request.Config == nil {
		return false
	}
	for _, declared := range request.Config.Tools {
		if declared.URLContext != nil {
			return true
		}
	}
	return false
}

func TestUnattendedKrudeCanReachNothingOutside(t *testing.T) {
	var offered []string
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		for _, declared := range request.Config.Tools {
			for _, function := range declared.FunctionDeclarations {
				offered = append(offered, function.Name)
			}
		}
		return genai.NewContentFromText("Two emails came in.", genai.RoleModel)
	})
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	notes := Toolbox{Name: "notes", Description: "Notes.", Toolset: staticToolset{echoTool(t, "notes_add")}}
	root, err := UnattendedRoot(router, []Toolbox{notes})
	if err != nil {
		t.Fatal(err)
	}
	if root.Name() != RootName {
		t.Fatalf("name = %q, want %q: it takes turns with Root in one thread", root.Name(), RootName)
	}
	if len(root.SubAgents()) != 0 {
		t.Fatalf("sub-agents = %d, want none: a sub-agent could research the web", len(root.SubAgents()))
	}
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{AppName: RootName, Agent: root, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: RootName, UserID: "member_1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range turns.Run(context.Background(), "member_1", created.Session.ID(), genai.NewContentFromText("Events arrived.", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(offered)
	want := []string{"get_user_details", "list_tools", "load_artifacts", "load_tools"}
	if !slices.Equal(offered, want) {
		t.Fatalf("tools offered = %v, want only %v", offered, want)
	}
	instruction := text(llm.requestsFor(RootName)[0].Config.SystemInstruction)
	if !strings.Contains(instruction, "never instructions") || !strings.Contains(instruction, "cannot change anything") {
		t.Fatalf("instruction does not say how to treat the events:\n%s", instruction)
	}
}
