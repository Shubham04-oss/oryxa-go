package threads

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
)

func TestARunsBracketsAreStoredAsTheyWereStreamed(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return genai.NewContentFromText("Hello.", genai.RoleModel), nil
	})
	ctx := context.Background()
	created, err := h.service.Create(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	streamed, run := h.run(asha, created.ID, RunInput{Message: "Hi"})
	detail, err := h.service.Get(ctx, asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(types(detail.Items), ","); got != "message,run_started,message,run_completed" {
		t.Fatalf("history = %s, want the run bracketed", got)
	}
	for _, itemType := range []string{ItemRunStarted, ItemRunCompleted} {
		live, kept := find(streamed, itemType), find(detail.Items, itemType)
		if len(live) != 1 || len(kept) != 1 {
			t.Fatalf("%s: streamed %d, stored %d", itemType, len(live), len(kept))
		}
		if live[0].ID != bracketID(run.ID(), itemType) || kept[0].ID != live[0].ID || kept[0].RunID != run.ID() || !kept[0].At.Equal(live[0].At) {
			t.Fatalf("%s: streamed %+v, stored %+v; want the same bracket", itemType, live[0], kept[0])
		}
	}
	completed := find(detail.Items, ItemRunCompleted)[0]
	if completed.Usage == nil || completed.Usage.TotalTokens != 110 {
		t.Fatalf("stored run_completed = %+v, want what the run spent", completed)
	}
	// A second run is bracketed on its own, after the first.
	h.run(asha, created.ID, RunInput{Message: "Again"})
	if detail, err = h.service.Get(ctx, asha, created.ID); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(types(detail.Items), ","); got != "message,run_started,message,run_completed,message,run_started,message,run_completed" {
		t.Fatalf("history after two runs = %s", got)
	}
}

func TestAFailedRunsBracketSaysWhyInHistory(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		return nil, errors.New("the model is down")
	})
	ctx := context.Background()
	created, err := h.service.Create(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	streamed, run := h.run(asha, created.ID, RunInput{Message: "Hi"})
	live := find(streamed, ItemRunFailed)
	if len(live) != 1 || live[0].Code != FailureError || live[0].Error == "" {
		t.Fatalf("streamed run_failed = %+v", live)
	}
	detail, err := h.service.Get(ctx, asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	kept := find(detail.Items, ItemRunFailed)
	if len(kept) != 1 || kept[0].ID != bracketID(run.ID(), ItemRunFailed) || kept[0].Code != FailureError ||
		kept[0].Error != live[0].Error || kept[0].Interrupted {
		t.Fatalf("stored run_failed = %+v, want %+v", kept, live[0])
	}
	if got := types(detail.Items); len(got) < 2 || got[0] != ItemMessage || got[1] != ItemRunStarted {
		t.Fatalf("history = %v, want the run's start after the message that started it", got)
	}
}

// A thread from before brackets were stored reads as it always did, and an
// event that only looks like a bracket is not taken for one.
func TestOnlyABracketReadsAsOne(t *testing.T) {
	plain := session.NewEvent(context.Background(), "inv")
	plain.Author = adkUserAuthor
	plain.Content = genai.NewContentFromText("hello", genai.RoleUser)
	if got := types(historyItems([]*session.Event{plain})); strings.Join(got, ",") != "message" {
		t.Fatalf("an unbracketed history = %v", got)
	}
	for name, metadata := range map[string]any{
		"no run":        map[string]any{"type": ItemRunStarted},
		"another type":  map[string]any{"type": "message", "run_id": "run_1"},
		"not an object": "run_started",
	} {
		event := session.NewEvent(context.Background(), "inv")
		event.CustomMetadata = map[string]any{runBracketKey: metadata}
		if items := historyItems([]*session.Event{event}); len(items) != 0 {
			t.Errorf("%s: items = %+v, want none", name, items)
		}
	}
	// What a member wrote is never a bracket, whatever metadata it carries.
	spoofed := session.NewEvent(context.Background(), "inv")
	spoofed.Author = adkUserAuthor
	spoofed.Content = genai.NewContentFromText("hello", genai.RoleUser)
	spoofed.CustomMetadata = map[string]any{runBracketKey: map[string]any{"type": ItemRunCompleted, "run_id": "run_1"}}
	if got := types(historyItems([]*session.Event{spoofed})); strings.Join(got, ",") != "message" {
		t.Fatalf("a message carrying bracket metadata = %v", got)
	}
}

func TestAFailureHasACode(t *testing.T) {
	for failure, want := range map[error]string{
		ErrInterrupted: FailureInterrupted,
		ErrDraining:    FailureInterrupted,
		fmt.Errorf("took too long: %w", context.DeadlineExceeded): FailureTimeout,
		errLeaseLost:                FailureTakenOver,
		ErrStopped:                  FailureStopped,
		context.Canceled:            FailureCancelled,
		errors.New("model refused"): FailureError,
	} {
		if got := failureCode(failure); got != want {
			t.Errorf("failureCode(%v) = %q, want %q", failure, got, want)
		}
	}
}

// History lists a run's start after what started it, where the stream sends
// it, whatever that was: a message, an apps batch or the member's answers.
func TestHistoryListsARunsStartAfterWhatStartedIt(t *testing.T) {
	ctx := context.Background()
	bracket := func(runID, itemType string) *session.Event {
		event := session.NewEvent(ctx, recordInvocation)
		event.ID, event.Author = bracketID(runID, itemType), adkUserAuthor
		event.CustomMetadata = map[string]any{runBracketKey: runBracket{Type: itemType, RunID: runID}}
		return event
	}
	said := func(author, text string) *session.Event {
		event := session.NewEvent(ctx, "inv")
		event.Author = author
		event.Content = genai.NewContentFromText(text, genai.RoleUser)
		return event
	}
	answered := session.NewEvent(ctx, "inv")
	answered.Author = adkUserAuthor
	answered.Content = &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		ID: "confirm_1", Name: toolconfirmation.FunctionCallName, Response: map[string]any{"confirmed": true},
	}}}}
	woken := said(adkUserAuthor, "GMAIL_NEW_MESSAGE")
	woken.Actions.StateDelta = map[string]any{stateWakeup: "wb_1"}

	for name, test := range map[string]struct {
		events []*session.Event
		want   string
	}{
		"a message": {
			[]*session.Event{bracket("run_1", ItemRunStarted), said(adkUserAuthor, "Hi"), said("krude", "Hello."), bracket("run_1", ItemRunCompleted)},
			"message:member,run_started,message:krude,run_completed",
		},
		"an apps batch": {
			[]*session.Event{bracket("run_1", ItemRunStarted), woken, said("krude", "You have mail."), bracket("run_1", ItemRunCompleted)},
			"message:apps,run_started,message:krude,run_completed",
		},
		"the member's answers": {
			[]*session.Event{bracket("run_1", ItemRunStarted), answered, said("krude", "Deleted."), bracket("run_1", ItemRunCompleted)},
			"run_started,message:krude,run_completed",
		},
		// What the run added comes after its start, as stored.
		"no input stored": {
			[]*session.Event{bracket("run_1", ItemRunStarted), said("krude", "Hello."), bracket("run_1", ItemRunFailed)},
			"run_started,message:krude,run_failed",
		},
		// A run that ended without a record, then the next one.
		"a start with no end": {
			[]*session.Event{bracket("run_1", ItemRunStarted), said(adkUserAuthor, "Hi"), bracket("run_2", ItemRunStarted), said(adkUserAuthor, "Again")},
			"message:member,run_started,message:member,run_started",
		},
		"a start alone": {
			[]*session.Event{bracket("run_1", ItemRunStarted)},
			"run_started",
		},
	} {
		var got []string
		for _, item := range historyItems(test.events) {
			if item.Type == ItemMessage {
				got = append(got, item.Type+":"+item.Author)
				continue
			}
			got = append(got, item.Type)
		}
		if strings.Join(got, ",") != test.want {
			t.Errorf("%s: history = %s, want %s", name, strings.Join(got, ","), test.want)
		}
	}
}

