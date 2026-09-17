package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// openTestStore migrates a fresh schema and drops it afterwards, so the test
// never leaves tables in a shared development database.
// KNOWLEDGE_TEST_DATABASE_URL must be a postgres:// URL.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("KNOWLEDGE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("KNOWLEDGE_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schemaName := "knowledge_test_" + randomHex(6)
	schema := pgx.Identifier{schemaName}.Sanitize()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		_ = admin.Close(cleanup)
	})

	scoped, err := url.Parse(databaseURL)
	if err != nil || scoped.Scheme == "" {
		t.Fatalf("KNOWLEDGE_TEST_DATABASE_URL must be a postgres:// URL")
	}
	query := scoped.Query()
	query.Set("search_path", schemaName+",public")
	scoped.RawQuery = query.Encode()

	store, err := Open(ctx, scoped.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}
	return store
}

func randomHex(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

func testEvent(t *testing.T, detailType string) domain.Event {
	t.Helper()
	data, err := json.Marshal(map[string]string{"detail_type": detailType})
	if err != nil {
		t.Fatal(err)
	}
	return domain.Event{ID: "evt_" + randomHex(8), DetailType: detailType, OccurredAt: testClock, Data: data}
}

func testSource(t *testing.T, store *Store, organizationID string) domain.Source {
	t.Helper()
	source := domain.Source{
		ID: "src_" + randomHex(8), OrganizationID: organizationID, ConnectionID: "con_" + randomHex(4),
		AppID: "app_1", Provider: "github", Target: map[string]string{"repository": "acme/api"},
		Scope: domain.ScopeOrganization, CreatedBy: "user-1", Status: domain.StatusActive,
		Interval: time.Hour, SyncAt: &testClock, CreatedAt: testClock,
	}
	if err := store.CreateSource(context.Background(), source, nil); err != nil {
		t.Fatalf("CreateSource() error = %v", err)
	}
	return source
}

func testDocument(source domain.Source, externalID, hash string) domain.Document {
	return domain.Document{
		ID: "doc_" + randomHex(8), OrganizationID: source.OrganizationID, SourceID: source.ID,
		ExternalID: externalID, Kind: "issue", Title: "Something broke", URL: "https://example.test/1",
		MIME: "text/markdown", ContentHash: hash, Body: "the body", ByteSize: 8,
		Metadata: map[string]string{"author": "asha"}, ExtractedAt: testClock,
	}
}

func TestCreateSourceStoresTheSourceAndItsEvent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	source := domain.Source{
		ID: "src_" + randomHex(8), OrganizationID: "org_1", ConnectionID: "con_1", AppID: "app_1",
		Provider: "github", Target: map[string]string{"repository": "acme/api"},
		Scope: domain.ScopeMember, MemberID: "user-1", CreatedBy: "user-1",
		Status: domain.StatusActive, Interval: 90 * time.Minute, SyncAt: &testClock, CreatedAt: testClock,
	}
	event := testEvent(t, "knowledge.source.registered.v1")
	if err := store.CreateSource(ctx, source, &event); err != nil {
		t.Fatalf("CreateSource() error = %v", err)
	}

	read, err := store.Source(ctx, "org_1", source.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if read.Provider != "github" || read.Target["repository"] != "acme/api" {
		t.Fatalf("Source() = %+v", read)
	}
	if read.Scope != domain.ScopeMember || read.MemberID != "user-1" || read.TeamID != "" {
		t.Fatalf("Source() placement = %s %q %q", read.Scope, read.MemberID, read.TeamID)
	}
	if read.Interval != 90*time.Minute {
		t.Fatalf("Source() interval = %s, want %s", read.Interval, 90*time.Minute)
	}

	var recorded bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_outbox WHERE id = $1)`, event.ID).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("the source's event was not in the outbox: found = %v, err = %v", recorded, err)
	}
}

func TestCreateSourceRefusesTheSameTargetTwice(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")

	again := source
	again.ID = "src_" + randomHex(8)
	if err := store.CreateSource(ctx, again, nil); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CreateSource() error = %v, want ErrConflict", err)
	}
}

func TestSourcesShowAMemberOnlyTheirOwn(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	mine := testSource(t, store, "org_1")
	theirs := mine
	theirs.ID = "src_" + randomHex(8)
	theirs.CreatedBy = "user-2"
	theirs.Target = map[string]string{"repository": "acme/web"}
	if err := store.CreateSource(ctx, theirs, nil); err != nil {
		t.Fatalf("CreateSource() error = %v", err)
	}

	own, err := store.Sources(ctx, "org_1", domain.SourceQuery{CreatedBy: "user-1"})
	if err != nil {
		t.Fatalf("Sources() error = %v", err)
	}
	if len(own) != 1 || own[0].ID != mine.ID {
		t.Fatalf("Sources(member) = %d sources, want only their own", len(own))
	}

	all, err := store.Sources(ctx, "org_1", domain.SourceQuery{Admin: true})
	if err != nil {
		t.Fatalf("Sources() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Sources(admin) = %d sources, want 2", len(all))
	}
}

func TestUpsertDocumentPublishesOnlyWhatChanged(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "issue-1", strings.Repeat("a", 64))

	published, err := store.UpsertDocument(ctx, document, func(domain.Document) (domain.Event, error) {
		return testEvent(t, "knowledge.document.published.v1"), nil
	})
	if err != nil || !published {
		t.Fatalf("UpsertDocument(new) = %v, %v, want published", published, err)
	}

	published, err = store.UpsertDocument(ctx, document, func(domain.Document) (domain.Event, error) {
		return testEvent(t, "knowledge.document.published.v1"), nil
	})
	if err != nil || published {
		t.Fatalf("UpsertDocument(unchanged) = %v, %v, want nothing published", published, err)
	}

	document.ContentHash = strings.Repeat("b", 64)
	published, err = store.UpsertDocument(ctx, document, func(domain.Document) (domain.Event, error) {
		return testEvent(t, "knowledge.document.published.v1"), nil
	})
	if err != nil || !published {
		t.Fatalf("UpsertDocument(changed) = %v, %v, want published", published, err)
	}

	var outboxCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_outbox`).Scan(&outboxCount); err != nil || outboxCount != 2 {
		t.Fatalf("outbox events = %d, error = %v, want 2", outboxCount, err)
	}
}

