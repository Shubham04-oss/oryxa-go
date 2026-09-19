package application

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

func memberCredentials() authclient.Credentials {
	return authclient.Credentials{Authorization: "Bearer member-token"}
}

func TestSetSourceKindsRecordsTheChoiceAndBringsTheSweepForward(t *testing.T) {
	store := newFakeStore()
	service := judgedService(t, store, nil, &fakeJudge{}, nil, nil)
	source := activeSource(store, "gmail")
	later := testClock.Add(time.Hour)
	source.SyncAt = &later
	store.sources[source.ID] = source

	updated, err := service.SetSourceKinds(context.Background(), memberCredentials(), "org_1", source.ID,
		[]string{"event_update", "conversation", "event_update"})
	if err != nil {
		t.Fatalf("SetSourceKinds() error = %v", err)
	}
	if !slices.Equal(updated.IncludeKinds, []string{"conversation", "event_update"}) {
		t.Fatalf("IncludeKinds = %v, want the choice once each, in the order kinds are shown", updated.IncludeKinds)
	}
	if !updated.KindsPending || updated.SyncAt == nil || updated.SyncAt.After(testClock) {
		t.Fatalf("source = pending %t, sync at %v; want its documents decided again, now", updated.KindsPending, updated.SyncAt)
	}

	back, err := service.SetSourceKinds(context.Background(), memberCredentials(), "org_1", source.ID, nil)
	if err != nil || back.IncludeKinds != nil {
		t.Fatalf("SetSourceKinds(nil) = %v, %v; want the default back", back.IncludeKinds, err)
	}
}

