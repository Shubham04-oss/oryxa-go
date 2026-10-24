package subagents

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/usage"
)

// TestSpawnWithWaitReturnsTheResult: spawn_agent with wait true returns the
// sub-agent's result, records it, and keeps its transcript in a session of
// its own, apart from the thread.
func TestSpawnWithWaitReturnsTheResult(t *testing.T) {
	m := newTestManager(t, answer("The widget costs $4, per the vendor's page."))
	ctx, _ := m.run(t, context.Background(), "run_1")

	view, err := m.spawn(ctx, testBrief(true, ToolWebSearch))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StateDone || view.Result != "The widget costs $4, per the vendor's page." || view.ModelCalls != 1 {
		t.Fatalf("view = %+v", view)
	}
	record := m.store.record(t, view.ID)
	if record.Thread != testThread || record.ParentRunID != "run_1" || !slices.Equal(record.Tools, []string{ToolWebSearch}) {
		t.Fatalf("record = %+v", record)
	}
	if record.InputTokens != 10 || record.OutputTokens != 5 || record.TotalTokens != 15 {
		t.Fatalf("record usage = %d/%d/%d", record.InputTokens, record.OutputTokens, record.TotalTokens)
	}

	got, err := m.sessions.Get(context.Background(), &session.GetRequest{AppName: AppName, UserID: "member_1", SessionID: "thread_1/" + view.ID})
	if err != nil {
		t.Fatalf("the sub-agent has no session of its own: %v", err)
	}
	if organization, _ := got.Session.State().Get("organization_id"); organization != "org_1" {
		t.Fatalf("the sub-agent's session does not act for the member's organization: %v", organization)
	}
	if agentID, _ := got.Session.State().Get("managed_agent_id"); agentID != "agent_1" {
		t.Fatalf("the sub-agent's session does not carry the member's agent: %v", agentID)
	}
	first := m.model.requests()[0]
	if text := lastUserText(first); !strings.Contains(text, "Find the price of the widget.") || !strings.Contains(text, "The member sells widgets.") {
		t.Fatalf("the brief did not reach the sub-agent: %q", text)
	}
	if !strings.Contains(text(first.Config.SystemInstruction), "ask_parent") {
		t.Fatal("the sub-agent's instruction does not tell it to ask Krude")
	}
	if worker := m.builder.built[0]; !slices.Equal(worker.Tools, []string{ToolWebSearch}) || worker.Thread != testThread || len(worker.Own) != 1 || worker.Own[0].Name() != AskToolName {
		t.Fatalf("worker = %+v", worker)
	}
}

func text(content *genai.Content) string {
	if content == nil {
		return ""
	}
	var all strings.Builder
	for _, part := range content.Parts {
		all.WriteString(part.Text)
	}
	return all.String()
}

