package agents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// resultsInteractions keeps what each answer arrived with of what its run read.
type resultsInteractions struct {
	fakeInteractions
	mu      sync.Mutex
	results [][]ToolResult
}

// Get is the request the run delivers, with what its asker wrote in it.
func (*resultsInteractions) Get(_ context.Context, _ InteractionActor, id string) (Interaction, error) {
	return Interaction{ID: id, Task: "THE ASKERS WORDS"}, nil
}

func (service *resultsInteractions) Respond(ctx context.Context, _ InteractionActor, id string, input InteractionResponseInput) (Interaction, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.results = append(service.results, RunResults(ctx))
	return Interaction{ID: id, Status: input.Status}, nil
}

type queryArgs struct {
	Text string `json:"text"`
}

type passage struct {
	Title string  `json:"title"`
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}

type passages struct {
	Passages []passage `json:"passages"`
	Total    int       `json:"total"`
}

// searchTool is a tool that returns the passages it is given.
func searchTool(t *testing.T, name string, found ...passage) tool.Tool {
	t.Helper()
	search, err := functiontool.New(functiontool.Config{Name: name, Description: "Searches."}, func(agent.Context, queryArgs) (passages, error) {
		return passages{Passages: found, Total: len(found)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return search
}

type nothing struct{}

// emptyTool is a tool that returns nothing at all.
func emptyTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	empty, err := functiontool.New(functiontool.Config{Name: name, Description: "Returns nothing."}, func(agent.Context, queryArgs) (nothing, error) {
		return nothing{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return empty
}

// brokenTool is a tool whose every call fails.
func brokenTool(t *testing.T, name string) tool.Tool {
	t.Helper()
	broken, err := functiontool.New(functiontool.Config{Name: name, Description: "Fails."}, func(agent.Context, queryArgs) (passages, error) {
		return passages{}, errors.New("the app is not connected: SECRETREASON")
	})
	if err != nil {
		t.Fatal(err)
	}
	return broken
}

// box is a toolbox of tools, which a toolbox's tools are all named after.
func box(name string, tools ...tool.Tool) Toolbox {
	return Toolbox{Name: name, Description: "Some of " + name + ".", Instructions: strings.ToUpper(name) + " SKILL.", Toolset: staticToolset(tools)}
}

// delivering is a run delivering irq_1 for its answer, that reads with the
// calls, all of them in one step, and then answers. It returns what the answer
// arrived with, and what the model was shown of each call.
func delivering(t *testing.T, service *resultsInteractions, calls []*genai.FunctionCall, boxes ...Toolbox) (*treeModel, []ToolResult) {
	t.Helper()
	toolbox, err := InteractionToolbox(InteractionConfig{Service: service, Delivering: true})
	if err != nil {
		t.Fatal(err)
	}
	llm := newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, "interactions_respond") != "":
			return genai.NewContentFromText("Done.", genai.RoleModel)
		case hasFunctionResponse(request):
			return functionCalls(&genai.FunctionCall{ID: "answer", Name: "interactions_respond", Args: map[string]any{
				"request_id": "irq_1", "status": "completed", "summary": "An answer.", "content": "From what was read.",
			}})
		default:
			return functionCalls(calls...)
		}
	})
	deliveringHarness(t, llm, append(boxes, toolbox)...).turn("user_b", stateWithSources([]string{"knowledge"}), "Deliver irq_1.")
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.results) != 1 {
		t.Fatalf("the ledger was given %d answers, want 1", len(service.results))
	}
	return llm, service.results[0]
}

func call(id, name, text string) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: name, Args: map[string]any{"text": text}}
}

