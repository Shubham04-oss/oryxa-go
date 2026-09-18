package threads

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adkmemory"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/usage"
)

const testModel = "gemini-3.8-flash"

// scriptedModel answers every request with what respond returns, streamed
// the way ADK's Gemini model streams: partial chunks, then one finished
// response carrying the call's usage.
type scriptedModel struct {
	respond func(request *model.LLMRequest) (*genai.Content, error)
	mu      sync.Mutex
	tokens  []string
}

func (m *scriptedModel) Name() string { return testModel }

func (m *scriptedModel) GenerateContent(ctx context.Context, request *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	m.tokens = append(m.tokens, memberauth.AccessToken(ctx))
	m.mu.Unlock()
	content, err := m.respond(request)
	return func(yield func(*model.LLMResponse, error) bool) {
		if err != nil {
			yield(nil, err)
			return
		}
		finished := &model.LLMResponse{
			Content:       content,
			TurnComplete:  true,
			FinishReason:  genai.FinishReasonStop,
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 10, TotalTokenCount: 110},
		}
		if stream {
			if text := contentText(content); len(text) > 1 {
				for _, piece := range []string{text[:len(text)/2], text[len(text)/2:]} {
					partial := &model.LLMResponse{Content: genai.NewContentFromText(piece, genai.RoleModel), Partial: true}
					if !yield(partial, nil) {
						return
					}
				}
			}
		}
		yield(finished, nil)
	}
}

func contentText(content *genai.Content) string {
	var text strings.Builder
	for _, part := range content.Parts {
		text.WriteString(part.Text)
	}
	return text.String()
}

// lastFunctionResponse is the newest function response a request carries,
// if its last content is one.
func lastFunctionResponse(request *model.LLMRequest) *genai.FunctionResponse {
	if len(request.Contents) == 0 {
		return nil
	}
	for _, part := range request.Contents[len(request.Contents)-1].Parts {
		if part.FunctionResponse != nil {
			return part.FunctionResponse
		}
	}
	return nil
}

type fakeRuns struct {
	mu        sync.Mutex
	started   []runstore.Run
	finished  map[string]runstore.Outcome
	events    map[string]events.Event
	cancelled []string
	startErr  error
	renewErr  error
	// onStart runs as a run takes its thread, before it holds it: what
	// another run could do in that moment.
	onStart func()
}

func newFakeRuns() *fakeRuns {
	return &fakeRuns{finished: map[string]runstore.Outcome{}, events: map[string]events.Event{}}
}

func (runs *fakeRuns) Start(_ context.Context, run runstore.Run, _ time.Duration) error {
	if hook := runs.onStart; hook != nil {
		runs.onStart = nil
		hook()
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if runs.startErr != nil {
		return runs.startErr
	}
	runs.started = append(runs.started, run)
	return nil
}

func (runs *fakeRuns) Cancel(_ context.Context, runID string) error {
	runs.mu.Lock()
	defer runs.mu.Unlock()
	runs.cancelled = append(runs.cancelled, runID)
	return nil
}

func (runs *fakeRuns) Renew(context.Context, string, time.Duration) error {
	runs.mu.Lock()
	defer runs.mu.Unlock()
	return runs.renewErr
}

func (runs *fakeRuns) Finish(_ context.Context, runID string, outcome runstore.Outcome, event events.Event) error {
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if _, done := runs.finished[runID]; done {
		return runstore.ErrNotRunning
	}
	runs.finished[runID] = outcome
	runs.events[runID] = event
	return nil
}

type harness struct {
	t        *testing.T
	service  *Service
	runs     *fakeRuns
	model    *scriptedModel
	deleted  *[]string
	sessions session.Service
}

// newHarness builds the service over an agent named krude with one tool that
// asks the member to confirm it first.
func newHarness(t *testing.T, respond func(request *model.LLMRequest) (*genai.Content, error)) *harness {
	t.Helper()
	llm := &scriptedModel{respond: respond}
	var deleted []string
	deleteTool, err := functiontool.New(functiontool.Config{
		Name:                "delete_file",
		Description:         "Deletes a file.",
		RequireConfirmation: true,
	}, func(_ agent.Context, args struct {
		Name string `json:"name"`
	}) (map[string]any, error) {
		deleted = append(deleted, args.Name)
		return map[string]any{"deleted": args.Name}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	krude, err := llmagent.New(llmagent.Config{
		Name:        "krude",
		Model:       usage.Counted(llm),
		Instruction: "You are Krude.",
		Tools:       []tool.Tool{deleteTool},
	})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	runs := newFakeRuns()
	sessions := session.InMemoryService()
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: sessions, Artifacts: artifact.InMemoryService(),
		Runs: runs, Prices: prices, RunTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, service: service, runs: runs, model: llm, deleted: &deleted, sessions: sessions}
}

var asha = Member{UserID: "user_asha", OrganizationID: "org_1", AccessToken: "asha-token"}

// run starts and executes a run, returning every item it streamed.
func (h *harness) run(member Member, threadID string, input RunInput) ([]Item, *Run) {
	h.t.Helper()
	run, err := h.service.StartRun(context.Background(), member, threadID, input)
	if err != nil {
		h.t.Fatalf("StartRun() error = %v", err)
	}
	var items []Item
	run.Execute(context.Background(), func(item Item) error {
		items = append(items, item)
		return nil
	})
	return items, run
}

func types(items []Item) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Type)
	}
	return names
}