// TestSpawnWithoutWaitReturnsAtOnce: wait false returns the id while the
// sub-agent works; agent_status reads it as it goes and once it is done.
func TestSpawnWithoutWaitReturnsAtOnce(t *testing.T) {
	release := make(chan struct{})
	m := newTestManager(t, func(ctx context.Context, _ *model.LLMRequest) (*genai.Content, error) {
		select {
		case <-release:
			return genai.NewContentFromText("Done.", genai.RoleModel), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	ctx, _ := m.run(t, context.Background(), "run_1")
	view, err := m.spawn(ctx, testBrief(false))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StateRunning || view.ID == "" || view.ExpiresAt == nil {
		t.Fatalf("view = %+v", view)
	}
	status, err := m.status(ctx, statusArgs{})
	if err != nil || len(status.Agents) != 1 || status.Agents[0].State != StateRunning {
		t.Fatalf("status = %+v, %v", status, err)
	}
	close(release)
	eventually(t, "the sub-agent to finish", m.idle)
	status, err = m.status(ctx, statusArgs{ID: view.ID})
	if err != nil || status.Agents[0].State != StateDone || status.Agents[0].Result != "Done." {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

// TestSpawnsInOneTurnRunConcurrently: three sub-agents spawned at once are
// all working at the same time: none answers until all three have asked.
func TestSpawnsInOneTurnRunConcurrently(t *testing.T) {
	var arrived atomic.Int32
	all := make(chan struct{})
	m := newTestManager(t, func(ctx context.Context, _ *model.LLMRequest) (*genai.Content, error) {
		if arrived.Add(1) == 3 {
			close(all)
		}
		select {
		case <-all:
			return genai.NewContentFromText("Part done.", genai.RoleModel), nil
		case <-time.After(5 * time.Second):
			return nil, errors.New("the other sub-agents never started")
		}
	})
	ctx, _ := m.run(t, context.Background(), "run_1")
	var wg sync.WaitGroup
	views := make([]View, 3)
	errs := make([]error, 3)
	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			views[i], errs[i] = m.spawn(ctx, testBrief(true))
		}()
	}
	wg.Wait()
	for i := range 3 {
		if errs[i] != nil || views[i].State != StateDone {
			t.Fatalf("spawn %d = %+v, %v", i, views[i], errs[i])
		}
	}
}

// askingModel asks Krude once, then answers from Krude's reply.
func askingModel(_ context.Context, request *model.LLMRequest) (*genai.Content, error) {
	if strings.Contains(lastUserText(request), "Krude's reply") {
		return genai.NewContentFromText("Using "+strings.TrimSpace(strings.TrimPrefix(lastUserText(request), "Krude's reply:"))+": $4.", genai.RoleModel), nil
	}
	return call(AskToolName, map[string]any{"question": "Which currency should the price be in?"}), nil
}

// TestAskParentWaitsForKrudesAnswer: a sub-agent that asks waits on Krude;
// message_agent answers it, and it finishes with the answer in its
// transcript.
func TestAskParentWaitsForKrudesAnswer(t *testing.T) {
	m := newTestManager(t, askingModel)
	ctx, _ := m.run(t, context.Background(), "run_1")
	view, err := m.spawn(ctx, testBrief(true))
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StateWaiting || view.Question != "Which currency should the price be in?" || !strings.Contains(view.Note, "message_agent") {
		t.Fatalf("view = %+v", view)
	}
	if calls := len(m.model.requests()); calls != 1 {
		t.Fatalf("model calls after asking = %d, want 1: ask_parent ends the assignment", calls)
	}
	answered, err := m.message(ctx, messageArgs{ID: view.ID, Message: "US dollars", Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	if answered.State != StateDone || answered.Result != "Using US dollars: $4." || answered.Question != "" {
		t.Fatalf("answered = %+v", answered)
	}
	last := m.model.requests()[1]
	if !slices.ContainsFunc(last.Contents, func(content *genai.Content) bool {
		return slices.ContainsFunc(content.Parts, func(part *genai.Part) bool {
			return part.FunctionCall != nil && part.FunctionCall.Name == AskToolName
		})
	}) {
		t.Fatal("the sub-agent's own question is not in its transcript when it is answered")
	}
}

// TestMessageReopensADoneSubAgent: a follow-up to a finished sub-agent
// picks up with its transcript.
func TestMessageReopensADoneSubAgent(t *testing.T) {
	m := newTestManager(t, func(_ context.Context, request *model.LLMRequest) (*genai.Content, error) {
		if strings.Contains(lastUserText(request), "Krude's reply") {
			return genai.NewContentFromText("It was $3 last year.", genai.RoleModel), nil
		}
		return genai.NewContentFromText("It costs $4.", genai.RoleModel), nil
	})
	ctx, _ := m.run(t, context.Background(), "run_1")
	view, err := m.spawn(ctx, testBrief(true))
	if err != nil || view.State != StateDone {
		t.Fatalf("spawn = %+v, %v", view, err)
	}
	again, err := m.message(ctx, messageArgs{ID: view.ID, Message: "And last year?", Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.State != StateDone || again.Result != "It was $3 last year." || again.ModelCalls != 2 {
		t.Fatalf("again = %+v", again)
	}
	followUp := m.model.requests()[1]
	if !slices.ContainsFunc(followUp.Contents, func(content *genai.Content) bool { return text(content) == "It costs $4." }) {
		t.Fatal("a reopened sub-agent does not see its earlier work")
	}
}

// TestMessageToARunningSubAgentIsItsNextAssignment: a message sent while a
// sub-agent works is read once its current assignment ends.
func TestMessageToARunningSubAgentIsItsNextAssignment(t *testing.T) {
	release := make(chan struct{})
	m := newTestManager(t, func(ctx context.Context, request *model.LLMRequest) (*genai.Content, error) {
		if strings.Contains(lastUserText(request), "Krude's reply") {
			return genai.NewContentFromText("In euros: €3.70.", genai.RoleModel), nil
		}
		<-release
		return genai.NewContentFromText("$4.", genai.RoleModel), nil
	})
	ctx, _ := m.run(t, context.Background(), "run_1")
	view, err := m.spawn(ctx, testBrief(false))
	if err != nil {
		t.Fatal(err)
	}
	queued, err := m.message(ctx, messageArgs{ID: view.ID, Message: "Give it in euros too."})
	if err != nil || queued.State != StateRunning {
		t.Fatalf("queued = %+v, %v", queued, err)
	}
	close(release)
	eventually(t, "the sub-agent to finish", m.idle)
	if record := m.store.record(t, view.ID); record.State != StateDone || record.Result != "In euros: €3.70." {
		t.Fatalf("record = %+v", record)
	}
}

// blocking never answers until its call is cancelled.
func blocking(ctx context.Context, _ *model.LLMRequest) (*genai.Content, error) {
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

// TestStopAgentStopsIt: stop_agent stops a working sub-agent; a stopped one
// cannot be resumed.
func TestStopAgentStopsIt(t *testing.T) {
	m := newTestManager(t, blocking)
	ctx, _ := m.run(t, context.Background(), "run_1")
	view, err := m.spawn(ctx, testBrief(false))
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := m.stop(ctx, stopArgs{ID: view.ID})
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != StateStopped || stopped.Error != errStopped.Error() {
		t.Fatalf("stopped = %+v", stopped)
	}
	if !m.idle() {
		t.Fatal("a stopped sub-agent is still working")
	}
	if _, err := m.message(ctx, messageArgs{ID: view.ID, Message: "Go on."}); err == nil || !strings.Contains(err.Error(), "cannot be resumed") {
		t.Fatalf("message to a stopped sub-agent = %v", err)
	}
	// Stopping a finished one keeps it from being reopened.
	m.model.respond = answer("Done.")
	done, err := m.spawn(ctx, testBrief(true))
	if err != nil {
		t.Fatal(err)
	}
	if stopped, err := m.stop(ctx, stopArgs{ID: done.ID}); err != nil || stopped.State != StateStopped {
		t.Fatalf("stopping a done sub-agent = %+v, %v", stopped, err)
	}
}

// TestBudgetOfModelCalls: a sub-agent that would make more model calls than
// its budget fails on the first one past it.
func TestBudgetOfModelCalls(t *testing.T) {
	m := newTestManager(t, func(context.Context, *model.LLMRequest) (*genai.Content, error) {
		return call("lookup", map[string]any{}), nil
	})
	lookup, err := functiontool.New(functiontool.Config{Name: "lookup", Description: "Looks something up."}, func(agent.Context, struct{}) (map[string]any, error) {
		return map[string]any{"found": "nothing yet"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.builder.extra = []tool.Tool{lookup}
	ctx, _ := m.run(t, context.Background(), "run_1")
	in := testBrief(true)
	in.Budget = Budget{ModelCalls: 3}
	view, err := m.spawn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StateFailed || !strings.Contains(view.Error, "budget of model calls (3)") || view.ModelCalls != 3 {
		t.Fatalf("view = %+v", view)
	}
	if calls := len(m.model.requests()); calls != 3 {
		t.Fatalf("model calls = %d, want 3: the fourth is refused before it is made", calls)
	}
	if record := m.store.record(t, view.ID); record.State != StateFailed || record.ModelCalls != 3 {
		t.Fatalf("record = %+v", record)
	}
}

// TestBudgetOfMinutes: a sub-agent that works past its minutes fails.
func TestBudgetOfMinutes(t *testing.T) {
	m := newTestManager(t, blocking)
	m.minute = 20 * time.Millisecond
	ctx, _ := m.run(t, context.Background(), "run_1")
	in := testBrief(true)
	in.Budget = Budget{Minutes: 1}
	view, err := m.spawn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != StateFailed || !strings.Contains(view.Error, "budget of minutes (1)") {
		t.Fatalf("view = %+v", view)
	}
}

func TestBudgetsAreBounded(t *testing.T) {
	m := newTestManager(t, answer("Done."))
	ctx, _ := m.run(t, context.Background(), "run_1")
	in := testBrief(false)
	in.Budget = Budget{ModelCalls: 500, Minutes: 90}
	view, err := m.spawn(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if view.Budget != (Budget{ModelCalls: MaxModelCalls, Minutes: MaxMinutes}) || !strings.Contains(view.Note, "lowered") {
		t.Fatalf("view = %+v", view)
	}
	defaults, err := m.spawn(ctx, testBrief(false))
	if err != nil || defaults.Budget != (Budget{ModelCalls: DefaultModelCalls, Minutes: DefaultMinutes}) {
		t.Fatalf("defaults = %+v, %v", defaults, err)
	}
}

// TestCaps: at most PerRun sub-agents work at once in a run, and PerProcess
// in the process.
func TestCaps(t *testing.T) {
	m := newTestManager(t, blocking, func(config *Config) { config.PerRun, config.PerProcess = 2, 3 })
	first, endFirst := m.run(t, context.Background(), "run_1")
	for range 2 {
		if _, err := m.spawn(first, testBrief(false)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.spawn(first, testBrief(false)); err == nil || !strings.Contains(err.Error(), "2 sub-agents are already working in this run") {
		t.Fatalf("a third in one run = %v", err)
	}
	second, _ := m.run(t, context.Background(), "run_2")
	second.thread = "thread_2"
	if _, err := m.spawn(second, testBrief(false)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.spawn(second, testBrief(false)); err == nil || !strings.Contains(err.Error(), "too many sub-agents") {
		t.Fatalf("a fourth in the process = %v", err)
	}
	endFirst()
	if _, err := m.spawn(second, testBrief(false)); err != nil {
		t.Fatalf("once a run ends its slots are free: %v", err)
	}
}

// TestTTL: a sub-agent past its TTL reads as expired and cannot be
// resumed; one expired for another TTL is removed with its transcript.
func TestTTL(t *testing.T) {
	m := newTestManager(t, answer("Done."), func(config *Config) { config.TTL = time.Hour })
	ctx, _ := m.run(t, context.Background(), "run_1")
	view, err := m.spawn(ctx, testBrief(true))
	if err != nil {
		t.Fatal(err)
	}
	m.clock.advance(time.Hour + time.Minute)
	status, err := m.status(ctx, statusArgs{ID: view.ID})
	if err != nil || status.Agents[0].State != StateExpired || status.Agents[0].Result != "" || !strings.Contains(status.Agents[0].Note, "expired") {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if _, err := m.message(ctx, messageArgs{ID: view.ID, Message: "More?"}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("message to an expired sub-agent = %v", err)
	}
	m.clock.advance(time.Hour)
	if _, err := m.spawn(ctx, testBrief(false)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.status(ctx, statusArgs{ID: view.ID}); err == nil || !strings.Contains(err.Error(), "may have expired") {
		t.Fatalf("status of a swept sub-agent = %v", err)
	}
	if _, err := m.sessions.Get(context.Background(), &session.GetRequest{AppName: AppName, UserID: "member_1", SessionID: "thread_1/" + view.ID}); err == nil {
		t.Fatal("a swept sub-agent's transcript is still kept")
	}
}

// TestTheRunEndingStopsItsWorkingSubAgents: the end of the run that set a
// sub-agent working stops it; a done or waiting one is kept for the
// thread's next run.
func TestTheRunEndingStopsItsWorkingSubAgents(t *testing.T) {
	m := newTestManager(t, func(ctx context.Context, request *model.LLMRequest) (*genai.Content, error) {
		switch {
		case strings.Contains(lastUserText(request), "Keep working"):
			return blocking(ctx, request)
		case strings.Contains(lastUserText(request), "Ask me"):
			return call(AskToolName, map[string]any{"question": "Which one?"}), nil
		case strings.Contains(lastUserText(request), "Krude's reply"):
			return genai.NewContentFromText("Answered on the next run.", genai.RoleModel), nil
		}
		return genai.NewContentFromText("Done.", genai.RoleModel), nil
	})
	ctx, end := m.run(t, context.Background(), "run_1")
	working := testBrief(false)
	working.Objective = "Keep working."
	busy, err := m.spawn(ctx, working)
	if err != nil {
		t.Fatal(err)
	}
	asking := testBrief(true)
	asking.Objective = "Ask me."
	waiting, err := m.spawn(ctx, asking)
	if err != nil || waiting.State != StateWaiting {
		t.Fatalf("waiting = %+v, %v", waiting, err)
	}
	done, err := m.spawn(ctx, testBrief(true))
	if err != nil || done.State != StateDone {
		t.Fatalf("done = %+v, %v", done, err)
	}

	end()
	if !m.idle() {
		t.Fatal("a sub-agent outlived the run that set it working")
	}
	if record := m.store.record(t, busy.ID); record.State != StateStopped || record.Error != errParentEnded.Error() {
		t.Fatalf("working sub-agent after its run = %+v", record)
	}
	if _, err := m.spawn(ctx, testBrief(false)); !errors.Is(err, errRunEnding) {
		t.Fatalf("spawn after the run ended = %v", err)
	}

	next, _ := m.run(t, context.Background(), "run_2")
	if record := m.store.record(t, waiting.ID); record.State != StateWaiting {
		t.Fatalf("waiting sub-agent after its run = %+v", record)
	}
	answered, err := m.message(next, messageArgs{ID: waiting.ID, Message: "The blue one.", Wait: true})
	if err != nil || answered.State != StateDone {
		t.Fatalf("answering on the next run = %+v, %v", answered, err)
	}
	if record := m.store.record(t, waiting.ID); record.ParentRunID != "run_2" {
		t.Fatalf("the sub-agent works for run %q, want run_2", record.ParentRunID)
	}
	reopened, err := m.message(next, messageArgs{ID: done.ID, Message: "Anything else?", Wait: true})
	if err != nil || reopened.State != StateDone {
		t.Fatalf("reopening on the next run = %+v, %v", reopened, err)
	}
}

// TestTheRunsContextCancelledStopsItsSubAgents: a run stopped by the
// member, a drain or a lost lease stops its sub-agents at once.
func TestTheRunsContextCancelledStopsItsSubAgents(t *testing.T) {
	m := newTestManager(t, blocking)
	runContext, cancel := context.WithCancel(context.Background())
	ctx, _ := m.run(t, runContext, "run_1")
	view, err := m.spawn(ctx, testBrief(false))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	eventually(t, "the sub-agent to stop", m.idle)
	if record := m.store.record(t, view.ID); record.State != StateStopped {
		t.Fatalf("record = %+v", record)
	}
}

// TestSpawnOutsideARunIsRefused: without a run's scope nothing can own a
// sub-agent's lifetime, so none starts.
func TestSpawnOutsideARunIsRefused(t *testing.T) {
	m := newTestManager(t, answer("Done."))
	ctx := &toolCtx{StrictContextMock: agent.NewStrictContextMock(context.Background()), state: mapState{"organization_id": "org_1"}, artifacts: m.files, thread: "thread_1"}
	if _, err := m.spawn(ctx, testBrief(true)); !errors.Is(err, errNoScope) {
		t.Fatalf("spawn outside a run = %v", err)
	}
}

// TestBriefsAreChecked: a brief names only tools from the read-only set
// this deployment has, and carries an objective and an output.
func TestBriefsAreChecked(t *testing.T) {
	m := newTestManager(t, answer("Done."))
	m.builder.families = []string{ToolKnowledge, ToolWebSearch}
	ctx, _ := m.run(t, context.Background(), "run_1")
	for name, tc := range map[string]struct {
		brief Brief
		want  string
	}{
		"a write tool":          {testBrief(true, "apps"), "not a tool a sub-agent can have"},
		"the browser":           {testBrief(true, "browser"), "not a tool a sub-agent can have"},
		"nesting":               {testBrief(true, SpawnToolName), "not a tool a sub-agent can have"},
		"unavailable here":      {testBrief(true, ToolSandbox), `"sandbox" is not available here`},
		"no objective":          {Brief{Output: "x"}, "needs an objective"},
		"no output":             {Brief{Objective: "x"}, "needs an output"},
		"too many files":        {Brief{Objective: "x", Output: "y", Files: []string{"a", "b", "c", "d", "e", "f"}}, "at most 5 files"},
		"an unknown file":       {Brief{Objective: "x", Output: "y", Files: []string{"missing.pdf"}}, `no file "missing.pdf"`},
		"an objective too long": {Brief{Objective: strings.Repeat("x", maxObjectiveRunes+1), Output: "y"}, "longer than"},
	} {
		if _, err := m.spawn(ctx, tc.brief); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	view, err := m.spawn(ctx, testBrief(true, " Web_Search ", "knowledge", "knowledge"))
	if err != nil || !slices.Equal(view.Tools, []string{ToolKnowledge, ToolWebSearch}) {
		t.Fatalf("tools = %+v, %v", view.Tools, err)
	}
}

// TestFilesAreHandedOverReadOnly: the files a brief names reach the
// sub-agent in its first message, and nothing else of the thread's does.
func TestFilesAreHandedOverReadOnly(t *testing.T) {
	m := newTestManager(t, answer("Read it."))
	m.files.parts = map[string]*genai.Part{
		"prices.csv": genai.NewPartFromBytes([]byte("widget,4\n"), "text/csv"),
		"secret.txt": genai.NewPartFromText("not handed over"),
	}
	ctx, _ := m.run(t, context.Background(), "run_1")
	in := testBrief(true)
	in.Files = []string{"prices.csv"}
	if _, err := m.spawn(ctx, in); err != nil {
		t.Fatal(err)
	}
	first := m.model.requests()[0]
	var handed, leaked bool
	for _, content := range first.Contents {
		for _, part := range content.Parts {
			handed = handed || (part.InlineData != nil && string(part.InlineData.Data) == "widget,4\n")
			leaked = leaked || strings.Contains(part.Text, "not handed over")
		}
	}
	if !handed || leaked {
		t.Fatalf("handed = %v, leaked = %v", handed, leaked)
	}
	m.files.parts["big.bin"] = genai.NewPartFromBytes(make([]byte, maxFileBytes+1), "application/octet-stream")
	in.Files = []string{"big.bin"}
	if _, err := m.spawn(ctx, in); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a file too large = %v", err)
	}
}

// TestALongResultIsSavedAsAnArtifact: a result longer than the record holds
// is condensed there and saved whole as a file of the thread, named in the
// record, the first time Krude reads it.
func TestALongResultIsSavedAsAnArtifact(t *testing.T) {
	long := strings.Repeat("row of findings\n", 600)
	m := newTestManager(t, answer(long))
	ctx, _ := m.run(t, context.Background(), "run_1")
	view, err := m.spawn(ctx, testBrief(true))
	if err != nil {
		t.Fatal(err)
	}
	name := "subagent-" + view.ID + ".md"
	if view.ResultArtifact != name || len([]rune(view.Result)) > maxResultRunes || !strings.Contains(view.Result, "result_artifact") {
		t.Fatalf("view = %+v", view.ResultArtifact)
	}
	saved := m.files.parts[name]
	if saved == nil || string(saved.InlineData.Data) != strings.TrimSpace(long) {
		t.Fatal("the whole result was not saved as an artifact of the thread")
	}
	if record := m.store.record(t, view.ID); record.FullResult != "" || record.ResultArtifact != name {
		t.Fatalf("record keeps %d runes of full result, artifact %q", len(record.FullResult), record.ResultArtifact)
	}
	if _, err := m.status(ctx, statusArgs{}); err != nil || len(m.files.saved) != 1 {
		t.Fatalf("reading it again saved it again: %v, %v", m.files.saved, err)
	}
}

// TestUsageIsPricedIntoTheRun: what a sub-agent's model calls spend is on
// the usage counter of the run that set it working, as Krude's own calls
// are, and on its record.
func TestUsageIsPricedIntoTheRun(t *testing.T) {
	m := newTestManager(t, answer("Done."))
	m.builder.llm = usage.Counted(m.model)
	counter := usage.NewCounter()
	ctx, _ := m.run(t, usage.WithCounter(context.Background(), counter), "run_1")
	view, err := m.spawn(ctx, testBrief(true))
	if err != nil {
		t.Fatal(err)
	}
	if total := counter.Total(); total.InputTokens != 10 || total.OutputTokens != 5 || total.TotalTokens != 15 {
		t.Fatalf("the run's usage = %+v, want the sub-agent's call on it", total)
	}
	if record := m.store.record(t, view.ID); record.TotalTokens != 15 {
		t.Fatalf("the record's usage = %d", record.TotalTokens)
	}
}

// TestSubAgentsAreTheThreadsAlone: another thread, member or organization
// reads none of a thread's sub-agents.
func TestSubAgentsAreTheThreadsAlone(t *testing.T) {
	m := newTestManager(t, answer("Done."))
	ctx, _ := m.run(t, context.Background(), "run_1")
	view, err := m.spawn(ctx, testBrief(true))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := m.run(t, context.Background(), "run_2")
	other.thread = "thread_2"
	if _, err := m.status(other, statusArgs{ID: view.ID}); err == nil {
		t.Fatal("another thread read the sub-agent")
	}
	if _, err := m.message(other, messageArgs{ID: view.ID, Message: "hi"}); err == nil {
		t.Fatal("another thread messaged the sub-agent")
	}
	if listed, err := m.status(other, statusArgs{}); err != nil || len(listed.Agents) != 0 {
		t.Fatalf("another thread lists %+v, %v", listed, err)
	}
	foreign, _ := m.run(t, context.Background(), "run_3")
	foreign.state = mapState{"organization_id": "org_2"}
	if _, err := m.stop(foreign, stopArgs{ID: view.ID}); err == nil {
		t.Fatal("another organization stopped the sub-agent")
	}
}

// TestForgettingRemovesSubAgents: a deleted thread, or a member forgotten,
// takes their sub-agents and transcripts with them.
func TestForgettingRemovesSubAgents(t *testing.T) {
	m := newTestManager(t, answer("Done."))
	ctx, _ := m.run(t, context.Background(), "run_1")
	first, err := m.spawn(ctx, testBrief(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ForgetThread(context.Background(), "org_1", "member_1", "krude", "thread_1"); err != nil {
		t.Fatal(err)
	}
	if len(m.store.records) != 0 {
		t.Fatal("the thread's sub-agents outlived it")
	}
	if _, err := m.sessions.Get(context.Background(), &session.GetRequest{AppName: AppName, UserID: "member_1", SessionID: "thread_1/" + first.ID}); err == nil {
		t.Fatal("a sub-agent's transcript outlived its thread")
	}
	if _, err := m.spawn(ctx, testBrief(true)); err != nil {
		t.Fatal(err)
	}
	if err := m.Forget(context.Background(), "org_1", "member_1"); err != nil || len(m.store.records) != 0 {
		t.Fatalf("forget = %v, %d left", err, len(m.store.records))
	}
}

// TestKrudesToolsAreTheFour: Krude manages sub-agents with exactly these.
func TestKrudesToolsAreTheFour(t *testing.T) {
	m := newTestManager(t, answer("Done."))
	tools, err := m.Tools()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, built := range tools {
		names = append(names, built.Name())
	}
	if !slices.Equal(names, []string{SpawnToolName, MessageToolName, StatusToolName, StopToolName}) {
		t.Fatalf("tools = %v", names)
	}
}

// TestARunningRecordNothingWorksOnReadsAsStopped: a record left running by
// a run whose process died reads as stopped, not as working forever.
func TestARunningRecordNothingWorksOnReadsAsStopped(t *testing.T) {
	m := newTestManager(t, answer("Done."))
	ctx, _ := m.run(t, context.Background(), "run_2")
	now := m.clock.Now()
	if err := m.store.Create(context.Background(), Record{
		ID: "sa_left", Thread: testThread, ParentRunID: "run_1", Objective: "x", State: StateRunning,
		BudgetModelCalls: 1, BudgetMinutes: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	status, err := m.status(ctx, statusArgs{})
	if err != nil || len(status.Agents) != 1 || status.Agents[0].State != StateStopped || !strings.Contains(status.Agents[0].Error, "run ended") {
		t.Fatalf("status = %+v, %v", status, err)
	}
}
