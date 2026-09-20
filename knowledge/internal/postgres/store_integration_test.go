package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"slices"
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

// TestUpsertDocumentStoresAndClearsAnOriginal covers the three columns a
// document whose bytes knowledge does not read itself is written with, and
// that a later write without an Original — which nothing here actually does
// today, but the column is nullable, not conditionally required — clears them
// rather than leaving a stale bucket and key behind.
func TestUpsertDocumentStoresAndClearsAnOriginal(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "pdf-1", strings.Repeat("a", 64))
	document.Original = &domain.Original{Bucket: "oryxa-knowledge-originals", Key: "originals/org_1/src/doc/aaaa", Size: 1234}

	if _, err := store.UpsertDocument(ctx, document, nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}
	read, err := store.DocumentByExternalID(ctx, source.ID, "pdf-1")
	if err != nil {
		t.Fatalf("DocumentByExternalID() error = %v", err)
	}
	if read.Original == nil {
		t.Fatal("DocumentByExternalID() Original = nil, want the bucket and key written")
	}
	if read.Original.Bucket != document.Original.Bucket || read.Original.Key != document.Original.Key ||
		read.Original.Size != document.Original.Size {
		t.Fatalf("Original = %+v, want %+v", read.Original, document.Original)
	}
	if read.Original.SHA256 != read.ContentHash {
		t.Fatalf("Original.SHA256 = %q, want the document's own content hash (%q)", read.Original.SHA256, read.ContentHash)
	}

	document.ContentHash = strings.Repeat("b", 64)
	document.Original = nil
	if _, err := store.UpsertDocument(ctx, document, nil); err != nil {
		t.Fatalf("UpsertDocument() [clearing] error = %v", err)
	}
	cleared, err := store.DocumentByExternalID(ctx, source.ID, "pdf-1")
	if err != nil {
		t.Fatalf("DocumentByExternalID() error = %v", err)
	}
	if cleared.Original != nil {
		t.Fatalf("Original = %+v, want nil once a write no longer carries one", cleared.Original)
	}
}

// TestDocumentByExternalIDAnswersNotFoundForAnUnknownDocument covers the
// lookup keepOriginal makes before deciding whether a document's bytes are
// worth writing again.
func TestDocumentByExternalIDAnswersNotFoundForAnUnknownDocument(t *testing.T) {
	store := openTestStore(t)
	source := testSource(t, store, "org_1")

	if _, err := store.DocumentByExternalID(context.Background(), source.ID, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("DocumentByExternalID() error = %v, want ErrNotFound", err)
	}
}

// TestDocumentByExternalIDAnswersARemovedDocumentToo covers why this cannot
// simply be Document(): a document a full sweep already retracted must still
// answer here, so a provider's own incremental feed reporting the same
// document again reuses its stable id rather than minting a new one.
func TestDocumentByExternalIDAnswersARemovedDocumentToo(t *testing.T) {
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

	read, err := store.DocumentByExternalID(ctx, source.ID, "issue-1")
	if err != nil {
		t.Fatalf("DocumentByExternalID() error = %v", err)
	}
	if read.ID != document.ID || read.RemovedAt == nil {
		t.Fatalf("DocumentByExternalID() = %+v, want the same, now-removed document", read)
	}
}