func find(items []Item, itemType string) []Item {
	var found []Item
	for _, item := range items {
		if item.Type == itemType {
			found = append(found, item)
		}
	}
	return found
}

func TestAMessageStreamsKrudesAnswerAndRecordsWhatItCost(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("Hello, Asha.", genai.RoleModel), nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.CreatedAt.IsZero() || created.Title != "" {
		t.Fatalf("created = %+v", created)
	}

	items, run := h.run(asha, created.ID, RunInput{Message: "  Say hello\nto me  "})
	got := types(items)
	if got[0] != ItemRunStarted || got[len(got)-1] != ItemRunCompleted {
		t.Fatalf("items = %v, want the run to open and close the stream", got)
	}
	if deltas := find(items, ItemDelta); len(deltas) != 2 || deltas[0].Text+deltas[1].Text != "Hello, Asha." {
		t.Fatalf("deltas = %+v", deltas)
	}
	messages := find(items, ItemMessage)
	last := messages[len(messages)-1]
	if last.Author != "krude" || last.Text != "Hello, Asha." || last.ID == "" {
		t.Fatalf("answer = %+v", last)
	}
	if first := messages[0]; first.Author != AuthorMember || first.Text != "Say hello\nto me" {
		t.Fatalf("the member's own message was not streamed back first: %+v", messages)
	}
	completed := items[len(items)-1]
	if completed.RunID != run.ID() || completed.Usage == nil || completed.Usage.InputTokens != 100 || completed.Usage.OutputTokens != 10 {
		t.Fatalf("run_completed = %+v", completed)
	}

	// The member's own token rode on the run, for the tools acting as them.
	if len(h.model.tokens) == 0 || h.model.tokens[0] != "asha-token" {
		t.Fatalf("the model call's context carried %v, want the member's token", h.model.tokens)
	}

	if len(h.runs.started) != 1 || h.runs.started[0].SessionID != created.ID || h.runs.started[0].StartedBy != agentsv1.StartedByMember {
		t.Fatalf("started = %+v", h.runs.started)
	}
	outcome := h.runs.finished[run.ID()]
	if outcome.Status != runstore.StatusCompleted || outcome.Usage.TotalTokens != 110 || outcome.CostMicros <= 0 {
		t.Fatalf("outcome = %+v", outcome)
	}
	event := h.runs.events[run.ID()]
	var detail agentsv1.RunCompleted
	if event.DetailType != agentsv1.RunCompletedDetailType || json.Unmarshal(event.Data, &detail) != nil {
		t.Fatalf("event = %+v", event)
	}
	if detail.OrganizationID != "org_1" || detail.UserID != "user_asha" || len(detail.Models) != 1 ||
		detail.Models[0].Model != testModel || !detail.Models[0].Priced || detail.Cost.TotalMicros != outcome.CostMicros {
		t.Fatalf("run.completed = %+v", detail)
	}

	thread, err := h.service.Get(context.Background(), asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Title != "Say hello" {
		t.Fatalf("title = %q, want the first line of the first message", thread.Title)
	}
	if strings.Join(types(thread.Items), ",") != "message,message" || len(thread.Pending) != 0 {
		t.Fatalf("history = %+v", thread)
	}
}

func TestAToolThatAsksWaitsForTheMembersAnswer(t *testing.T) {
	h := newHarness(t, func(request *model.LLMRequest) (*genai.Content, error) {
		if response := lastFunctionResponse(request); response != nil {
			if _, failed := response.Response["error"]; failed {
				return genai.NewContentFromText("I left it alone.", genai.RoleModel), nil
			}
			return genai.NewContentFromText("Deleted it.", genai.RoleModel), nil
		}
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "call_1", Name: "delete_file", Args: map[string]any{"name": "plan.txt"},
		}}}}, nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}

	items, _ := h.run(asha, created.ID, RunInput{Message: "Delete plan.txt"})
	calls := find(items, ItemToolCall)
	if len(calls) != 1 || calls[0].Tool != "delete_file" || calls[0].Arguments["name"] != "plan.txt" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if results := find(items, ItemToolResult); len(results) != 0 {
		t.Fatalf("a tool waiting for its confirmation showed a result: %+v", results)
	}
	confirmations := find(items, ItemConfirmation)
	if len(confirmations) != 1 || confirmations[0].Tool != "delete_file" || confirmations[0].Status != ConfirmationPending ||
		confirmations[0].Arguments["name"] != "plan.txt" || confirmations[0].CallID == "" || confirmations[0].ToolCallID != calls[0].CallID {
		t.Fatalf("confirmations = %+v, want one holding back call %s", confirmations, calls[0].CallID)
	}
	if confirmations[0].Hint != "" {
		t.Fatalf("hint = %q, want ADK's default left out", confirmations[0].Hint)
	}
	if len(*h.deleted) != 0 {
		t.Fatalf("the tool ran before it was confirmed: %v", *h.deleted)
	}
	confirmationID := confirmations[0].CallID

	thread, err := h.service.Get(context.Background(), asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(thread.Pending, []string{confirmationID}) {
		t.Fatalf("pending = %v, want [%s]", thread.Pending, confirmationID)
	}

	// Nothing else can happen until it is answered, and only it can be.
	if _, err := h.service.StartRun(context.Background(), asha, created.ID, RunInput{Message: "never mind"}); !errors.Is(err, ErrConfirmationPending) {
		t.Fatalf("a message while a tool waits = %v, want ErrConfirmationPending", err)
	}
	if _, err := h.service.StartRun(context.Background(), asha, created.ID, RunInput{Confirmations: []Answer{{ID: "made_up", Confirmed: true}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an answer to nothing waiting = %v, want ErrInvalid", err)
	}
	if _, err := h.service.StartRun(context.Background(), asha, created.ID, RunInput{
		Confirmations: []Answer{{ID: confirmationID, Confirmed: true}, {ID: confirmationID, Confirmed: false}},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an answer given twice = %v, want ErrInvalid", err)
	}

	items, _ = h.run(asha, created.ID, RunInput{Confirmations: []Answer{{ID: confirmationID, Confirmed: true}}})
	if !slices.Equal(*h.deleted, []string{"plan.txt"}) {
		t.Fatalf("deleted = %v, want the approved call to run once", *h.deleted)
	}
	results := find(items, ItemToolResult)
	if len(results) != 1 || results[0].Failed || results[0].Tool != "delete_file" {
		t.Fatalf("results = %+v", results)
	}
	if answers := find(items, ItemMessage); answers[len(answers)-1].Text != "Deleted it." {
		t.Fatalf("answer = %+v", answers)
	}

	thread, err = h.service.Get(context.Background(), asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(thread.Pending) != 0 {
		t.Fatalf("pending after the answer = %v", thread.Pending)
	}
	history := find(thread.Items, ItemConfirmation)
	if len(history) != 1 || history[0].Status != ConfirmationApproved {
		t.Fatalf("confirmation in history = %+v", history)
	}
}

func TestARunChecksTheThreadOnlyOnceItHoldsIt(t *testing.T) {
	h := newHarness(t, func(request *model.LLMRequest) (*genai.Content, error) {
		if lastFunctionResponse(request) != nil {
			return genai.NewContentFromText("Deleted it.", genai.RoleModel), nil
		}
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "call_1", Name: "delete_file", Args: map[string]any{"name": "plan.txt"},
		}}}}, nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}

	// A message whose sender last saw the thread idle, while another run
	// finishes by asking for a confirmation just before this one takes it.
	var confirmationID string
	h.runs.onStart = func() {
		items, _ := h.run(asha, created.ID, RunInput{Message: "Delete plan.txt"})
		confirmationID = find(items, ItemConfirmation)[0].CallID
	}
	if _, err := h.service.StartRun(context.Background(), asha, created.ID, RunInput{Message: "never mind"}); !errors.Is(err, ErrConfirmationPending) {
		t.Fatalf("a message racing a new confirmation = %v, want ErrConfirmationPending", err)
	}
	if len(h.runs.cancelled) != 1 {
		t.Fatalf("cancelled = %v, want the refused run released", h.runs.cancelled)
	}

	// An answer whose sender last saw the confirmation waiting, while another
	// run applies the same answer first, must not run the tool again.
	answer := RunInput{Confirmations: []Answer{{ID: confirmationID, Confirmed: true}}}
	h.runs.onStart = func() { h.run(asha, created.ID, answer) }
	if _, err := h.service.StartRun(context.Background(), asha, created.ID, answer); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a replayed answer = %v, want ErrInvalid", err)
	}
	if !slices.Equal(*h.deleted, []string{"plan.txt"}) {
		t.Fatalf("deleted = %v, want the approved call to run exactly once", *h.deleted)
	}
}

