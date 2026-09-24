package application

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// judgedService is a service with a judge, and a bucket when originals is
// set. A nil clock is testClock.
func judgedService(t *testing.T, store *fakeStore, extractor *fakeExtractor, judge Judge, originals *fakeOriginals, clock func() time.Time) *Service {
	t.Helper()
	registry := sources.Registry{}
	if extractor != nil {
		registry[extractor.provider] = extractor
	}
	config := Config{}
	var bucket Originals
	if originals != nil {
		bucket = originals
		config.OriginalsBucket = "test-bucket"
	}
	if clock == nil {
		clock = func() time.Time { return testClock }
	}
	return New(Dependencies{
		Store:      store,
		Directory:  &fakeDirectory{membership: domain.OrgMembership{OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember}},
		Grants:     &fakeGrants{token: "member-token"},
		Extractors: registry,
		Originals:  bucket,
		Judge:      judge,
		Clock:      clock,
	}, config)
}

func titled(externalID, title, body string) sources.Item {
	updated := testClock.Add(-time.Hour)
	return sources.Item{
		ExternalID: externalID, Kind: knowledgev1.KindMessage, Title: title,
		MIME: transform.MIMEPlain, Content: []byte(body), UpdatedAt: &updated,
	}
}

func onePage(provider string, items ...sources.Item) *fakeExtractor {
	watermark := testClock.Add(-time.Hour)
	return &fakeExtractor{provider: provider, pages: []sources.Page{{Items: items, Watermark: &watermark}}}
}

func documentNamed(t *testing.T, store *fakeStore, externalID string) domain.Document {
	t.Helper()
	document, ok := store.documents["src_1\x00"+externalID]
	if !ok {
		t.Fatalf("no document %q", externalID)
	}
	return document
}

func completedRuns(t *testing.T, store *fakeStore) []knowledgev1.SyncCompleted {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	var completed []knowledgev1.SyncCompleted
	for _, event := range store.events {
		if event.DetailType != knowledgev1.SyncCompletedDetailType {
			continue
		}
		var run knowledgev1.SyncCompleted
		if err := json.Unmarshal(event.Data, &run); err != nil {
			t.Fatalf("decode a completed run: %v", err)
		}
		completed = append(completed, run)
	}
	return completed
}

func TestClassificationKeepsProviderMetadataAndDetectsLabelChanges(t *testing.T) {
	provider := map[string]string{"labels": "INBOX"}
	classified := withCategory(domain.Document{Metadata: provider}, &domain.Verdict{Category: "financial_record"})
	if classified.Metadata[categoryMetadataKey] != "financial_record" || provider[categoryMetadataKey] != "" {
		t.Fatalf("classification = %v, provider metadata = %v", classified.Metadata, provider)
	}
	if !sourceMetadataEqual(classified.Metadata, provider) {
		t.Fatal("the generated category made unchanged provider metadata look different")
	}
	if sourceMetadataEqual(classified.Metadata, map[string]string{"labels": "IMPORTANT"}) {
		t.Fatal("a changed Gmail label was treated as unchanged")
	}
}

func TestSyncExcludesWhatTheJudgeCallsNoiseAndPublishesTheRest(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdicts: map[string]domain.Verdict{
		"50% off everything": {Keep: false, Reason: "promotional mail"},
	}}
	extractor := onePage("gmail",
		titled("m1", "50% off everything", "Shop now, sale ends Sunday."),
		titled("m2", "Launch plan", "We ship the beta on the 3rd; Priya owns the rollout."),
	)
	service := judgedService(t, store, extractor, judge, nil, nil)
	source := activeSource(store, "gmail")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 1 {
		t.Fatalf("published %d documents, want only the one worth keeping", got)
	}
	promotion := documentNamed(t, store, "m1")
	if promotion.RemovedAt == nil || promotion.Excluded != "promotional mail" {
		t.Fatalf("promotion = removed %v, excluded %q; want it kept out with the judge's reason", promotion.RemovedAt, promotion.Excluded)
	}
	if promotion.JudgedPolicy != qualityPolicy {
		t.Fatalf("JudgedPolicy = %d, want %d", promotion.JudgedPolicy, qualityPolicy)
	}
	if plan := documentNamed(t, store, "m2"); plan.RemovedAt != nil || plan.JudgedPolicy != qualityPolicy {
		t.Fatalf("plan = removed %v, policy %d; want it live and judged", plan.RemovedAt, plan.JudgedPolicy)
	}
	if run := store.lastRun(); run.DocumentsExcluded != 1 || run.DocumentsPublished != 1 {
		t.Fatalf("run = %+v, want 1 published and 1 excluded", run)
	}
	if completed := completedRuns(t, store); len(completed) != 1 || completed[0].DocumentsExcluded != 1 {
		t.Fatalf("completed = %+v, want the event to count the exclusion", completed)
	}
}