// TestTheRespondToolTellsTheLedgerWhatTheRunsToolsReturned: the answer reaches
// the ledger with what the run's tools returned before it, as text, in the
// order they were called, and with nothing the model says about what it read.
func TestTheRespondToolTellsTheLedgerWhatTheRunsToolsReturned(t *testing.T) {
	service := &resultsInteractions{}
	_, results := delivering(t, service,
		[]*genai.FunctionCall{call("one", "knowledge_search", "deployments")},
		box("knowledge", searchTool(t, "knowledge_search",
			passage{Title: "Runbook", Text: "Deployments run every Tuesday at nine.", Score: 0.83},
			passage{Title: "Freeze", Text: "A release manager may call a freeze."})))
	if len(results) != 1 || results[0].Tool != "knowledge_search" {
		t.Fatalf("the ledger was told %+v, want the one result of knowledge_search", results)
	}
	want := []string{
		"passages.score: 0.83", "passages.text: Deployments run every Tuesday at nine.", "passages.title: Runbook",
		"passages.score: 0", "passages.text: A release manager may call a freeze.", "passages.title: Freeze", "total: 2",
	}
	if !slices.Equal(results[0].Parts, want) {
		t.Fatalf("parts = %q, want %q", results[0].Parts, want)
	}
}

// What is not read is not recorded: the request the run delivers, a call that
// failed, and a result with nothing in it.
func TestOnlyWhatARunReadIsRecorded(t *testing.T) {
	service := &resultsInteractions{}
	llm, results := delivering(t, service,
		[]*genai.FunctionCall{
			{ID: "get", Name: "interactions_get", Args: map[string]any{"request_id": "irq_1"}}, call("one", "knowledge_search", "deployments"),
			call("two", "apps_run_read_tool", "mail"), call("three", "knowledge_empty", "nothing"),
		},
		box("knowledge", searchTool(t, "knowledge_search", passage{Text: "Deployments run every Tuesday."}), emptyTool(t, "knowledge_empty")),
		box("apps", brokenTool(t, "apps_run_read_tool")))
	if len(results) != 1 || results[0].Tool != "knowledge_search" {
		t.Fatalf("the ledger was told %+v, want only what knowledge_search returned", results)
	}
	// The model was shown every result all the same, as it would have been.
	second := llm.requestsFor(RootName)[1]
	if got := functionResponse(second, "interactions_get"); !strings.Contains(got, "THE ASKERS WORDS") {
		t.Errorf("the model was shown %q of the request, so its call did not read it", got)
	}
	if got := functionResponse(second, "knowledge_search"); !strings.Contains(got, "Deployments run every Tuesday.") {
		t.Errorf("the model was shown %q of knowledge_search", got)
	}
	if got := functionResponse(second, "apps_run_read_tool"); !strings.Contains(got, "SECRETREASON") {
		t.Errorf("the model was shown %q of the call that failed", got)
	}
}

// A tool nobody has heard of is recorded like any other: what the run read is
// whatever it called.
func TestAnyToolIsRecordedWhateverItIsCalled(t *testing.T) {
	service := &resultsInteractions{}
	_, results := delivering(t, service,
		[]*genai.FunctionCall{call("one", "screen_get_current", "now"), call("two", "widgets_some_tool_next_year", "later")},
		box("screen", searchTool(t, "screen_get_current", passage{Text: "the screen"})),
		box("widgets", searchTool(t, "widgets_some_tool_next_year", passage{Text: "something new"})))
	names := []string{}
	for _, result := range results {
		names = append(names, result.Tool)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"screen_get_current", "widgets_some_tool_next_year"}) {
		t.Fatalf("the ledger was told the results of %v", names)
	}
}