func TestADeclinedToolNeverRuns(t *testing.T) {
	h := newHarness(t, func(request *model.LLMRequest) (*genai.Content, error) {
		if lastFunctionResponse(request) != nil {
			return genai.NewContentFromText("I left it alone.", genai.RoleModel), nil
		}
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "call_1", Name: "delete_file", Args: map[string]any{"name": "plan.txt"},
		}}}}, nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := h.run(asha, created.ID, RunInput{Message: "Delete plan.txt"})
	confirmationID := find(items, ItemConfirmation)[0].CallID

	h.run(asha, created.ID, RunInput{Confirmations: []Answer{{ID: confirmationID, Confirmed: false}}})
	if len(*h.deleted) != 0 {
		t.Fatalf("a declined tool ran: %v", *h.deleted)
	}
	thread, err := h.service.Get(context.Background(), asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if history := find(thread.Items, ItemConfirmation); len(history) != 1 || history[0].Status != ConfirmationDeclined {
		t.Fatalf("confirmation in history = %+v", history)
	}
}

func TestAThreadIsItsMembersOwnInItsOrganization(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("ok", genai.RoleModel), nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := Member{UserID: asha.UserID, OrganizationID: "org_2"}
	someoneElse := Member{UserID: "user_ravi", OrganizationID: asha.OrganizationID}
	for _, member := range []Member{elsewhere, someoneElse} {
		if _, err := h.service.Get(context.Background(), member, created.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get as %+v = %v, want ErrNotFound", member, err)
		}
		if _, err := h.service.StartRun(context.Background(), member, created.ID, RunInput{Message: "hi"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("StartRun as %+v = %v, want ErrNotFound", member, err)
		}
		if err := h.service.Delete(context.Background(), member, created.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Delete as %+v = %v, want ErrNotFound", member, err)
		}
		listed, err := h.service.List(context.Background(), member)
		if err != nil || len(listed) != 0 {
			t.Errorf("List as %+v = %v, %v, want nothing", member, listed, err)
		}
	}
	listed, err := h.service.List(context.Background(), asha)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("List as the owner = %v, %v", listed, err)
	}
	if err := h.service.Delete(context.Background(), asha, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Get(context.Background(), asha, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
}

func TestAFailedRunSaysWhatTheMemberCanDoAndRecordsWhy(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return nil, errors.New("vertex said 429 RESOURCE_EXHAUSTED")
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	items, run := h.run(asha, created.ID, RunInput{Message: "hi"})
	failed := items[len(items)-1]
	if failed.Type != ItemRunFailed || failed.Error != "Krude ran into a problem and stopped. Try again." {
		t.Fatalf("last item = %+v", failed)
	}
	if strings.Contains(failed.Error, "429") {
		t.Fatalf("the model's error reached the member: %q", failed.Error)
	}
	outcome := h.runs.finished[run.ID()]
	if outcome.Status != runstore.StatusFailed || !strings.Contains(outcome.Error, "RESOURCE_EXHAUSTED") {
		t.Fatalf("outcome = %+v", outcome)
	}
	if h.runs.events[run.ID()].DetailType != agentsv1.RunFailedDetailType {
		t.Fatalf("event = %+v", h.runs.events[run.ID()])
	}
}

func TestAMemberWhoStopsListeningStopsTheRun(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("a long answer", genai.RoleModel), nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.service.StartRun(context.Background(), asha, created.ID, RunInput{Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	sent := 0
	run.Execute(context.Background(), func(Item) error {
		sent++
		return errors.New("broken pipe")
	})
	if sent != 1 {
		t.Fatalf("items sent after the member left = %d, want only the first", sent)
	}
	if outcome := h.runs.finished[run.ID()]; outcome.Status != runstore.StatusFailed || !strings.Contains(outcome.Error, "stopped listening") {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestARunThatCannotStartSaysWhy(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("ok", genai.RoleModel), nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []RunInput{
		{},
		{Message: "   "},
		{Message: "hi", Confirmations: []Answer{{ID: "c", Confirmed: true}}},
		{Message: strings.Repeat("a", maxMessageRunes+1)},
		{Confirmations: []Answer{{ID: "c", Confirmed: true}}},
		{Message: "hi", StartedBy: "a stranger"},
		{Confirmations: []Answer{{ID: "c", Confirmed: true}}, Wakeup: "wb_1"},
	} {
		if _, err := h.service.StartRun(context.Background(), asha, created.ID, input); !errors.Is(err, ErrInvalid) {
			t.Errorf("StartRun(%.40v) = %v, want ErrInvalid", input, err)
		}
	}
	if _, err := h.service.StartRun(context.Background(), asha, "no_such_thread", RunInput{Message: "hi"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StartRun on a missing thread = %v", err)
	}
	h.runs.startErr = runstore.ErrRunInProgress
	if _, err := h.service.StartRun(context.Background(), asha, created.ID, RunInput{Message: "hi"}); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("StartRun while another runs = %v, want ErrRunInProgress", err)
	}
}

func TestADiscardedRunFreesItsThread(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("ok", genai.RoleModel), nil
	})
	created, err := h.service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.service.StartRun(context.Background(), asha, created.ID, RunInput{Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	run.Discard(context.Background(), "the stream could not be opened")
	run.Execute(context.Background(), func(Item) error {
		t.Fatal("a discarded run executed")
		return nil
	})
	if outcome := h.runs.finished[run.ID()]; outcome.Status != runstore.StatusFailed || outcome.Error != "the stream could not be opened" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(h.model.tokens) != 0 {
		t.Fatal("a discarded run called the model")
	}
}

func TestForgettingAMemberOrAnOrganizationRemovesTheirThreadsThere(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("ok", genai.RoleModel), nil
	})
	ctx := context.Background()
	ravi := Member{UserID: "user_ravi", OrganizationID: "org_1"}
	ashaElsewhere := Member{UserID: asha.UserID, OrganizationID: "org_2"}
	for _, member := range []Member{asha, asha, ravi, ashaElsewhere} {
		if _, err := h.service.Create(ctx, member); err != nil {
			t.Fatal(err)
		}
	}
	count := func(member Member) int {
		t.Helper()
		listed, err := h.service.List(ctx, member)
		if err != nil {
			t.Fatal(err)
		}
		return len(listed)
	}

	removed, err := h.service.Forget(ctx, "org_1", asha.UserID)
	if err != nil || removed != 2 {
		t.Fatalf("Forget(asha in org_1) = %d, %v, want 2", removed, err)
	}
	if count(asha) != 0 || count(ravi) != 1 || count(ashaElsewhere) != 1 {
		t.Fatalf("after Asha left org_1: asha %d, ravi %d, asha in org_2 %d", count(asha), count(ravi), count(ashaElsewhere))
	}
	if again, err := h.service.Forget(ctx, "org_1", asha.UserID); err != nil || again != 0 {
		t.Fatalf("Forget again = %d, %v, want nothing more", again, err)
	}

	removed, err = h.service.Forget(ctx, "org_1", "")
	if err != nil || removed != 1 || count(ravi) != 0 || count(ashaElsewhere) != 1 {
		t.Fatalf("Forget(org_1) = %d, %v; ravi %d, asha in org_2 %d", removed, err, count(ravi), count(ashaElsewhere))
	}
	if _, err := h.service.Forget(ctx, "", ""); err == nil {
		t.Fatal("Forget with no organization would forget everyone's threads")
	}
}

// TestForgettingAlsoForgetsWhatKrudeRemembered checks that memory leaves with
// the organization it was learned in. Threads and artifacts already go when a
// member leaves or an organization is deleted; memory distilled from those
// same threads must not be what outlives the place they were held in.
func TestForgettingAlsoForgetsWhatKrudeRemembered(t *testing.T) {
	ctx := context.Background()
	remembering, err := adkmemory.New(adkmemory.InMemory())
	if err != nil {
		t.Fatal(err)
	}
	krude, err := llmagent.New(llmagent.Config{
		Name:  "krude",
		Model: &scriptedModel{respond: func(*model.LLMRequest) (*genai.Content, error) { return nil, nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: session.InMemoryService(),
		Runs: newFakeRuns(), Prices: prices, Memory: remembering,
	})
	if err != nil {
		t.Fatal(err)
	}

	view := func(member Member) memory.Service {
		t.Helper()
		scoped, err := remembering.For(adkmemory.Scope{
			OrganizationID: member.OrganizationID, UserID: member.UserID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return scoped
	}
	remember := func(member Member, said string) {
		t.Helper()
		sessions := session.InMemoryService()
		created, err := sessions.Create(ctx, &session.CreateRequest{AppName: "krude", UserID: member.UserID})
		if err != nil {
			t.Fatal(err)
		}
		event := session.NewEvent(ctx, "invocation")
		event.Author = "user"
		event.Content = genai.NewContentFromText(said, genai.RoleUser)
		if err := sessions.AppendEvent(ctx, created.Session, event); err != nil {
			t.Fatal(err)
		}
		if err := view(member).AddSessionToMemory(ctx, created.Session); err != nil {
			t.Fatal(err)
		}
	}
	recalled := func(member Member) int {
		t.Helper()
		found, err := view(member).SearchMemory(ctx, &memory.SearchRequest{
			AppName: "krude", UserID: member.UserID, Query: "migration",
		})
		if err != nil {
			t.Fatal(err)
		}
		return len(found.Memories)
	}

	ravi := Member{UserID: "user_ravi", OrganizationID: asha.OrganizationID}
	ashaElsewhere := Member{UserID: asha.UserID, OrganizationID: "org_2"}
	for member, said := range map[Member]string{
		asha:          "the migration ships in March",
		ravi:          "the migration needs review",
		ashaElsewhere: "the migration is on hold",
	} {
		remember(member, said)
	}

	if _, err := service.Forget(ctx, asha.OrganizationID, asha.UserID); err != nil {
		t.Fatalf("Forget(asha in %s): %v", asha.OrganizationID, err)
	}
	if got := recalled(asha); got != 0 {
		t.Fatalf("a member who left is still remembered there: %d memories", got)
	}
	if got := recalled(ashaElsewhere); got != 1 {
		t.Fatalf("leaving one organization took the member's memory in another: %d memories", got)
	}
	if got := recalled(ravi); got != 1 {
		t.Fatalf("one member leaving took another's memory: %d memories", got)
	}

	if _, err := service.Forget(ctx, asha.OrganizationID, ""); err != nil {
		t.Fatalf("Forget(%s): %v", asha.OrganizationID, err)
	}
	if got := recalled(ravi); got != 0 {
		t.Fatalf("a deleted organization is still remembered: %d memories", got)
	}
	if got := recalled(ashaElsewhere); got != 1 {
		t.Fatalf("deleting one organization took another's memories: %d memories", got)
	}
}

// rememberingBank records what a run handed to memory.
type rememberingBank struct {
	mu      sync.Mutex
	written []struct {
		scope adkmemory.Scope
		said  []string
	}
}

func (b *rememberingBank) Write(_ context.Context, scope adkmemory.Scope, conversation session.Session) error {
	var said []string
	for event := range conversation.Events().All() {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.Text != "" {
				said = append(said, part.Text)
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.written = append(b.written, struct {
		scope adkmemory.Scope
		said  []string
	}{scope, said})
	return nil
}

func (b *rememberingBank) Search(context.Context, adkmemory.Scope, string) ([]memory.Entry, error) {
	return nil, nil
}

func (b *rememberingBank) Forget(context.Context, string, string) error { return nil }

// TestACompletedRunIsRemembered checks the write half of the wiring: without
// it nothing ever calls AddSessionToMemory, since ADK does not, and a memory
// service would sit behind the runner holding nothing.
func TestACompletedRunIsRemembered(t *testing.T) {
	bank := &rememberingBank{}
	remembering, err := adkmemory.New(bank)
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedModel{respond: func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("noted", genai.RoleModel), nil
	}}
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: usage.Counted(llm), Instruction: "You are Krude."})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	runs := newFakeRuns()
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: session.InMemoryService(),
		Runs: runs, Prices: prices, RunTimeout: time.Minute, Memory: remembering,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, service: service, runs: runs, model: llm}
	created, err := service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}

	h.run(asha, created.ID, RunInput{Message: "the migration ships in March"})

	if len(bank.written) != 1 {
		t.Fatalf("memory was written %d times, want once", len(bank.written))
	}
	written := bank.written[0]
	if written.scope.OrganizationID != asha.OrganizationID || written.scope.UserID != asha.UserID {
		t.Fatalf("scope = %+v, want the member's own organization", written.scope)
	}
	if !slices.Contains(written.said, "the migration ships in March") {
		t.Fatalf("remembered %q, want what the member said", written.said)
	}

	// A second run remembers its own turns, not the thread from the start.
	h.run(asha, created.ID, RunInput{Message: "it slipped to April"})
	if len(bank.written) != 2 {
		t.Fatalf("memory was written %d times, want twice", len(bank.written))
	}
	second := bank.written[1]
	if !slices.Contains(second.said, "it slipped to April") {
		t.Fatalf("the second run remembered %q", second.said)
	}
	if slices.Contains(second.said, "the migration ships in March") {
		t.Fatalf("the second run sent the whole thread again: %q", second.said)
	}
}

// TestAFailedRunIsNotRemembered keeps a run that never answered out of
// memory: what it added to the thread is a question and an error.
func TestAFailedRunIsNotRemembered(t *testing.T) {
	bank := &rememberingBank{}
	remembering, err := adkmemory.New(bank)
	if err != nil {
		t.Fatal(err)
	}
	llm := &scriptedModel{respond: func(*model.LLMRequest) (*genai.Content, error) {
		return nil, errors.New("the model was unreachable")
	}}
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: usage.Counted(llm), Instruction: "You are Krude."})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	runs := newFakeRuns()
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: session.InMemoryService(),
		Runs: runs, Prices: prices, RunTimeout: time.Minute, Memory: remembering,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, service: service, runs: runs, model: llm}
	created, err := service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}

	h.run(asha, created.ID, RunInput{Message: "the migration ships in March"})

	if len(bank.written) != 0 {
		t.Fatalf("a failed run was remembered: %+v", bank.written)
	}
}

// waitingModel answers only once the run is stopped.
type waitingModel struct{}

func (waitingModel) Name() string { return testModel }

func (waitingModel) GenerateContent(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		<-ctx.Done()
		yield(nil, ctx.Err())
	}
}

