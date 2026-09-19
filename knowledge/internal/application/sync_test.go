package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// testServiceWithOriginals is testService with a bucket configured, for a
// test that exercises keepOriginal.
func testServiceWithOriginals(t *testing.T, store *fakeStore, extractor *fakeExtractor, originals *fakeOriginals) *Service {
	t.Helper()
	registry := sources.Registry{}
	if extractor != nil {
		registry[extractor.provider] = extractor
	}
	return New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: domain.OrgMembership{OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember}},
		Grants:     &fakeGrants{token: "member-token"},
		Extractors: registry,
		Originals:  originals,
		Clock:      func() time.Time { return testClock },
	}, Config{OriginalsBucket: "test-bucket"})
}

// pdfItem is an item whose MIME is a passthrough type: transform never reads
// text out of one, so it is only ever kept, or skipped, never normalized.
func pdfItem(externalID string, content []byte, updated time.Time) sources.Item {
	return sources.Item{
		ExternalID: externalID, Kind: knowledgev1.KindFile, Title: "Report",
		MIME: transform.MIMEPDF, Content: content, UpdatedAt: &updated,
	}
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// theOnlyDocument is the one row a test put there, for an assertion that does
// not care about its map key.
func theOnlyDocument(t *testing.T, store *fakeStore) domain.Document {
	t.Helper()
	if len(store.documents) != 1 {
		t.Fatalf("documents = %d, want exactly 1", len(store.documents))
	}
	for _, document := range store.documents {
		return document
	}
	panic("unreachable")
}

// testService wires the service to fakes and a frozen clock.
func testService(t *testing.T, store *fakeStore, extractor *fakeExtractor) (*Service, *fakeGrants) {
	t.Helper()
	return testServiceWithClock(t, store, extractor, func() time.Time { return testClock })
}

// testServiceWithClock is testService with a clock the caller controls, for a
// test that must tell two runs apart in time rather than see the same frozen
// instant from both.
func testServiceWithClock(t *testing.T, store *fakeStore, extractor *fakeExtractor, clock func() time.Time) (*Service, *fakeGrants) {
	t.Helper()
	grants := &fakeGrants{token: "member-token"}
	registry := sources.Registry{}
	if extractor != nil {
		registry[extractor.provider] = extractor
	}
	service := New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: domain.OrgMembership{OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember}},
		Grants:     grants,
		Extractors: registry,
		Clock:      clock,
	}, Config{})
	return service, grants
}

// advancingClock moves forward by a second on every call, for a test where
// two calls to Sync must be told apart in time — unlike testClock, which
// never moves.
func advancingClock(start time.Time) func() time.Time {
	next := start
	return func() time.Time {
		now := next
		next = next.Add(time.Second)
		return now
	}
}

// activeSource is a source already registered and claimed under a lease.
// FullSweepAt is set to now, as it would be right after a source's first
// pass drains, so a test does not accidentally exercise the periodic sweep
// without asking for it; a test of that overrides FullSweepAt itself.
func activeSource(store *fakeStore, provider string) domain.Source {
	swept := testClock
	source := domain.Source{
		ID: "src_1", OrganizationID: "org_1",
		Provider: provider, Target: map[string]string{"team_id": "team_1"},
		Scope: domain.ScopeOrganization, CreatedBy: "user-1", Status: domain.StatusActive,
		Interval: time.Hour, CreatedAt: testClock, FullSweepAt: &swept,
	}
	store.sources[source.ID] = source
	store.lease[source.ID] = "lease-a"
	return source
}

func item(externalID, body string, updated time.Time) sources.Item {
	return sources.Item{
		ExternalID: externalID, Kind: knowledgev1.KindIssue, Title: "Something",
		MIME: transform.MIMEMarkdown, Content: []byte(body), UpdatedAt: &updated,
	}
}