func TestUpsertDocumentPublishesADocumentThatCameBack(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "issue-1", strings.Repeat("a", 64))

	if _, err := store.UpsertDocument(ctx, document, nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}
	later := testClock.Add(time.Hour)
	if _, err := store.RetractUnseen(ctx, source.ID, later, later, nil); err != nil {
		t.Fatalf("RetractUnseen() error = %v", err)
	}

	// The same content, but the consumer was told to forget it, so it must be
	// told again.
	published, err := store.UpsertDocument(ctx, document, func(domain.Document) (domain.Event, error) {
		return testEvent(t, "knowledge.document.published.v1"), nil
	})
	if err != nil || !published {
		t.Fatalf("UpsertDocument(returned) = %v, %v, want published", published, err)
	}
}

func TestRetractUnseenForgetsWhatAFullPassDidNotFind(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")

	stale := testDocument(source, "issue-1", strings.Repeat("a", 64))
	if _, err := store.UpsertDocument(ctx, stale, nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}
	runStarted := testClock.Add(time.Hour)
	fresh := testDocument(source, "issue-2", strings.Repeat("b", 64))
	fresh.ExtractedAt = runStarted.Add(time.Minute)
	if _, err := store.UpsertDocument(ctx, fresh, nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}

	retracted, err := store.RetractUnseen(ctx, source.ID, runStarted, runStarted, func(domain.Document) (domain.Event, error) {
		return testEvent(t, "knowledge.document.removed.v1"), nil
	})
	if err != nil {
		t.Fatalf("RetractUnseen() error = %v", err)
	}
	if retracted != 1 {
		t.Fatalf("RetractUnseen() = %d, want 1", retracted)
	}

	// Retracting again finds nothing: the run is safe to repeat.
	retracted, err = store.RetractUnseen(ctx, source.ID, runStarted, runStarted, nil)
	if err != nil || retracted != 0 {
		t.Fatalf("RetractUnseen(again) = %d, %v, want 0", retracted, err)
	}
}

