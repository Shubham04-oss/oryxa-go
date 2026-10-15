package threads

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
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
	if got := strings.Join(types(detail.Items), ","); got != "run_started,message,message,run_completed" {
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
	if got := strings.Join(types(detail.Items), ","); got != "run_started,message,message,run_completed,run_started,message,message,run_completed" {
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
	if len(live) != 1 || live[0].ErrorCode != FailureError || live[0].Error == "" {
		t.Fatalf("streamed run_failed = %+v", live)
	}
	detail, err := h.service.Get(ctx, asha, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	kept := find(detail.Items, ItemRunFailed)
	if len(kept) != 1 || kept[0].ID != bracketID(run.ID(), ItemRunFailed) || kept[0].ErrorCode != FailureError ||
		kept[0].Error != live[0].Error || kept[0].Interrupted {
		t.Fatalf("stored run_failed = %+v, want %+v", kept, live[0])
	}
	if types(detail.Items)[0] != ItemRunStarted {
		t.Fatalf("history = %v, want the run's start first", types(detail.Items))
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