func TestSyncPublishesOnlyWhatChanged(t *testing.T) {
	store := newFakeStore()
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("1", "first", updated), item("2", "second", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 2 {
		t.Fatalf("published %d documents, want 2", got)
	}

	// The same content again publishes nothing, however many times it is read.
	extractor.pages = append(extractor.pages, sources.Page{
		Items: []sources.Item{item("1", "first", updated), item("2", "second", updated)}, Watermark: &updated,
	})
	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 2 {
		t.Fatalf("published %d documents after an unchanged pass, want still 2", got)
	}
}

func TestSyncKeepsThePlaceButNotTheWatermarkWhenAWalkStopsEarly(t *testing.T) {
	// A provider that answers newest first puts the newest change on page one.
	// Persisting that mid-walk would make the next run ask only for things
	// after it, skipping everything this run had not reached.
	store := newFakeStore()
	newest := testClock.Add(-time.Minute)
	extractor := &fakeExtractor{
		provider: "files",
		pages: []sources.Page{
			{Items: []sources.Item{item("1", "newest", newest)}, Cursor: "page-2", Watermark: &newest},
		},
		errs: []error{nil, domain.ErrProviderUnavailable},
	}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	cursor := store.cursors[source.ID]
	if cursor.Value != "page-2" {
		t.Fatalf("cursor value = %q, want page-2 so the next run resumes where this stopped", cursor.Value)
	}
	if cursor.Watermark != nil {
		t.Fatalf("cursor watermark = %v, want none: the window this pass opened never closed", cursor.Watermark)
	}
}

func TestSyncAdvancesTheWatermarkOnceTheWalkFinishes(t *testing.T) {
	store := newFakeStore()
	newest := testClock.Add(-time.Minute)
	older := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("1", "newest", newest)}, Cursor: "page-2", Watermark: &newest},
		{Items: []sources.Item{item("2", "older", older)}, Cursor: "", Watermark: &older},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	cursor := store.cursors[source.ID]
	if cursor.Watermark == nil || !cursor.Watermark.Equal(newest) {
		t.Fatalf("cursor watermark = %v, want the newest change the whole pass saw (%v)", cursor.Watermark, newest)
	}
	if cursor.Value != "" {
		t.Fatalf("cursor value = %q, want empty: the source was drained", cursor.Value)
	}
}

func TestSyncStopsWhenAnotherWorkerTookTheSource(t *testing.T) {
	store := newFakeStore()
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("1", "first", updated)}, Cursor: "page-2", Watermark: &updated},
		{Items: []sources.Item{item("2", "second", updated)}, Cursor: "", Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")
	store.lease[source.ID] = "someone-else"

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(extractor.calls) != 1 {
		t.Fatalf("fetched %d pages, want 1: the run must stop once it has lost the source", len(extractor.calls))
	}
	if run := store.lastRun(); run.Error != knowledgev1.SyncErrorLeaseLost {
		t.Fatalf("run error = %q, want %q", run.Error, knowledgev1.SyncErrorLeaseLost)
	}
	// Nothing about the source may be touched: another replica owns it now,
	// and writing its cursor would move that replica's place in the walk.
	if store.failedReason != "" {
		t.Fatalf("failed the source with %q, want nothing: it is not this run's to schedule", store.failedReason)
	}
	if _, written := store.cursors[source.ID]; written {
		t.Fatal("wrote a cursor for a source another worker owns")
	}
}