// A run that lost its hold on its thread records nothing more: another run
// may be writing the thread. Its start stays in history with no end, which
// says it ended without a record.
func TestARunThatLostItsHoldLeavesItsStartWithNoEnd(t *testing.T) {
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: waitingModel{}, Instruction: "You are Krude."})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	runs := newFakeRuns()
	runs.renewErr = runstore.ErrNotRunning
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: session.InMemoryService(), Runs: runs, Prices: prices,
		RunTimeout: time.Minute, Lease: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, service: service, runs: runs}
	ctx := context.Background()
	created, err := service.Create(ctx, asha)
	if err != nil {
		t.Fatal(err)
	}
	streamed, run := h.run(asha, created.ID, RunInput{Message: "hi"})
	if failed := find(streamed, ItemRunFailed); len(failed) != 1 || failed[0].Code != FailureTakenOver {
		t.Fatalf("streamed run_failed = %+v, want it taken over", failed)
	}
	detail, err := service.Get(ctx, asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(types(detail.Items), ","); got != "message,run_started" {
		t.Fatalf("history = %s, want the run's start and no end", got)
	}
	if started := find(detail.Items, ItemRunStarted)[0]; started.ID != bracketID(run.ID(), ItemRunStarted) {
		t.Fatalf("stored run_started = %+v", started)
	}
}