// A document published before there was a judge, or under an older policy,
// is judged the next time a pass reads it, and retracted if it is noise.
func TestSyncRetractsALiveDocumentJudgedNoise(t *testing.T) {
	store := newFakeStore()
	body := "Someone liked your post."
	normalized, err := transform.Normalize([]byte(body), transform.MIMEPlain)
	if err != nil {
		t.Fatal(err)
	}
	store.documents["src_1\x00m1"] = domain.Document{
		ID: "doc_1", OrganizationID: "org_1", SourceID: "src_1", ExternalID: "m1", Title: "New like",
		ContentHash: normalized.Hash, Body: normalized.Body, LastSeenAt: testClock.Add(-time.Hour),
	}
	judge := &fakeJudge{verdicts: map[string]domain.Verdict{"New like": {Keep: false, Reason: "social notification"}}}
	service := judgedService(t, store, onePage("gmail", titled("m1", "New like", body)), judge, nil, nil)
	source := activeSource(store, "gmail")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if judge.called() != 1 {
		t.Fatalf("judge called %d times, want once: an unjudged document is judged even unchanged", judge.called())
	}
	if reasons := store.publishedReasons(knowledgev1.DocumentRemovedDetailType); len(reasons) != 1 || reasons[0] != knowledgev1.RemovedReasonExcluded {
		t.Fatalf("removals = %v, want one saying the document was excluded", reasons)
	}
	if document := documentNamed(t, store, "m1"); document.RemovedAt == nil || document.ID != "doc_1" {
		t.Fatalf("document = %+v, want the same row removed", document)
	}
}

func TestSyncDoesNotJudgeTheSameContentTwice(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdicts: map[string]domain.Verdict{"Promo": {Keep: false, Reason: "promotional"}}}
	later := testClock.Add(time.Hour)
	now := testClock
	items := []sources.Item{titled("m1", "Promo", "Buy now."), titled("m2", "Notes", "Decided to hire two engineers.")}
	watermark := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "gmail", pages: []sources.Page{
		{Items: items, Watermark: &watermark},
		{Items: items, Watermark: &watermark},
	}}
	service := judgedService(t, store, extractor, judge, nil, func() time.Time { return now })
	source := activeSource(store, "gmail")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() [first] error = %v", err)
	}
	now = later
	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() [second] error = %v", err)
	}
	if judge.called() != 2 {
		t.Fatalf("judge called %d times, want 2: once per document, never again for the same content", judge.called())
	}
	if kept := documentNamed(t, store, "m2"); !kept.LastSeenAt.Equal(later) {
		t.Fatalf("LastSeenAt = %v, want the second run to have noted it saw the document", kept.LastSeenAt)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 1 {
		t.Fatalf("published %d, want 1: the second run changed nothing", got)
	}
}

func TestSyncExcludesEmptyTextWithoutAskingTheJudge(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{}
	blank := sources.Item{ExternalID: "sheet-1", Kind: knowledgev1.KindFile, Title: "Untitled spreadsheet",
		MIME: transform.MIMECSV, Content: []byte(",,,\n,,,\n")}
	service := judgedService(t, store, onePage("googledrive", blank), judge, nil, nil)
	source := activeSource(store, "googledrive")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if judge.called() != 0 {
		t.Fatalf("judge called %d times, want none: nothing in it to read", judge.called())
	}
	if document := documentNamed(t, store, "sheet-1"); document.RemovedAt == nil || document.Excluded == "" {
		t.Fatalf("document = removed %v, excluded %q; want it kept out", document.RemovedAt, document.Excluded)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 0 {
		t.Fatalf("published %d, want none", got)
	}
}

func TestSyncNeverJudgesTheMembersOwnFiles(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdicts: map[string]domain.Verdict{"Upload": {Keep: false, Reason: "would be noise"}}}
	service := judgedService(t, store, onePage("files", titled("f1", "Upload", "Anything at all.")), judge, nil, nil)
	source := activeSource(store, "files")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if judge.called() != 0 {
		t.Fatalf("judge called %d times, want none: a member's own upload is theirs to choose", judge.called())
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 1 {
		t.Fatalf("published %d, want the upload", got)
	}
}

func TestSyncStopsWhenTheQualityCheckIsUnavailable(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{err: fmt.Errorf("vertex answered 503: %w", domain.ErrQualityCheckUnavailable)}
	service := judgedService(t, store, onePage("gmail", titled("m1", "Launch plan", "Ship on the 3rd.")), judge, nil, nil)
	source := activeSource(store, "gmail")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v, want the failure recorded on the run instead", err)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 0 {
		t.Fatalf("published %d, want nothing published unjudged", got)
	}
	if len(store.documents) != 0 {
		t.Fatalf("documents = %d, want none written: the page is read again whole", len(store.documents))
	}
	if store.failedReason != knowledgev1.SyncErrorQualityCheckUnavailable {
		t.Fatalf("failed reason = %q, want %q", store.failedReason, knowledgev1.SyncErrorQualityCheckUnavailable)
	}
	if run := store.lastRun(); run.Status != domain.RunFailed || !run.Retryable {
		t.Fatalf("run = %+v, want a failed, retryable run", run)
	}
}