func TestSyncRetractsWhatAFullPassDidNotFind(t *testing.T) {
	store := newFakeStore()
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("1", "kept", updated), item("2", "goes away", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if store.retractUnseen != 1 {
		t.Fatalf("retracted unseen documents %d times, want 1 on a backfill", store.retractUnseen)
	}
	if run := store.lastRun(); run.Kind != knowledgev1.SyncBackfill {
		t.Fatalf("run kind = %q, want %q for a source never read through", run.Kind, knowledgev1.SyncBackfill)
	}
}

func TestSyncNeverRetractsOnAnIncrementalPass(t *testing.T) {
	// An incremental pass asks only for what changed, so it has not looked at
	// everything and cannot say what is missing.
	store := newFakeStore()
	updated := testClock.Add(-time.Hour)
	watermark := testClock.Add(-2 * time.Hour)
	store.cursors["src_1"] = domain.Cursor{SourceID: "src_1", Watermark: &watermark}
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("1", "changed", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if store.retractUnseen != 0 {
		t.Fatalf("retracted unseen documents %d times, want 0 on an incremental pass", store.retractUnseen)
	}
	if run := store.lastRun(); run.Kind != knowledgev1.SyncIncremental {
		t.Fatalf("run kind = %q, want %q", run.Kind, knowledgev1.SyncIncremental)
	}
	if !extractor.calls[0].Full {
		return
	}
	t.Fatal("an incremental pass asked the extractor for everything")
}

func TestSyncRetractsAfterASweptSourceComesDueAgain(t *testing.T) {
	// A source that finished its first pass long ago is read incrementally
	// from then on. Once its full sweep interval elapses, a pass starts over
	// from the beginning and can once again say what the source no longer
	// holds, rather than staying incremental — and silent about deletions —
	// forever.
	store := newFakeStore()
	watermark := testClock.Add(-30 * 24 * time.Hour)
	store.cursors["src_1"] = domain.Cursor{SourceID: "src_1", Watermark: &watermark}
	gone := domain.Document{
		ID: "doc_gone", OrganizationID: "org_1", SourceID: "src_1", ExternalID: "2",
		ContentHash: "unchanged", LastSeenAt: testClock.Add(-2 * time.Hour),
	}
	store.documents["src_1\x00"+gone.ExternalID] = gone

	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("1", "kept", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")
	stale := testClock.Add(-8 * 24 * time.Hour) // older than the default weekly interval
	source.FullSweepAt = &stale
	store.sources[source.ID] = source

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if run := store.lastRun(); run.Kind != knowledgev1.SyncBackfill {
		t.Fatalf("run kind = %q, want %q: the sweep had come due", run.Kind, knowledgev1.SyncBackfill)
	}
	if request := extractor.calls[0]; !request.Full || request.Cursor.Value != "" || request.Cursor.Watermark != nil {
		t.Fatalf("request = %+v, want a full pass starting from the beginning", request)
	}
	if removed := store.documents["src_1\x00"+gone.ExternalID]; removed.RemovedAt == nil {
		t.Fatal("the document missing from this pass's page was not retracted")
	}
	if got := store.published(knowledgev1.DocumentRemovedDetailType); got != 1 {
		t.Fatalf("published %d removal events, want 1", got)
	}
	swept := store.sources[source.ID].FullSweepAt
	if swept == nil || !swept.Equal(testClock) {
		t.Fatalf("full_sweep_at = %v, want now (%v): the sweep just completed", swept, testClock)
	}
}

func TestSyncReadsAsTheMemberWhoRegisteredTheSource(t *testing.T) {
	// Internal storage has no service identity to accept, so every page is
	// read with the token the registering member's grant mints, never as the
	// service itself.
	store := newFakeStore()
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{{}}}
	service, grants := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(grants.members) != 1 || grants.members[0] != "org_1/user-1" {
		t.Fatalf("grants minted for %v, want only the registering member org_1/user-1", grants.members)
	}
	if len(extractor.calls) != 1 || extractor.calls[0].Credential.AccessToken != "member-token" {
		t.Fatalf("extractor calls = %+v, want one read with the member's token", extractor.calls)
	}
}

func TestSyncReadsAProviderSourceThroughItsComposioAccountNeverAGrant(t *testing.T) {
	store := newFakeStore()
	extractor := &fakeExtractor{provider: "github", pages: []sources.Page{{}}}
	service, grants := testService(t, store, extractor)
	source := activeSource(store, "github")
	source.ConnectionID = "apc_1"
	source.ComposioAccountID = "ca_1"
	store.sources[source.ID] = source

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(grants.members) != 0 {
		t.Fatalf("grants was minted for %v, want none: a Composio account never reads through a grant", grants.members)
	}
	if len(extractor.calls) != 1 {
		t.Fatalf("fetched %d pages, want 1", len(extractor.calls))
	}
	if got := extractor.calls[0].Credential.Metadata["composio_account_id"]; got != "ca_1" {
		t.Fatalf("composio_account_id = %q, want ca_1", got)
	}
	if extractor.calls[0].Credential.AccessToken != "" {
		t.Fatal("a Composio credential must never carry an access token: the account is proxied through, not held")
	}
}

func TestSyncPausesEveryProviderSourceOnAConnectionThatNeedsReconnecting(t *testing.T) {
	// A run that discovers its connection needs attention should not wait for
	// the apps.connection.expired.v1 event to say so: every other source on
	// the same connection is paused right away.
	store := newFakeStore()
	failing := &fakeExtractor{provider: "github", errs: []error{domain.ErrNeedsReconnect}}
	service, _ := testService(t, store, failing)
	source := activeSource(store, "github")
	source.ConnectionID = "apc_1"
	source.ComposioAccountID = "ca_1"
	store.sources[source.ID] = source

	sibling := source
	sibling.ID = "src_2"
	store.sources[sibling.ID] = sibling

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got := store.sources[source.ID].Status; got != domain.StatusPaused {
		t.Fatalf("source status = %s, want paused", got)
	}
	if got := store.sources[sibling.ID].Status; got != domain.StatusPaused {
		t.Fatalf("sibling source status = %s, want paused: it reads through the same connection", got)
	}
	if store.failedReason != "" {
		t.Fatalf("failed the source with %q, want nothing: it was paused, not backed off", store.failedReason)
	}
}

func TestSyncFailsWithoutAGrantToReadAs(t *testing.T) {
	store := newFakeStore()
	extractor := &fakeExtractor{provider: "files"}
	service := New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: domain.OrgMembership{OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember}},
		Extractors: sources.Registry{"files": extractor},
		Clock:      func() time.Time { return testClock },
	}, Config{})
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(extractor.calls) != 0 {
		t.Fatal("nothing may be read without a member's grant to read as")
	}
	if store.failedReason != knowledgev1.SyncErrorForbidden {
		t.Fatalf("failed with %q, want %q", store.failedReason, knowledgev1.SyncErrorForbidden)
	}
	if run := store.lastRun(); run.Retryable {
		t.Fatal("a missing grant is not something running again unchanged would fix")
	}
}