func TestRetractUnseenEmptiesTheBodyItForgot(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "issue-1", strings.Repeat("a", 64))
	if _, err := store.UpsertDocument(ctx, document, nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}

	later := testClock.Add(time.Hour)
	if _, err := store.RetractUnseen(ctx, source.ID, later, later, nil); err != nil {
		t.Fatalf("RetractUnseen() error = %v", err)
	}

	read, err := store.Document(ctx, "org_1", document.ID)
	if err != nil {
		t.Fatalf("Document() error = %v", err)
	}
	if read.RemovedAt == nil {
		t.Fatal("Document() removed_at = nil, want the retraction time")
	}
	if read.Body != "" {
		t.Fatalf("Document() body = %q, want it emptied", read.Body)
	}
}

func TestClaimDueSourcesHandsEachSourceToOneWorker(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	testSource(t, store, "org_1")

	due := testClock.Add(time.Minute)
	first, err := store.ClaimDueSources(ctx, due, 10, "lease-a", due.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimDueSources() error = %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("ClaimDueSources() = %d sources, want 1", len(first))
	}

	second, err := store.ClaimDueSources(ctx, due, 10, "lease-b", due.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimDueSources() error = %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("ClaimDueSources(while leased) = %d sources, want none", len(second))
	}
}

func TestClaimDueSourcesTakesBackAnExpiredLease(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	testSource(t, store, "org_1")

	due := testClock.Add(time.Minute)
	if _, err := store.ClaimDueSources(ctx, due, 10, "lease-a", due.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimDueSources() error = %v", err)
	}

	// The worker holding lease-a died; its lease has run out.
	afterLease := due.Add(2 * time.Minute)
	retaken, err := store.ClaimDueSources(ctx, afterLease, 10, "lease-b", afterLease.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimDueSources() error = %v", err)
	}
	if len(retaken) != 1 {
		t.Fatalf("ClaimDueSources(after the lease lapsed) = %d sources, want 1", len(retaken))
	}
}

func TestRenewLeaseOnlyHoldsWhatIsStillThisRuns(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")

	due := testClock.Add(time.Minute)
	if _, err := store.ClaimDueSources(ctx, due, 10, "lease-a", due.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimDueSources() error = %v", err)
	}

	held, err := store.RenewLease(ctx, source.ID, "lease-a", due.Add(5*time.Minute))
	if err != nil || !held {
		t.Fatalf("RenewLease(own lease) = %v, %v, want held", held, err)
	}
	held, err = store.RenewLease(ctx, source.ID, "lease-b", due.Add(5*time.Minute))
	if err != nil || held {
		t.Fatalf("RenewLease(someone else's lease) = %v, %v, want not held", held, err)
	}
}

