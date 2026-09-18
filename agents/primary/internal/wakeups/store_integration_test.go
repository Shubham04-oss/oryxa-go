package wakeups

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// The store's tests need the shared development PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' \
//	  go test ./agents/primary/internal/wakeups
//
// Each test works in an organization of its own and removes its rows.
func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agents-wakeups-test"})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	organizationID := "wakeups_test_" + hex.EncodeToString(random)
	t.Cleanup(func() {
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM agents_primary.wakeup_events WHERE organization_id = $1`, organizationID)
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM agents_primary.wakeup_threads WHERE organization_id = $1`, organizationID)
		database.Close()
	})
	return store, organizationID
}

func record(t *testing.T, store *Store, organizationID, memberID, id string) {
	t.Helper()
	recorded, err := store.Record(context.Background(), Event{
		ID: organizationID + "_" + memberID + "_" + id, OrganizationID: organizationID, MemberID: memberID,
		Toolkit: "gmail", Trigger: "GMAIL_NEW_MESSAGE", Data: json.RawMessage(`{"n":"` + id + `"}`), ReceivedAt: time.Now().UTC(),
	})
	if err != nil || !recorded {
		t.Fatalf("Record(%s) = %t, %v", id, recorded, err)
	}
}

// ours keeps the batches of this test's organization: the table is shared.
func ours(batches []Batch, organizationID string) []Batch {
	var kept []Batch
	for _, batch := range batches {
		if batch.OrganizationID == organizationID {
			kept = append(kept, batch)
		}
	}
	return kept
}