func TestSyncBacksOffAProviderThatIsDown(t *testing.T) {
	store := newFakeStore()
	extractor := &fakeExtractor{provider: "files", errs: []error{domain.ErrProviderUnavailable}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if store.sources[source.ID].Status != domain.StatusActive {
		t.Fatal("a provider being down must not pause the source")
	}
	if store.failedReason != knowledgev1.SyncErrorProviderUnavailable {
		t.Fatalf("failed with %q, want %q", store.failedReason, knowledgev1.SyncErrorProviderUnavailable)
	}
	if run := store.lastRun(); !run.Retryable {
		t.Fatal("a provider being down is worth trying again")
	}
}

func TestSyncSkipsAnItemThatHoldsNoText(t *testing.T) {
	store := newFakeStore()
	updated := testClock.Add(-time.Hour)
	unreadable := item("1", "binary", updated)
	unreadable.MIME = "application/pdf"
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{unreadable, item("2", "readable", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 1 {
		t.Fatalf("published %d documents, want 1: the unreadable one is skipped, not fatal", got)
	}
	if run := store.lastRun(); run.Status != domain.RunCompleted || run.DocumentsSeen != 2 {
		t.Fatalf("run = %+v, want a completed pass that saw both", run)
	}
}

func TestSyncComesStraightBackWhenAPassStoppedEarly(t *testing.T) {
	store := newFakeStore()
	updated := testClock.Add(-time.Hour)
	pages := make([]sources.Page, 0, pagesPerRun)
	for range pagesPerRun {
		pages = append(pages, sources.Page{
			Items: []sources.Item{item("1", "same", updated)}, Cursor: "more", Watermark: &updated,
		})
	}
	extractor := &fakeExtractor{provider: "files", pages: pages}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if len(extractor.calls) != pagesPerRun {
		t.Fatalf("fetched %d pages, want the per-run bound of %d", len(extractor.calls), pagesPerRun)
	}
	if !store.finishedNext.Equal(testClock) {
		t.Fatalf("next run at %v, want now (%v): there is more of this source to read",
			store.finishedNext, testClock)
	}
}

func TestSyncRetractsOnlyWhatNeitherRunOfAWalkSaw(t *testing.T) {
	// A source whose first full pass outgrows one run's page budget spans
	// several runs before it drains. Retracting against the run that
	// happens to finish the walk, rather than against when the walk itself
	// began, would forget what an earlier run of that very walk already
	// read — the bug the periodic sweep makes far more likely to bite, since
	// a large source now revisits this every week instead of only once.
	store := newFakeStore()
	updated := testClock.Add(-time.Hour)
	firstRunPages := make([]sources.Page, 0, pagesPerRun)
	for range pagesPerRun {
		firstRunPages = append(firstRunPages, sources.Page{
			Items: []sources.Item{item("1", "seen by the first run", updated)}, Cursor: "more", Watermark: &updated,
		})
	}
	secondRunPage := sources.Page{Items: []sources.Item{item("2", "seen by the second run", updated)}, Watermark: &updated}
	extractor := &fakeExtractor{provider: "files", pages: append(firstRunPages, secondRunPage)}
	// The clock must move between the two runs, or nothing distinguishes
	// "the walk's start" from "this run's start" and the bug cannot show up.
	service, _ := testServiceWithClock(t, store, extractor, advancingClock(testClock))
	source := activeSource(store, "files")

	// A document from an earlier walk that this one genuinely never finds:
	// gone at the provider, unlike "1" and "2" which the walk still holds.
	gone := domain.Document{
		ID: "doc_gone", OrganizationID: "org_1", SourceID: source.ID, ExternalID: "3",
		ContentHash: "unchanged", LastSeenAt: testClock.Add(-2 * time.Hour),
	}
	store.documents[source.ID+"\x00"+gone.ExternalID] = gone

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() [first run] error = %v", err)
	}
	if len(extractor.calls) != pagesPerRun {
		t.Fatalf("fetched %d pages in the first run, want the per-run bound of %d", len(extractor.calls), pagesPerRun)
	}
	if store.retractUnseen != 0 {
		t.Fatalf("retracted unseen documents %d times after the first run, want 0: the walk has not drained", store.retractUnseen)
	}

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() [second run] error = %v", err)
	}

	if store.retractUnseen != 1 {
		t.Fatalf("retracted unseen documents %d times, want 1 once the walk drains", store.retractUnseen)
	}
	if removed := store.documents[source.ID+"\x00"+gone.ExternalID]; removed.RemovedAt == nil {
		t.Fatal("the document neither run of the walk saw was not retracted")
	}
	if kept := store.documents[source.ID+"\x001"]; kept.RemovedAt != nil {
		t.Fatal("a document the walk's first run read was wrongly retracted against the second run's own start")
	}
	if kept := store.documents[source.ID+"\x002"]; kept.RemovedAt != nil {
		t.Fatal("a document the walk's second run read was wrongly retracted")
	}
}

func TestSyncWaitsAnIntervalOnceASourceIsDrained(t *testing.T) {
	store := newFakeStore()
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("1", "only", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if want := testClock.Add(source.Interval); !store.finishedNext.Equal(want) {
		t.Fatalf("next run at %v, want %v", store.finishedNext, want)
	}
}

func TestSyncLeavesTheSourceAloneWhenTheProcessIsGoingAway(t *testing.T) {
	store := newFakeStore()
	extractor := &fakeExtractor{provider: "files", errs: []error{context.Canceled}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	err := service.Sync(context.Background(), source, "lease-a")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync() error = %v, want context.Canceled", err)
	}
	if store.failedReason != "" {
		t.Fatalf("failed the source with %q, want nothing: the source did not fail, the process did", store.failedReason)
	}
}

func TestSyncRefusesASourceThatIsNotActive(t *testing.T) {
	store := newFakeStore()
	service, _ := testService(t, store, &fakeExtractor{provider: "files"})
	source := activeSource(store, "files")
	source.Status = domain.StatusPaused

	if err := service.Sync(context.Background(), source, "lease-a"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Sync(paused) error = %v, want ErrConflict", err)
	}
}

// Page.Removed lets an incremental feed that already knows a document is
// gone — Drive's changes, Gmail's history — say so immediately, rather than
// waiting for the next full sweep to notice the same thing the slow way.

func TestSyncRetractsImmediatelyWhatAPagesOwnFeedSaysIsGone(t *testing.T) {
	store := newFakeStore()
	watermark := testClock.Add(-2 * time.Hour)
	store.cursors["src_1"] = domain.Cursor{SourceID: "src_1", Watermark: &watermark}
	store.documents["src_1\x00gone-1"] = domain.Document{
		ID: "doc_gone", OrganizationID: "org_1", SourceID: "src_1", ExternalID: "gone-1",
		ContentHash: "unchanged", LastSeenAt: testClock.Add(-90 * time.Minute),
	}
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("1", "kept", updated)}, Watermark: &updated, Removed: []string{"gone-1", "already-gone"}},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if run := store.lastRun(); run.Kind != knowledgev1.SyncIncremental {
		t.Fatalf("run kind = %q, want incremental: Page.Removed must not itself force a full pass's own retraction", run.Kind)
	}
	if removed := store.documents["src_1\x00gone-1"]; removed.RemovedAt == nil {
		t.Fatal("a document the page's own feed said was gone was not retracted")
	}
	if got := store.published(knowledgev1.DocumentRemovedDetailType); got != 1 {
		t.Fatalf("published %d removal events, want 1: the second id names nothing live", got)
	}
	if store.retractUnseen != 0 {
		t.Fatalf("retracted unseen documents %d times, want 0: an incremental pass never calls RetractUnseen", store.retractUnseen)
	}
	if run := store.lastRun(); run.DocumentsRemoved != 1 {
		t.Fatalf("run.DocumentsRemoved = %d, want 1", run.DocumentsRemoved)
	}
}