// Each run has its own record: what one read is never what another is answered
// against.
func TestARunIsAnsweredAgainstItsOwnResultsAlone(t *testing.T) {
	service := &resultsInteractions{}
	for _, word := range []string{"ALPHA", "BETA"} {
		delivering(t, service, []*genai.FunctionCall{call("one", "knowledge_search", word)},
			box("knowledge", searchTool(t, "knowledge_search", passage{Text: "The " + word + " document."})))
		service.mu.Lock()
		service.results = nil
		service.mu.Unlock()
	}
	// Reading it again, from the other run.
	first := &resultsInteractions{}
	_, results := delivering(t, first, []*genai.FunctionCall{call("one", "knowledge_search", "GAMMA")},
		box("knowledge", searchTool(t, "knowledge_search", passage{Text: "The GAMMA document."})))
	joined := fmt.Sprint(results)
	if !strings.Contains(joined, "GAMMA") || strings.Contains(joined, "ALPHA") || strings.Contains(joined, "BETA") {
		t.Fatalf("a run was answered against %s", joined)
	}
}

// An answer that comes from no run, as a member's own does, is told of no
// results.
func TestAnAnswerFromNoRunHasNoResults(t *testing.T) {
	if got := RunResults(context.Background()); len(got) != 0 {
		t.Errorf("RunResults of a context with none = %v", got)
	}
	ctx := WithRunResults(context.Background(), []ToolResult{{Tool: "knowledge_search", Parts: []string{"a"}}})
	got := RunResults(ctx)
	got[0].Parts[0] = "changed"
	if again := RunResults(ctx); again[0].Parts[0] != "a" {
		t.Errorf("what a context carries was changed through a copy: %v", again)
	}
}

func TestOnlyARunDeliveringAnInteractionRecords(t *testing.T) {
	if (*delivery)(nil).afterTools() != nil {
		t.Error("a run that delivers nothing records what its tools return")
	}
	if callbacks := (&delivery{}).afterTools(); len(callbacks) != 1 {
		t.Errorf("a run delivering an interaction has %d callbacks after a tool, want the recorder", len(callbacks))
	}
}

