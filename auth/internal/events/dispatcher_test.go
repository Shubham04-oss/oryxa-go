package events

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

type fakeOutbox struct {
	events          []domain.OutboxEvent
	published       []string
	failed          []string
	lastRetryAfter  time.Duration
	lastFailureText string
}

func (fake *fakeOutbox) ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error) {
	return fake.events, nil
}

func (fake *fakeOutbox) MarkOutboxPublished(_ context.Context, eventID string) error {
	fake.published = append(fake.published, eventID)
	return nil
}

func (fake *fakeOutbox) MarkOutboxFailed(_ context.Context, eventID string, retryAfter time.Duration, message string) error {
	fake.failed = append(fake.failed, eventID)
	fake.lastRetryAfter = retryAfter
	fake.lastFailureText = message
	return nil
}

type fakePublisher struct {
	err error
}

func (fake fakePublisher) PublishEvent(context.Context, domain.Event) error {
	return fake.err
}

func TestDispatcherMarksPublishedEvent(t *testing.T) {
	store := &fakeOutbox{events: []domain.OutboxEvent{{Event: domain.Event{ID: "evt-1"}}}}
	dispatcher := NewDispatcher(store, fakePublisher{})

	dispatcher.flush(context.Background())

	if len(store.published) != 1 || store.published[0] != "evt-1" || len(store.failed) != 0 {
		t.Fatalf("unexpected outbox result: %+v", store)
	}
}

func TestDispatcherSchedulesFailedEventWithBackoff(t *testing.T) {
	store := &fakeOutbox{events: []domain.OutboxEvent{{Event: domain.Event{ID: "evt-1"}, Attempts: 3}}}
	dispatcher := NewDispatcher(store, fakePublisher{err: errors.New("EventBridge unavailable")})

	dispatcher.flush(context.Background())

	if len(store.failed) != 1 || store.lastRetryAfter != 8*time.Second || store.lastFailureText == "" {
		t.Fatalf("unexpected outbox retry: %+v", store)
	}
}

func TestRetryDelayIsCapped(t *testing.T) {
	if got := retryDelay(100); got != 64*time.Second {
		t.Fatalf("retryDelay(100) = %s, want 64s", got)
	}
}