// TestRetractDocumentForgetsOneDocumentImmediately covers the removal path
// Page.Removed drives: an incremental feed that already knows a document is
// gone retracts it by external id at once, rather than waiting for a full
// pass to notice the same thing.
func TestRetractDocumentForgetsOneDocumentImmediately(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "issue-1", strings.Repeat("a", 64))
	if _, err := store.UpsertDocument(ctx, document, nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}

	var eventPassed domain.Document
	retracted, found, err := store.RetractDocument(ctx, source.ID, "issue-1", testClock, func(document domain.Document) (domain.Event, error) {
		eventPassed = document
		return testEvent(t, "knowledge.document.removed.v1"), nil
	})
	if err != nil || !found {
		t.Fatalf("RetractDocument() = %v, %v, %v, want found", retracted, found, err)
	}
	if eventPassed.ID != document.ID {
		t.Fatalf("the callback saw document %+v, want %s", eventPassed, document.ID)
	}
	read, err := store.Document(ctx, "org_1", document.ID)
	if err != nil {
		t.Fatalf("Document() error = %v", err)
	}
	if read.RemovedAt == nil || read.Body != "" {
		t.Fatalf("Document() = %+v, want removed and emptied", read)
	}

	// Retracting the same document again finds nothing live to retract, and
	// runs no callback: a redelivered removal must not publish twice.
	_, foundAgain, err := store.RetractDocument(ctx, source.ID, "issue-1", testClock, func(domain.Document) (domain.Event, error) {
		t.Fatal("the callback ran for a document already retracted")
		return domain.Event{}, nil
	})
	if err != nil || foundAgain {
		t.Fatalf("RetractDocument(again) = %v, %v, want not found", foundAgain, err)
	}

	// An id nothing here ever held answers the same way: not found, no error.
	_, foundNever, err := store.RetractDocument(ctx, source.ID, "never-existed", testClock, nil)
	if err != nil || foundNever {
		t.Fatalf("RetractDocument(unknown) = %v, %v, want not found", foundNever, err)
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

func TestCreateSourceStoresTheComposioAccount(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := domain.Source{
		ID: "src_" + randomHex(8), OrganizationID: "org_1", ConnectionID: "con_1", ComposioAccountID: "ca_1",
		Provider: "github", Target: map[string]string{"repository": "acme/api"},
		Scope: domain.ScopeMember, MemberID: "user-1", CreatedBy: "user-1",
		Status: domain.StatusActive, Interval: time.Hour, SyncAt: &testClock, CreatedAt: testClock,
	}
	if err := store.CreateSource(ctx, source, nil); err != nil {
		t.Fatalf("CreateSource() error = %v", err)
	}

	read, err := store.Source(ctx, "org_1", source.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if read.ComposioAccountID != "ca_1" {
		t.Fatalf("Source() composio_account_id = %q, want ca_1", read.ComposioAccountID)
	}
}

func TestSourcesFiltersByConnection(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mine := testSource(t, store, "org_1")

	elsewhere := mine
	elsewhere.ID = "src_" + randomHex(8)
	elsewhere.ConnectionID = "con_" + randomHex(4)
	elsewhere.Target = map[string]string{"repository": "acme/elsewhere"}
	if err := store.CreateSource(ctx, elsewhere, nil); err != nil {
		t.Fatalf("CreateSource() error = %v", err)
	}

	listed, err := store.Sources(ctx, "org_1", domain.SourceQuery{Admin: true, ConnectionID: mine.ConnectionID})
	if err != nil {
		t.Fatalf("Sources() error = %v", err)
	}
	if len(listed) != 1 || listed[0].ID != mine.ID {
		t.Fatalf("Sources(connection_id) = %d sources, want only %s", len(listed), mine.ID)
	}
}

// TestSourcesAnswersRemovedOnesOnlyWhenAsked: a listing never shows a source
// a member removed, but deciding whether to register one on their behalf
// has to see it, since removing it was them saying no.
func TestSourcesAnswersRemovedOnesOnlyWhenAsked(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	if err := store.RemoveSource(ctx, "org_1", source.ID, testClock, nil); err != nil {
		t.Fatalf("RemoveSource() error = %v", err)
	}

	listed, err := store.Sources(ctx, "org_1", domain.SourceQuery{Admin: true, ConnectionID: source.ConnectionID})
	if err != nil {
		t.Fatalf("Sources() error = %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("Sources() = %d sources, want the removed one left out", len(listed))
	}
	listed, err = store.Sources(ctx, "org_1", domain.SourceQuery{
		Admin: true, ConnectionID: source.ConnectionID, IncludeRemoved: true,
	})
	if err != nil {
		t.Fatalf("Sources(include removed) error = %v", err)
	}
	if len(listed) != 1 || listed[0].ID != source.ID || listed[0].Status != domain.StatusRemoved {
		t.Fatalf("Sources(include removed) = %+v, want the removed source", listed)
	}
}

func TestPauseSourcesStopsWhatReadsThroughAConnection(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")

	// A source on another connection must be left alone.
	elsewhere := source
	elsewhere.ID = "src_" + randomHex(8)
	elsewhere.ConnectionID = "con_" + randomHex(4)
	elsewhere.Target = map[string]string{"repository": "acme/elsewhere"}
	if err := store.CreateSource(ctx, elsewhere, nil); err != nil {
		t.Fatalf("CreateSource() error = %v", err)
	}

	var recordedReason string
	paused, err := store.PauseSources(ctx, source.ConnectionID, "needs_reconnect", testClock,
		func(paused domain.Source) (domain.Event, error) {
			recordedReason = paused.Reason
			return testEvent(t, "knowledge.source.paused.v1"), nil
		})
	if err != nil {
		t.Fatalf("PauseSources() error = %v", err)
	}
	if len(paused) != 1 || paused[0].ID != source.ID {
		t.Fatalf("PauseSources() = %d sources, want only %s", len(paused), source.ID)
	}
	if recordedReason != "needs_reconnect" {
		t.Fatalf("the event's source reason = %q, want needs_reconnect", recordedReason)
	}

	read, err := store.Source(ctx, "org_1", source.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if read.Status != domain.StatusPaused || read.Reason != "needs_reconnect" || read.SyncAt != nil {
		t.Fatalf("Source() after pausing = %+v", read)
	}
	other, err := store.Source(ctx, "org_1", elsewhere.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if other.Status != domain.StatusActive {
		t.Fatalf("a source on another connection was paused: status = %s", other.Status)
	}

	// Running it again pauses nothing further: a redelivered event is
	// harmless.
	again, err := store.PauseSources(ctx, source.ConnectionID, "needs_reconnect", testClock, nil)
	if err != nil || len(again) != 0 {
		t.Fatalf("PauseSources(again) = %d, %v, want none", len(again), err)
	}
}

func TestRemoveSourcesByConnectionRetractsWhatItHeld(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "issue-1", strings.Repeat("a", 64))
	if _, err := store.UpsertDocument(ctx, document, nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}

	elsewhere := source
	elsewhere.ID = "src_" + randomHex(8)
	elsewhere.ConnectionID = "con_" + randomHex(4)
	elsewhere.Target = map[string]string{"repository": "acme/elsewhere"}
	if err := store.CreateSource(ctx, elsewhere, nil); err != nil {
		t.Fatalf("CreateSource() error = %v", err)
	}

	var retracted int
	removed, err := store.RemoveSourcesByConnection(ctx, source.ConnectionID, testClock,
		func(_ domain.Source, documents []domain.Document) ([]domain.Event, error) {
			retracted = len(documents)
			events := make([]domain.Event, 0, len(documents))
			for range documents {
				events = append(events, testEvent(t, "knowledge.document.removed.v1"))
			}
			return events, nil
		})
	if err != nil {
		t.Fatalf("RemoveSourcesByConnection() error = %v", err)
	}
	if len(removed) != 1 || removed[0].ID != source.ID {
		t.Fatalf("RemoveSourcesByConnection() = %d sources, want only %s", len(removed), source.ID)
	}
	if retracted != 1 {
		t.Fatalf("RemoveSourcesByConnection() retracted %d documents, want 1", retracted)
	}

	read, err := store.Source(ctx, "org_1", source.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if read.Status != domain.StatusRemoved {
		t.Fatalf("Source() status = %s, want removed", read.Status)
	}
	other, err := store.Source(ctx, "org_1", elsewhere.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if other.Status != domain.StatusActive {
		t.Fatalf("a source on another connection was removed: status = %s", other.Status)
	}

	// Running it again finds nothing further: a redelivered event is
	// harmless.
	again, err := store.RemoveSourcesByConnection(ctx, source.ConnectionID, testClock, nil)
	if err != nil || len(again) != 0 {
		t.Fatalf("RemoveSourcesByConnection(again) = %d, %v, want none", len(again), err)
	}
}

// TestRemoveSourcesByTeamRetiresOnlyThatTeam is the regression for a deleted
// team's documents coming back: rag drops the team's corpora when the team is
// deleted, and a team source left running would publish into a new one.
func TestRemoveSourcesByTeamRetiresOnlyThatTeam(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	team := func(teamID, repository string) domain.Source {
		source := domain.Source{
			ID: "src_" + randomHex(8), OrganizationID: "org_1", ConnectionID: "con_" + randomHex(4),
			AppID: "app_1", Provider: "github", Target: map[string]string{"repository": repository},
			Scope: domain.ScopeTeam, TeamID: teamID, CreatedBy: "user-1", Status: domain.StatusActive,
			Interval: time.Hour, SyncAt: &testClock, CreatedAt: testClock,
		}
		if err := store.CreateSource(ctx, source, nil); err != nil {
			t.Fatalf("CreateSource() error = %v", err)
		}
		return source
	}
	gone := team("team_gone", "acme/gone")
	kept := team("team_kept", "acme/kept")
	organization := testSource(t, store, "org_1")
	if _, err := store.UpsertDocument(ctx, testDocument(gone, "issue-1", strings.Repeat("b", 64)), nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}

	var retracted int
	removed, err := store.RemoveSourcesByTeam(ctx, "org_1", "team_gone", testClock,
		func(_ domain.Source, documents []domain.Document) ([]domain.Event, error) {
			retracted = len(documents)
			events := make([]domain.Event, 0, len(documents))
			for range documents {
				events = append(events, testEvent(t, "knowledge.document.removed.v1"))
			}
			return events, nil
		})
	if err != nil {
		t.Fatalf("RemoveSourcesByTeam() error = %v", err)
	}
	if len(removed) != 1 || removed[0].ID != gone.ID || retracted != 1 {
		t.Fatalf("RemoveSourcesByTeam() = %d sources, %d documents retracted; want %s and its one document", len(removed), retracted, gone.ID)
	}
	for _, untouched := range []domain.Source{kept, organization} {
		read, err := store.Source(ctx, "org_1", untouched.ID)
		if err != nil || read.Status != domain.StatusActive {
			t.Fatalf("source %s outside the deleted team = %s, %v; want it active", untouched.ID, read.Status, err)
		}
	}

	// A redelivered event finds nothing further.
	if again, err := store.RemoveSourcesByTeam(ctx, "org_1", "team_gone", testClock, nil); err != nil || len(again) != 0 {
		t.Fatalf("RemoveSourcesByTeam(again) = %d, %v, want none", len(again), err)
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
		SourceID: source.ID, Value: "page-2", Watermark: &watermark, Token: "start-token", WalkStartedAt: &walkStarted, UpdatedAt: testClock,
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
	// Token is watermark's counterpart for a provider whose position is an
	// opaque cursor rather than a moment — Drive's changes.startPageToken,
	// Gmail's historyId — and is written and read back the same way.
	if read.Token != "start-token" {
		t.Fatalf("Cursor() token = %q, want start-token", read.Token)
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
	run.DocumentsSeen, run.DocumentsPublished, run.DocumentsRemoved, run.DocumentsExcluded = 10, 3, 1, 4
	run.FinishedAt = &finished
	if err := store.FinishRun(ctx, run, nil); err != nil {
		t.Fatalf("FinishRun() error = %v", err)
	}

	runs, err := store.Runs(ctx, "org_1", source.ID, 10)
	if err != nil {
		t.Fatalf("Runs() error = %v", err)
	}
	if len(runs) != 1 || runs[0].Status != domain.RunCompleted || runs[0].DocumentsPublished != 3 || runs[0].DocumentsExcluded != 4 {
		t.Fatalf("Runs() = %+v", runs)
	}
}

func outboxCount(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.pool.QueryRow(context.Background(), `SELECT count(*) FROM knowledge_outbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestExcludeDocumentRetractsALiveDocumentOnceAndKeepsTheVerdict(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "m-1", strings.Repeat("a", 64))
	if _, err := store.UpsertDocument(ctx, document, nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}

	excluded := document
	excluded.ID = "doc_ignored"
	excluded.Excluded = "promotional mail"
	excluded.JudgedPolicy = 1
	excluded.Judgment = []byte(`{"jev": {"kind": "promotion"}}`)
	later := testClock.Add(time.Hour)
	retracted, err := store.ExcludeDocument(ctx, excluded, later, func(domain.Document) (domain.Event, error) {
		return testEvent(t, "knowledge.document.removed.v1"), nil
	})
	if err != nil || !retracted {
		t.Fatalf("ExcludeDocument(live) = %v, %v, want it retracted", retracted, err)
	}
	if got := outboxCount(t, store); got != 1 {
		t.Fatalf("outbox events = %d, want the one removal", got)
	}
	read, err := store.DocumentByExternalID(ctx, source.ID, "m-1")
	if err != nil {
		t.Fatalf("DocumentByExternalID() error = %v", err)
	}
	if read.ID != document.ID || read.RemovedAt == nil || read.Excluded != "promotional mail" || read.JudgedPolicy != 1 {
		t.Fatalf("read = %+v, want the same row removed with its verdict", read)
	}
	var judged map[string]map[string]string
	if err := json.Unmarshal(read.Judgment, &judged); err != nil || judged["jev"]["kind"] != "promotion" {
		t.Fatalf("judgment = %s, %v; want the judge's answer kept", read.Judgment, err)
	}
	full, err := store.Document(ctx, "org_1", document.ID)
	if err != nil || full.Body != "the body" {
		t.Fatalf("Document() = body %q, %v, want the text kept, for a member who later chooses its kind", full.Body, err)
	}

	// Excluded again, as a later pass would: nothing more to retract.
	retracted, err = store.ExcludeDocument(ctx, excluded, later.Add(time.Hour), func(domain.Document) (domain.Event, error) {
		return testEvent(t, "knowledge.document.removed.v1"), nil
	})
	if err != nil || retracted {
		t.Fatalf("ExcludeDocument(again) = %v, %v, want nothing retracted", retracted, err)
	}
	if got := outboxCount(t, store); got != 1 {
		t.Fatalf("outbox events = %d, want still the one removal", got)
	}
	again, _ := store.DocumentByExternalID(ctx, source.ID, "m-1")
	if !again.RemovedAt.Equal(*read.RemovedAt) {
		t.Fatalf("removed_at moved from %v to %v, want when it was first removed", read.RemovedAt, again.RemovedAt)
	}
}

func TestExcludeDocumentRecordsANewDocumentWithoutAnEvent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "m-2", strings.Repeat("c", 64))
	document.Excluded = "empty: nothing in it to read"
	document.JudgedPolicy = 1

	retracted, err := store.ExcludeDocument(ctx, document, testClock, func(domain.Document) (domain.Event, error) {
		return testEvent(t, "knowledge.document.removed.v1"), nil
	})
	if err != nil || retracted {
		t.Fatalf("ExcludeDocument(new) = %v, %v, want recorded, nothing to retract", retracted, err)
	}
	if got := outboxCount(t, store); got != 0 {
		t.Fatalf("outbox events = %d, want none: nobody was ever told about it", got)
	}
	read, err := store.DocumentByExternalID(ctx, source.ID, "m-2")
	if err != nil || read.RemovedAt == nil || read.Excluded == "" {
		t.Fatalf("read = %+v, %v, want the row kept, removed, with its reason", read, err)
	}
}

// A document excluded under an older policy and judged worth keeping under a
// newer one comes back: it is published, and its exclusion cleared.
func TestSourceHasNoChoiceOfKindsUntilItsMemberMakesOne(t *testing.T) {
	store := openTestStore(t)
	source := testSource(t, store, "org_1")
	read, err := store.Source(context.Background(), "org_1", source.ID)
	if err != nil {
		t.Fatalf("Source() error = %v", err)
	}
	if read.IncludeKinds != nil {
		t.Fatalf("IncludeKinds = %v, want nil: the default choice", read.IncludeKinds)
	}
}

func TestUpsertDocumentPublishesAnExcludedDocumentJudgedWorthKeeping(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "m-3", strings.Repeat("d", 64))
	excluded := document
	excluded.Excluded, excluded.JudgedPolicy = "looked like a newsletter", 1
	if _, err := store.ExcludeDocument(ctx, excluded, testClock, nil); err != nil {
		t.Fatalf("ExcludeDocument() error = %v", err)
	}

	document.JudgedPolicy = 2
	published, err := store.UpsertDocument(ctx, document, func(domain.Document) (domain.Event, error) {
		return testEvent(t, "knowledge.document.published.v1"), nil
	})
	if err != nil || !published {
		t.Fatalf("UpsertDocument() = %v, %v, want it published again", published, err)
	}
	read, _ := store.DocumentByExternalID(ctx, source.ID, "m-3")
	if read.RemovedAt != nil || read.Excluded != "" || read.JudgedPolicy != 2 {
		t.Fatalf("read = %+v, want it live, no longer excluded, under the newer policy", read)
	}
}

func TestRetractUnseenLeavesExcludedDocumentsAlone(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	excluded := testDocument(source, "m-4", strings.Repeat("e", 64))
	excluded.Excluded, excluded.JudgedPolicy = "promotional mail", 1
	if _, err := store.ExcludeDocument(ctx, excluded, testClock, nil); err != nil {
		t.Fatalf("ExcludeDocument() error = %v", err)
	}
	later := testClock.Add(time.Hour)
	retracted, err := store.RetractUnseen(ctx, source.ID, later, later, func(domain.Document) (domain.Event, error) {
		return testEvent(t, "knowledge.document.removed.v1"), nil
	})
	if err != nil || retracted != 0 {
		t.Fatalf("RetractUnseen() = %d, %v, want nothing: an excluded document is already out", retracted, err)
	}
}

func TestTouchDocumentKeepsADocumentSeenThroughAFullPass(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	document := testDocument(source, "pdf-1", strings.Repeat("f", 64))
	if _, err := store.UpsertDocument(ctx, document, nil); err != nil {
		t.Fatalf("UpsertDocument() error = %v", err)
	}
	walkStarted := testClock.Add(24 * time.Hour)
	if err := store.TouchDocument(ctx, source.ID, "pdf-1", walkStarted.Add(time.Minute)); err != nil {
		t.Fatalf("TouchDocument() error = %v", err)
	}
	retracted, err := store.RetractUnseen(ctx, source.ID, walkStarted, walkStarted.Add(time.Hour), nil)
	if err != nil || retracted != 0 {
		t.Fatalf("RetractUnseen() = %d, %v, want nothing: the pass saw it", retracted, err)
	}
}

// TestHastenSourcesBringsForwardOnlyWhatItShould pins the three rules that
// make a trigger safe to act on: it moves this connection's live sources, it
// never delays one already due, and it never reads a provider again inside
// the floor.
func TestHastenSourcesBringsForwardOnlyWhatItShould(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	later := testClock.Add(time.Hour)
	// Due later, never swept: the one a trigger is for.
	waiting := testSource(t, store, "org_1")
	waiting.SyncAt = &later
	mustSchedule(t, store, waiting)

	// Due sooner already: a trigger must not push it back.
	sooner := testClock.Add(time.Minute)
	overdue := testSource(t, store, "org_1")
	overdue.ConnectionID = waiting.ConnectionID
	overdue.SyncAt = &sooner
	mustSchedule(t, store, overdue)

	// Another connection's, which this trigger says nothing about.
	elsewhere := testSource(t, store, "org_1")
	elsewhere.SyncAt = &later
	mustSchedule(t, store, elsewhere)

	moved, err := store.HastenSources(ctx, waiting.ConnectionID, testClock, 2*time.Minute)
	if err != nil {
		t.Fatalf("HastenSources() error = %v", err)
	}
	if moved != 1 {
		t.Fatalf("moved %d sources, want only the one due later on this connection", moved)
	}
	if due := readSyncAt(t, store, waiting.ID); due == nil || !due.Equal(testClock) {
		t.Fatalf("the waiting source is due at %v, want now", due)
	}
	if due := readSyncAt(t, store, overdue.ID); due == nil || !due.Equal(sooner) {
		t.Fatalf("a source already due sooner was moved to %v", due)
	}
	if due := readSyncAt(t, store, elsewhere.ID); due == nil || !due.Equal(later) {
		t.Fatalf("another connection's source was moved to %v", due)
	}
}

// TestHastenSourcesDefersOneSweptWithinTheFloor: inside the floor a source
// is brought forward to the floor, not to now and not left an interval away.
func TestHastenSourcesDefersOneSweptWithinTheFloor(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	later := testClock.Add(time.Hour)
	justSwept := testClock.Add(-30 * time.Second)
	source := testSource(t, store, "org_1")
	source.SyncAt = &later
	source.LastSyncedAt = &justSwept
	mustSchedule(t, store, source)

	moved, err := store.HastenSources(ctx, source.ConnectionID, testClock, 2*time.Minute)
	if err != nil {
		t.Fatalf("HastenSources() error = %v", err)
	}
	if moved != 1 {
		t.Fatalf("moved %d sources swept within the floor, want 1", moved)
	}
	if due := readSyncAt(t, store, source.ID); due == nil || !due.Equal(justSwept.Add(2*time.Minute)) {
		t.Fatalf("due at %v, want the floor after the last sweep", due)
	}

	// The same delivery again changes nothing: it is already that soon.
	if moved, err = store.HastenSources(ctx, source.ConnectionID, testClock, 2*time.Minute); err != nil || moved != 0 {
		t.Fatalf("HastenSources(again) = %d, %v; want nothing moved", moved, err)
	}

	// Past the floor, a delivery brings it to now.
	if moved, err = store.HastenSources(ctx, source.ConnectionID, testClock, 10*time.Second); err != nil || moved != 1 {
		t.Fatalf("HastenSources(past the floor) = %d, %v; want 1", moved, err)
	}
	if due := readSyncAt(t, store, source.ID); due == nil || !due.Equal(testClock) {
		t.Fatalf("due at %v, want now", due)
	}
}

// TestHastenSourcesLeavesAFailingSourceToItsBackoff: last_synced_at is a
// failing source's last success, however long ago, so the floor alone would
// let every delivery pull it forward into another refused read.
func TestHastenSourcesLeavesAFailingSourceToItsBackoff(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	backedOff := testClock.Add(5 * time.Hour)
	longAgo := testClock.Add(-10 * time.Hour)
	source := testSource(t, store, "org_1")
	source.SyncAt = &backedOff
	source.LastSyncedAt = &longAgo
	mustSchedule(t, store, source)
	if _, err := store.pool.Exec(ctx, `UPDATE knowledge_sources SET attempts = 3 WHERE id = $1`, source.ID); err != nil {
		t.Fatal(err)
	}

	moved, err := store.HastenSources(ctx, source.ConnectionID, testClock, 2*time.Minute)
	if err != nil {
		t.Fatalf("HastenSources() error = %v", err)
	}
	if moved != 0 {
		t.Fatalf("moved %d failing sources, want none", moved)
	}
	if due := readSyncAt(t, store, source.ID); due == nil || !due.Equal(backedOff) {
		t.Fatalf("a failing source is due at %v, want its backoff kept", due)
	}
}

// mustSchedule writes a source's schedule directly, since nothing in the
// store's own API sets sync_at and last_synced_at together.
func mustSchedule(t *testing.T, store *Store, source domain.Source) {
	t.Helper()
	if _, err := store.pool.Exec(context.Background(),
		`UPDATE knowledge_sources SET sync_at = $2, last_synced_at = $3 WHERE id = $1`,
		source.ID, source.SyncAt, source.LastSyncedAt); err != nil {
		t.Fatalf("schedule %s: %v", source.ID, err)
	}
}

func readSyncAt(t *testing.T, store *Store, sourceID string) *time.Time {
	t.Helper()
	var due *time.Time
	if err := store.pool.QueryRow(context.Background(),
		`SELECT sync_at FROM knowledge_sources WHERE id = $1`, sourceID).Scan(&due); err != nil {
		t.Fatalf("read sync_at for %s: %v", sourceID, err)
	}
	if due != nil {
		utc := due.UTC()
		return &utc
	}
	return nil
}

// TestHastenSourceMovesOneSourceWhoeverAsks is the member-facing half: a
// person has a reason the schedule does not know, so unlike a trigger this
// does not care when the source was last read.
func TestHastenSourceMovesOneSourceWhoeverAsks(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	later := testClock.Add(time.Hour)
	justSwept := testClock.Add(-10 * time.Second)
	asked := testSource(t, store, "org_1")
	asked.SyncAt = &later
	asked.LastSyncedAt = &justSwept
	mustSchedule(t, store, asked)

	other := testSource(t, store, "org_1")
	other.SyncAt = &later
	mustSchedule(t, store, other)

	moved, err := store.HastenSource(ctx, "org_1", asked.ID, testClock)
	if err != nil || !moved {
		t.Fatalf("HastenSource() = %v, %v, want moved", moved, err)
	}
	if due := readSyncAt(t, store, asked.ID); due == nil || !due.Equal(testClock) {
		t.Fatalf("the asked source is due at %v, want now", due)
	}
	if due := readSyncAt(t, store, other.ID); due == nil || !due.Equal(later) {
		t.Fatalf("another source was moved to %v", due)
	}

	// Asking again changes nothing: it is already due.
	if moved, err = store.HastenSource(ctx, "org_1", asked.ID, testClock); err != nil || moved {
		t.Fatalf("HastenSource(already due) = %v, %v, want unmoved", moved, err)
	}
}

func TestHastenSourceLeavesWhatIsNotItsToMove(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	later := testClock.Add(time.Hour)
	source := testSource(t, store, "org_1")
	source.SyncAt = &later
	mustSchedule(t, store, source)

	// Another organization's, which is not this caller's to hurry.
	if moved, err := store.HastenSource(ctx, "org_2", source.ID, testClock); err != nil || moved {
		t.Fatalf("HastenSource(wrong organization) = %v, %v, want unmoved", moved, err)
	}
	if due := readSyncAt(t, store, source.ID); due == nil || !due.Equal(later) {
		t.Fatalf("another organization moved a source to %v", due)
	}

	// One that is not active has something wrong with it that hurrying does
	// not fix.
	if err := store.RemoveSource(ctx, "org_1", source.ID, testClock,
		func(domain.Source, []domain.Document) ([]domain.Event, error) { return nil, nil }); err != nil {
		t.Fatalf("RemoveSource() error = %v", err)
	}
	if moved, err := store.HastenSource(ctx, "org_1", source.ID, testClock); err != nil || moved {
		t.Fatalf("HastenSource(removed) = %v, %v, want unmoved", moved, err)
	}
}

func TestSetSourceKindsMarksTheSourceToDecideAgain(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")
	now := testClock.Add(-time.Minute)

	changed, err := store.SetSourceKinds(ctx, "org_1", source.ID, []string{"conversation", "event_update"}, now)
	if err != nil || !changed {
		t.Fatalf("SetSourceKinds() = %t, %v", changed, err)
	}
	read, err := store.Source(ctx, "org_1", source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(read.IncludeKinds, []string{"conversation", "event_update"}) || !read.KindsPending ||
		read.SyncAt == nil || read.SyncAt.After(now) {
		t.Fatalf("source = kinds %v, pending %t, sync at %v", read.IncludeKinds, read.KindsPending, read.SyncAt)
	}
	if changed, err := store.SetSourceKinds(ctx, "org_2", source.ID, nil, now); err != nil || changed {
		t.Fatalf("SetSourceKinds(another organization) = %t, %v; want nothing changed", changed, err)
	}
	if _, err := store.SetSourceKinds(ctx, "org_1", source.ID, nil, now); err != nil {
		t.Fatal(err)
	}
	if read, _ := store.Source(ctx, "org_1", source.ID); read.IncludeKinds != nil {
		t.Fatalf("IncludeKinds = %v, want NULL: the default", read.IncludeKinds)
	}

	due := testClock.Add(time.Hour)
	if _, err := store.ClaimDueSources(ctx, due, 10, "lease-a", due.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearKindsPending(ctx, source.ID, "lease-b"); err != nil {
		t.Fatal(err)
	}
	if read, _ := store.Source(ctx, "org_1", source.ID); !read.KindsPending {
		t.Fatal("a run not holding the lease cleared the pending choice")
	}
	if err := store.ClearKindsPending(ctx, source.ID, "lease-a"); err != nil {
		t.Fatal(err)
	}
	if read, _ := store.Source(ctx, "org_1", source.ID); read.KindsPending {
		t.Fatal("the run holding the lease did not clear the pending choice")
	}
}

func TestDocumentsToRedecideFindsWhatAJudgeDecided(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	source := testSource(t, store, "org_1")

	live := testDocument(source, "live", strings.Repeat("1", 64))
	live.Judgment = []byte(`{"jev":{"kind":"conversation"}}`)
	excluded := testDocument(source, "excluded", strings.Repeat("2", 64))
	excluded.Judgment, excluded.Excluded, excluded.Body = []byte(`{"jev":{"kind":"newsletter"}}`), "newsletter", "ten links"
	unjudged := testDocument(source, "unjudged", strings.Repeat("3", 64))
	gone := testDocument(source, "gone", strings.Repeat("4", 64))
	gone.Judgment = []byte(`{"jev":{"kind":"conversation"}}`)
	for _, document := range []domain.Document{live, unjudged, gone} {
		if _, err := store.UpsertDocument(ctx, document, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ExcludeDocument(ctx, excluded, testClock, nil); err != nil {
		t.Fatal(err)
	}
	// Retracted because its source no longer holds it: nothing to decide.
	if _, _, err := store.RetractDocument(ctx, source.ID, "gone", testClock, nil); err != nil {
		t.Fatal(err)
	}

	var found []domain.Document
	after := ""
	for {
		page, err := store.DocumentsToRedecide(ctx, source.ID, after, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		found = append(found, page...)
		after = page[len(page)-1].ID
	}
	if len(found) != 2 {
		t.Fatalf("found %d documents, want the judged live one and the excluded one", len(found))
	}
	for _, document := range found {
		if document.Judgment == nil || document.Body == "" {
			t.Fatalf("document %s = judgment %s, body %q; want both, to decide it again", document.ExternalID, document.Judgment, document.Body)
		}
	}
}