func TestAResultIsItsValuesAsText(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result map[string]any
		want   []string
	}{
		{"a text", map[string]any{"text": "Hello there"}, []string{"text: Hello there"}},
		{"what is nothing", map[string]any{"a": nil, "b": "", "c": "   ", "d": []any{}, "e": map[string]any{}}, nil},
		{"numbers and booleans", map[string]any{"count": float64(3), "ratio": 0.25, "ok": true, "big": float64(1_000_000)},
			[]string{"big: 1000000", "count: 3", "ok: true", "ratio: 0.25"}},
		{"names in order, nested", map[string]any{"b": map[string]any{"y": "two", "x": "one"}, "a": "first"},
			[]string{"a: first", "b.x: one", "b.y: two"}},
		{"a list adds nothing to the path", map[string]any{"items": []any{map[string]any{"name": "one"}, map[string]any{"name": "two"}, "loose"}},
			[]string{"items.name: one", "items.name: two", "items: loose"}},
		{"lists of text", map[string]any{"tags": []string{"a", " ", "b"}, "rows": []map[string]any{{"id": "1"}}},
			[]string{"rows.id: 1", "tags: a", "tags: b"}},
		{"text is kept as it is, trimmed", map[string]any{"text": "  line one\nline two  "}, []string{"text: line one\nline two"}},
		{"only an error", map[string]any{"error": "something failed"}, nil},
		{"an error beside other things", map[string]any{"error": "partial", "text": "found"}, []string{"error: partial", "text: found"}},
		{"nothing at all", nil, nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := resultParts(testCase.result, maxRunResultChars); !slices.Equal(got, testCase.want) {
				t.Errorf("parts = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestAResultOfAnySizeIsReadInBoundedWork(t *testing.T) {
	long := strings.Repeat("word ", 20_000)
	parts := resultParts(map[string]any{"a": long, "b": "never reached"}, 1_000)
	if len(parts) != 1 || len([]rune(parts[0])) != 1_000 {
		t.Errorf("a result of a very long text kept %d parts, the first %d characters long", len(parts), len([]rune(parts[0])))
	}
	total := 0
	for _, part := range resultParts(map[string]any{"a": strings.Repeat("x", 300), "b": strings.Repeat("y", 300), "c": strings.Repeat("z", 300)}, 700) {
		total += len([]rune(part))
	}
	if total != 700 {
		t.Errorf("kept %d characters, want the 700 it was allowed", total)
	}
	many := map[string]any{}
	for index := range maxResultParts * 2 {
		many[fmt.Sprintf("k%05d", index)] = "v"
	}
	if got := resultParts(many, maxRunResultChars); len(got) != maxResultParts {
		t.Errorf("kept %d values of a result of %d", len(got), len(many))
	}
	deep := any("deepest")
	for range maxResultDepth * 2 {
		deep = map[string]any{"in": deep}
	}
	if got := resultParts(deep.(map[string]any), maxRunResultChars); len(got) != 0 {
		t.Errorf("read a value %d levels down: %q", maxResultDepth*2, got)
	}
	shallow := any("shallow")
	for range maxResultDepth - 1 {
		shallow = map[string]any{"in": shallow}
	}
	if got := resultParts(shallow.(map[string]any), maxRunResultChars); len(got) != 1 {
		t.Errorf("did not read a value %d levels down: %q", maxResultDepth-1, got)
	}
}

func result(tool string, parts ...string) ToolResult { return ToolResult{Tool: tool, Parts: parts} }

func TestARunKeepsItsLatestResults(t *testing.T) {
	log := newResultLog(time.Now)
	for index := range maxRunResults + 5 {
		log.record("run", result("tool", fmt.Sprintf("result %d", index)))
	}
	got := log.read("run")
	if len(got) != maxRunResults || got[0].Parts[0] != "result 5" || got[len(got)-1].Parts[0] != fmt.Sprintf("result %d", maxRunResults+4) {
		t.Fatalf("kept %d results, from %q to %q", len(got), got[0].Parts, got[len(got)-1].Parts)
	}
}

func TestARunKeepsNoMoreCharactersThanBedrockTakes(t *testing.T) {
	log := newResultLog(time.Now)
	chunk := strings.Repeat("x", 40_000)
	for _, name := range []string{"first", "second", "third", "fourth"} {
		log.record("run", result(name, chunk))
	}
	got := log.read("run")
	names := []string{}
	total := 0
	for _, kept := range got {
		names = append(names, kept.Tool)
		total += charsOf(kept)
	}
	if !slices.Equal(names, []string{"third", "fourth"}) || total > maxRunResultsChars {
		t.Fatalf("kept %v, %d characters; want the newest two within %d", names, total, maxRunResultsChars)
	}
}

func TestRunsAreKeptApartAndCopiesAreCopies(t *testing.T) {
	log := newResultLog(time.Now)
	log.record("one", result("a", "first"))
	log.record("two", result("b", "second"))
	if got := log.read("one"); len(got) != 1 || got[0].Tool != "a" {
		t.Errorf("one read %v", got)
	}
	if got := log.read("nobody"); got != nil {
		t.Errorf("a run that recorded nothing read %v", got)
	}
	copied := log.read("one")
	copied[0].Parts[0] = "changed"
	if again := log.read("one"); again[0].Parts[0] != "first" {
		t.Errorf("the record was changed through a copy: %v", again)
	}
}

func TestARecordIsForgottenWhenNothingHasTouchedItForAWhile(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	log := newResultLog(func() time.Time { return now })
	log.record("old", result("a", "one"))
	now = now.Add(runResultsTTL - time.Minute)
	log.record("young", result("b", "two"))
	if got := log.read("old"); len(got) != 1 {
		t.Fatalf("a record just inside its time to live was read as %v", got)
	}
	now = now.Add(2 * time.Minute)
	if got := log.read("old"); got != nil {
		t.Errorf("an expired record was read as %v", got)
	}
	if got := log.read("young"); len(got) != 1 {
		t.Errorf("a record still live was read as %v", got)
	}
	if len(log.runs) != 1 {
		t.Errorf("%d records are held, want the live one", len(log.runs))
	}
}

func TestOnlyTheMostRecentlyTouchedRunsAreHeld(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	log := newResultLog(func() time.Time { return now })
	for index := range maxRecordedRuns {
		now = now.Add(time.Second)
		log.record(fmt.Sprintf("run-%d", index), result("a", "x"))
	}
	now = now.Add(time.Second)
	log.record("run-0", result("a", "again")) // the oldest, touched again
	now = now.Add(time.Second)
	log.record("newcomer", result("a", "x")) // one too many: the longest untouched goes
	if len(log.runs) != maxRecordedRuns {
		t.Fatalf("%d runs are held, want %d", len(log.runs), maxRecordedRuns)
	}
	if log.read("run-1") != nil || log.read("run-0") == nil || log.read("newcomer") == nil {
		t.Errorf("the wrong run was dropped: run-1 %v, run-0 %v", log.read("run-1") != nil, log.read("run-0") != nil)
	}
}

func TestResultsAreRecordedAtOnceFromManyCalls(t *testing.T) {
	log := newResultLog(time.Now)
	var wait sync.WaitGroup
	for index := range 40 {
		wait.Go(func() {
			log.record("run", result("tool", fmt.Sprintf("result %d", index)))
			log.read("run")
			log.record(fmt.Sprintf("other-%d", index%5), result("tool", "x"))
		})
	}
	wait.Wait()
	if got := log.read("run"); len(got) != 16 {
		t.Errorf("kept %d results", len(got))
	}
}

// invocationContext is a tool's context for an invocation of the given id, and
// nothing else: what the recorder asks of one.
type invocationContext struct {
	agent.StrictContextMock
	id string
}

func (ctx *invocationContext) InvocationID() string { return ctx.id }

func inInvocation(id string) *invocationContext {
	return &invocationContext{StrictContextMock: agent.NewStrictContextMock(context.Background()), id: id}
}

// TestTheRecorderKeepsWhatWasReadAndLeavesEveryResultAsItWas is the callback on
// its own: what it records, what it leaves out, and that it never changes the
// result or the error the model is shown.
func TestTheRecorderKeepsWhatWasReadAndLeavesEveryResultAsItWas(t *testing.T) {
	result := map[string]any{"text": "Deployments run every Tuesday."}
	for _, testCase := range []struct {
		name       string
		tool       string
		invocation string
		result     map[string]any
		err        error
		recorded   bool
	}{
		{"a result", "knowledge_search", "inv-recorder-a", result, nil, true},
		{"a call that failed with what it had so far", "knowledge_search", "inv-recorder-b", result, errors.New("failed"), false},
		{"a call with no invocation", "knowledge_search", "", result, nil, false},
		{"the request being delivered", "interactions_get", "inv-recorder-c", result, nil, false},
		{"the answer being recorded", "interactions_respond", "inv-recorder-d", result, nil, false},
		{"the answer given as the last word", "set_model_response", "inv-recorder-e", result, nil, false},
		{"a result of nothing", "knowledge_search", "inv-recorder-f", map[string]any{"text": " "}, nil, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			replaced, err := recordToolResult(inInvocation(testCase.invocation), searchTool(t, testCase.tool), map[string]any{"text": "q"}, testCase.result, testCase.err)
			if replaced != nil || err != nil {
				t.Fatalf("the callback replaced the result with %v, %v: it must leave what the model is shown as it was", replaced, err)
			}
			recorded := deliveryResults.read(testCase.invocation)
			if testCase.recorded != (len(recorded) == 1) {
				t.Fatalf("recorded %v, want recorded = %v", recorded, testCase.recorded)
			}
			if testCase.recorded && (recorded[0].Tool != testCase.tool || !slices.Equal(recorded[0].Parts, []string{"text: Deployments run every Tuesday."})) {
				t.Fatalf("recorded %+v", recorded)
			}
		})
	}
}
