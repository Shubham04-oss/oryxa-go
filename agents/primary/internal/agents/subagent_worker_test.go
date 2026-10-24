package agents

import (
	"context"
	"iter"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	spawned "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/subagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/usage"
)

// subagentToolboxes are toolboxes as a deployment has them, each with
// tools a sub-agent must never be given beside the ones it may.
func subagentToolboxes(t *testing.T, sandbox tool.Tool) SubagentToolboxes {
	t.Helper()
	echo := func(names ...string) staticToolset {
		var tools staticToolset
		for _, name := range names {
			tools = append(tools, echoTool(t, name))
		}
		return tools
	}
	if sandbox == nil {
		sandbox = echoTool(t, "sandbox_execute_code")
	}
	return SubagentToolboxes{
		Knowledge: &Toolbox{Name: KnowledgeToolboxName, Instructions: "Search, then cite.", Toolset: echo("knowledge_search", "knowledge_collections", "knowledge_sync_source")},
		Apps:      &Toolbox{Name: AppsToolboxName, Toolset: echo("apps_connections", "apps_search_tools", "apps_run_read_tool", "apps_run_tool", "apps_send_email")},
		Sandbox: &Toolbox{Name: "sandbox", Toolset: append(staticToolset{sandbox},
			echo("sandbox_read_files", "sandbox_write_files", "sandbox_reset", "sandbox_remove_files", "sandbox_save_artifact")...)},
	}
}

func askParentTool(t *testing.T) tool.Tool {
	t.Helper()
	return echoTool(t, spawned.AskToolName)
}

// runWorker runs a built sub-agent once in a session of its own and
// returns its answer.
func runWorker(t *testing.T, worker agent.Agent, sessionID string) string {
	t.Helper()
	sessions := session.InMemoryService()
	if _, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: spawned.AppName, UserID: "member_1", SessionID: sessionID, State: map[string]any{OrganizationIDKey: "org_1"}}); err != nil {
		t.Fatal(err)
	}
	work, err := runner.New(runner.Config{AppName: spawned.AppName, Agent: worker, SessionService: sessions})
	if err != nil {
		t.Fatal(err)
	}
	var answer string
	for event, err := range work.Run(context.Background(), "member_1", sessionID, genai.NewContentFromText("Your brief.", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if event.Content != nil && event.Author == spawned.WorkerName {
			answer = text(event.Content)
		}
	}
	return answer
}