func TestSyncShowsTheJudgeAPDFsOwnBytes(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{}
	content := []byte("%PDF-1.4 quarterly report")
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "googledrive", pages: []sources.Page{
		{Items: []sources.Item{pdfItem("pdf-1", content, updated)}, Watermark: &updated},
	}}
	service := judgedService(t, store, extractor, judge, newFakeOriginals(), nil)
	source := activeSource(store, "googledrive")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if judge.called() != 1 {
		t.Fatalf("judge called %d times, want once", judge.called())
	}
	shown := judge.calls[0]
	if string(shown.Original) != string(content) || shown.Text != "" || shown.MIME != transform.MIMEPDF {
		t.Fatalf("candidate = MIME %q, %d original bytes, text %q; want the PDF itself", shown.MIME, len(shown.Original), shown.Text)
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 1 {
		t.Fatalf("published %d, want the PDF", got)
	}
}

// A full sweep that reads an original again, unchanged, must note that it saw
// it: otherwise the sweep's own retraction of what it did not see takes the
// document down every week, and the next sweep puts it back.
func TestFullSweepDoesNotRetractAnUnchangedOriginal(t *testing.T) {
	store := newFakeStore()
	fakeBucket := newFakeOriginals()
	content := []byte("%PDF-1.4 the same every week")
	first := testClock
	second := testClock.Add(8 * 24 * time.Hour)
	now := first
	updated := testClock.Add(-time.Hour)
	extractor := &fakeExtractor{provider: "googledrive", pages: []sources.Page{
		{Items: []sources.Item{pdfItem("pdf-1", content, updated)}, Watermark: &updated},
		{Items: []sources.Item{pdfItem("pdf-1", content, updated)}, Watermark: &updated},
	}}
	service := judgedService(t, store, extractor, nil, fakeBucket, func() time.Time { return now })
	source := activeSource(store, "googledrive")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() [first] error = %v", err)
	}
	// Past the full-sweep interval since the last sweep: the second pass reads
	// the whole source again and retracts what it does not see.
	now = second
	swept := first
	source.FullSweepAt = &swept
	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() [second] error = %v", err)
	}
	if kind := extractor.calls[1].Full; !kind {
		t.Fatal("the second pass was not a full sweep; the test would prove nothing")
	}
	if document := theOnlyDocument(t, store); document.RemovedAt != nil {
		t.Fatal("the full sweep retracted an original it had just read, unchanged")
	}
	if got := store.published(knowledgev1.DocumentRemovedDetailType); got != 0 {
		t.Fatalf("removals = %d, want none", got)
	}
	if len(fakeBucket.puts) != 1 {
		t.Fatalf("uploaded %d times, want 1: the same bytes are not written again", len(fakeBucket.puts))
	}
}

// What the judge answered is kept with the document, kept or excluded, and an
// excluded document keeps its text: a member who later chooses to index its
// kind can have it without anything being read or judged again.
func TestSyncKeepsTheJudgmentAndAnExcludedDocumentsText(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdicts: map[string]domain.Verdict{
		"Workshop update": {Keep: false, Reason: "event update", Judgment: []byte(`{"jev":{"kind":"event_update"}}`)},
		"Launch plan":     {Keep: true, Reason: "conversation", Judgment: []byte(`{"jev":{"kind":"conversation"}}`)},
	}}
	extractor := onePage("gmail",
		titled("m1", "Workshop update", "The venue moved to Hall B."),
		titled("m2", "Launch plan", "We ship the beta on the 3rd."),
	)
	service := judgedService(t, store, extractor, judge, nil, nil)
	source := activeSource(store, "gmail")

	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	update := documentNamed(t, store, "m1")
	if string(update.Judgment) != `{"jev":{"kind":"event_update"}}` || update.Body == "" || update.RemovedAt == nil {
		t.Fatalf("excluded = judgment %s, body %q; want both kept with it removed", update.Judgment, update.Body)
	}
	if plan := documentNamed(t, store, "m2"); string(plan.Judgment) != `{"jev":{"kind":"conversation"}}` {
		t.Fatalf("kept judgment = %s", plan.Judgment)
	}
}