// Token is Watermark's counterpart for a provider whose position is an
// opaque cursor rather than a moment — Drive's changes.startPageToken,
// Gmail's historyId — and follows exactly the same lifecycle: read back to
// start the next incremental pass, and moved only once a pass drains.

func TestSyncTreatsATokenLikeAWatermarkForDecidingAFullPass(t *testing.T) {
	store := newFakeStore()
	store.cursors["src_1"] = domain.Cursor{SourceID: "src_1", Token: "some-opaque-token"}
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("1", "changed", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if run := store.lastRun(); run.Kind != knowledgev1.SyncIncremental {
		t.Fatalf("run kind = %q, want incremental: a source with a token has been read through before, the same as one with a watermark", run.Kind)
	}
}

func TestSyncKeepsThePlaceButNotTheTokenWhenAWalkStopsEarly(t *testing.T) {
	store := newFakeStore()
	extractor := &fakeExtractor{
		provider: "files",
		pages:    []sources.Page{{Cursor: "page-2", Token: "tok-mid"}},
		errs:     []error{nil, domain.ErrProviderUnavailable},
	}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	cursor := store.cursors[source.ID]
	if cursor.Value != "page-2" {
		t.Fatalf("cursor value = %q, want page-2", cursor.Value)
	}
	if cursor.Token != "" {
		t.Fatalf("cursor token = %q, want none: the pass has not drained", cursor.Token)
	}
}

func TestSyncPersistsATokenOnceTheWalkFinishes(t *testing.T) {
	store := newFakeStore()
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Cursor: "page-2", Token: "tok-mid"},
		{Cursor: "", Token: "tok-final"},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	cursor := store.cursors[source.ID]
	if cursor.Token != "tok-final" {
		t.Fatalf("cursor token = %q, want the final page's token, not one from mid-walk", cursor.Token)
	}
	if cursor.Value != "" {
		t.Fatalf("cursor value = %q, want empty: the source was drained", cursor.Value)
	}
}

