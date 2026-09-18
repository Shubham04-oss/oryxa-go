package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// These tests use the shared development PostgreSQL:
//
//	BATCHING_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' \
//	  go test ./batching/internal/postgres
//
// Every test owns a random organization and removes its rows.
func testStore(t *testing.T, rule domain.Rule) (*Store, string) {
	t.Helper()
	databaseURL := os.Getenv("BATCHING_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BATCHING_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-batching-test"})
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := domain.NewAdaptiveScheduler(rule)
	if err != nil {
		t.Fatal(err)
	}
	store := New(database, scheduler)
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	organizationID := "batching_test_" + hex.EncodeToString(random)
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM batching_triggers WHERE organization_id = $1`, organizationID)
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM batching_traffic_profiles WHERE organization_id = $1`, organizationID)
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM batching_outbox WHERE id LIKE $1`, organizationID+"%")
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM batching_forgotten WHERE organization_id = $1`, organizationID)
		database.Close()
	})
	return store, organizationID
}

func testTrigger(organizationID, memberID, id string, lane domain.Lane) domain.Trigger {
	return domain.Trigger{
		ID: organizationID + "_" + id, TriggerID: "apt_" + id, ConnectionID: "apc_1",
		OrganizationID: organizationID, MemberID: memberID, Toolkit: "gmail", Trigger: "new_message",
		Data: json.RawMessage(`{"id":"` + id + `"}`), ReceivedAt: time.Now().UTC(),
		Classification: domain.Classification{Lane: lane, Priority: 0, Classifier: "test"},
	}
}

func recordTrigger(t *testing.T, store *Store, trigger domain.Trigger) {
	t.Helper()
	admission, err := store.Record(context.Background(), trigger)
	if err != nil || admission != domain.Admitted {
		t.Fatalf("Record(%s) = %d, %v", trigger.ID, admission, err)
	}
}

// refused says Record kept nothing of a trigger because of a removal.
func refused(t *testing.T, store *Store, trigger domain.Trigger) {
	t.Helper()
	admission, err := store.Record(context.Background(), trigger)
	if err != nil || admission != domain.Forgotten {
		t.Fatalf("Record(%s) = %d, %v, want it refused as forgotten", trigger.ID, admission, err)
	}
}

func count(t *testing.T, store *Store, query string, args ...any) int {
	t.Helper()
	var found int
	if err := store.database.Pool.QueryRow(context.Background(), query, args...).Scan(&found); err != nil {
		t.Fatal(err)
	}
	return found
}

func ownBatches(batches []domain.Batch, organizationID string) []domain.Batch {
	var own []domain.Batch
	for _, batch := range batches {
		if batch.OrganizationID == organizationID {
			own = append(own, batch)
		}
	}
	return own
}

func waitForDatabaseDeadline(t *testing.T, store *Store, deadline time.Time) {
	t.Helper()
	var databaseNow time.Time
	if err := store.database.Pool.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		t.Fatal(err)
	}
	if wait := deadline.Sub(databaseNow) + 20*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
}

