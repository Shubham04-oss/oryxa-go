package agents

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
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
	name := RootName
	if strings.Contains(instruction, "You are a temporary helper") {
		name = "helper"
	}
	for _, subAgent := range []string{plannerName, researcherName, teamsName} {
		if strings.Contains(instruction, "You are the "+subAgent+" sub-agent") {
			name = subAgent
		}
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
	answer := runTurn(t, llm, "hi", nil)

	if answer != "Hello from Krude." {
		t.Fatalf("answer = %q", answer)
	}
	instruction := text(llm.requestsFor(RootName)[0].Config.SystemInstruction)
	want := "You are Krude. You will be assisting the member you are talking with in their organization."
	if !strings.Contains(instruction, want) {
		t.Fatalf("instruction is missing %q:\n%s", want, instruction)
	}
	if strings.Contains(instruction, "calls you") || strings.Contains(instruction, "Oryxa") {
		t.Fatalf("instruction names a nickname or Oryxa:\n%s", instruction)
	}
	declared := declaredFunctions(llm.requestsFor(RootName)[0])
	for _, name := range []string{plannerName, researcherName, teamsName, "set_nickname", "list_tools", "load_tools", "list_helpers", "spawn_helper", "load_artifacts", "list_skills", "load_skill"} {
		if !slices.Contains(declared, name) {
			t.Fatalf("Krude can call %v, missing %q", declared, name)
		}
	}
}

func TestKrudeInstructionReadsSessionState(t *testing.T) {
	llm := newTreeModel(func(string, *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText("Hi Asha.", genai.RoleModel)
	})
	runTurn(t, llm, "hi", map[string]any{UserNameKey: "Asha", UserAboutKey: "a nice lady", OrgNameKey: "Acme", NicknameKey: "Kru"})

	instruction := text(llm.requestsFor(RootName)[0].Config.SystemInstruction)
	want := "You are Krude, though Asha calls you Kru. You will be assisting Asha, a nice lady in Acme."
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

func TestKrudeKeepsItsNicknameForTheMember(t *testing.T) {
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case hasFunctionResponse(request):
			return genai.NewContentFromText("Kru it is.", genai.RoleModel)
		case strings.Contains(contentsText(request), "Your nickname is Kru."):
			return functionCalls(&genai.FunctionCall{ID: "call_nickname", Name: "set_nickname", Args: map[string]any{"nickname": " Kru\n"}})
		default:
			return genai.NewContentFromText("Hello.", genai.RoleModel)
		}
	})
	h := newHarness(t, llm, nil)
	if answer := h.turn("member_1", map[string]any{UserNameKey: "Asha"}, "Your nickname is Kru."); answer != "Kru it is." {
		t.Fatalf("answer = %q", answer)
	}

	h.turn("member_1", map[string]any{UserNameKey: "Asha"}, "hi")
	if instruction := lastInstruction(llm); !strings.Contains(instruction, "You are Krude, though Asha calls you Kru.") {
		t.Fatalf("a later session of the same member lost the nickname:\n%s", instruction)
	}
	h.turn("member_2", map[string]any{UserNameKey: "Ravi"}, "hi")
	if instruction := lastInstruction(llm); strings.Contains(instruction, "calls you") {
		t.Fatalf("another member's session got the nickname:\n%s", instruction)
	}
}

func TestKrudeLoadsToolboxesWhenATaskNeedsThem(t *testing.T) {
	notes := Toolbox{Name: "notes", Description: "Notes.", Toolset: staticToolset{echoTool(t, "notes_add")}}
	misnamed := Toolbox{Name: "bad", Description: "Misnamed tools.", Toolset: staticToolset{echoTool(t, "add_thing")}}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "notes_add") != "":
			return genai.NewContentFromText("Noted.", genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "call_note", Name: "notes_add", Args: map[string]any{"text": "buy milk"}})
		default:
			return functionCalls(&genai.FunctionCall{ID: "call_load", Name: "load_tools", Args: map[string]any{"names": []any{"notes", "slack", "bad", "nope"}}})
		}
	})
	h := newHarness(t, llm, append(Providers(), notes, misnamed))
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
	for _, want := range []string{"loaded:[notes]", "slack", "not connected yet", "bad", "not named after the toolbox", "nope", "no toolbox with this name"} {
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
	h := newHarness(t, llm, []Toolbox{notes})
	answer := h.turn("member_1", map[string]any{loadedToolsKey: []string{"notes"}}, "Add another note.")

	if answer != "Noted again." {
		t.Fatalf("answer = %q", answer)
	}
	first := declaredFunctions(llm.requestsFor(RootName)[0])
	if count := countName(first, "notes_add"); count != 1 {
		t.Fatalf("notes_add was declared %d times, want once; declarations: %v", count, first)
	}
}