// A document whose bytes knowledge does not read itself — a PDF, an Office
// file — is kept in the configured bucket instead of being skipped, once
// there is one.

func TestSyncStoresAPassthroughDocumentAsAnOriginalRatherThanNormalizingIt(t *testing.T) {
	store := newFakeStore()
	fakeBucket := newFakeOriginals()
	updated := testClock.Add(-time.Hour)
	content := []byte("%PDF-1.4 fake bytes")
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{pdfItem("pdf-1", content, updated)}, Watermark: &updated},
	}}
	service := testServiceWithOriginals(t, store, extractor, fakeBucket)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 1 {
		t.Fatalf("published %d documents, want 1", got)
	}
	document := theOnlyDocument(t, store)
	if document.Original == nil {
		t.Fatal("the document was published without an Original")
	}
	if document.Original.Bucket != "test-bucket" {
		t.Fatalf("Original.Bucket = %q, want the configured bucket", document.Original.Bucket)
	}
	if !fakeBucket.has(document.Original.Key) {
		t.Fatal("the bucket does not hold the object the document names")
	}
	if document.Body != "" {
		t.Fatalf("Body = %q, want empty: the bytes live in the bucket, not the row", document.Body)
	}
	if document.MIME != transform.MIMEPDF {
		t.Fatalf("MIME = %q, want the real content type", document.MIME)
	}
	if want := sha256Hex(content); document.ContentHash != want {
		t.Fatalf("ContentHash = %q, want sha256 of the bytes (%q)", document.ContentHash, want)
	}
	// What a consumer is told, not only what the row holds: the event is the
	// only place rag learns where the bytes are.
	events := store.publishedDocuments(t)
	if len(events) != 1 || events[0].Original == nil {
		t.Fatalf("published events = %+v, want one carrying the Original", events)
	}
	want := knowledgev1.Original{Bucket: "test-bucket", Key: document.Original.Key, Size: int64(len(content)), SHA256: sha256Hex(content)}
	if got := *events[0].Original; got != want {
		t.Fatalf("published Original = %+v, want %+v", got, want)
	}
}