func TestFinishSourceChangesNothingWithoutTheLease(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")

	due := testClock.Add(time.Minute)
	if _, err := store.ClaimDueSources(ctx, due, 10, "lease-a", due.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimDueSources() error = %v", err)
	}

	event := testEvent(t, "knowledge.sync.completed.v1")
	claimed, err := store.FinishSource(ctx, source.ID, "lease-b", due, due.Add(time.Hour), nil, &event)
	if err != nil {
		t.Fatalf("FinishSource() error = %v", err)
	}
	if claimed {
		t.Fatal("FinishSource(lost lease) = true, want false")
	}
	var outboxCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_outbox`).Scan(&outboxCount); err != nil || outboxCount != 0 {
		t.Fatalf("outbox events = %d, error = %v, want none from a run that lost its lease", outboxCount, err)
	}

	claimed, err = store.FinishSource(ctx, source.ID, "lease-a", due, due.Add(time.Hour), nil, &event)
	if err != nil || !claimed {
		t.Fatalf("FinishSource(own lease) = %v, %v, want claimed", claimed, err)
	}
	read, err := store.Source(ctx, "org_1", source.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if read.LeaseToken != "" || read.Attempts != 0 {
		t.Fatalf("Source() after finishing = lease %q, attempts %d", read.LeaseToken, read.Attempts)
	}
	if read.FullSweepAt != nil {
		t.Fatalf("Source() full_sweep_at = %v, want nil: this pass did not sweep in full", read.FullSweepAt)
	}
}

func TestFinishSourceRecordsAFullSweepAndKeepsItOnALaterIncrementalPass(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")

	due := testClock.Add(time.Minute)
	if _, err := store.ClaimDueSources(ctx, due, 10, "lease-a", due.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimDueSources() error = %v", err)
	}
	swept := due
	if _, err := store.FinishSource(ctx, source.ID, "lease-a", due, due.Add(time.Hour), &swept, nil); err != nil {
		t.Fatalf("FinishSource() error = %v", err)
	}
	read, err := store.Source(ctx, "org_1", source.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if read.FullSweepAt == nil || !read.FullSweepAt.Equal(swept) {
		t.Fatalf("Source() full_sweep_at = %v, want %v: a full pass just drained", read.FullSweepAt, swept)
	}

	// A later, ordinary incremental pass must not erase what the full sweep
	// recorded: nil leaves the column alone rather than clearing it back to
	// never-swept.
	later := due.Add(time.Hour)
	if _, err := store.ClaimDueSources(ctx, later, 10, "lease-b", later.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimDueSources() error = %v", err)
	}
	if _, err := store.FinishSource(ctx, source.ID, "lease-b", later, later.Add(time.Hour), nil, nil); err != nil {
		t.Fatalf("FinishSource() error = %v", err)
	}
	read, err = store.Source(ctx, "org_1", source.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if read.FullSweepAt == nil || !read.FullSweepAt.Equal(swept) {
		t.Fatalf("Source() full_sweep_at after an incremental pass = %v, want it unchanged at %v", read.FullSweepAt, swept)
	}
}

func TestFailSourceBacksOffAndKeepsTheReason(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")

	due := testClock.Add(time.Minute)
	if _, err := store.ClaimDueSources(ctx, due, 10, "lease-a", due.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimDueSources() error = %v", err)
	}
	retryAt := due.Add(domain.RetryAfter(0))
	claimed, err := store.FailSource(ctx, source.ID, "lease-a", "provider_unavailable", due, retryAt, nil)
	if err != nil || !claimed {
		t.Fatalf("FailSource() = %v, %v, want claimed", claimed, err)
	}

	read, err := store.Source(ctx, "org_1", source.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if read.Attempts != 1 || read.Reason != "provider_unavailable" || read.LeaseToken != "" {
		t.Fatalf("Source() after failing = %+v", read)
	}
	if read.SyncAt == nil || !read.SyncAt.Equal(retryAt) {
		t.Fatalf("Source() sync_at = %v, want %v", read.SyncAt, retryAt)
	}
}

func TestRemoveSourceRetractsWhatItHeld(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "issue-1", strings.Repeat("a", 64))
	if _, err := store.UpsertDocument(ctx, document, nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}

	var retracted int
	err := store.RemoveSource(ctx, "org_1", source.ID, testClock, func(_ domain.Source, documents []domain.Document) ([]domain.Event, error) {
		retracted = len(documents)
		events := make([]domain.Event, 0, len(documents))
		for range documents {
			events = append(events, testEvent(t, "knowledge.document.removed.v1"))
		}
		return events, nil
	})
	if err != nil {
		t.Fatalf("RemoveSource() error = %v", err)
	}
	if retracted != 1 {
		t.Fatalf("RemoveSource() retracted %d documents, want 1", retracted)
	}

	read, err := store.Source(ctx, "org_1", source.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if read.Status != domain.StatusRemoved {
		t.Fatalf("Source() status = %s, want removed", read.Status)
	}
	if err := store.RemoveSource(ctx, "org_1", source.ID, testClock, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RemoveSource(again) error = %v, want ErrNotFound", err)
	}
}

func TestEventHandledIsTrueOnlyAfterMarkEventHandled(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	handled, err := store.EventHandled(ctx, "evt_1")
	if err != nil || handled {
		t.Fatalf("EventHandled(before) = %v, %v, want false", handled, err)
	}
	if err := store.MarkEventHandled(ctx, "evt_1", "connections.connection.disconnected.v1", testClock); err != nil {
		t.Fatalf("MarkEventHandled() error = %v", err)
	}
	handled, err = store.EventHandled(ctx, "evt_1")
	if err != nil || !handled {
		t.Fatalf("EventHandled(after) = %v, %v, want true", handled, err)
	}
	// A redelivery racing another one marks the same event again; that must
	// be tolerated rather than treated as an error, since the handler it
	// followed is idempotent under that race.
	if err := store.MarkEventHandled(ctx, "evt_1", "connections.connection.disconnected.v1", testClock); err != nil {
		t.Fatalf("MarkEventHandled(again) error = %v", err)
	}

	forgotten, err := store.DeleteHandledEventsBefore(ctx, testClock.Add(time.Hour))
	if err != nil || forgotten != 1 {
		t.Fatalf("DeleteHandledEventsBefore() = %d, %v, want 1", forgotten, err)
	}
}

func TestCursorRemembersWhereARunStopped(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")

	empty, err := store.Cursor(ctx, source.ID)
	if err != nil {
		t.Fatalf("Cursor() error = %v", err)
	}
	if empty.Value != "" || empty.Watermark != nil || empty.WalkStartedAt != nil {
		t.Fatalf("Cursor(never run) = %+v, want an empty cursor", empty)
	}

	watermark := testClock.Add(-time.Hour)
	walkStarted := testClock.Add(-30 * time.Minute)
	if err := store.SaveCursor(ctx, domain.Cursor{
		SourceID: source.ID, Value: "page-2", Watermark: &watermark, WalkStartedAt: &walkStarted, UpdatedAt: testClock,
	}); err != nil {
		t.Fatalf("SaveCursor() error = %v", err)
	}
	read, err := store.Cursor(ctx, source.ID)
	if err != nil {
		t.Fatalf("Cursor() error = %v", err)
	}
	if read.Value != "page-2" || read.Watermark == nil || !read.Watermark.Equal(watermark) {
		t.Fatalf("Cursor() = %+v", read)
	}
	if read.WalkStartedAt == nil || !read.WalkStartedAt.Equal(walkStarted) {
		t.Fatalf("Cursor() walk_started_at = %v, want %v: a walk spanning several runs must resume knowing when it began", read.WalkStartedAt, walkStarted)
	}

	// A save with no walk in progress writes exactly what it is given, the
	// same as any other cursor field: once a walk drains, the application
	// layer stops carrying its start forward and this must actually clear.
	if err := store.SaveCursor(ctx, domain.Cursor{
		SourceID: source.ID, Value: "", Watermark: &watermark, UpdatedAt: testClock,
	}); err != nil {
		t.Fatalf("SaveCursor() error = %v", err)
	}
	drained, err := store.Cursor(ctx, source.ID)
	if err != nil {
		t.Fatalf("Cursor() error = %v", err)
	}
	if drained.WalkStartedAt != nil {
		t.Fatalf("Cursor() walk_started_at = %v, want nil once the walk that started it is over", drained.WalkStartedAt)
	}

	if err := store.ClearCursor(ctx, source.ID); err != nil {
		t.Fatalf("ClearCursor() error = %v", err)
	}
	cleared, err := store.Cursor(ctx, source.ID)
	if err != nil || cleared.Value != "" {
		t.Fatalf("Cursor(cleared) = %+v, %v", cleared, err)
	}
}

func TestRunsRecordWhatEachPassDid(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")

	run := domain.SyncRun{
		ID: "run_" + randomHex(8), OrganizationID: "org_1", SourceID: source.ID,
		Kind: "backfill", StartedAt: testClock,
	}
	if err := store.StartRun(ctx, run); err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	finished := testClock.Add(time.Minute)
	run.Status = domain.RunCompleted
	run.DocumentsSeen, run.DocumentsPublished, run.DocumentsRemoved = 10, 3, 1
	run.FinishedAt = &finished
	if err := store.FinishRun(ctx, run, nil); err != nil {
		t.Fatalf("FinishRun() error = %v", err)
	}

	runs, err := store.Runs(ctx, "org_1", source.ID, 10)
	if err != nil {
		t.Fatalf("Runs() error = %v", err)
	}
	if len(runs) != 1 || runs[0].Status != domain.RunCompleted || runs[0].DocumentsPublished != 3 {
		t.Fatalf("Runs() = %+v", runs)
	}
}
