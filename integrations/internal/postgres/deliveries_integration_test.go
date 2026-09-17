package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestRecordDelivery(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_delivery"

	sub1 := newSubscription(org, "sub_deliv_1", "con_1", "app_1", "user_1", domain.SubscriptionActive, now)
	event1 := testEvent("integrations.subscription.created.v1", now)
	if err := store.CreateSubscription(ctx, sub1, &event1); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	sub2 := newSubscription(org, "sub_deliv_2", "con_2", "app_1", "user_1", domain.SubscriptionActive, now)
	event2 := testEvent("integrations.subscription.created.v1", now)
	if err := store.CreateSubscription(ctx, sub2, &event2); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	deliveryEvent := testEvent("integrations.delivery.received.v1", now)
	recorded, err := store.RecordDelivery(ctx, sub1.ID, "delivery-key-1", now, deliveryEvent)
	if err != nil {
		t.Fatalf("RecordDelivery() first error = %v", err)
	}
	if !recorded {
		t.Fatalf("RecordDelivery() first = false, want true")
	}
	assertOutboxHas(t, ctx, store, deliveryEvent.ID, true)

	repeatEvent := testEvent("integrations.delivery.received.v1", now)
	recorded, err = store.RecordDelivery(ctx, sub1.ID, "delivery-key-1", now.Add(time.Minute), repeatEvent)
	if err != nil {
		t.Fatalf("RecordDelivery() repeat error = %v", err)
	}
	if recorded {
		t.Fatalf("RecordDelivery() repeat = true, want false")
	}
	assertOutboxHas(t, ctx, store, repeatEvent.ID, false)

	otherSubEvent := testEvent("integrations.delivery.received.v1", now)
	recorded, err = store.RecordDelivery(ctx, sub2.ID, "delivery-key-1", now, otherSubEvent)
	if err != nil {
		t.Fatalf("RecordDelivery() other subscription error = %v", err)
	}
	if !recorded {
		t.Fatalf("RecordDelivery() same key for another subscription = false, want true (new)")
	}
	assertOutboxHas(t, ctx, store, otherSubEvent.ID, true)
}

// assertOutboxHas checks whether an event with the given id is (or is not) in
// integrations_outbox.
func assertOutboxHas(t *testing.T, ctx context.Context, store *Store, eventID string, want bool) {
	t.Helper()
	var found bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM integrations_outbox WHERE id = $1)`, eventID).Scan(&found); err != nil {
		t.Fatalf("query outbox for %s: %v", eventID, err)
	}
	if found != want {
		t.Fatalf("outbox has %s = %v, want %v", eventID, found, want)
	}
}

func TestDeleteDeliveriesBefore(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_delivery_prune"

	sub := newSubscription(org, "sub_prune", "con_1", "app_1", "user_1", domain.SubscriptionActive, now)
	event := testEvent("integrations.subscription.created.v1", now)
	if err := store.CreateSubscription(ctx, sub, &event); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}

	old := now.Add(-48 * time.Hour)
	recent := now.Add(-time.Hour)
	cutoff := now.Add(-24 * time.Hour)

	if _, err := store.RecordDelivery(ctx, sub.ID, "old-key", old, testEvent("integrations.delivery.received.v1", old)); err != nil {
		t.Fatalf("RecordDelivery(old) error = %v", err)
	}
	if _, err := store.RecordDelivery(ctx, sub.ID, "recent-key", recent, testEvent("integrations.delivery.received.v1", recent)); err != nil {
		t.Fatalf("RecordDelivery(recent) error = %v", err)
	}

	removed, err := store.DeleteDeliveriesBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteDeliveriesBefore() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("DeleteDeliveriesBefore() removed = %d, want 1", removed)
	}

	// The recent delivery is still remembered: recording it again changes nothing.
	recordedAgain, err := store.RecordDelivery(ctx, sub.ID, "recent-key", now, testEvent("integrations.delivery.received.v1", now))
	if err != nil {
		t.Fatalf("RecordDelivery(recent again) error = %v", err)
	}
	if recordedAgain {
		t.Fatalf("RecordDelivery(recent again) = true, want false: it was not pruned")
	}

	// The old delivery was forgotten: recording it again is new.
	recordedOldAgain, err := store.RecordDelivery(ctx, sub.ID, "old-key", now, testEvent("integrations.delivery.received.v1", now))
	if err != nil {
		t.Fatalf("RecordDelivery(old again) error = %v", err)
	}
	if !recordedOldAgain {
		t.Fatalf("RecordDelivery(old again) = false, want true: it was pruned")
	}
}
