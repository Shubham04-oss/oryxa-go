package runstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// testIDPrefix marks every row these tests create, in both agent_runs and
// agents_outbox, so a leak is easy to find and clean up by hand:
//
//	SELECT count(*) FROM agents_primary.agent_runs WHERE id LIKE 'runstore_test_%';
//	SELECT count(*) FROM agents_primary.agents_outbox WHERE id LIKE 'runstore_test_%';
const testIDPrefix = "runstore_test_"

// openTestDatabase connects to the shared development PostgreSQL named by
// AGENTS_PRIMARY_TEST_DATABASE_URL and migrates agents_primary into it. It
// deliberately opens with no Schema set: the store's own queries name their
// schema on every statement, and this is what proves that holds even when
// the pool's search_path is left at the default public.
func openTestDatabase(t *testing.T) *postgreskit.Database {
	t.Helper()
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-agents-runstore-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	store := New(database)
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}
	return database
}

func randomHex(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

// testID returns an id carrying testIDPrefix, unique enough that concurrent
// test runs against the shared database never collide.
func testID(part string) string {
	return testIDPrefix + part + "_" + randomHex(8)
}

// testRun is a Run ready to Start, in a session of its own unless sessionID
// says otherwise.
func testRun(sessionID string) Run {
	return Run{
		ID:             testID("run"),
		SessionID:      sessionID,
		UserID:         testIDPrefix + "user",
		OrganizationID: testIDPrefix + "org",
		StartedBy:      agentsv1.StartedByMember,
	}
}

// testEvent is an event ready for Finish, carrying runID in its detail so a
// test can tell one run's event apart from another's.
func testEvent(t *testing.T, runID, detailType string) events.Event {
	t.Helper()
	data, err := json.Marshal(map[string]string{"run_id": runID, "detail_type": detailType})
	if err != nil {
		t.Fatal(err)
	}
	return events.Event{ID: testID("evt"), DetailType: detailType, OccurredAt: time.Now().UTC(), Data: data}
}

func deleteRuns(t *testing.T, database *postgreskit.Database, runIDs ...string) {
	t.Helper()
	if len(runIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := database.Pool.Exec(ctx, `DELETE FROM agents_primary.agent_runs WHERE id = ANY($1)`, runIDs); err != nil {
		t.Errorf("clean up agent_runs: %v", err)
	}
}

func deleteOutboxEvents(t *testing.T, database *postgreskit.Database, eventIDs ...string) {
	t.Helper()
	if len(eventIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE id = ANY($1)`, eventIDs); err != nil {
		t.Errorf("clean up agents_outbox: %v", err)
	}
}

// runRow is what the tests below check of a finished or abandoned run; it
// mirrors agent_runs' own columns rather than any exported type, since
// runstore does not otherwise expose a way to read a run back.
type runRow struct {
	Status                                               string
	FinishedAt, LeaseUntil                               *time.Time
	Error                                                *string
	InputTokens, OutputTokens, CachedTokens, TotalTokens int32
	CostMicros                                           int64
}

func fetchRun(t *testing.T, database *postgreskit.Database, runID string) runRow {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var row runRow
	err := database.Pool.QueryRow(ctx, `
		SELECT status, finished_at, lease_until, error,
		       input_tokens, output_tokens, cached_tokens, total_tokens, cost_micros
		FROM agents_primary.agent_runs WHERE id = $1`, runID).Scan(
		&row.Status, &row.FinishedAt, &row.LeaseUntil, &row.Error,
		&row.InputTokens, &row.OutputTokens, &row.CachedTokens, &row.TotalTokens, &row.CostMicros)
	if err != nil {
		t.Fatalf("fetch run: %v", err)
	}
	return row
}

type outboxRow struct {
	DetailType string
	Detail     []byte
}

func fetchOutboxEvents(t *testing.T, database *postgreskit.Database, eventID string) []outboxRow {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := database.Pool.Query(ctx, `
		SELECT detail_type, detail FROM agents_primary.agents_outbox WHERE id = $1`, eventID)
	if err != nil {
		t.Fatalf("query agents_outbox: %v", err)
	}
	defer rows.Close()
	var found []outboxRow
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(&row.DetailType, &row.Detail); err != nil {
			t.Fatalf("scan agents_outbox row: %v", err)
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read agents_outbox rows: %v", err)
	}
	return found
}

func TestStartSecondRunInSameSessionIsRefused(t *testing.T) {
	database := openTestDatabase(t)
	store := New(database)
	ctx := context.Background()

	sessionID := testID("session")
	first := testRun(sessionID)
	second := testRun(sessionID)
	elsewhere := testRun(testID("session"))
	t.Cleanup(func() { deleteRuns(t, database, first.ID, second.ID, elsewhere.ID) })

	if err := store.Start(ctx, first, time.Minute); err != nil {
		t.Fatalf("Start() first run in the session: %v", err)
	}
	if err := store.Start(ctx, second, time.Minute); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("Start() second run in the same session = %v, want ErrRunInProgress", err)
	}
	if err := store.Start(ctx, elsewhere, time.Minute); err != nil {
		t.Fatalf("Start() run in a different session: %v", err)
	}
}

func TestStartDuplicateRunIDIsAnOrdinaryError(t *testing.T) {
	database := openTestDatabase(t)
	store := New(database)
	ctx := context.Background()

	run := testRun(testID("session"))
	reused := run
	reused.SessionID = testID("session") // a different conversation, same run id
	t.Cleanup(func() { deleteRuns(t, database, run.ID) })

	if err := store.Start(ctx, run, time.Minute); err != nil {
		t.Fatalf("Start() first run: %v", err)
	}
	err := store.Start(ctx, reused, time.Minute)
	if err == nil {
		t.Fatal("Start() with a reused run id = nil, want an error")
	}
	if errors.Is(err, ErrRunInProgress) {
		t.Fatalf("Start() with a reused run id = %v, want an ordinary error, not ErrRunInProgress", err)
	}
	if errors.Is(err, ErrNotRunning) {
		t.Fatalf("Start() with a reused run id = %v, want an ordinary error, not ErrNotRunning", err)
	}
}

func TestRenewExtendsOrReportsNotRunning(t *testing.T) {
	database := openTestDatabase(t)
	store := New(database)
	ctx := context.Background()

	run := testRun(testID("session"))
	t.Cleanup(func() { deleteRuns(t, database, run.ID) })
	if err := store.Start(ctx, run, 2*time.Second); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	before := fetchRun(t, database, run.ID)

	if err := store.Renew(ctx, run.ID, 10*time.Minute); err != nil {
		t.Fatalf("Renew() on a running run: %v", err)
	}
	after := fetchRun(t, database, run.ID)
	if before.LeaseUntil == nil || after.LeaseUntil == nil || !after.LeaseUntil.After(*before.LeaseUntil) {
		t.Fatalf("Renew() lease_until = %v, want it later than %v", after.LeaseUntil, before.LeaseUntil)
	}

	if err := store.Renew(ctx, testID("run"), time.Minute); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Renew() on an unknown run = %v, want ErrNotRunning", err)
	}
}

func TestStartAbandonsAnExpiredLeaseAndTheAbandonedRunCanStillFinish(t *testing.T) {
	database := openTestDatabase(t)
	store := New(database)
	ctx := context.Background()

	sessionID := testID("session")
	expired := testRun(sessionID)
	replacement := testRun(sessionID)
	event := testEvent(t, expired.ID, agentsv1.RunCompletedDetailType)
	t.Cleanup(func() { deleteRuns(t, database, expired.ID, replacement.ID) })
	t.Cleanup(func() { deleteOutboxEvents(t, database, event.ID) })

	if err := store.Start(ctx, expired, 50*time.Millisecond); err != nil {
		t.Fatalf("Start() the run that will expire: %v", err)
	}
	time.Sleep(250 * time.Millisecond)

	if err := store.Start(ctx, replacement, time.Minute); err != nil {
		t.Fatalf("Start() after the lease lapsed: %v", err)
	}

	abandoned := fetchRun(t, database, expired.ID)
	if abandoned.Status != StatusAbandoned {
		t.Fatalf("abandoned run status = %q, want %q", abandoned.Status, StatusAbandoned)
	}
	if abandoned.LeaseUntil != nil {
		t.Fatalf("abandoned run lease_until = %v, want nil", abandoned.LeaseUntil)
	}
	if abandoned.FinishedAt == nil {
		t.Fatal("abandoned run finished_at = nil, want it set")
	}
	if abandoned.Error == nil || *abandoned.Error == "" {
		t.Fatal("abandoned run error = empty, want an explanation")
	}

	// A run whose lease lapsed, say because of a long pause, may still finish
	// late. Finish must record how it actually ended even though Start already
	// marked it abandoned.
	outcome := Outcome{Status: StatusCompleted, Usage: agentsv1.Usage{TotalTokens: 7}}
	if err := store.Finish(ctx, expired.ID, outcome, event); err != nil {
		t.Fatalf("Finish() on an abandoned run: %v", err)
	}
	finished := fetchRun(t, database, expired.ID)
	if finished.Status != StatusCompleted {
		t.Fatalf("finished run status = %q, want %q", finished.Status, StatusCompleted)
	}
	if rows := fetchOutboxEvents(t, database, event.ID); len(rows) != 1 {
		t.Fatalf("outbox rows for the abandoned run's event = %d, want 1", len(rows))
	}
}

func TestFinishCompletedRecordsOutcomeAndFreesTheSession(t *testing.T) {
	database := openTestDatabase(t)
	store := New(database)
	ctx := context.Background()

	sessionID := testID("session")
	run := testRun(sessionID)
	event := testEvent(t, run.ID, agentsv1.RunCompletedDetailType)
	t.Cleanup(func() { deleteRuns(t, database, run.ID) })
	t.Cleanup(func() { deleteOutboxEvents(t, database, event.ID) })

	if err := store.Start(ctx, run, time.Minute); err != nil {
		t.Fatalf("Start(): %v", err)
	}

	outcome := Outcome{
		Status:     StatusCompleted,
		Usage:      agentsv1.Usage{InputTokens: 120, OutputTokens: 45, CachedTokens: 30, TotalTokens: 165},
		CostMicros: 4321,
	}
	if err := store.Finish(ctx, run.ID, outcome, event); err != nil {
		t.Fatalf("Finish(): %v", err)
	}

	row := fetchRun(t, database, run.ID)
	if row.Status != StatusCompleted {
		t.Fatalf("status = %q, want %q", row.Status, StatusCompleted)
	}
	if row.FinishedAt == nil {
		t.Fatal("finished_at = nil, want it set")
	}
	if row.LeaseUntil != nil {
		t.Fatalf("lease_until = %v, want nil", row.LeaseUntil)
	}
	if row.Error != nil {
		t.Fatalf("error = %v, want nil", *row.Error)
	}
	if row.InputTokens != 120 || row.OutputTokens != 45 || row.CachedTokens != 30 || row.TotalTokens != 165 {
		t.Fatalf("token usage = %+v, want 120/45/30/165", row)
	}
	if row.CostMicros != 4321 {
		t.Fatalf("cost_micros = %d, want 4321", row.CostMicros)
	}

	outboxRows := fetchOutboxEvents(t, database, event.ID)
	if len(outboxRows) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(outboxRows))
	}
	if outboxRows[0].DetailType != event.DetailType {
		t.Fatalf("outbox detail_type = %q, want %q", outboxRows[0].DetailType, event.DetailType)
	}
	var detail map[string]string
	if err := json.Unmarshal(outboxRows[0].Detail, &detail); err != nil {
		t.Fatalf("decode outbox detail: %v", err)
	}
	if detail["run_id"] != run.ID {
		t.Fatalf("outbox detail run_id = %q, want %q", detail["run_id"], run.ID)
	}

	// The session is free again once its only run has finished.
	again := testRun(sessionID)
	t.Cleanup(func() { deleteRuns(t, database, again.ID) })
	if err := store.Start(ctx, again, time.Minute); err != nil {
		t.Fatalf("Start() after the session's run finished: %v", err)
	}
}

func TestFinishTwiceIsRefusedWithoutDuplicatingTheEvent(t *testing.T) {
	database := openTestDatabase(t)
	store := New(database)
	ctx := context.Background()

	run := testRun(testID("session"))
	firstEvent := testEvent(t, run.ID, agentsv1.RunCompletedDetailType)
	secondEvent := testEvent(t, run.ID, agentsv1.RunCompletedDetailType)
	t.Cleanup(func() { deleteRuns(t, database, run.ID) })
	t.Cleanup(func() { deleteOutboxEvents(t, database, firstEvent.ID, secondEvent.ID) })

	if err := store.Start(ctx, run, time.Minute); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	if err := store.Finish(ctx, run.ID, Outcome{Status: StatusCompleted}, firstEvent); err != nil {
		t.Fatalf("Finish() first call: %v", err)
	}
	if err := store.Finish(ctx, run.ID, Outcome{Status: StatusCompleted}, secondEvent); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Finish() second call = %v, want ErrNotRunning", err)
	}

	if rows := fetchOutboxEvents(t, database, firstEvent.ID); len(rows) != 1 {
		t.Fatalf("outbox rows for the first event = %d, want 1", len(rows))
	}
	if rows := fetchOutboxEvents(t, database, secondEvent.ID); len(rows) != 0 {
		t.Fatalf("outbox rows for the refused second call's event = %d, want 0", len(rows))
	}
}

func TestFinishFailedRecordsTheErrorText(t *testing.T) {
	database := openTestDatabase(t)
	store := New(database)
	ctx := context.Background()

	run := testRun(testID("session"))
	event := testEvent(t, run.ID, agentsv1.RunFailedDetailType)
	t.Cleanup(func() { deleteRuns(t, database, run.ID) })
	t.Cleanup(func() { deleteOutboxEvents(t, database, event.ID) })

	if err := store.Start(ctx, run, time.Minute); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	outcome := Outcome{Status: StatusFailed, Error: "the sandbox ran out of disk"}
	if err := store.Finish(ctx, run.ID, outcome, event); err != nil {
		t.Fatalf("Finish(): %v", err)
	}

	row := fetchRun(t, database, run.ID)
	if row.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", row.Status, StatusFailed)
	}
	if row.Error == nil || *row.Error != outcome.Error {
		t.Fatalf("error = %v, want %q", row.Error, outcome.Error)
	}
}

// TestFinishInterruptedFreesTheSessionAndSaysSo: a run a restart stopped is
// recorded interrupted, which the database's status check now knows, and its
// conversation is free for the run that replaces it.
func TestFinishInterruptedFreesTheSessionAndSaysSo(t *testing.T) {
	database := openTestDatabase(t)
	store := New(database)
	ctx := context.Background()

	run := testRun(testID("session"))
	event := testEvent(t, run.ID, agentsv1.RunFailedDetailType)
	again := testRun(run.SessionID)
	t.Cleanup(func() { deleteRuns(t, database, run.ID, again.ID) })
	t.Cleanup(func() { deleteOutboxEvents(t, database, event.ID) })

	if err := store.Start(ctx, run, time.Minute); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	outcome := Outcome{Status: StatusInterrupted, Error: "the run was interrupted because Oryxa restarted"}
	if err := store.Finish(ctx, run.ID, outcome, event); err != nil {
		t.Fatalf("Finish(): %v", err)
	}
	if row := fetchRun(t, database, run.ID); row.Status != StatusInterrupted {
		t.Fatalf("status = %q, want %q", row.Status, StatusInterrupted)
	}
	if err := store.Start(ctx, again, time.Minute); err != nil {
		t.Fatalf("starting the run again in its conversation: %v", err)
	}
	if err := store.Finish(ctx, again.ID, Outcome{Status: "paused"}, testEvent(t, again.ID, agentsv1.RunFailedDetailType)); err == nil {
		t.Fatal("an outcome nobody knows was recorded")
	}
}

// TestStartConcurrencyAllowsExactlyOneLiveRunPerSession is what actually
// proves the partial unique index does its job: ten replicas racing to start
// a run in the same conversation must leave exactly one of them running.
func TestStartConcurrencyAllowsExactlyOneLiveRunPerSession(t *testing.T) {
	database := openTestDatabase(t)
	store := New(database)
	ctx := context.Background()

	sessionID := testID("session")
	const attempts = 10
	runs := make([]Run, attempts)
	runIDs := make([]string, attempts)
	for i := range runs {
		runs[i] = testRun(sessionID)
		runIDs[i] = runs[i].ID
	}
	t.Cleanup(func() { deleteRuns(t, database, runIDs...) })

	var ready, wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, attempts)
	ready.Add(attempts)
	wg.Add(attempts)
	for i := range runs {
		go func(i int) {
			defer wg.Done()
			ready.Done()
			<-start
			errs[i] = store.Start(ctx, runs[i], time.Minute)
		}(i)
	}
	ready.Wait()
	close(start)
	wg.Wait()

	var successes, inProgress int
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRunInProgress):
			inProgress++
		default:
			t.Fatalf("Start() unexpected error = %v", err)
		}
	}
	if successes != 1 || inProgress != attempts-1 {
		t.Fatalf("successes = %d, inProgress = %d, want 1 and %d", successes, inProgress, attempts-1)
	}
}

func TestCancelForgetsARunThatNeverRanAndFreesItsSession(t *testing.T) {
	database := openTestDatabase(t)
	store := New(database)
	ctx := context.Background()

	sessionID := testID("session")
	cancelled := testRun(sessionID)
	next := testRun(sessionID)
	t.Cleanup(func() { deleteRuns(t, database, cancelled.ID, next.ID) })

	if err := store.Start(ctx, cancelled, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.Cancel(ctx, cancelled.ID); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
	var remaining int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agent_runs WHERE id = $1`, cancelled.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("a cancelled run left %d rows (%v)", remaining, err)
	}
	if err := store.Cancel(ctx, cancelled.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Cancel() twice = %v, want ErrNotRunning", err)
	}
	if err := store.Start(ctx, next, time.Minute); err != nil {
		t.Fatalf("Start() after a cancel = %v, want the session free", err)
	}
	if err := store.Cancel(ctx, "never_started"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Cancel() of an unknown run = %v, want ErrNotRunning", err)
	}
}