func earliestSoftDeadline(t *testing.T, store *Store, organizationID, memberID string) time.Time {
	t.Helper()
	var deadline time.Time
	if err := store.database.Pool.QueryRow(context.Background(), `
		SELECT min(soft_due_at) FROM batching_triggers
		WHERE organization_id = $1 AND member_id = $2 AND batched_at IS NULL`,
		organizationID, memberID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	return deadline
}

func TestDigestWaitsForItsSizeAndInputDeliveryIsIdempotent(t *testing.T) {
	rule := domain.Rule{Size: 3, MaxWait: time.Hour, Largest: 20}
	store, organizationID := testStore(t, rule)
	ctx := context.Background()

	first := testTrigger(organizationID, "user_1", "1", domain.LaneDigest)
	recordTrigger(t, store, first)
	recordTrigger(t, store, testTrigger(organizationID, "user_1", "2", domain.LaneDigest))
	if admission, err := store.Record(ctx, first); err != nil || admission != domain.AlreadyRecorded {
		t.Fatalf("duplicate Record() = %d, %v", admission, err)
	}
	var observations int
	if err := store.database.Pool.QueryRow(ctx, `
		SELECT total_events FROM batching_traffic_profiles
		WHERE organization_id = $1 AND member_id = 'user_1'`, organizationID).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 2 {
		t.Fatalf("traffic observations after duplicate = %d", observations)
	}
	claimed, err := store.ClaimDue(ctx, rule, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := ownBatches(claimed, organizationID); len(got) != 0 {
		t.Fatalf("two of three triggers made a batch: %+v", got)
	}

	recordTrigger(t, store, testTrigger(organizationID, "user_1", "3", domain.LaneDigest))
	claimed, err = store.ClaimDue(ctx, rule, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	got := ownBatches(claimed, organizationID)
	if len(got) != 1 || len(got[0].Triggers) != 3 {
		t.Fatalf("claimed = %+v", got)
	}
}

func TestImmediateTrafficLeavesCapacityForADueDigest(t *testing.T) {
	rule := domain.Rule{Size: 10, MaxWait: time.Millisecond, Largest: 20}
	store, organizationID := testStore(t, rule)
	ctx := context.Background()
	for index := range 3 {
		recordTrigger(t, store, testTrigger(organizationID, "urgent", string(rune('a'+index)), domain.LaneImmediate))
	}
	recordTrigger(t, store, testTrigger(organizationID, "digest", "ordinary", domain.LaneDigest))
	waitForDatabaseDeadline(t, store, earliestSoftDeadline(t, store, organizationID, "digest"))

	claimed, err := store.ClaimDue(ctx, rule, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	got := ownBatches(claimed, organizationID)
	if len(got) != 2 {
		t.Fatalf("claimed = %+v", got)
	}
	lanes := map[domain.Lane]int{}
	for _, batch := range got {
		lanes[batch.Lane]++
	}
	if lanes[domain.LaneImmediate] != 1 || lanes[domain.LaneDigest] != 1 {
		t.Fatalf("lanes = %v", lanes)
	}
}

func TestCompletingAClaimAtomicallyWritesTheOutbox(t *testing.T) {
	rule := domain.Rule{Size: 1, MaxWait: time.Millisecond, Largest: 20}
	store, organizationID := testStore(t, rule)
	ctx := context.Background()
	recordTrigger(t, store, testTrigger(organizationID, "user_1", "1", domain.LaneDigest))
	waitForDatabaseDeadline(t, store, earliestSoftDeadline(t, store, organizationID, "user_1"))
	claimed, err := store.ClaimDue(ctx, rule, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	got := ownBatches(claimed, organizationID)
	if len(got) != 1 {
		t.Fatalf("claimed = %+v", got)
	}
	event := domain.Event{
		ID: organizationID + "_event", DetailType: "batching.trigger-batch.ready.v1",
		Data: json.RawMessage(`{"batch_id":"test"}`), OccurredAt: time.Now().UTC(),
	}
	if err := store.Complete(ctx, got[0], event); err != nil {
		t.Fatal(err)
	}
	var outboxRows, settledRows int
	if err := store.database.Pool.QueryRow(ctx, `SELECT count(*) FROM batching_outbox WHERE id = $1`, event.ID).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if err := store.database.Pool.QueryRow(ctx, `
		SELECT count(*) FROM batching_triggers
		WHERE organization_id = $1 AND batched_at IS NOT NULL AND data IS NULL`, organizationID).Scan(&settledRows); err != nil {
		t.Fatal(err)
	}
	if outboxRows != 1 || settledRows != 1 {
		t.Fatalf("outbox rows = %d, settled rows = %d", outboxRows, settledRows)
	}
	if err := store.Complete(ctx, got[0], event); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("second Complete() error = %v", err)
	}
}

// TestForgettingAMemberTakesTheirUnpublishedBatches: a batch completed just
// before the member was removed still waits in the outbox with their
// payloads, and is not published after they are gone. Another member's
// stays.
func TestForgettingAMemberTakesTheirUnpublishedBatches(t *testing.T) {
	store, organizationID := testStore(t, domain.Rule{Size: 1, MaxWait: time.Millisecond, Largest: 20})
	ctx := context.Background()
	write := func(id, memberID string) {
		t.Helper()
		if _, err := store.database.Pool.Exec(ctx, `
			INSERT INTO batching_outbox (id, detail_type, detail, occurred_at)
			VALUES ($1, 'batching.trigger-batch.ready.v1', $2, now())`,
			organizationID+"_"+id, `{"organization_id":"`+organizationID+`","member_id":"`+memberID+`","triggers":[]}`); err != nil {
			t.Fatal(err)
		}
	}
	write("gone", "user_1")
	write("stays", "user_2")

	if err := store.Forget(ctx, organizationID, "user_1"); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := store.database.Pool.Query(ctx, `SELECT id FROM batching_outbox WHERE id LIKE $1 ORDER BY id`, organizationID+"%")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if len(left) != 1 || left[0] != organizationID+"_stays" {
		t.Fatalf("outbox = %v, want only the other member's batch", left)
	}

	// The organization going takes everything of it.
	if err := store.Forget(ctx, organizationID, ""); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.database.Pool.QueryRow(ctx, `SELECT count(*) FROM batching_outbox WHERE id LIKE $1`, organizationID+"%").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("outbox rows = %d after the organization was deleted, want none", count)
	}
}

func TestAQuietPeriodFlushesItsPartialBatchAtThePersistedDeadline(t *testing.T) {
	rule := domain.Rule{Size: 5, MaxWait: 250 * time.Millisecond, Largest: 20}
	store, organizationID := testStore(t, rule)
	ctx := context.Background()
	for index := range 5 {
		recordTrigger(t, store, testTrigger(organizationID, "user_1", "warm_"+string(rune('a'+index)), domain.LaneDigest))
	}
	warm, err := store.ClaimDue(ctx, rule, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	warm = ownBatches(warm, organizationID)
	if len(warm) != 1 || len(warm[0].Triggers) != 5 {
		t.Fatalf("warm batch = %+v", warm)
	}
	if err := store.Complete(ctx, warm[0], domain.Event{
		ID: organizationID + "_warm", DetailType: "batching.trigger-batch.ready.v1",
		Data: json.RawMessage(`{"batch":"warm"}`), OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	recordTrigger(t, store, testTrigger(organizationID, "user_1", "quiet_1", domain.LaneDigest))
	recordTrigger(t, store, testTrigger(organizationID, "user_1", "quiet_2", domain.LaneDigest))
	var target int
	var hardDue time.Time
	if err := store.database.Pool.QueryRow(ctx, `
		SELECT min(target_size), min(hard_due_at)
		FROM batching_triggers
		WHERE organization_id = $1 AND batched_at IS NULL`, organizationID).Scan(&target, &hardDue); err != nil {
		t.Fatal(err)
	}
	if target <= 2 {
		t.Fatalf("quiet period unexpectedly reached its target %d", target)
	}
	waitForDatabaseDeadline(t, store, hardDue)
	quiet, err := store.ClaimDue(ctx, rule, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	quiet = ownBatches(quiet, organizationID)
	if len(quiet) != 1 || len(quiet[0].Triggers) != 2 || quiet[0].FlushReason != "hard_deadline" {
		t.Fatalf("quiet batch = %+v", quiet)
	}
}

// TestATriggerDeliveredAfterItsMemberWasRemovedIsRefused: the bus orders
// nothing, so a trigger apps received before a member was removed can arrive
// after the removal. It must not start a new group for them, nor leave a
// traffic profile behind; another member's triggers are untouched, and the
// same member added back later is not held up.
func TestATriggerDeliveredAfterItsMemberWasRemovedIsRefused(t *testing.T) {
	store, organizationID := testStore(t, domain.Rule{Size: 5, MaxWait: time.Hour, Largest: 20})
	ctx := context.Background()
	early := testTrigger(organizationID, "user_1", "early", domain.LaneDigest)
	recordTrigger(t, store, early)
	late := testTrigger(organizationID, "user_1", "late", domain.LaneDigest)
	lateImmediate := testTrigger(organizationID, "user_1", "late_immediate", domain.LaneImmediate)
	other := testTrigger(organizationID, "user_2", "other", domain.LaneDigest)

	if err := store.Forget(ctx, organizationID, "user_1"); err != nil {
		t.Fatal(err)
	}
	refused(t, store, late)
	refused(t, store, lateImmediate)
	refused(t, store, early) // delivered again after the removal
	if got := count(t, store, `SELECT count(*) FROM batching_triggers WHERE organization_id = $1 AND member_id = 'user_1'`, organizationID); got != 0 {
		t.Fatalf("user_1 has %d triggers after the removal, want none", got)
	}
	if got := count(t, store, `SELECT count(*) FROM batching_traffic_profiles WHERE organization_id = $1 AND member_id = 'user_1'`, organizationID); got != 0 {
		t.Fatalf("a refused trigger left %d traffic profiles, want none", got)
	}
	recordTrigger(t, store, other)

	// Added back, the member's apps report something new: it is theirs.
	returned := testTrigger(organizationID, "user_1", "returned", domain.LaneDigest)
	returned.ReceivedAt = time.Now().UTC()
	recordTrigger(t, store, returned)
}

func TestDeletingAnOrganizationRefusesEveryMembersLateTriggers(t *testing.T) {
	store, organizationID := testStore(t, domain.Rule{Size: 5, MaxWait: time.Hour, Largest: 20})
	ctx := context.Background()
	lateFirst := testTrigger(organizationID, "user_1", "late_1", domain.LaneDigest)
	lateSecond := testTrigger(organizationID, "user_2", "late_2", domain.LaneImmediate)
	if err := store.Forget(ctx, organizationID, ""); err != nil {
		t.Fatal(err)
	}
	refused(t, store, lateFirst)
	refused(t, store, lateSecond)
	if got := count(t, store, `SELECT count(*) FROM batching_triggers WHERE organization_id = $1`, organizationID); got != 0 {
		t.Fatalf("the deleted organization has %d triggers, want none", got)
	}
}

// TestForgetWaitsForATriggerStillBeingRecorded: a trigger whose insert is
// not yet committed when the removal runs is invisible to the removal's
// delete, so without the organization's lock it would outlive it.
func TestForgetWaitsForATriggerStillBeingRecorded(t *testing.T) {
	store, organizationID := testStore(t, domain.Rule{Size: 5, MaxWait: time.Hour, Largest: 20})
	ctx := context.Background()
	tx, err := store.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if admission, err := store.record(ctx, tx, testTrigger(organizationID, "user_1", "in_flight", domain.LaneDigest)); err != nil || admission != domain.Admitted {
		t.Fatalf("record() = %d, %v", admission, err)
	}

	forgotten := make(chan error, 1)
	go func() { forgotten <- store.Forget(ctx, organizationID, "user_1") }()
	select {
	case err := <-forgotten:
		t.Fatalf("Forget() returned %v while a trigger was still being recorded", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-forgotten; err != nil {
		t.Fatal(err)
	}
	if got := count(t, store, `SELECT count(*) FROM batching_triggers WHERE organization_id = $1`, organizationID); got != 0 {
		t.Fatalf("%d triggers outlived the removal, want none", got)
	}
}

// TestRecordWaitsForARemovalUnderWay and then honours its tombstone.
func TestRecordWaitsForARemovalUnderWay(t *testing.T) {
	store, organizationID := testStore(t, domain.Rule{Size: 5, MaxWait: time.Hour, Largest: 20})
	ctx := context.Background()
	tx, err := store.database.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, organizationLock(organizationID)); err != nil {
		t.Fatal(err)
	}

	type result struct {
		admission domain.Admission
		err       error
	}
	recorded := make(chan result, 1)
	go func() {
		admission, err := store.Record(ctx, testTrigger(organizationID, "user_1", "racing", domain.LaneDigest))
		recorded <- result{admission, err}
	}()
	select {
	case got := <-recorded:
		t.Fatalf("Record() = %d, %v while a removal was under way", got.admission, got.err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := remember(ctx, tx, organizationID, "user_1", "", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-recorded; got.err != nil || got.admission != domain.Forgotten {
		t.Fatalf("Record() = %d, %v, want it refused once the removal committed", got.admission, got.err)
	}
}

func TestExpiredRemovalsAreSwept(t *testing.T) {
	store, organizationID := testStore(t, domain.Rule{Size: 5, MaxWait: time.Hour, Largest: 20})
	ctx := context.Background()
	past := time.Now().UTC().Add(-45 * 24 * time.Hour)
	store.now = func() time.Time { return past }
	if err := store.Forget(ctx, organizationID, "user_old"); err != nil {
		t.Fatal(err)
	}
	store.now = time.Now
	if err := store.Forget(ctx, organizationID, "user_recent"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteForgotten(ctx, time.Now().UTC().Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := store.database.Pool.Query(ctx, `SELECT member_id FROM batching_forgotten WHERE organization_id = $1`, organizationID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var memberID string
		if err := rows.Scan(&memberID); err != nil {
			t.Fatal(err)
		}
		left = append(left, memberID)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if len(left) != 1 || left[0] != "user_recent" {
		t.Fatalf("removals left = %v, want only the recent one", left)
	}
}
