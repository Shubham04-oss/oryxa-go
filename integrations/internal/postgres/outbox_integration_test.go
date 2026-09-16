package postgres

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// writeEvents puts events in the outbox by creating one subscription per
// event, which is how every event is written.
func writeEvents(t *testing.T, store *Store, at time.Time, detailTypes ...string) []domain.Event {
	t.Helper()
	ctx := context.Background()
	var written []domain.Event
	for index, detailType := range detailTypes {
		event := testEvent(detailType, at.Add(time.Duration(index)*time.Minute))
		event.Data = json.RawMessage(`{"index":` + string(rune('0'+index)) + `}`)
		subscription := newSubscription("org_1", "sub_"+randomHex(4), "conn_1", "app_1", "user-1", domain.SubscriptionActive, at)
		if err := store.CreateSubscription(ctx, subscription, &event); err != nil {
			t.Fatal(err)
		}
		written = append(written, event)
	}
	return written
}

func TestClaimOutboxTakesUnpublishedEventsInOrderAndLocksThem(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	written := writeEvents(t, store, at, "integrations.subscription.created.v1", "integrations.trigger.received.v1", "integrations.subscription.stopped.v1")

	claimed, err := store.ClaimOutbox(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimOutbox() error = %v", err)
	}
	var ids []string
	for _, event := range claimed {
		ids = append(ids, event.ID)
		if event.Attempts != 0 {
			t.Fatalf("ClaimOutbox() attempts = %d, want 0", event.Attempts)
		}
	}
	want := []string{written[0].ID, written[1].ID, written[2].ID}
	if !slices.Equal(ids, want) {
		t.Fatalf("ClaimOutbox() = %v, want %v in occurrence order", ids, want)
	}
	// The data comes back as jsonb keeps it, which is the same document
	// rather than the same bytes.
	var data map[string]int
	if err := json.Unmarshal(claimed[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if claimed[0].DetailType != "integrations.subscription.created.v1" || data["index"] != 0 || !claimed[0].OccurredAt.Equal(written[0].OccurredAt) {
		t.Fatalf("ClaimOutbox()[0] = %+v, want the event as written", claimed[0])
	}

	// They are locked: another dispatcher claims nothing.
	if again, err := store.ClaimOutbox(ctx, 10); err != nil || len(again) != 0 {
		t.Fatalf("ClaimOutbox() while locked = %d events, %v; want none", len(again), err)
	}
}

func TestMarkOutboxPublishedAndFailed(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	written := writeEvents(t, store, at, "integrations.trigger.received.v1", "integrations.trigger.received.v1")
	if _, err := store.ClaimOutbox(ctx, 10); err != nil {
		t.Fatal(err)
	}

	// One is published and never comes back; the other is delayed and then
	// claimed again with its attempt counted.
	if err := store.MarkOutboxPublished(ctx, written[0].ID); err != nil {
		t.Fatalf("MarkOutboxPublished() error = %v", err)
	}
	if err := store.MarkOutboxFailed(ctx, written[1].ID, time.Hour, "EventBridge said no"); err != nil {
		t.Fatalf("MarkOutboxFailed() error = %v", err)
	}
	if claimed, err := store.ClaimOutbox(ctx, 10); err != nil || len(claimed) != 0 {
		t.Fatalf("ClaimOutbox() while one is published and one is delayed = %d events, %v; want none", len(claimed), err)
	}

	if err := store.MarkOutboxFailed(ctx, written[1].ID, 0, "EventBridge said no again"); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != written[1].ID || claimed[0].Attempts != 2 {
		t.Fatalf("ClaimOutbox() after the delay = %+v, want the failed event with 2 attempts", claimed)
	}
	if err := store.MarkOutboxPublished(ctx, written[1].ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimOutbox(ctx, 10); err != nil || len(claimed) != 0 {
		t.Fatalf("ClaimOutbox() after everything is published = %d events, %v; want none", len(claimed), err)
	}
}

func TestClaimOutboxHonoursItsLimit(t *testing.T) {
	store := openTestStore(t)
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	writeEvents(t, store, at, "integrations.trigger.received.v1", "integrations.trigger.received.v1", "integrations.trigger.received.v1")

	claimed, err := store.ClaimOutbox(context.Background(), 2)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("ClaimOutbox(2) = %d events, %v; want 2", len(claimed), err)
	}
}
