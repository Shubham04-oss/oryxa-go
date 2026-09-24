package ingest

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

// A document reclassified without its text changing keeps its chunks and
// their vectors: nothing is embedded again, nothing is deleted first, and a
// search filtered on the new category finds it while one on the old category
// no longer does. Before, every chunk got a new id, its vector was deleted,
// and the whole document was embedded again.
func TestAReclassifiedDocumentIsRewrittenNotReembedded(t *testing.T) {
	databaseURL := os.Getenv("RAG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RAG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := func(detailType string) domain.Event {
		id, _ := domain.NewID(domain.EventIDPrefix)
		return domain.Event{ID: id, DetailType: detailType, OccurredAt: now, Data: json.RawMessage(`{}`)}
	}
	corpus := testCorpus()
	corpus.ID, _ = domain.NewID(domain.CorpusIDPrefix)
	corpus.OrganizationID = "org-relabel-" + now.Format("20060102150405.000000000")
	corpus.Slug, corpus.Name, corpus.Scope = "relabel", "Relabel", domain.ScopeOrganization
	corpus.CreatedBy, corpus.CreatedAt, corpus.UpdatedAt = "usr_ingest", now, now
	if err := store.CreateCorpus(ctx, corpus, event("rag.corpus.created.v1")); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = store.DeleteCorpus(cleanup, corpus.OrganizationID, corpus.ID, event("rag.corpus.deleted.v1"))
	}()

	documentID, _ := domain.NewID(domain.DocumentIDPrefix)
	offer := func(category string) domain.Document {
		t.Helper()
		document, _, err := store.UpsertDocument(ctx, domain.Document{
			ID: documentID, CorpusID: corpus.ID, OrganizationID: corpus.OrganizationID,
			Source: domain.Source{Kind: domain.SourceExternal, Ref: "https://example.com/invoice"}, Title: "Invoice",
			ContentType: "text/markdown", ContentSHA256: domain.Hash(body), ByteSize: int64(len(body)),
			Metadata: domain.Metadata{"provider": "gmail", "category": category}, Access: domain.AccessMembers,
			CreatedBy: "usr_ingest", CreatedAt: now, UpdatedAt: time.Now().UTC(),
		}, body, event("rag.document.received.v1"))
		if err != nil {
			t.Fatal(err)
		}
		return document
	}
	vectors := vectorstore.NewPgvector(store.Pool(), store.PgvectorVersion())
	embedder := &fakeEmbedder{}
	pipeline := New(store, vectorstore.NewRouter(vectors), fakeEmbedders{embedder}, nil, Options{}, nil)

	first, err := pipeline.Index(ctx, offer("financial_record"))
	if err != nil {
		t.Fatal(err)
	}
	embeddedFirst := len(embedder.texts)
	if embeddedFirst == 0 || len(first.Sync.Fresh) != embeddedFirst {
		t.Fatalf("first index embedded %d texts for %d chunks", embeddedFirst, len(first.Sync.Fresh))
	}
	ids := func(chunks []domain.Chunk) []string {
		collected := make([]string, 0, len(chunks))
		for _, chunk := range chunks {
			collected = append(collected, chunk.ID)
		}
		slices.Sort(collected)
		return collected
	}

	second, err := pipeline.Index(ctx, offer("account_record"))
	if err != nil {
		t.Fatal(err)
	}
	if len(embedder.texts) != embeddedFirst {
		t.Fatalf("a reclassification embedded %d more texts, want none", len(embedder.texts)-embeddedFirst)
	}
	if len(second.Sync.Removed) != 0 || !slices.Equal(ids(second.Sync.Fresh), ids(first.Sync.Fresh)) {
		t.Fatalf("a reclassification changed the chunks: %+v, want the same ids and nothing removed", second.Sync)
	}
	search := func(category string) int {
		t.Helper()
		hits, err := vectors.Search(ctx, corpus, vectorstore.Search{Vector: []float32{1, 0, 0, 0}, TopK: 10, Filter: domain.Filter{
			Conditions: []domain.Condition{{Key: "category", Operator: domain.OperatorEqual, Values: []string{category}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return len(hits)
	}
	if got := search("account_record"); got != len(first.Sync.Fresh) {
		t.Fatalf("a search on the new category found %d chunks, want %d", got, len(first.Sync.Fresh))
	}
	if got := search("financial_record"); got != 0 {
		t.Fatalf("a search on the old category still found %d chunks", got)
	}
	ready, err := store.Document(ctx, corpus.OrganizationID, documentID)
	if err != nil || ready.Status != domain.StatusReady {
		t.Fatalf("document = %+v, %v; want ready", ready.Status, err)
	}
}
