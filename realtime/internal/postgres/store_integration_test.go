package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	realtimev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/realtime/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// These tests need a PostgreSQL they may create schemas in:
//
//	REALTIME_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' \
//	  go test ./realtime/internal/postgres
//
// Each test migrates realtime into a schema of its own, which it drops when
// it ends, so it sees no other test's rows and leaves none behind.
func testStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("REALTIME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("REALTIME_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-realtime-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	schema := "realtime_test_" + hex.EncodeToString(random)
	if _, err := admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-realtime-test", Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	store := New(database)
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

type row struct {
	id, detailType, detail string
	occurredAt             time.Time
}

// outboxRows is the outbox, in the order it was written.
func outboxRows(t *testing.T, store *Store) []row {
	t.Helper()
	rows, err := store.Pool().Query(context.Background(),
		`SELECT id, detail_type, detail::text, occurred_at FROM realtime_outbox ORDER BY occurred_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []row
	for rows.Next() {
		var next row
		if err := rows.Scan(&next.id, &next.detailType, &next.detail, &next.occurredAt); err != nil {
			t.Fatal(err)
		}
		found = append(found, next)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

func acceptedIDs(t *testing.T, store *Store) []string {
	t.Helper()
	rows, err := store.Pool().Query(context.Background(), `SELECT id FROM realtime_accepted_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func received(workerID string, at time.Time) Received {
	return Received{
		WorkerID: workerID, DetailType: realtimev1.ConversationReadDetailType, OccurredAt: at,
		Data: json.RawMessage(`{"organization_id":"org_1","reader_user_id":"user-b","worker":"` + workerID + `"}`),
	}
}

func TestAcceptRecordsEachOfTheWorkersEventsOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	at := time.Date(2026, time.September, 24, 10, 5, 0, 123000000, time.UTC)

	recorded, err := store.Accept(ctx, []Received{received("evt_a", at), received("evt_b", at.Add(time.Second))})
	if err != nil || recorded != 2 {
		t.Fatalf("Accept(a, b) = %d, %v", recorded, err)
	}
	// Sent again, with one new, and one twice over in the same call.
	recorded, err = store.Accept(ctx, []Received{received("evt_a", at), received("evt_c", at.Add(2*time.Second)),
		received("evt_c", at.Add(2*time.Second))})
	if err != nil || recorded != 1 {
		t.Fatalf("Accept(a, c, c) = %d, %v; want c alone recorded", recorded, err)
	}

	rows := outboxRows(t, store)
	if len(rows) != 3 {
		t.Fatalf("outbox = %+v, want three events", rows)
	}
	for index, workerID := range []string{"evt_a", "evt_b", "evt_c"} {
		got := rows[index]
		var detail map[string]string
		if err := json.Unmarshal([]byte(got.detail), &detail); err != nil {
			t.Fatal(err)
		}
		if got.id != WorkerEventID(workerID) || got.detailType != realtimev1.ConversationReadDetailType ||
			detail["worker"] != workerID || !got.occurredAt.Equal(at.Add(time.Duration(index)*time.Second)) {
			t.Errorf("event %d = %+v, want the Worker's %s under %s", index, got, workerID, WorkerEventID(workerID))
		}
	}
	if ids := acceptedIDs(t, store); !slices.Equal(ids, []string{"evt_a", "evt_b", "evt_c"}) {
		t.Fatalf("accepted ids = %v", ids)
	}
}

// Everything a call carries is recorded, or nothing is.
func TestAcceptRecordsAllOrNothing(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	// The last id, last in the order Accept takes ids in too, is not one the
	// Worker could give, which the table refuses after the first two have
	// been written in the transaction.
	if _, err := store.Accept(ctx, []Received{received("evt_a", at), received("evt_b", at), received("evt_c!", at)}); err == nil {
		t.Fatal("Accept() with an id the table refuses = nil error")
	}
	if rows, ids := outboxRows(t, store), acceptedIDs(t, store); len(rows) != 0 || len(ids) != 0 {
		t.Fatalf("after a failed Accept: outbox %+v, accepted %v; want nothing", rows, ids)
	}
	if _, err := store.Accept(ctx, []Received{{WorkerID: "evt_a", DetailType: realtimev1.ConversationReadDetailType, OccurredAt: at}}); err == nil {
		t.Fatal("Accept() of an event without data = nil error")
	}
}

// The Worker's retry can race the attempt whose answer it never got. One
// event comes of it.
func TestAcceptRecordsAnIdOnceWhenTwoCallsCarryItAtOnce(t *testing.T) {
	store := testStore(t)
	at := time.Now().UTC()
	var group sync.WaitGroup
	totals := make([]int, 8)
	for index := range totals {
		group.Go(func() {
			recorded, err := store.Accept(context.Background(), []Received{received("evt_raced", at)})
			if err != nil {
				t.Error(err)
			}
			totals[index] = recorded
		})
	}
	group.Wait()
	sum := 0
	for _, recorded := range totals {
		sum += recorded
	}
	if rows := outboxRows(t, store); sum != 1 || len(rows) != 1 {
		t.Fatalf("recorded %d across the calls, outbox %+v; want one event", sum, rows)
	}
}

// Two calls carrying the same ids in opposite orders take them one after the
// other, rather than deadlock. The other call here is a transaction that has
// taken evt_a and goes on to take evt_b, as a call takes them, while Accept
// is sent evt_b before evt_a. Had Accept taken evt_b first, the other call
// would wait for it while it waited for evt_a, and PostgreSQL would fail one
// of them.
func TestAcceptTakesIdsInOrderSoTwoCallsCannotDeadlock(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	other, err := store.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Rollback(ctx) }()
	if _, err := other.Exec(ctx, `INSERT INTO realtime_accepted_events (id) VALUES ('evt_a')`); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		recorded int
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		recorded, err := store.Accept(ctx, []Received{received("evt_b", at), received("evt_a", at)})
		done <- outcome{recorded, err}
	}()
	// Accept is waiting for the other call's evt_a before the other call goes
	// on to evt_b.
	for waiting, deadline := false, time.Now().Add(10*time.Second); !waiting; {
		if err := store.Pool().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if !waiting && time.Now().After(deadline) {
			t.Fatal("Accept never waited for the other call's evt_a")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := other.Exec(ctx, `INSERT INTO realtime_accepted_events (id) VALUES ('evt_b')`); err != nil {
		t.Fatalf("the other call taking evt_b: %v", err)
	}
	if err := other.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.err != nil || got.recorded != 0 {
		t.Fatalf("Accept(b, a) = %d, %v; want it to wait its turn and find both taken", got.recorded, got.err)
	}
}

// Once forgotten, an id's event is recorded again only when the outbox no
// longer holds it either: its id is the same.
func TestForgetAcceptedDeletesOnlyWhatIsOldEnough(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	if _, err := store.Accept(ctx, []Received{received("evt_old", at), received("evt_new", at)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE realtime_accepted_events SET accepted_at = now() - interval '8 days' WHERE id = 'evt_old'`); err != nil {
		t.Fatal(err)
	}
	forgotten, err := store.ForgetAccepted(ctx, time.Now().Add(-AcceptedRetention), 100)
	if err != nil || forgotten != 1 {
		t.Fatalf("ForgetAccepted() = %d, %v; want the old id alone", forgotten, err)
	}
	if ids := acceptedIDs(t, store); !slices.Equal(ids, []string{"evt_new"}) {
		t.Fatalf("accepted ids = %v", ids)
	}
	if recorded, err := store.Accept(ctx, []Received{received("evt_old", at)}); err != nil || recorded != 0 {
		t.Fatalf("Accept(old) while the outbox keeps its event = %d, %v; want nothing recorded", recorded, err)
	}
	if _, err := store.Pool().Exec(ctx, `DELETE FROM realtime_outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `DELETE FROM realtime_accepted_events`); err != nil {
		t.Fatal(err)
	}
	if recorded, err := store.Accept(ctx, []Received{received("evt_old", at)}); err != nil || recorded != 1 {
		t.Fatalf("Accept(old) once both forgot it = %d, %v", recorded, err)
	}
	if rows := outboxRows(t, store); len(rows) != 1 || rows[0].id != WorkerEventID("evt_old") {
		t.Fatalf("outbox = %+v, want the event under its same id", rows)
	}
}

func TestRecordPurgeCompletedRecordsEachPurgeOnce(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	ist := time.FixedZone("IST", 5*60*60+30*60)
	completed := realtimev1.PurgeCompleted{
		OrganizationID: "org_1", Scope: realtimev1.PurgeScopeMember, UserID: "user-b",
		RemovedAt:     time.Date(2026, time.September, 24, 15, 30, 0, 123456789, ist),
		SourceEventID: "evt_auth_1", CompletedAt: time.Date(2026, time.September, 24, 10, 0, 1, 0, time.UTC),
	}
	if recorded, err := store.RecordPurgeCompleted(ctx, completed); err != nil || !recorded {
		t.Fatalf("RecordPurgeCompleted() = %v, %v", recorded, err)
	}
	later := completed
	later.CompletedAt = completed.CompletedAt.Add(time.Hour)
	if recorded, err := store.RecordPurgeCompleted(ctx, later); err != nil || recorded {
		t.Fatalf("RecordPurgeCompleted() again = %v, %v; want nothing new", recorded, err)
	}
	rows := outboxRows(t, store)
	if len(rows) != 1 || rows[0].id != PurgeCompletedEventID("evt_auth_1") || rows[0].detailType != realtimev1.PurgeCompletedDetailType ||
		!rows[0].occurredAt.Equal(completed.CompletedAt) {
		t.Fatalf("outbox = %+v", rows)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(rows[0].detail), &detail); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"organization_id": "org_1", "scope": "member", "user_id": "user-b", "removed_at": "2026-09-24T10:00:00.123456789Z",
		"source_event_id": "evt_auth_1", "completed_at": "2026-09-24T10:00:01Z",
	}
	if len(detail) != len(want) {
		t.Fatalf("detail = %v, want %v", detail, want)
	}
	for key, value := range want {
		if detail[key] != value {
			t.Errorf("detail[%s] = %v, want %v", key, detail[key], value)
		}
	}
	if _, err := store.RecordPurgeCompleted(ctx, realtimev1.PurgeCompleted{OrganizationID: "org_1", CompletedAt: time.Now()}); err == nil {
		t.Fatal("RecordPurgeCompleted() naming no auth event = nil error")
	}
}

// A teamspace's purge is recorded with its teamspace, and a deleted
// teamspace's names no member, as the contract has them.
func TestRecordPurgeCompletedKeepsATeamspacesPurge(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	at := time.Date(2026, time.September, 24, 10, 0, 0, 123456789, time.UTC)
	for _, completed := range []realtimev1.PurgeCompleted{
		{
			OrganizationID: "org_1", Scope: realtimev1.PurgeScopeTeamspaceMember, UserID: "user-b", TeamspaceID: "tsp_1",
			RemovedAt: at, SourceEventID: "evt_agents_1", CompletedAt: at.Add(time.Second),
		},
		{
			OrganizationID: "org_1", Scope: realtimev1.PurgeScopeTeamspace, TeamspaceID: "tsp_1",
			RemovedAt: at, SourceEventID: "evt_agents_2", CompletedAt: at.Add(time.Second),
		},
	} {
		if recorded, err := store.RecordPurgeCompleted(ctx, completed); err != nil || !recorded {
			t.Fatalf("RecordPurgeCompleted(%+v) = %v, %v", completed, recorded, err)
		}
	}
	details := map[string]map[string]any{}
	for _, row := range outboxRows(t, store) {
		var detail map[string]any
		if err := json.Unmarshal([]byte(row.detail), &detail); err != nil {
			t.Fatal(err)
		}
		details[row.id] = detail
	}
	for sourceEventID, want := range map[string]map[string]any{
		"evt_agents_1": {
			"organization_id": "org_1", "scope": "teamspace_member", "user_id": "user-b", "teamspace_id": "tsp_1",
			"removed_at": "2026-09-24T10:00:00.123456789Z", "source_event_id": "evt_agents_1", "completed_at": "2026-09-24T10:00:01.123456789Z",
		},
		"evt_agents_2": {
			"organization_id": "org_1", "scope": "teamspace", "teamspace_id": "tsp_1",
			"removed_at": "2026-09-24T10:00:00.123456789Z", "source_event_id": "evt_agents_2", "completed_at": "2026-09-24T10:00:01.123456789Z",
		},
	} {
		detail := details[PurgeCompletedEventID(sourceEventID)]
		if len(detail) != len(want) {
			t.Fatalf("detail = %v, want %v", detail, want)
		}
		for key, value := range want {
			if detail[key] != value {
				t.Errorf("%s: detail[%s] = %v, want %v", sourceEventID, key, detail[key], value)
			}
		}
	}
}

// The shared dispatcher publishes from realtime's outbox as from any other:
// the table has the columns it claims and marks.
func TestTheSharedDispatcherPublishesFromTheOutbox(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Minute)
	if _, err := store.Accept(ctx, []Received{received("evt_a", at)}); err != nil {
		t.Fatal(err)
	}
	table, err := outbox.NewTable(store.Pool(), OutboxTable)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := table.Claim(ctx, 10, time.Minute, "token")
	if err != nil || len(claimed) != 1 || claimed[0].ID != WorkerEventID("evt_a") ||
		claimed[0].DetailType != realtimev1.ConversationReadDetailType || !claimed[0].OccurredAt.Equal(at.Truncate(time.Microsecond)) {
		t.Fatalf("Claim() = %+v, %v", claimed, err)
	}
	if marked, err := table.MarkPublished(ctx, claimed[0].ID, "token"); err != nil || !marked {
		t.Fatalf("MarkPublished() = %v, %v", marked, err)
	}
}