func TestKrudeListsItsHelpers(t *testing.T) {
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if hasFunctionResponse(request) {
			return genai.NewContentFromText("Listed.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_list", Name: "list_helpers", Args: map[string]any{}})
	})
	runTurn(t, llm, "Who helps you?", nil)

	result := functionResponse(llm.requestsFor(RootName)[1], "list_helpers")
	for _, want := range []string{plannerName, researcherName, teamsName, "call it alone", "spawn_helper"} {
		if !strings.Contains(result, want) {
			t.Fatalf("list_helpers result %s is missing %q", result, want)
		}
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
	answer := runTurn(t, llm, "Write and check a launch note.", nil)

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
		for _, forbidden := range []string{"spawn_helper", "set_nickname", "list_tools", "load_tools", "list_helpers"} {
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

func TestKrudeLoadsASkill(t *testing.T) {
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if hasFunctionResponse(request) {
			return genai.NewContentFromText("Here is the plan.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_skill", Name: "load_skill", Args: map[string]any{"name": "team-session-planning"}})
	})
	runTurn(t, llm, "Plan a design review with my team.", nil)

	krude := llm.requestsFor(RootName)
	if !strings.Contains(text(krude[0].Config.SystemInstruction), "team-session-planning") {
		t.Fatal("Krude was not told which skills exist")
	}
	if result := functionResponse(krude[1], "load_skill"); !strings.Contains(result, "Planning a working session") {
		t.Fatalf("load_skill did not return the skill's instructions: %s", result)
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
	answer := runTurn(t, llm, "Write a launch note.", nil)

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
	if tools := declaredFunctions(research); len(tools) != 0 {
		t.Fatalf("researcher was offered function tools %v beside Google Search", tools)
	}
}

func TestKrudeGetsATaskSubAgentsResult(t *testing.T) {
	llm := newTreeModel(func(agentName string, request *model.LLMRequest) *genai.Content {
		switch {
		case agentName == teamsName:
			return functionCalls(&genai.FunctionCall{ID: "call_finish", Name: "finish_task", Args: map[string]any{"result": "Teams tools are not connected yet."}})
		case hasFunctionResponse(request):
			return genai.NewContentFromText("I can't reach teams yet.", genai.RoleModel)
		default:
			return functionCalls(&genai.FunctionCall{ID: "call_teams", Name: teamsName, Args: map[string]any{"request": "Find the design team."}})
		}
	})
	answer := runTurn(t, llm, "Who is on the design team?", nil)

	if answer != "I can't reach teams yet." {
		t.Fatalf("answer = %q", answer)
	}
	if tools := declaredFunctions(llm.requestsFor(teamsName)[0]); !slices.Equal(tools, []string{"finish_task"}) {
		t.Fatalf("teams was offered %v, want only finish_task", tools)
	}
	if result := functionResponse(llm.requestsFor(RootName)[1], teamsName); !strings.Contains(result, "Teams tools are not connected yet.") {
		t.Fatalf("Krude did not get teams' result back, got %q", result)
	}
}

func TestGeminiRequiresAProject(t *testing.T) {
	if _, err := Gemini(context.Background(), "gemini-3.8-flash", "", "global"); err == nil {
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

	llm, err := Gemini(context.Background(), "gemini-3.8-flash", "oryxa-test", "global")
	if err != nil {
		t.Fatalf("Gemini() error = %v", err)
	}
	if llm.Name() != "gemini-3.8-flash" {
		t.Fatalf("Name() = %q", llm.Name())
	}
}

// harness runs Krude against one in-memory session service, so state scoped to
// a member carries from one of their sessions to the next.
type harness struct {
	t        *testing.T
	sessions session.Service
	turns    *runner.Runner
}

func newHarness(t *testing.T, llm model.LLM, toolboxes []Toolbox) *harness {
	t.Helper()
	root, err := Root(context.Background(), llm, toolboxes)
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

// turn sends one message to Krude in a new session for the member, starting
// with state, and returns Krude's reply.
func (h *harness) turn(userID string, state map[string]any, message string) string {
	h.t.Helper()
	ctx := context.Background()
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

func runTurn(t *testing.T, llm model.LLM, message string, state map[string]any) string {
	t.Helper()
	return newHarness(t, llm, nil).turn("member_1", state, message)
}

func lastInstruction(llm *treeModel) string {
	requests := llm.requestsFor(RootName)
	return text(requests[len(requests)-1].Config.SystemInstruction)
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

func countName(names []string, want string) int {
	count := 0
	for _, name := range names {
		if name == want {
			count++
		}
	}
	return count
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