func TestARunThatCannotRenewItsHoldForAWholeLeaseStops(t *testing.T) {
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: waitingModel{}, Instruction: "You are Krude."})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	runs := newFakeRuns()
	runs.renewErr = errors.New("the database stalled")
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: session.InMemoryService(), Runs: runs, Prices: prices,
		RunTimeout: time.Minute, Lease: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, service: service, runs: runs}
	created, err := service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	items, run := h.run(asha, created.ID, RunInput{Message: "hi"})
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("the run went on for %s without its hold", elapsed)
	}
	if last := items[len(items)-1]; last.Type != ItemRunFailed || last.Error != "Another run took over this thread." {
		t.Fatalf("last item = %+v", last)
	}
	if outcome := runs.finished[run.ID()]; outcome.Status != runstore.StatusFailed {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestTitleFrom(t *testing.T) {
	cases := map[string]string{
		"  Plan   the launch  \nand more": "Plan the launch",
		strings.Repeat("é", 100):          strings.Repeat("é", maxTitleRunes-1) + "…",
	}
	for message, want := range cases {
		if got := titleFrom(message); got != want {
			t.Errorf("titleFrom(%q) = %q, want %q", message, got, want)
		}
	}
}

func TestATriggerRunIsAnsweredByTheUnattendedAgent(t *testing.T) {
	answering := func(answer string) *scriptedModel {
		return &scriptedModel{respond: func(*model.LLMRequest) (*genai.Content, error) {
			return genai.NewContentFromText(answer, genai.RoleModel), nil
		}}
	}
	attended, err := llmagent.New(llmagent.Config{Name: "krude", Model: answering("attended"), Instruction: "You are Krude."})
	if err != nil {
		t.Fatal(err)
	}
	unattended, err := llmagent.New(llmagent.Config{Name: "krude", Model: answering("unattended"), Instruction: "You are Krude, alone."})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{AppName: "krude", Agent: attended, Unattended: unattended, Sessions: session.InMemoryService(), Runs: newFakeRuns(), Prices: prices})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, service: service}
	created, err := service.Create(context.Background(), asha)
	if err != nil {
		t.Fatal(err)
	}
	away := Member{UserID: asha.UserID, OrganizationID: asha.OrganizationID}
	items, _ := h.run(away, created.ID, RunInput{Message: "events", StartedBy: agentsv1.StartedByTrigger, Wakeup: "wb_1"})
	if answers := find(items, ItemMessage); answers[len(answers)-1].Text != "unattended" {
		t.Fatalf("a trigger run was answered by %q", answers[len(answers)-1].Text)
	}
	items, _ = h.run(asha, created.ID, RunInput{Message: "hi"})
	if answers := find(items, ItemMessage); answers[len(answers)-1].Text != "attended" {
		t.Fatalf("a member's run was answered by %q", answers[len(answers)-1].Text)
	}

	other, err := llmagent.New(llmagent.Config{Name: "someone_else", Model: answering("x"), Instruction: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{AppName: "krude", Agent: attended, Unattended: other, Sessions: session.InMemoryService(), Runs: newFakeRuns(), Prices: prices}); err == nil {
		t.Fatal("New() accepted an unattended agent answering to another name")
	}
}