func TestABatchWaitsForItsSizeOrItsOldestEvent(t *testing.T) {
	store, organizationID := testStore(t)
	ctx := context.Background()
	rule := Rule{Size: 3, MaxWait: time.Hour, Largest: 20}

	record(t, store, organizationID, "user_a", "1")
	record(t, store, organizationID, "user_a", "2")
	if again, err := store.Record(ctx, Event{ID: organizationID + "_user_a_1", OrganizationID: organizationID, MemberID: "user_a"}); err != nil || again {
		t.Fatalf("recording an event twice = %t, %v", again, err)
	}
	claimed, err := store.Claim(ctx, rule, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := ours(claimed, organizationID); len(got) != 0 {
		t.Fatalf("two of three events made a batch: %+v", got)
	}

	record(t, store, organizationID, "user_a", "3")
	record(t, store, organizationID, "user_b", "1")
	claimed, err = store.Claim(ctx, rule, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := ours(claimed, organizationID)
	if len(got) != 1 || got[0].MemberID != "user_a" || len(got[0].Events) != 3 {
		t.Fatalf("batches = %+v, want user_a's three", got)
	}
	var ids []string
	for _, event := range got[0].Events {
		ids = append(ids, event.ID)
	}
	want := []string{organizationID + "_user_a_1", organizationID + "_user_a_2", organizationID + "_user_a_3"}
	if !slices.Equal(ids, want) || string(got[0].Events[0].Data) != `{"n": "1"}` {
		t.Fatalf("events = %v, data %s", ids, got[0].Events[0].Data)
	}

	// A claimed batch is not claimed again while it is held.
	if again, err := store.Claim(ctx, Rule{Size: 1, MaxWait: time.Hour, Largest: 20}, time.Minute, 100); err != nil {
		t.Fatal(err)
	} else if batches := ours(again, organizationID); len(batches) != 1 || batches[0].MemberID != "user_b" {
		t.Fatalf("second claim = %+v, want only user_b's", batches)
	}

	// A lone event waits no longer than the wait.
	record(t, store, organizationID, "user_c", "1")
	lone, err := store.Claim(ctx, Rule{Size: 10, MaxWait: time.Millisecond, Largest: 20}, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	if batches := ours(lone, organizationID); len(batches) != 1 || batches[0].MemberID != "user_c" {
		t.Fatalf("lone claim = %+v, want user_c's single event", batches)
	}
}

func TestABatchIsBoundedAndItsLeaseRunsOut(t *testing.T) {
	store, organizationID := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"1", "2", "3", "4", "5"} {
		record(t, store, organizationID, "user_a", id)
	}
	claimed, err := store.Claim(ctx, Rule{Size: 1, MaxWait: time.Hour, Largest: 2}, 50*time.Millisecond, 100)
	if err != nil {
		t.Fatal(err)
	}
	first := ours(claimed, organizationID)
	if len(first) != 1 || len(first[0].Events) != 2 {
		t.Fatalf("batches = %+v, want one of the two oldest", first)
	}
	time.Sleep(100 * time.Millisecond)
	claimed, err = store.Claim(ctx, Rule{Size: 1, MaxWait: time.Hour, Largest: 20}, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	second := ours(claimed, organizationID)
	if len(second) != 1 || len(second[0].Events) != 5 || second[0].ID == first[0].ID {
		t.Fatalf("after the lease ran out = %+v, want all five in a new batch", second)
	}
}

func TestDoneReleaseAndDrop(t *testing.T) {
	store, organizationID := testStore(t)
	ctx := context.Background()
	record(t, store, organizationID, "user_a", "1")
	claim := func() Batch {
		t.Helper()
		claimed, err := store.Claim(ctx, Rule{Size: 1, MaxWait: time.Hour, Largest: 20}, time.Minute, 100)
		if err != nil {
			t.Fatal(err)
		}
		batches := ours(claimed, organizationID)
		if len(batches) != 1 {
			t.Fatalf("claimed %+v", batches)
		}
		return batches[0]
	}

	batch := claim()
	if dropped, err := store.Release(ctx, batch.ID, false, 2, 0); err != nil || dropped != 0 {
		t.Fatalf("Release(busy) = %d, %v", dropped, err)
	}
	batch = claim()
	if dropped, err := store.Release(ctx, batch.ID, true, 2, 0); err != nil || dropped != 0 {
		t.Fatalf("Release(failed once) = %d, %v", dropped, err)
	}
	batch = claim()
	if dropped, err := store.Release(ctx, batch.ID, true, 2, 0); err != nil || dropped != 1 {
		t.Fatalf("Release(failed twice) = %d, %v, want the event dropped", dropped, err)
	}
	if claimed, err := store.Claim(ctx, Rule{Size: 1, MaxWait: time.Hour, Largest: 20}, time.Minute, 100); err != nil || len(ours(claimed, organizationID)) != 0 {
		t.Fatalf("a dropped event was claimed again: %+v, %v", claimed, err)
	}

	record(t, store, organizationID, "user_a", "2")
	batch = claim()
	if err := store.Done(ctx, batch.ID); err != nil {
		t.Fatal(err)
	}
	if again, err := store.Record(ctx, Event{ID: organizationID + "_user_a_2", OrganizationID: organizationID, MemberID: "user_a"}); err != nil || again {
		t.Fatalf("a late repeat of a handled event was recorded again: %t, %v", again, err)
	}
	if claimed, err := store.Claim(ctx, Rule{Size: 1, MaxWait: time.Hour, Largest: 20}, time.Minute, 100); err != nil || len(ours(claimed, organizationID)) != 0 {
		t.Fatalf("a handled event was claimed again: %+v, %v", claimed, err)
	}
	var data []byte
	if err := store.database.Pool.QueryRow(ctx, `SELECT data FROM agents_primary.wakeup_events WHERE id = $1`, organizationID+"_user_a_2").Scan(&data); err != nil || data != nil {
		t.Fatalf("a handled event kept its data: %s, %v", data, err)
	}
	if err := store.Purge(ctx, 0); err != nil {
		t.Fatal(err)
	}
}

func TestAReleasedBatchWaitsBeforeItIsClaimedAgain(t *testing.T) {
	store, organizationID := testStore(t)
	ctx := context.Background()
	record(t, store, organizationID, "user_a", "1")
	rule := Rule{Size: 1, MaxWait: time.Hour, Largest: 20}
	claimed, err := store.Claim(ctx, rule, time.Minute, 100)
	if err != nil || len(ours(claimed, organizationID)) != 1 {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if _, err := store.Release(ctx, ours(claimed, organizationID)[0].ID, false, 3, time.Hour); err != nil {
		t.Fatal(err)
	}
	if again, err := store.Claim(ctx, rule, time.Minute, 100); err != nil || len(ours(again, organizationID)) != 0 {
		t.Fatalf("a batch released to wait was claimed at once: %+v, %v", again, err)
	}
}

func TestTheUpdatesThreadIsRemembered(t *testing.T) {
	store, organizationID := testStore(t)
	ctx := context.Background()
	if threadID, err := store.Thread(ctx, organizationID, "user_a"); err != nil || threadID != "" {
		t.Fatalf("Thread() before any = %q, %v", threadID, err)
	}
	for _, threadID := range []string{"thread_1", "thread_2"} {
		if err := store.SetThread(ctx, organizationID, "user_a", threadID); err != nil {
			t.Fatal(err)
		}
	}
	if threadID, err := store.Thread(ctx, organizationID, "user_a"); err != nil || threadID != "thread_2" {
		t.Fatalf("Thread() = %q, %v, want the latest", threadID, err)
	}
}

func TestForgettingRemovesAMembersOrAnOrganizationsWakeUps(t *testing.T) {
	store, organizationID := testStore(t)
	ctx := context.Background()
	record(t, store, organizationID, "user_a", "1")
	record(t, store, organizationID, "user_b", "1")
	for _, member := range []string{"user_a", "user_b"} {
		if err := store.SetThread(ctx, organizationID, member, "thread_"+member); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Forget(ctx, organizationID, "user_a"); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(ctx, Rule{Size: 1, MaxWait: time.Hour, Largest: 20}, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	if batches := ours(claimed, organizationID); len(batches) != 1 || batches[0].MemberID != "user_b" {
		t.Fatalf("after user_a left: %+v, want only user_b's events", batches)
	}
	if threadID, _ := store.Thread(ctx, organizationID, "user_a"); threadID != "" {
		t.Fatalf("user_a's thread is still remembered: %q", threadID)
	}
	if err := store.Forget(ctx, organizationID, ""); err != nil {
		t.Fatal(err)
	}
	if threadID, _ := store.Thread(ctx, organizationID, "user_b"); threadID != "" {
		t.Fatalf("the organization's threads are still remembered: %q", threadID)
	}
	if err := store.Forget(ctx, "", ""); err == nil {
		t.Fatal("Forget with no organization would forget everyone's wake-ups")
	}
}

func TestDroppedUnclaimedAndStaleEvents(t *testing.T) {
	store, organizationID := testStore(t)
	ctx := context.Background()
	rule := Rule{Size: 1, MaxWait: time.Hour, Largest: 20}
	record(t, store, organizationID, "user_a", "1")
	record(t, store, organizationID, "user_a", "2")

	claimed, err := store.Claim(ctx, rule, time.Minute, 100)
	if err != nil || len(ours(claimed, organizationID)) != 1 {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	batch := ours(claimed, organizationID)[0]
	// One event gives way to the next batch at once; the other is dropped.
	if err := store.Unclaim(ctx, []string{batch.Events[1].ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.Drop(ctx, batch.ID); err != nil {
		t.Fatal(err)
	}
	again, err := store.Claim(ctx, rule, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	if batches := ours(again, organizationID); len(batches) != 1 || len(batches[0].Events) != 1 || batches[0].Events[0].ID != batch.Events[1].ID {
		t.Fatalf("after unclaim and drop = %+v, want only the unclaimed event", batches)
	}

	record(t, store, organizationID, "user_b", "1")
	if _, err := store.database.Pool.Exec(ctx, `UPDATE agents_primary.wakeup_events SET recorded_at = now() - interval '4 days' WHERE organization_id = $1 AND member_id = 'user_b'`, organizationID); err != nil {
		t.Fatal(err)
	}
	dropped, err := store.DropStale(ctx, 72*time.Hour)
	if err != nil || dropped < 1 {
		t.Fatalf("DropStale() = %d, %v", dropped, err)
	}
	stale, err := store.Claim(ctx, rule, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range ours(stale, organizationID) {
		if batch.MemberID == "user_b" {
			t.Fatalf("a stale event was claimed: %+v", batch)
		}
	}
}

func TestReplaceThreadSettlesOnTheFirstRecorded(t *testing.T) {
	store, organizationID := testStore(t)
	ctx := context.Background()
	first, err := store.ReplaceThread(ctx, organizationID, "user_a", "", "thread_1")
	if err != nil || first != "thread_1" {
		t.Fatalf("first = %q, %v", first, err)
	}
	// A second wake-up that also saw none gets the first one's.
	second, err := store.ReplaceThread(ctx, organizationID, "user_a", "", "thread_2")
	if err != nil || second != "thread_1" {
		t.Fatalf("second = %q, %v, want thread_1", second, err)
	}
	// Replacing a thread the member deleted works only from the one expected.
	replaced, err := store.ReplaceThread(ctx, organizationID, "user_a", "thread_1", "thread_3")
	if err != nil || replaced != "thread_3" {
		t.Fatalf("replaced = %q, %v", replaced, err)
	}
	stale, err := store.ReplaceThread(ctx, organizationID, "user_a", "thread_1", "thread_4")
	if err != nil || stale != "thread_3" {
		t.Fatalf("a stale replacement = %q, %v, want thread_3 to stand", stale, err)
	}
}
