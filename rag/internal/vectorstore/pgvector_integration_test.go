package vectorstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
)

// fixture is a corpus with a document and some chunks already in PostgreSQL,
// which the vector tables reference. Vectors cannot exist without their
// chunks, by design, so a test of the vector store starts from real rows.
type fixture struct {
	store  *postgres.Store
	vector *Pgvector
	corpus domain.Corpus
	docID  string
	chunks []domain.Chunk
}

func setup(t *testing.T, dimensions int, metric domain.Metric) (fixture, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("RAG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RAG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	organizationID := "org-vec-" + time.Now().UTC().Format("20060102150405.000000000")
	corpusID, _ := domain.NewID(domain.CorpusIDPrefix)
	now := time.Now().UTC().Truncate(time.Microsecond)
	spec := domain.EmbeddingSpec{
		Provider: domain.ProviderBedrock, Model: "test-model",
		Dimensions: dimensions, Metric: metric,
	}
	corpus := domain.Corpus{
		ID: corpusID, OrganizationID: organizationID,
		Slug: fmt.Sprintf("vec-%d-%s", dimensions, corpusID[4:12]), Name: "Vectors",
		Scope: domain.ScopeOrganization, Spec: spec, Backend: domain.BackendPgvector,
		Chunking:  domain.ChunkPolicy{Strategy: domain.StrategyStructure, MaxTokens: 512, OverlapTokens: 64},
		CreatedBy: "usr_vec", CreatedAt: now, UpdatedAt: now,
	}
	event := func(kind string) domain.Event {
		id, _ := domain.NewID(domain.EventIDPrefix)
		return domain.Event{ID: id, DetailType: kind, OccurredAt: now, Data: json.RawMessage(`{}`)}
	}
	if err := store.CreateCorpus(ctx, corpus, event("rag.corpus.created.v1")); err != nil {
		t.Fatalf("CreateCorpus: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = store.DeleteCorpus(cleanupCtx, organizationID, corpusID, event("rag.corpus.deleted.v1"))
	})

	body := "body"
	documentID, _ := domain.NewID(domain.DocumentIDPrefix)
	document, _, err := store.UpsertDocument(ctx, domain.Document{
		ID: documentID, CorpusID: corpusID, OrganizationID: organizationID,
		Source: domain.Source{Kind: domain.SourceInline}, Title: "Doc",
		ContentType: "text/plain", ContentSHA256: domain.Hash(body), ByteSize: int64(len(body)),
		CreatedBy: "usr_vec", CreatedAt: now, UpdatedAt: now,
	}, body, event("rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}

	chunks := make([]domain.Chunk, 0, 5)
	for index := range 5 {
		text := fmt.Sprintf("chunk %d", index)
		chunks = append(chunks, domain.Chunk{
			Ordinal: index, Text: text, TokenCount: 2, SHA256: domain.Hash(text),
			StartByte: index, EndByte: index + 1,
			Metadata: domain.Metadata{"team": map[bool]string{true: "platform", false: "people"}[index%2 == 0]},
		})
	}
	sync, err := store.SyncChunks(ctx, document, chunks, now)
	if err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}
	return fixture{
		store:  store,
		vector: NewPgvector(store.Pool(), store.PgvectorVersion()),
		corpus: corpus,
		docID:  document.ID,
		chunks: sync.Fresh,
	}, ctx
}

// graded returns a unit vector whose similarity to probe() falls as rank
// rises, so a test can assert an exact ordering rather than a set.
func graded(rank, dimensions int) []float32 {
	values := make([]float32, dimensions)
	values[0] = 1
	values[1] = float32(rank)
	length := float32(math.Sqrt(float64(1 + rank*rank)))
	values[0] /= length
	values[1] /= length
	return values
}

func probe(dimensions int) []float32 {
	values := make([]float32, dimensions)
	values[0] = 1
	return values
}

func (fixture fixture) vectors() []domain.Vector {
	vectors := make([]domain.Vector, 0, len(fixture.chunks))
	for rank, chunk := range fixture.chunks {
		vectors = append(vectors, domain.Vector{
			ChunkID:    chunk.ID,
			DocumentID: fixture.docID,
			Values:     graded(rank, fixture.corpus.Spec.Dimensions),
			Metadata:   chunk.Metadata,
		})
	}
	return vectors
}

func TestPgvectorSearchRanksByDistance(t *testing.T) {
	fixture, ctx := setup(t, 768, domain.MetricCosine)
	if err := fixture.vector.Upsert(ctx, fixture.corpus, fixture.vectors()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	hits, err := fixture.vector.Search(ctx, fixture.corpus, Search{
		Vector: probe(768), TopK: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 5 {
		t.Fatalf("got %d hits, want 5", len(hits))
	}
	for index, hit := range hits {
		if hit.ChunkID != fixture.chunks[index].ID {
			t.Fatalf("hit %d is %s, want %s — ranking is wrong", index, hit.ChunkID, fixture.chunks[index].ID)
		}
		if index > 0 && hit.Score > hits[index-1].Score {
			t.Fatalf("scores are not descending: %v then %v", hits[index-1].Score, hit.Score)
		}
	}
	// The nearest vector is the query itself, so its cosine distance is zero
	// and its score is one.
	if math.Abs(hits[0].Score-1) > 1e-5 {
		t.Fatalf("the exact match scored %v, want 1", hits[0].Score)
	}
}

func TestPgvectorUpsertIsIdempotent(t *testing.T) {
	fixture, ctx := setup(t, 768, domain.MetricCosine)
	for range 3 {
		if err := fixture.vector.Upsert(ctx, fixture.corpus, fixture.vectors()); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	hits, err := fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(768), TopK: 50})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Re-ingesting a document must not leave two vectors for one chunk.
	if len(hits) != 5 {
		t.Fatalf("got %d hits after three upserts, want 5", len(hits))
	}
}

func TestPgvectorFiltersNarrowTheSearch(t *testing.T) {
	fixture, ctx := setup(t, 768, domain.MetricCosine)
	if err := fixture.vector.Upsert(ctx, fixture.corpus, fixture.vectors()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	hits, err := fixture.vector.Search(ctx, fixture.corpus, Search{
		Vector: probe(768), TopK: 10,
		Filter: domain.Filter{Conditions: []domain.Condition{
			{Key: "team", Operator: domain.OperatorEqual, Values: []string{"people"}},
		}},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Chunks 1 and 3 are the "people" ones.
	if len(hits) != 2 {
		t.Fatalf("got %d hits under the filter, want 2", len(hits))
	}
	wanted := map[string]bool{fixture.chunks[1].ID: true, fixture.chunks[3].ID: true}
	for _, hit := range hits {
		if !wanted[hit.ChunkID] {
			t.Fatalf("a chunk outside the filter was returned: %s", hit.ChunkID)
		}
	}

	notIn, err := fixture.vector.Search(ctx, fixture.corpus, Search{
		Vector: probe(768), TopK: 10,
		Filter: domain.Filter{Conditions: []domain.Condition{
			{Key: "team", Operator: domain.OperatorNotIn, Values: []string{"people"}},
		}},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(notIn) != 3 {
		t.Fatalf("got %d hits for not-in, want 3", len(notIn))
	}

	byDocument, err := fixture.vector.Search(ctx, fixture.corpus, Search{
		Vector: probe(768), TopK: 10,
		Filter: domain.Filter{DocumentIDs: []string{fixture.docID}},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(byDocument) != 5 {
		t.Fatalf("got %d hits for the document filter, want 5", len(byDocument))
	}
	elsewhere, err := fixture.vector.Search(ctx, fixture.corpus, Search{
		Vector: probe(768), TopK: 10,
		Filter: domain.Filter{DocumentIDs: []string{"doc_somewhere_else"}},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(elsewhere) != 0 {
		t.Fatalf("got %d hits for a document that is not there", len(elsewhere))
	}
}

func TestPgvectorExcludesAdminOnlyDocumentsBeforeRanking(t *testing.T) {
	fixture, ctx := setup(t, 768, domain.MetricCosine)
	if err := fixture.vector.Upsert(ctx, fixture.corpus, fixture.vectors()); err != nil {
		t.Fatal(err)
	}
	document, err := fixture.store.Document(ctx, fixture.corpus.OrganizationID, fixture.docID)
	if err != nil {
		t.Fatal(err)
	}
	document.Access = domain.AccessAdmins
	document.UpdatedAt = time.Now().UTC()
	// An event id of its own each run: the outbox outlives the corpus.
	eventID, _ := domain.NewID(domain.EventIDPrefix)
	if _, _, err := fixture.store.UpsertDocument(ctx, document, "body", domain.Event{
		ID: eventID, DetailType: "rag.document.received.v1", OccurredAt: time.Now().UTC(), Data: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	member, err := fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(768), TopK: 10})
	if err != nil || len(member) != 0 {
		t.Fatalf("member hits = %+v, %v", member, err)
	}
	admin, err := fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(768), TopK: 10, IncludeAdminOnly: true})
	if err != nil || len(admin) != 5 {
		t.Fatalf("admin hits = %+v, %v", admin, err)
	}
}

// A member's access is checked after the index answers, so the admin-only
// chunks nearest a question use up the candidates HNSW hands back. With
// enough of them — a corpus full of admin-only files, say — a member search
// came back empty from a corpus that plainly held passages they may read. The
// index must keep scanning past them, as it does for a metadata filter.
func TestPgvectorMemberSearchReachesPastNearerAdminOnlyChunks(t *testing.T) {
	fixture, ctx := setup(t, 256, domain.MetricCosine)
	now := time.Now().UTC().Truncate(time.Microsecond)
	pool := fixture.store.Pool()
	random := rand.New(rand.NewPCG(1, 2))
	near := func(spread float32) []float32 {
		values := make([]float32, 256)
		values[0] = 1
		for index := 1; index < len(values); index++ {
			values[index] = (random.Float32()*2 - 1) * spread
		}
		return values
	}

	body := "payroll"
	adminID, _ := domain.NewID(domain.DocumentIDPrefix)
	admin, _, err := fixture.store.UpsertDocument(ctx, domain.Document{
		ID: adminID, CorpusID: fixture.corpus.ID, OrganizationID: fixture.corpus.OrganizationID,
		Source: domain.Source{Kind: domain.SourceExternal, Ref: "https://example.com/payroll"}, Title: "Payroll",
		ContentType: "text/plain", ContentSHA256: domain.Hash(body), ByteSize: int64(len(body)), Access: domain.AccessAdmins,
		CreatedBy: "usr_vec", CreatedAt: now, UpdatedAt: now,
	}, body, domain.Event{ID: "evt_" + adminID, DetailType: "rag.document.received.v1", OccurredAt: now, Data: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	// Far more admin-only chunks nearest the question than ef_search keeps.
	const adminChunks = 2000
	chunks := make([]domain.Chunk, 0, adminChunks)
	for index := range adminChunks {
		text := fmt.Sprintf("payroll %d", index)
		chunks = append(chunks, domain.Chunk{Ordinal: index, Text: text, TokenCount: 2, SHA256: domain.Hash(text), StartByte: 0, EndByte: 1})
	}
	synced, err := fixture.store.SyncChunks(ctx, admin, chunks, now)
	if err != nil {
		t.Fatal(err)
	}
	vectors := make([]domain.Vector, 0, adminChunks)
	for _, chunk := range synced.Fresh {
		vectors = append(vectors, domain.Vector{ChunkID: chunk.ID, DocumentID: admin.ID, Values: near(0.02)})
	}
	if err := fixture.vector.Upsert(ctx, fixture.corpus, vectors); err != nil {
		t.Fatal(err)
	}
	// The member document's chunks, further away.
	member := fixture.vectors()
	for index := range member {
		member[index].Values = near(0.2)
	}
	if err := fixture.vector.Upsert(ctx, fixture.corpus, member); err != nil {
		t.Fatal(err)
	}
	// Member documents with no vectors, as a real corpus holds many of: they
	// make it dearer to start from the member documents than to walk the
	// index, which is the plan a real corpus gets.
	if _, err := pool.Exec(ctx, `
		INSERT INTO rag_documents (id, corpus_id, organization_id, source_kind, source_ref, title, content_type,
			content_sha256, byte_size, body, metadata, status, created_by, created_at, updated_at, access)
		SELECT 'doc_filler_' || $1 || '_' || g, $1, $2, 'external', 'https://example.com/filler/' || g, 'Filler', 'text/plain',
			md5(g::text), 4, 'body', '{}', 'ready', 'usr_vec', now(), now(), 'members'
		FROM generate_series(1, 3000) AS g`, fixture.corpus.ID, fixture.corpus.OrganizationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO rag_chunks (id, document_id, corpus_id, organization_id, ordinal, body, token_count,
			start_byte, end_byte, sha256, metadata, created_at)
		SELECT 'chk_filler_' || $1 || '_' || g, 'doc_filler_' || $1 || '_' || g, $1, $2, 0, 'filler', 1, 0, 1, md5(g::text), '{}', now()
		FROM generate_series(1, 3000) AS g`, fixture.corpus.ID, fixture.corpus.OrganizationID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE rag_vectors_256; ANALYZE rag_documents; ANALYZE rag_chunks`); err != nil {
		t.Fatal(err)
	}

	hits, err := fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(256), TopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	memberChunks := map[string]bool{}
	for _, vector := range member {
		memberChunks[vector.ChunkID] = true
	}
	if len(hits) == 0 {
		t.Fatal("a member search came back empty from a corpus holding passages the member may read")
	}
	for _, hit := range hits {
		if !memberChunks[hit.ChunkID] {
			t.Fatalf("a member search returned %s, which is not the member document's", hit.ChunkID)
		}
	}
}

func TestPgvectorScopesToTheCorpusAndOrganization(t *testing.T) {
	fixture, ctx := setup(t, 768, domain.MetricCosine)
	if err := fixture.vector.Upsert(ctx, fixture.corpus, fixture.vectors()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// Tenant isolation is applied by the store from the corpus, not by the
	// caller, so an impostor corpus in another organization finds nothing.
	impostor := fixture.corpus
	impostor.OrganizationID = "org-not-yours"
	hits, err := fixture.vector.Search(ctx, impostor, Search{Vector: probe(768), TopK: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("another organization saw %d vectors", len(hits))
	}
}

func TestPgvectorDeleteAndDropCorpus(t *testing.T) {
	fixture, ctx := setup(t, 768, domain.MetricCosine)
	if err := fixture.vector.Upsert(ctx, fixture.corpus, fixture.vectors()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := fixture.vector.Delete(ctx, fixture.corpus, []string{fixture.chunks[0].ID, fixture.chunks[1].ID}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	hits, err := fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(768), TopK: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("got %d hits after deleting 2 of 5", len(hits))
	}
	if err := fixture.vector.DropCorpus(ctx, fixture.corpus); err != nil {
		t.Fatalf("DropCorpus: %v", err)
	}
	hits, err = fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(768), TopK: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("%d vectors survived DropCorpus", len(hits))
	}
	// Deleting nothing is not an error; an ingest with no removals says so by
	// passing an empty list.
	if err := fixture.vector.Delete(ctx, fixture.corpus, nil); err != nil {
		t.Fatalf("Delete(nil): %v", err)
	}
}

func TestPgvectorVectorsGoWithTheirChunks(t *testing.T) {
	fixture, ctx := setup(t, 768, domain.MetricCosine)
	if err := fixture.vector.Upsert(ctx, fixture.corpus, fixture.vectors()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	id, _ := domain.NewID(domain.EventIDPrefix)
	if _, err := fixture.store.DeleteDocument(ctx, fixture.corpus.OrganizationID, fixture.docID,
		domain.Event{ID: id, DetailType: "rag.document.removed.v1", OccurredAt: time.Now().UTC(), Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	// The foreign key is what stops this table drifting into holding vectors
	// for chunks that no longer exist.
	hits, err := fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(768), TopK: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("%d vectors outlived the document they came from", len(hits))
	}
}

func TestPgvectorHalfPrecisionWidth(t *testing.T) {
	// 3072 is what gemini-embedding-001 emits, and it is over pgvector's
	// 2000-dimension ceiling for an indexed vector column, so it is stored as
	// halfvec. This is the path that would break first if that mapping were
	// wrong.
	fixture, ctx := setup(t, 3072, domain.MetricCosine)
	if !HalfPrecision(3072) {
		t.Fatal("3072 should be stored at half precision")
	}
	if err := fixture.vector.Upsert(ctx, fixture.corpus, fixture.vectors()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	hits, err := fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(3072), TopK: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 5 {
		t.Fatalf("got %d hits, want 5", len(hits))
	}
	if hits[0].ChunkID != fixture.chunks[0].ID {
		t.Fatal("half precision changed which vector was nearest")
	}
}

func TestPgvectorUnlistedWidthStillWorks(t *testing.T) {
	// A width with no table of its own falls into the shared unindexed table.
	// It must still be correct — a model released after this code was written
	// should work on the day it is configured.
	const width = 999
	if Indexed(width) {
		t.Fatalf("%d should not be an indexed width", width)
	}
	if Table(width) != otherTable {
		t.Fatalf("Table(%d) = %q", width, Table(width))
	}
	fixture, ctx := setup(t, width, domain.MetricCosine)
	if err := fixture.vector.Upsert(ctx, fixture.corpus, fixture.vectors()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	hits, err := fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(width), TopK: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 5 || hits[0].ChunkID != fixture.chunks[0].ID {
		t.Fatalf("got %d hits, first %v", len(hits), hits)
	}
}

func TestPgvectorEuclideanMetric(t *testing.T) {
	fixture, ctx := setup(t, 768, domain.MetricL2)
	if err := fixture.vector.Upsert(ctx, fixture.corpus, fixture.vectors()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	hits, err := fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(768), TopK: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 5 || hits[0].ChunkID != fixture.chunks[0].ID {
		t.Fatalf("euclidean ranking is wrong: %+v", hits)
	}
	for index := 1; index < len(hits); index++ {
		if hits[index].Score > hits[index-1].Score {
			t.Fatalf("scores are not descending under l2: %v then %v", hits[index-1].Score, hits[index].Score)
		}
	}
}

func TestPgvectorRefusesAQueryOfTheWrongWidth(t *testing.T) {
	fixture, ctx := setup(t, 768, domain.MetricCosine)
	_, err := fixture.vector.Search(ctx, fixture.corpus, Search{Vector: probe(512), TopK: 5})
	if err == nil {
		t.Fatal("a 512-wide query was run against a 768-wide corpus")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	err = fixture.vector.Upsert(ctx, fixture.corpus, []domain.Vector{{
		ChunkID: fixture.chunks[0].ID, DocumentID: fixture.docID, Values: probe(512),
	}})
	if err == nil {
		t.Fatal("a 512-wide vector was written into a 768-wide corpus")
	}
}
