package postgres

import (
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/knowledgebase"
)

// An ingest's outcome is recorded only for the version of the document it
// worked from. A change that lands while the ingest is under way puts the
// document back in the queue, and marking it ready, failed or submitted
// afterwards would leave the change never indexed.
func TestIngestOutcomesAreNotRecordedOverAChangedDocument(t *testing.T) {
	store, ctx := openTestStore(t)
	corpus := testCorpus(t, store, ctx, uniqueOrganization(), titanSpec())
	body := "An invoice for the September service."
	source := domain.Source{Kind: domain.SourceExternal, Ref: "https://example.com/changed-mid-ingest"}
	initial := newDocument(corpus, source, "Invoice", body)
	initial.Metadata = domain.Metadata{"provider": "gmail", "category": "financial_record"}
	claimed, _, err := store.UpsertDocument(ctx, initial, body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatal(err)
	}

	changed := newDocument(corpus, source, "Invoice", body)
	changed.Metadata = domain.Metadata{"provider": "gmail", "category": "account_record"}
	changed, _, err = store.UpsertDocument(ctx, changed, body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatal(err)
	}

	at := time.Now().UTC()
	if recorded, err := store.MarkKnowledgeBaseSubmitted(ctx, claimed, "v2:the-old-version", at); err != nil || recorded {
		t.Fatalf("MarkKnowledgeBaseSubmitted for the old version = %v, %v; want nothing recorded", recorded, err)
	}
	if marked, err := store.MarkDocumentReady(ctx, claimed, 1, at, testEvent(t, "rag.document.indexed.v1")); err != nil || marked {
		t.Fatalf("MarkDocumentReady for the old version = %v, %v; want nothing recorded", marked, err)
	}
	if marked, err := store.MarkDocumentFailed(ctx, claimed, "stale", at, testEvent(t, "rag.document.failed.v1")); err != nil || marked {
		t.Fatalf("MarkDocumentFailed for the old version = %v, %v; want nothing recorded", marked, err)
	}
	current, err := store.Document(ctx, corpus.OrganizationID, changed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != domain.StatusPending || current.KBSyncedHash != "" || current.Metadata["category"] != "account_record" {
		t.Fatalf("the changed document = %+v; want it still pending, unsynced and changed", current)
	}

	// The version the change queued is recorded as usual.
	if recorded, err := store.MarkKnowledgeBaseSubmitted(ctx, current, "v2:the-new-version", at); err != nil || !recorded {
		t.Fatalf("MarkKnowledgeBaseSubmitted for the current version = %v, %v", recorded, err)
	}
	current.KBSyncedHash = "v2:the-new-version"
	if marked, err := store.MarkDocumentReady(ctx, current, 1, at, testEvent(t, "rag.document.indexed.v1")); err != nil || !marked {
		t.Fatalf("MarkDocumentReady for the current version = %v, %v", marked, err)
	}
	ready, err := store.Document(ctx, corpus.OrganizationID, changed.ID)
	if err != nil || ready.Status != domain.StatusReady || ready.KBSyncedHash != "v2:the-new-version" {
		t.Fatalf("the current version = %+v, %v; want it ready and synced", ready, err)
	}
}

// A ready document whose submission was recorded under an older marker was
// submitted without something rag now writes, such as its access, and is put
// back in the queue. Nothing else is: a current marker, a document still in
// the queue, and a document on another backend, which never has a marker.
func TestRequeueUnsyncedKnowledgeBaseDocuments(t *testing.T) {
	store, ctx := openTestStore(t)
	corpus := testCorpus(t, store, ctx, uniqueOrganization(), titanSpec())
	at := time.Now().UTC()
	documents := map[string]domain.Document{}
	for _, name := range []string{"old-ready", "current-ready", "old-pending", "never-submitted"} {
		body := "the " + name + " document"
		document, _, err := store.UpsertDocument(ctx,
			newDocument(corpus, domain.Source{Kind: domain.SourceExternal, Ref: "https://example.com/" + name}, name, body),
			body, testEvent(t, "rag.document.received.v1"))
		if err != nil {
			t.Fatal(err)
		}
		documents[name] = document
	}
	// Recorded as the build before markers did: the bare content hash.
	for name, marker := range map[string]string{
		"old-ready":     documents["old-ready"].ContentSHA256,
		"current-ready": knowledgebase.SyncMarkerPrefix + "current",
		"old-pending":   documents["old-pending"].ContentSHA256,
	} {
		if recorded, err := store.MarkKnowledgeBaseSubmitted(ctx, documents[name], marker, at); err != nil || !recorded {
			t.Fatalf("submit %s = %v, %v", name, recorded, err)
		}
	}
	for _, name := range []string{"old-ready", "current-ready", "never-submitted"} {
		if marked, err := store.MarkDocumentReady(ctx, documents[name], 0, at, testEvent(t, "rag.document.indexed.v1")); err != nil || !marked {
			t.Fatalf("ready %s = %v, %v", name, marked, err)
		}
	}

	requeued, err := store.RequeueUnsyncedKnowledgeBaseDocuments(ctx, 1000)
	if err != nil || requeued < 1 {
		t.Fatalf("RequeueUnsyncedKnowledgeBaseDocuments = %d, %v", requeued, err)
	}
	for name, want := range map[string]domain.IngestStatus{
		"old-ready": domain.StatusPending, "current-ready": domain.StatusReady,
		"old-pending": domain.StatusPending, "never-submitted": domain.StatusReady,
	} {
		document, err := store.Document(ctx, corpus.OrganizationID, documents[name].ID)
		if err != nil || document.Status != want {
			t.Fatalf("%s = %+v, %v; want %s", name, document.Status, err, want)
		}
	}
}

// The requeue's prefix is written out three times, since an index predicate
// cannot take a parameter: the knowledge base's own, this package's query,
// and the partial index that query is served by.
func TestTheSyncMarkerPrefixIsTheSameEverywhere(t *testing.T) {
	if knowledgeBaseSyncedPrefix != knowledgebase.SyncMarkerPrefix {
		t.Fatalf("query prefix %q, knowledge base prefix %q", knowledgeBaseSyncedPrefix, knowledgebase.SyncMarkerPrefix)
	}
	migration, err := fs.ReadFile(migrations, "migrations/013_kb_sync_marker.sql")
	if err != nil {
		t.Fatal(err)
	}
	if want := "NOT LIKE '" + knowledgebase.SyncMarkerPrefix + "%'"; !strings.Contains(string(migration), want) {
		t.Fatalf("the partial index does not test %s", want)
	}
}