func TestSyncSkipsAPassthroughDocumentWithoutABucketConfigured(t *testing.T) {
	store := newFakeStore()
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{pdfItem("pdf-1", []byte("%PDF-1.4"), updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 0 {
		t.Fatalf("published %d documents, want 0: nothing here can keep a PDF's bytes without a bucket", got)
	}
}

func TestSyncSkipsReuploadingAnOriginalWhoseContentHasNotChanged(t *testing.T) {
	store := newFakeStore()
	fakeBucket := newFakeOriginals()
	updated := testClock.Add(-time.Hour)
	content := []byte("same bytes both times")
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{pdfItem("pdf-1", content, updated)}, Watermark: &updated},
		{Items: []sources.Item{pdfItem("pdf-1", content, updated)}, Watermark: &updated},
	}}
	service := testServiceWithOriginals(t, store, extractor, fakeBucket)
	source := activeSource(store, "files")

	for range 2 {
		if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 1 {
		t.Fatalf("published %d documents, want 1: the second pass changed nothing", got)
	}
	if len(fakeBucket.puts) != 1 {
		t.Fatalf("uploaded %d times, want 1: a weekly full sweep over an unchanged file must not re-upload it", len(fakeBucket.puts))
	}
}

func TestSyncReplacesTheOldOriginalWhenContentChanges(t *testing.T) {
	store := newFakeStore()
	fakeBucket := newFakeOriginals()
	first := testClock.Add(-2 * time.Hour)
	second := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{pdfItem("pdf-1", []byte("version one"), first)}, Watermark: &first},
		{Items: []sources.Item{pdfItem("pdf-1", []byte("version two"), second)}, Watermark: &second},
	}}
	service := testServiceWithOriginals(t, store, extractor, fakeBucket)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() [first] error = %v", err)
	}
	firstKey := theOnlyDocument(t, store).Original.Key
	if !fakeBucket.has(firstKey) {
		t.Fatal("the first version's object is missing")
	}

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() [second] error = %v", err)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 2 {
		t.Fatalf("published %d documents, want 2: one per version", got)
	}
	secondKey := theOnlyDocument(t, store).Original.Key
	if secondKey == firstKey {
		t.Fatal("the second version was written under the same content-addressed key as the first")
	}
	if !fakeBucket.has(secondKey) {
		t.Fatal("the second version's object is missing")
	}
	if fakeBucket.has(firstKey) {
		t.Fatal("the superseded object was not deleted from the bucket")
	}
}