// A GitHub issue's comment count and state, a Gmail message's labels: what a
// provider says about a document moves while what it says does not. The
// verdict it was given stands, drawn again from its kept judgment instead of
// paying the judge for the same content twice, and what moved is written
// but not published: nothing a consumer indexes changed.
func TestAMetadataOnlyChangeIsNotJudgedOrPublishedAgain(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdicts: map[string]domain.Verdict{
		"Fix login":        {Keep: true, Reason: "work material", Category: "work_material", Judgment: []byte("work_material")},
		"Fix login, again": {Keep: true, Reason: "work material", Category: "work_material", Judgment: []byte("work_material")},
		"Summer sale":      {Keep: false, Reason: "promotion", Category: "promotion", Judgment: []byte("promotion")},
	}}
	updated := testClock.Add(-time.Hour)
	issue := func(title, comments string) sources.Item {
		return sources.Item{
			ExternalID: "42", Kind: knowledgev1.KindIssue, Title: title,
			URL: "https://github.com/acme/app/issues/42", MIME: transform.MIMEMarkdown,
			Content:  []byte("Login fails when the session cookie expires."),
			Metadata: map[string]string{"state": "open", "number": "42", "comments": comments}, UpdatedAt: &updated,
		}
	}
	promotion := func(labels string) sources.Item {
		return sources.Item{
			ExternalID: "m1", Kind: knowledgev1.KindMessage, Title: "Summer sale", MIME: transform.MIMEPlain,
			Content: []byte("Everything is 40% off."), Metadata: map[string]string{"labels": labels}, UpdatedAt: &updated,
		}
	}
	page := func(items ...sources.Item) sources.Page { return sources.Page{Items: items, Watermark: &updated} }
	extractor := &fakeExtractor{provider: "github", pages: []sources.Page{
		page(issue("Fix login", "1"), promotion("INBOX")),
		page(issue("Fix login", "2"), promotion("INBOX, CATEGORY_PROMOTIONS")),
		page(issue("Fix login", "2"), promotion("INBOX, CATEGORY_PROMOTIONS")),
		page(issue("Fix login", "3"), promotion("CATEGORY_PROMOTIONS")),
	}}
	now := testClock
	service := judgedService(t, store, extractor, judge, nil, func() time.Time { return now })
	source := activeSource(store, "github")
	for pass := 1; pass <= 4; pass++ {
		now = testClock.Add(time.Duration(pass) * time.Hour)
		if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	if judge.called() != 2 {
		t.Fatalf("judge called %d times, want once for each document: only their metadata moved", judge.called())
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 1 {
		t.Fatalf("published %d, want only the issue's first sight", got)
	}
	kept := documentNamed(t, store, "42")
	if kept.RemovedAt != nil || kept.Metadata["comments"] != "3" || kept.Metadata[categoryMetadataKey] != "work_material" ||
		!kept.LastSeenAt.Equal(now) {
		t.Fatalf("issue = %+v, want it live with the latest comment count and its category", kept)
	}
	excluded := documentNamed(t, store, "m1")
	if excluded.RemovedAt == nil || excluded.Metadata["labels"] != "CATEGORY_PROMOTIONS" {
		t.Fatalf("promotion = %+v, want it still excluded, with its latest labels", excluded)
	}
	if got := store.published(knowledgev1.DocumentRemovedDetailType); got != 0 {
		t.Fatalf("removals = %d, want none: the promotion was never live", got)
	}

	// A new title is not metadata: the judge reads it, so it is asked again.
	extractor.pages = append(extractor.pages, page(issue("Fix login, again", "3"), promotion("CATEGORY_PROMOTIONS")))
	now = now.Add(time.Hour)
	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatal(err)
	}
	if judge.called() != 3 || judge.calls[2].Title != "Fix login, again" {
		t.Fatalf("judge calls = %d, want the retitled issue judged again", judge.called())
	}
}
