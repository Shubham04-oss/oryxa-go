package application

import (
	"context"
	"errors"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// testService wires the service to fakes and a frozen clock.
func testService(t *testing.T, store *fakeStore, extractor *fakeExtractor) (*Service, *fakeConnections) {
	t.Helper()
	connections := &fakeConnections{
		connection: domain.Connection{ID: "con_1", OrganizationID: "org_1", AppID: "app_1", Provider: "github", Scope: "organization", Status: "active"},
		credential: domain.Credential{AccessToken: "provider-token"},
	}
	registry := sources.Registry{}
	if extractor != nil {
		registry[extractor.provider] = extractor
	}
	service := New(Dependencies{
		Store:       store,
		Directory:   &fakeDirectory{membership: domain.OrgMembership{OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember}},
		Connections: connections,
		Extractors:  registry,
		Clock:       func() time.Time { return testClock },
	}, Config{})
	return service, connections
}

// activeSource is a source already registered and claimed under a lease.
func activeSource(store *fakeStore, provider string) domain.Source {
	source := domain.Source{
		ID: "src_1", OrganizationID: "org_1", ConnectionID: "con_1", AppID: "app_1",
		Provider: provider, Target: map[string]string{"repository": "acme/api"},
		Scope: domain.ScopeOrganization, CreatedBy: "user-1", Status: domain.StatusActive,
		Interval: time.Hour, CreatedAt: testClock,
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
	extractor := &fakeExtractor{provider: "github", pages: []sources.Page{
		{Items: []sources.Item{item("1", "first", updated), item("2", "second", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")

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
		provider: "github",
		pages: []sources.Page{
			{Items: []sources.Item{item("1", "newest", newest)}, Cursor: "page-2", Watermark: &newest},
		},
		errs: []error{nil, domain.ErrProviderUnavailable},
	}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")

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
	extractor := &fakeExtractor{provider: "github", pages: []sources.Page{
		{Items: []sources.Item{item("1", "newest", newest)}, Cursor: "page-2", Watermark: &newest},
		{Items: []sources.Item{item("2", "older", older)}, Cursor: "", Watermark: &older},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")

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
	extractor := &fakeExtractor{provider: "github", pages: []sources.Page{
		{Items: []sources.Item{item("1", "first", updated)}, Cursor: "page-2", Watermark: &updated},
		{Items: []sources.Item{item("2", "second", updated)}, Cursor: "", Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")
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
	extractor := &fakeExtractor{provider: "github", pages: []sources.Page{
		{Items: []sources.Item{item("1", "kept", updated), item("2", "goes away", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")

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
	extractor := &fakeExtractor{provider: "github", pages: []sources.Page{
		{Items: []sources.Item{item("1", "changed", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")

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

func TestSyncPausesASourceWhoseCredentialMustBeReconnected(t *testing.T) {
	store := newFakeStore()
	extractor := &fakeExtractor{provider: "github", errs: []error{domain.ErrNeedsReconnect}}
	service, connections := testService(t, store, extractor)
	source := activeSource(store, "github")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if store.sources[source.ID].Status != domain.StatusPaused {
		t.Fatalf("source status = %s, want paused", store.sources[source.ID].Status)
	}
	if got := store.published(knowledgev1.SourcePausedDetailType); got != 1 {
		t.Fatalf("published %d pause events, want 1", got)
	}
	if len(connections.forgotten) != 1 {
		t.Fatal("a paused source must not keep using the credential it held")
	}
	if run := store.lastRun(); run.Retryable {
		t.Fatal("a reconnect is not something running again unchanged would fix")
	}
}

func TestSyncBacksOffAProviderThatIsDown(t *testing.T) {
	store := newFakeStore()
	extractor := &fakeExtractor{provider: "github", errs: []error{domain.ErrProviderUnavailable}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")

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
	extractor := &fakeExtractor{provider: "github", pages: []sources.Page{
		{Items: []sources.Item{unreadable, item("2", "readable", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")

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
	extractor := &fakeExtractor{provider: "github", pages: pages}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")

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

func TestSyncWaitsAnIntervalOnceASourceIsDrained(t *testing.T) {
	store := newFakeStore()
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "github", pages: []sources.Page{
		{Items: []sources.Item{item("1", "only", updated)}, Watermark: &updated},
	}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if want := testClock.Add(source.Interval); !store.finishedNext.Equal(want) {
		t.Fatalf("next run at %v, want %v", store.finishedNext, want)
	}
}

func TestSyncLeavesTheSourceAloneWhenTheProcessIsGoingAway(t *testing.T) {
	store := newFakeStore()
	extractor := &fakeExtractor{provider: "github", errs: []error{context.Canceled}}
	service, _ := testService(t, store, extractor)
	source := activeSource(store, "github")

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
	service, _ := testService(t, store, &fakeExtractor{provider: "github"})
	source := activeSource(store, "github")
	source.Status = domain.StatusPaused

	if err := service.Sync(context.Background(), source, "lease-a"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Sync(paused) error = %v, want ErrConflict", err)
	}
}