func TestSetSourceKindsRefusesWhatItCannotRecord(t *testing.T) {
	store := newFakeStore()
	judged := judgedService(t, store, nil, &fakeJudge{}, nil, nil)
	source := activeSource(store, "gmail")
	for name, include := range map[string][]string{
		"a kind nobody can choose": {"conversation", "spam"},
		"nothing at all":           {},
	} {
		if _, err := judged.SetSourceKinds(context.Background(), memberCredentials(), "org_1", source.ID, include); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s: error = %v, want ErrInvalid", name, err)
		}
	}

	unjudged := judgedService(t, store, nil, nil, nil, nil)
	if _, err := unjudged.SetSourceKinds(context.Background(), memberCredentials(), "org_1", source.ID, []string{"conversation"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("without a judge: error = %v, want ErrConflict: there are no kinds to choose", err)
	}

	files := activeSource(newFakeStore(), "files")
	filesStore := newFakeStore()
	filesStore.sources[files.ID], filesStore.lease[files.ID] = files, "lease-a"
	if _, err := judgedService(t, filesStore, nil, &fakeJudge{}, nil, nil).SetSourceKinds(context.Background(), memberCredentials(), "org_1", files.ID, []string{"conversation"}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a member's own files: error = %v, want ErrConflict: they are never judged", err)
	}

	stranger := activeSource(store, "gmail")
	stranger.CreatedBy = "user-2"
	store.sources[stranger.ID] = stranger
	if _, err := judged.SetSourceKinds(context.Background(), memberCredentials(), "org_1", stranger.ID, []string{"conversation"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("someone else's source: error = %v, want ErrNotFound", err)
	}
}

func TestAnAgentCannotChangeWhatASourceIndexes(t *testing.T) {
	store := newFakeStore()
	service := New(Dependencies{
		Store: store,
		Directory: &fakeDirectory{
			membership: domain.OrgMembership{OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember},
			agentToken: "agent-token",
		},
		Judge: &fakeJudge{}, Clock: func() time.Time { return testClock },
	}, Config{})
	source := activeSource(store, "gmail")
	agent := authclient.Credentials{Authorization: "Bearer agent-token"}
	if _, err := service.SetSourceKinds(context.Background(), agent, "org_1", source.ID, []string{"conversation"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
}

// A member who chooses to index event updates, and not newsletters, has what
// the source already holds decided again from the kept judgments: nothing is
// read from the app again, and the judge is asked only about what its kept
// answer cannot settle.
func TestSyncDecidesWhatTheSourceHoldsAgainForANewChoice(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdicts: map[string]domain.Verdict{"Hard to say": {Keep: true, Reason: "conversation", Judgment: []byte("conversation")}}}
	watermark := testClock.Add(-2 * time.Hour)
	extractor := &fakeExtractor{provider: "gmail", pages: []sources.Page{{Watermark: &watermark}}}
	service := judgedService(t, store, extractor, judge, nil, nil)
	source := activeSource(store, "gmail")
	store.cursors[source.ID] = domain.Cursor{SourceID: source.ID, Watermark: &watermark}
	removed := testClock.Add(-time.Hour)
	for _, document := range []domain.Document{
		{ID: "doc_a", Title: "Weekly digest", Judgment: []byte("newsletter"), Body: "Ten links."},
		{ID: "doc_b", Title: "Venue moved", Judgment: []byte("event_update"), Body: "Now in Hall B.", RemovedAt: &removed, Excluded: "event update"},
		{ID: "doc_c", Title: "Hard to say", Judgment: []byte("unsure"), Body: "Some words.", RemovedAt: &removed, Excluded: "unsure"},
		{ID: "doc_d", Title: "Launch plan", Judgment: []byte("conversation"), Body: "Ship on the 3rd."},
	} {
		document.OrganizationID, document.SourceID, document.ExternalID = "org_1", source.ID, document.ID
		document.LastSeenAt = testClock.Add(-time.Hour)
		store.documents[source.ID+"\x00"+document.ExternalID] = document
	}

	source, err := service.SetSourceKinds(context.Background(), memberCredentials(), "org_1", source.ID, []string{"conversation", "event_update"})
	if err != nil {
		t.Fatalf("SetSourceKinds() error = %v", err)
	}
	if err := service.Sync(context.Background(), source, "lease-a"); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	if digest := documentNamed(t, store, "doc_a"); digest.RemovedAt == nil || digest.Excluded == "" {
		t.Fatalf("digest = removed %v, excluded %q; want it retracted now newsletters are not indexed", digest.RemovedAt, digest.Excluded)
	}
	if venue := documentNamed(t, store, "doc_b"); venue.RemovedAt != nil || venue.Body != "Now in Hall B." {
		t.Fatalf("venue = removed %v, body %q; want it published from its kept text", venue.RemovedAt, venue.Body)
	}
	if unsure := documentNamed(t, store, "doc_c"); unsure.RemovedAt != nil {
		t.Fatal("a document the kept judgment could not settle was not judged again")
	}
	if judge.called() != 1 || judge.calls[0].Text != "Some words." || !slices.Equal(judge.calls[0].Include, []string{"conversation", "event_update"}) {
		t.Fatalf("judge calls = %+v, want one, about the unsettled document's kept text, for the new choice", judge.calls)
	}
	if plan := documentNamed(t, store, "doc_d"); plan.RemovedAt != nil {
		t.Fatal("a conversation, still chosen, was retracted")
	}
	if got := store.published(knowledgev1.DocumentPublishedDetailType); got != 2 {
		t.Fatalf("published %d, want the venue and the unsettled one", got)
	}
	if reasons := store.publishedReasons(knowledgev1.DocumentRemovedDetailType); len(reasons) != 1 || reasons[0] != knowledgev1.RemovedReasonExcluded {
		t.Fatalf("removals = %v, want the digest, excluded", reasons)
	}
	if store.sources[source.ID].KindsPending {
		t.Fatal("the source still waits to have its documents decided again")
	}
	if run := store.lastRun(); run.DocumentsPublished != 2 || run.DocumentsExcluded != 1 {
		t.Fatalf("run = %+v, want the decisions counted", run)
	}
}