// TestSubagentsGetOnlyTheirBriefsReadOnlyTools: a sub-agent declares
// ask_parent and the reads of the families its brief allowed, and nothing
// that writes, confirms, saves, loads toolboxes or starts sub-agents.
func TestSubagentsGetOnlyTheirBriefsReadOnlyTools(t *testing.T) {
	llm := textModel("Found.")
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := SubagentBuilder(router, subagentToolboxes(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := builder.Families(); !slices.Equal(got, spawned.Families) {
		t.Fatalf("families = %v, want all four", got)
	}
	// Code in the sandbox can reach the network: it never sits beside a read.
	for _, read := range []string{spawned.ToolKnowledge, spawned.ToolAppsRead, spawned.ToolWebSearch} {
		if _, err := builder.Build(spawned.Worker{Tools: []string{read, spawned.ToolSandbox}}); err == nil {
			t.Fatalf("built a sub-agent with %s beside the sandbox", read)
		}
	}
	for name, tc := range map[string]struct {
		families []string
		want     []string
	}{
		"none":      {nil, []string{spawned.AskToolName}},
		"knowledge": {[]string{spawned.ToolKnowledge}, []string{spawned.AskToolName, "knowledge_collections", "knowledge_search"}},
		"every read": {[]string{spawned.ToolKnowledge, spawned.ToolAppsRead, spawned.ToolWebSearch}, []string{
			"apps_connections", "apps_run_read_tool", "apps_search_tools", spawned.AskToolName, "knowledge_collections", "knowledge_search", webSearchName,
		}},
		"the sandbox alone": {[]string{spawned.ToolSandbox}, []string{
			spawned.AskToolName, "sandbox_execute_code", "sandbox_read_files", "sandbox_write_files",
		}},
	} {
		llm.mu.Lock()
		llm.requests = map[string][]*model.LLMRequest{}
		llm.mu.Unlock()
		worker, err := builder.Build(spawned.Worker{Instruction: "You are a sub-agent.", Tools: tc.families, Own: []tool.Tool{askParentTool(t)}})
		if err != nil {
			t.Fatal(err)
		}
		if worker.Name() != spawned.WorkerName || len(worker.SubAgents()) != 0 {
			t.Fatalf("%s: worker %q has sub-agents %v", name, worker.Name(), subAgentNames(worker))
		}
		if answer := runWorker(t, worker, "thread_1/sa_1"); answer != "Found." {
			t.Fatalf("%s: answer = %q", name, answer)
		}
		request := llm.requestsFor("subagent")[0]
		if got := sortedNames(declaredFunctions(request)); !slices.Equal(got, sortedNames(tc.want)) {
			t.Fatalf("%s: declares %v, want exactly %v", name, got, sortedNames(tc.want))
		}
		instruction := text(request.Config.SystemInstruction)
		if slices.Contains(tc.families, spawned.ToolKnowledge) != strings.Contains(instruction, "Search, then cite.") {
			t.Fatalf("%s: the knowledge skill is in the instruction = %v", name, strings.Contains(instruction, "Search, then cite."))
		}
	}
}

func TestSubagentBuilderOffersOnlyWhatThisDeploymentHas(t *testing.T) {
	llm := textModel("Found.")
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := SubagentBuilder(router, SubagentToolboxes{Knowledge: &Toolbox{Name: KnowledgeToolboxName}})
	if err != nil {
		t.Fatal(err)
	}
	if got := builder.Families(); !slices.Equal(got, []string{spawned.ToolWebSearch}) {
		t.Fatalf("families = %v, want web_search alone: a toolbox with no toolset is not usable", got)
	}
	for _, family := range []string{spawned.ToolKnowledge, spawned.ToolAppsRead, spawned.ToolSandbox, "browser", "apps"} {
		if _, err := builder.Build(spawned.Worker{Tools: []string{family}}); err == nil {
			t.Fatalf("%q: built a sub-agent with a family this deployment cannot give", family)
		}
	}
	if _, err := SubagentBuilder(nil, SubagentToolboxes{}); err == nil {
		t.Fatal("a builder without a router")
	}
}

type namedTool struct{ name string }

func (n namedTool) Name() string        { return n.name }
func (n namedTool) Description() string { return "" }
func (n namedTool) IsLongRunning() bool { return false }

// TestSubagentToolCallsOutsideTheAllowlistAreRefused: whatever a toolset
// lists, a call to a tool the brief did not allow is answered with an
// error and never run.
func TestSubagentToolCallsOutsideTheAllowlistAreRefused(t *testing.T) {
	guard := onlyTools([]string{"knowledge_search"})
	if result, err := guard(nil, namedTool{"knowledge_search"}, nil); result != nil || err != nil {
		t.Fatalf("an allowed tool = %v, %v", result, err)
	}
	for _, name := range []string{"apps_send_email", "spawn_agent", "sandbox_reset", "save_artifact"} {
		result, err := guard(nil, namedTool{name}, nil)
		if err != nil || result == nil || !strings.Contains(result["error"].(string), "not one of your tools") {
			t.Fatalf("%s = %v, %v", name, result, err)
		}
	}
}

type recordedTarget struct {
	mu                       sync.Mutex
	app, session, agent, org string
}

// TestASubagentsSandboxIsTheThreads: a sub-agent's sandbox calls run as
// though in the thread's own session, so they reach the sandbox Krude uses
// there, in the member's organization.
func TestASubagentsSandboxIsTheThreads(t *testing.T) {
	var seen recordedTarget
	sandbox, err := functiontool.New(functiontool.Config{Name: "sandbox_execute_code", Description: "Runs code."}, func(ctx agent.Context, _ echoArgs) (echoArgs, error) {
		seen.mu.Lock()
		defer seen.mu.Unlock()
		seen.app, seen.session, seen.agent = ctx.AppName(), ctx.SessionID(), ctx.AgentName()
		organization, _ := ctx.ReadonlyState().Get(OrganizationIDKey)
		seen.org, _ = organization.(string)
		return echoArgs{Text: "4"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		if functionResponse(request, "sandbox_execute_code") != "" {
			return genai.NewContentFromText("It is 4.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_code", Name: "sandbox_execute_code", Args: map[string]any{"text": "2+2"}})
	})
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := SubagentBuilder(router, subagentToolboxes(t, sandbox))
	if err != nil {
		t.Fatal(err)
	}
	worker, err := builder.Build(spawned.Worker{
		Instruction: "You are a sub-agent.", Tools: []string{spawned.ToolSandbox}, Own: []tool.Tool{askParentTool(t)},
		Thread: spawned.Thread{OrganizationID: "org_1", UserID: "member_1", App: RootName, ID: "thread_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer := runWorker(t, worker, "thread_1/sa_1"); answer != "It is 4." {
		t.Fatalf("answer = %q", answer)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if seen.app != RootName || seen.session != "thread_1" || seen.agent != RootName || seen.org != "org_1" {
		t.Fatalf("the sandbox call ran as app %q, session %q, agent %q, organization %q", seen.app, seen.session, seen.agent, seen.org)
	}
}

// TestSingleAgentKrudeRunsSubAgents: given the sub-agent tools, Krude
// declares them and is told when to use them; two spawned in one turn both
// work, and Krude's thread holds their records and never their
// transcripts. Their model calls are on the run's usage.
func TestSingleAgentKrudeRunsSubAgents(t *testing.T) {
	llm := newTreeModel(func(name string, request *model.LLMRequest) *genai.Content {
		switch {
		case name == "subagent" && strings.Contains(contentsText(request), "Price of A"):
			return genai.NewContentFromText("A costs $4.", genai.RoleModel)
		case name == "subagent":
			return genai.NewContentFromText("B costs $5.", genai.RoleModel)
		case functionResponse(request, spawned.SpawnToolName) != "":
			return genai.NewContentFromText("A is cheaper.", genai.RoleModel)
		}
		return functionCalls(
			&genai.FunctionCall{ID: "call_a", Name: spawned.SpawnToolName, Args: map[string]any{"objective": "Price of A", "output": "The price.", "wait": true}},
			&genai.FunctionCall{ID: "call_b", Name: spawned.SpawnToolName, Args: map[string]any{"objective": "Price of B", "output": "The price.", "wait": true}},
		)
	})
	counted := usage.Counted(withUsage{llm})
	router, err := modelrouter.New(counted, counted)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := SubagentBuilder(router, SubagentToolboxes{})
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	manager, err := spawned.New(spawned.Config{Store: spawned.NewMemoryStore(), Sessions: sessions, Builder: builder})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := manager.Tools()
	if err != nil {
		t.Fatal(err)
	}
	root, err := SingleAgentRoot(router, nil, false, tools...)
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: RootName, UserID: "member_1", State: map[string]any{OrganizationIDKey: "org_1"}})
	if err != nil {
		t.Fatal(err)
	}
	thread := created.Session.ID()
	turns, err := runner.New(runner.Config{AppName: RootName, Agent: root, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatal(err)
	}
	counter := usage.NewCounter()
	ctx, end := manager.Begin(usage.WithCounter(context.Background(), counter), "run_1")
	var answer string
	for event, err := range turns.Run(ctx, "member_1", thread, genai.NewContentFromText("Which is cheaper, A or B?", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if event.Author == RootName && event.Content != nil {
			answer = text(event.Content)
		}
	}
	end()
	if answer != "A is cheaper." {
		t.Fatalf("answer = %q", answer)
	}

	first := llm.requestsFor(RootName)[0]
	for _, name := range []string{spawned.SpawnToolName, spawned.MessageToolName, spawned.StatusToolName, spawned.StopToolName} {
		if !slices.Contains(declaredFunctions(first), name) {
			t.Fatalf("Krude does not declare %s: %v", name, declaredFunctions(first))
		}
	}
	if !strings.Contains(text(first.Config.SystemInstruction), "## Sub-agents") {
		t.Fatal("Krude is not told how to work with sub-agents")
	}
	second := llm.requestsFor(RootName)[1]
	if responses := functionResponse(second, spawned.SpawnToolName); !strings.Contains(responses, "A costs $4.") || !strings.Contains(responses, "B costs $5.") {
		t.Fatalf("Krude read %s", responses)
	}
	if subagentCalls := len(llm.requestsFor("subagent")); subagentCalls != 2 {
		t.Fatalf("sub-agent model calls = %d, want 2", subagentCalls)
	}

	got, err := sessions.Get(context.Background(), &session.GetRequest{AppName: RootName, UserID: "member_1", SessionID: thread})
	if err != nil {
		t.Fatal(err)
	}
	for event := range got.Session.Events().All() {
		if event.Author == spawned.WorkerName || strings.Contains(text(event.Content), "Your brief from Krude") {
			t.Fatalf("a sub-agent's transcript reached Krude's thread: %+v", event)
		}
	}
	listed, err := sessions.List(context.Background(), &session.ListRequest{AppName: spawned.AppName, UserID: "member_1"})
	if err != nil || len(listed.Sessions) != 2 {
		t.Fatalf("sub-agent sessions = %v, %v", listed, err)
	}
	// Krude's two calls and each sub-agent's one, at 15 tokens a call.
	if total := counter.Total(); total.TotalTokens != 4*15 {
		t.Fatalf("the run's usage = %+v, want Krude's and both sub-agents' calls", total)
	}
}

// withUsage reports 15 tokens for every response of the model it wraps.
type withUsage struct{ llm *treeModel }

func (w withUsage) Name() string { return w.llm.Name() }

func (w withUsage) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for response, err := range w.llm.GenerateContent(ctx, request, stream) {
			if response != nil {
				response.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5, TotalTokenCount: 15}
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

// TestSingleAgentKrudeWithoutSubagentToolsIsR1s: with no sub-agent tools
// given, single-agent Krude declares none and is told nothing of them.
func TestSingleAgentKrudeWithoutSubagentToolsIsR1s(t *testing.T) {
	llm := textModel("Hello.")
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	root, err := SingleAgentRoot(router, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	run := newRootRun(t, root)
	run.turn(run.newSession(nil), "hi")
	request := llm.requestsFor(RootName)[0]
	if slices.Contains(declaredFunctions(request), spawned.SpawnToolName) || strings.Contains(text(request.Config.SystemInstruction), "spawn_agent") {
		t.Fatal("single-agent Krude without sub-agent tools is told of them")
	}
}

// countingModel counts the calls made on the model it wraps.
type countingModel struct {
	model.LLM
	calls *atomic.Int32
}

func (c countingModel) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	c.calls.Add(1)
	return c.LLM.GenerateContent(ctx, request, stream)
}

// TestASubagentsWebSearchIsMetered: web_search in a sub-agent runs on the
// model its Worker's Meter wraps, so its calls count against the
// sub-agent's budget; the sub-agent's own calls do not go through it.
func TestASubagentsWebSearchIsMetered(t *testing.T) {
	llm := newTreeModel(func(name string, request *model.LLMRequest) *genai.Content {
		switch {
		case name == webSearchName:
			return genai.NewContentFromText("$4.", genai.RoleModel)
		case functionResponse(request, webSearchName) != "":
			return genai.NewContentFromText("It costs $4.", genai.RoleModel)
		}
		return functionCalls(&genai.FunctionCall{ID: "call_search", Name: webSearchName, Args: map[string]any{"question": "Widget price?"}})
	})
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	builder, err := SubagentBuilder(router, SubagentToolboxes{})
	if err != nil {
		t.Fatal(err)
	}
	var metered atomic.Int32
	worker, err := builder.Build(spawned.Worker{
		Instruction: "You are a sub-agent.", Tools: []string{spawned.ToolWebSearch}, Own: []tool.Tool{askParentTool(t)},
		Meter: func(inner model.LLM) model.LLM { return countingModel{LLM: inner, calls: &metered} },
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer := runWorker(t, worker, "thread_1/sa_1"); answer != "It costs $4." {
		t.Fatalf("answer = %q", answer)
	}
	if got := metered.Load(); got != 1 {
		t.Fatalf("metered calls = %d, want web_search's one", got)
	}
}