func TestSyncDeletesAnOriginalWhenAFullSweepRetractsItsDocument(t *testing.T) {
	store := newFakeStore()
	fakeBucket := newFakeOriginals()
	key := "originals/org_1/src_1/doc_gone/somehash"
	store.documents["src_1\x00gone-1"] = domain.Document{
		ID: "doc_gone", OrganizationID: "org_1", SourceID: "src_1", ExternalID: "gone-1",
		ContentHash: "somehash", LastSeenAt: testClock.Add(-2 * time.Hour),
		Original: &domain.Original{Bucket: "test-bucket", Key: key, SHA256: "somehash"},
	}
	fakeBucket.objects[key] = []byte("stale bytes")
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("kept-1", "kept", updated)}, Watermark: &updated},
	}}
	service := testServiceWithOriginals(t, store, extractor, fakeBucket)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if run := store.lastRun(); run.Kind != knowledgev1.SyncBackfill {
		t.Fatalf("run kind = %q, want a backfill: only a full pass may retract what it did not see", run.Kind)
	}
	if fakeBucket.has(key) {
		t.Fatal("a retracted document's original was not deleted from the bucket")
	}
}

func TestSyncDeletesAnOriginalWhenItsDocumentIsRemovedThroughPageRemoved(t *testing.T) {
	store := newFakeStore()
	fakeBucket := newFakeOriginals()
	watermark := testClock.Add(-2 * time.Hour)
	store.cursors["src_1"] = domain.Cursor{SourceID: "src_1", Watermark: &watermark}
	key := "originals/org_1/src_1/doc_gone/somehash"
	store.documents["src_1\x00gone-1"] = domain.Document{
		ID: "doc_gone", OrganizationID: "org_1", SourceID: "src_1", ExternalID: "gone-1",
		ContentHash: "somehash", LastSeenAt: testClock.Add(-90 * time.Minute),
		Original: &domain.Original{Bucket: "test-bucket", Key: key, SHA256: "somehash"},
	}
	fakeBucket.objects[key] = []byte("stale bytes")
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "files", pages: []sources.Page{
		{Items: []sources.Item{item("kept-1", "kept", updated)}, Watermark: &updated, Removed: []string{"gone-1"}},
	}}
	service := testServiceWithOriginals(t, store, extractor, fakeBucket)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if fakeBucket.has(key) {
		t.Fatal("a document retracted through Page.Removed did not have its original deleted")
	}
}

func TestSyncDeletesAnOriginalWhenItsSourceIsRemoved(t *testing.T) {
	store := newFakeStore()
	fakeBucket := newFakeOriginals()
	key := "originals/org_1/src_1/doc_gone/somehash"
	source := activeSource(store, "files")
	store.documents["src_1\x00gone-1"] = domain.Document{
		ID: "doc_gone", OrganizationID: "org_1", SourceID: source.ID, ExternalID: "gone-1",
		ContentHash: "somehash",
		Original:    &domain.Original{Bucket: "test-bucket", Key: key, SHA256: "somehash"},
	}
	fakeBucket.objects[key] = []byte("stale bytes")
	service := testServiceWithOriginals(t, store, &fakeExtractor{provider: "files"}, fakeBucket)

	if err := service.RemoveSource(context.Background(),
		authclient.Credentials{Authorization: "Bearer t"}, "org_1", source.ID); err != nil {
		t.Fatalf("RemoveSource() error = %v", err)
	}
	if fakeBucket.has(key) {
		t.Fatal("removing a source did not delete its documents' originals from the bucket")
	}
}
