package events

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

type fakeOutbox struct {
	events    []OutboxEvent
	claimErr  error
	published []string
	failed    map[string]failedMark
}

type failedMark struct {
	after time.Duration
	error string
}

func (f *fakeOutbox) ClaimOutbox(context.Context, int) ([]OutboxEvent, error) {
	return f.events, f.claimErr
}

func (f *fakeOutbox) MarkOutboxPublished(_ context.Context, eventID string) error {
	f.published = append(f.published, eventID)
	return nil
}

func (f *fakeOutbox) MarkOutboxFailed(_ context.Context, eventID string, after time.Duration, message string) error {
	if f.failed == nil {
		f.failed = map[string]failedMark{}
	}
	f.failed[eventID] = failedMark{after: after, error: message}
	return nil
}

type fakePublisher struct {
	failFor   map[string]bool
	published []string
}

func (f *fakePublisher) PublishEvent(_ context.Context, event Event) error {
	if f.failFor[event.ID] {
		return errors.New("bus is down")
	}
	f.published = append(f.published, event.ID)
	return nil
}

func TestDispatcherPublishesAndRetriesPerEvent(t *testing.T) {
	first, _ := NewEvent("agents.session.created.v1", map[string]string{"n": "1"}, time.Now())
	second, _ := NewEvent("agents.session.created.v1", map[string]string{"n": "2"}, time.Now())
	store := &fakeOutbox{events: []OutboxEvent{
		{Event: first, Attempts: 0},
		{Event: second},
	}}
	publisher := &fakePublisher{failFor: map[string]bool{first.ID: true}}
	dispatcher := NewDispatcher(store, publisher, slog.Default())

	dispatcher.flush(context.Background())

	if len(store.published) != 1 || store.published[0] != second.ID {
		t.Fatalf("published = %v, want the second event", store.published)
	}
	if len(publisher.published) != 1 || publisher.published[0] != second.ID {
		t.Fatalf("publisher saw %v", publisher.published)
	}
	mark, ok := store.failed[first.ID]
	if !ok {
		t.Fatal("the failed event was not marked for retry")
	}
	if mark.after != time.Second {
		t.Fatalf("retry after = %v, want 1s for the first attempt", mark.after)
	}
	if mark.error == "" {
		t.Fatal("the failure reason was not recorded")
	}
}

func TestDispatcherBacksOffWithAttempts(t *testing.T) {
	cases := map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 3: 8 * time.Second, 10: 64 * time.Second}
	for attempts, want := range cases {
		if got := retryDelay(attempts); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", attempts, got, want)
		}
	}
}

func TestDispatcherSurvivesAClaimFailure(t *testing.T) {
	store := &fakeOutbox{claimErr: errors.New("database down")}
	publisher := &fakePublisher{}
	dispatcher := NewDispatcher(store, publisher, slog.Default())

	dispatcher.flush(context.Background())

	if len(publisher.published) != 0 {
		t.Fatal("events were published after a claim failure")
	}
}
