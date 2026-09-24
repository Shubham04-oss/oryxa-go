package postgres

import (
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/knowledgebase"
)

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
		if err := store.MarkKnowledgeBaseSubmitted(ctx, documents[name].ID, marker, at); err != nil {
			t.Fatalf("submit %s: %v", name, err)
		}
	}
	for _, name := range []string{"old-ready", "current-ready", "never-submitted"} {
		if err := store.MarkDocumentReady(ctx, documents[name].ID, 0, at, testEvent(t, "rag.document.indexed.v1")); err != nil {
			t.Fatalf("ready %s: %v", name, err)
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
