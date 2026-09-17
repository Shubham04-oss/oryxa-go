package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/embedding"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

func testCorpus() domain.Corpus {
	spec := domain.EmbeddingSpec{
		Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v2:0",
		Dimensions: 4, Metric: domain.MetricCosine,
	}
	return domain.Corpus{
		ID: "cor_1", OrganizationID: "org_1", Backend: domain.BackendPgvector, Spec: spec,
		Chunking: domain.ChunkPolicy{Strategy: domain.StrategyStructure, MaxTokens: 64, OverlapTokens: 8},
	}
}

func testDocument() domain.Document {
	return domain.Document{
		ID: "doc_1", CorpusID: "cor_1", OrganizationID: "org_1",
		Source: domain.Source{Kind: domain.SourceInline}, Title: "Runbook",
		ContentType: "text/markdown", Status: domain.StatusIngesting,
		Metadata: domain.Metadata{"team": "platform"},
	}
}

// fakeStore records what ingest did to a document.
type fakeStore struct {
	mutex      sync.Mutex
	corpus     domain.Corpus
	corpusErr  error
	body       string
	bodyErr    error
	sync       domain.ChunkSync
	syncErr    error
	syncedWith []domain.Chunk
	claim      [][]domain.Document
	ready      []string
	readyCount []int
	failed     []string
	reasons    []string
	released   []string
	embedded   []string
}

func (fake *fakeStore) Corpus(context.Context, string, string) (domain.Corpus, error) {
	return fake.corpus, fake.corpusErr
}

func (fake *fakeStore) DocumentBody(context.Context, string) (string, error) {
	return fake.body, fake.bodyErr
}

func (fake *fakeStore) SyncChunks(_ context.Context, _ domain.Document, chunks []domain.Chunk, _ time.Time) (domain.ChunkSync, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.syncedWith = chunks
	if fake.syncErr != nil {
		return domain.ChunkSync{}, fake.syncErr
	}
	if fake.sync.Fresh == nil && fake.sync.Reused == nil && fake.sync.Removed == nil {
		// By default every chunk is new, with ids assigned as the real store
		// would.
		fresh := make([]domain.Chunk, 0, len(chunks))
		for index, chunk := range chunks {
			chunk.ID = fmt.Sprintf("chk_%d", index)
			fresh = append(fresh, chunk)
		}
		return domain.ChunkSync{Fresh: fresh}, nil
	}
	return fake.sync, nil
}

func (fake *fakeStore) MarkChunksEmbedded(_ context.Context, chunkIDs []string, _ time.Time) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.embedded = append(fake.embedded, chunkIDs...)
	return nil
}

func (fake *fakeStore) ClaimDocuments(context.Context, int, time.Duration) ([]domain.Document, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if len(fake.claim) == 0 {
		return nil, nil
	}
	batch := fake.claim[0]
	fake.claim = fake.claim[1:]
	return batch, nil
}

func (fake *fakeStore) MarkDocumentReady(_ context.Context, documentID string, chunkCount int, _ time.Time, _ domain.Event) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.ready = append(fake.ready, documentID)
	fake.readyCount = append(fake.readyCount, chunkCount)
	return nil
}

func (fake *fakeStore) MarkDocumentFailed(_ context.Context, documentID, reason string, _ time.Time, _ domain.Event) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.failed = append(fake.failed, documentID)
	fake.reasons = append(fake.reasons, reason)
	return nil
}

func (fake *fakeStore) ReleaseDocument(_ context.Context, documentID string) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.released = append(fake.released, documentID)
	return nil
}

type fakeVectorStore struct {
	upserted  []domain.Vector
	deleted   []string
	upsertErr error
}

func (fake *fakeVectorStore) Upsert(_ context.Context, _ domain.Corpus, vectors []domain.Vector) error {
	fake.upserted = append(fake.upserted, vectors...)
	return fake.upsertErr
}

func (fake *fakeVectorStore) Search(context.Context, domain.Corpus, vectorstore.Search) ([]vectorstore.Hit, error) {
	return nil, nil
}

func (fake *fakeVectorStore) Delete(_ context.Context, _ domain.Corpus, ids []string) error {
	fake.deleted = append(fake.deleted, ids...)
	return nil
}

func (fake *fakeVectorStore) DropCorpus(context.Context, domain.Corpus) error { return nil }
func (fake *fakeVectorStore) Backend() domain.Backend                         { return domain.BackendPgvector }

type fakeVectors struct{ store *fakeVectorStore }

func (fake fakeVectors) For(domain.Corpus) (vectorstore.Store, error) { return fake.store, nil }

type fakeEmbedder struct {
	texts []string
	kinds []domain.InputKind
	err   error
}

func (fake *fakeEmbedder) Spec() domain.EmbeddingSpec { return testCorpus().Spec }

func (fake *fakeEmbedder) Embed(_ context.Context, kind domain.InputKind, texts []string) (embedding.Result, error) {
	if fake.err != nil {
		return embedding.Result{}, fake.err
	}
	fake.kinds = append(fake.kinds, kind)
	fake.texts = append(fake.texts, texts...)
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vectors[index] = []float32{1, 0, 0, 0}
	}
	return embedding.Result{Vectors: vectors, Usage: domain.Usage{EmbeddedTexts: len(texts), EmbeddingTokens: 11 * len(texts)}}, nil
}

type fakeEmbedders struct{ embedder *fakeEmbedder }

func (fake fakeEmbedders) For(context.Context, domain.EmbeddingSpec) (embedding.Embedder, error) {
	return fake.embedder, nil
}

func newPipeline(store *fakeStore, vectors *fakeVectorStore, embedder *fakeEmbedder) *Pipeline {
	return New(store, fakeVectors{vectors}, fakeEmbedders{embedder}, Options{}, nil)
}

const body = "# Rotation\n\nCredentials rotate every ninety days.\n\n## Failure\n\nA rotation that fails pages the on-call engineer.\n"

func TestIndexChunksEmbedsAndMarksReady(t *testing.T) {
	t.Parallel()
	store := &fakeStore{corpus: testCorpus(), body: body}
	vectors := &fakeVectorStore{}
	embedder := &fakeEmbedder{}
	pipeline := newPipeline(store, vectors, embedder)

	result, err := pipeline.Index(context.Background(), testDocument())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(result.Sync.Fresh) == 0 {
		t.Fatal("nothing was chunked")
	}
	if len(vectors.upserted) != len(result.Sync.Fresh) {
		t.Fatalf("wrote %d vectors for %d fresh chunks", len(vectors.upserted), len(result.Sync.Fresh))
	}
	if len(store.ready) != 1 || store.ready[0] != "doc_1" {
		t.Fatalf("ready = %v", store.ready)
	}
	if store.readyCount[0] != len(store.syncedWith) {
		t.Fatalf("marked ready with %d chunks, cut %d", store.readyCount[0], len(store.syncedWith))
	}
	if len(store.failed) != 0 {
		t.Fatalf("failed = %v", store.failed)
	}
	if result.Usage.EmbeddingTokens == 0 {
		t.Fatal("usage was not accounted for")
	}
}

func TestIndexEmbedsChunksAsDocumentsWithTheirHeadings(t *testing.T) {
	t.Parallel()
	store := &fakeStore{corpus: testCorpus(), body: body}
	embedder := &fakeEmbedder{}
	if _, err := newPipeline(store, &fakeVectorStore{}, embedder).Index(context.Background(), testDocument()); err != nil {
		t.Fatalf("Index: %v", err)
	}
	// Passages are embedded as documents, not as queries. Getting this
	// backwards is the quietest way to make retrieval mediocre.
	for _, kind := range embedder.kinds {
		if kind != domain.InputDocument {
			t.Fatalf("a chunk was embedded as %q", kind)
		}
	}
	// The heading trail is embedded with the text, so a passage keeps its
	// subject when it is read alone.
	var withHeadings int
	for _, text := range embedder.texts {
		if strings.HasPrefix(text, "Rotation") {
			withHeadings++
		}
	}
	if withHeadings == 0 {
		t.Fatalf("no embedded text carried its heading trail: %q", embedder.texts)
	}
}

func TestIndexWritesProvenanceOntoEveryVector(t *testing.T) {
	t.Parallel()
	store := &fakeStore{corpus: testCorpus(), body: body}
	vectors := &fakeVectorStore{}
	if _, err := newPipeline(store, vectors, &fakeEmbedder{}).Index(context.Background(), testDocument()); err != nil {
		t.Fatalf("Index: %v", err)
	}
	for _, vector := range vectors.upserted {
		if vector.DocumentID != "doc_1" {
			t.Fatalf("vector %s does not name its document", vector.ChunkID)
		}
		for _, key := range []string{"chunk_id", "corpus_id", "document_id", "organization_id", "source_kind"} {
			if vector.Metadata[key] == "" {
				t.Fatalf("vector %s is missing provenance key %q: %v", vector.ChunkID, key, vector.Metadata)
			}
		}
		// The caller's own metadata survives alongside the provenance.
		if vector.Metadata["team"] != "platform" {
			t.Fatalf("the document's metadata was lost: %v", vector.Metadata)
		}
		if vector.Metadata["title"] != "Runbook" {
			t.Fatalf("the title did not reach the vector: %v", vector.Metadata)
		}
	}
}

func TestIndexOnlyEmbedsWhatChanged(t *testing.T) {
	t.Parallel()
	// This is the whole reason for re-cutting rather than rebuilding: a
	// one-word edit costs one embedding call, not one per chunk.
	store := &fakeStore{corpus: testCorpus(), body: body, sync: domain.ChunkSync{
		Fresh:   []domain.Chunk{{ID: "chk_new", Text: "changed paragraph", SHA256: "h1"}},
		Reused:  []domain.Chunk{{ID: "chk_a", Text: "unchanged"}, {ID: "chk_b", Text: "also unchanged"}},
		Removed: []string{"chk_old"},
	}}
	vectors := &fakeVectorStore{}
	embedder := &fakeEmbedder{}
	result, err := newPipeline(store, vectors, embedder).Index(context.Background(), testDocument())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(embedder.texts) != 1 {
		t.Fatalf("embedded %d chunks, want only the changed one", len(embedder.texts))
	}
	if len(vectors.upserted) != 1 || vectors.upserted[0].ChunkID != "chk_new" {
		t.Fatalf("upserted = %+v", vectors.upserted)
	}
	// The old vector goes, or a search could return a passage the document
	// no longer contains.
	if len(vectors.deleted) != 1 || vectors.deleted[0] != "chk_old" {
		t.Fatalf("deleted = %v, want the stale chunk", vectors.deleted)
	}
	if len(result.Sync.Reused) != 2 {
		t.Fatalf("reused = %d, want 2", len(result.Sync.Reused))
	}
}

func TestIndexWithNothingToEmbedStillMarksReady(t *testing.T) {
	t.Parallel()
	// A document whose chunks all still match needs no provider call at all,
	// and must still come out searchable.
	store := &fakeStore{corpus: testCorpus(), body: body, sync: domain.ChunkSync{
		Reused: []domain.Chunk{{ID: "chk_a", Text: "unchanged"}},
	}}
	embedder := &fakeEmbedder{}
	if _, err := newPipeline(store, &fakeVectorStore{}, embedder).Index(context.Background(), testDocument()); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(embedder.texts) != 0 {
		t.Fatalf("embedded %d chunks when nothing changed", len(embedder.texts))
	}
	if len(store.ready) != 1 {
		t.Fatalf("ready = %v", store.ready)
	}
}

func TestIndexReleasesForRetryAndFailsAfterTheLimit(t *testing.T) {
	t.Parallel()
	// A transient failure is worth another go; the attempt count is what
	// decides when to stop, not the error.
	store := &fakeStore{corpus: testCorpus(), body: body}
	embedder := &fakeEmbedder{err: errors.New("throttled")}
	pipeline := newPipeline(store, &fakeVectorStore{}, embedder)

	early := testDocument()
	early.Attempts = 1
	if _, err := pipeline.Index(context.Background(), early); err == nil {
		t.Fatal("Index succeeded with a failing embedder")
	}
	if len(store.released) != 1 || len(store.failed) != 0 {
		t.Fatalf("released = %v, failed = %v; an early attempt should be released", store.released, store.failed)
	}

	// A failure that keeps repeating is the document, not the weather.
	exhausted := testDocument()
	exhausted.Attempts = MaxAttempts
	if _, err := pipeline.Index(context.Background(), exhausted); err == nil {
		t.Fatal("Index succeeded with a failing embedder")
	}
	if len(store.failed) != 1 {
		t.Fatalf("failed = %v, want the document recorded as failed", store.failed)
	}
	if !strings.Contains(store.reasons[0], "throttled") {
		t.Fatalf("the failure reason was lost: %q", store.reasons[0])
	}
}

func TestIndexDoesNotRetryWhatWillNotChange(t *testing.T) {
	t.Parallel()
	// An empty document will chunk to nothing every time. Retrying it five
	// times would just keep it in the queue.
	store := &fakeStore{corpus: testCorpus(), body: "   \n\n  "}
	pipeline := newPipeline(store, &fakeVectorStore{}, &fakeEmbedder{})
	if _, err := pipeline.Index(context.Background(), testDocument()); err == nil {
		t.Fatal("a document with no chunks was indexed")
	}
	if len(store.failed) != 1 {
		t.Fatalf("failed = %v, want it recorded as failed immediately", store.failed)
	}
	if len(store.released) != 0 {
		t.Fatalf("released = %v, want no retry", store.released)
	}
}

func TestIndexReportsAMissingCorpus(t *testing.T) {
	t.Parallel()
	store := &fakeStore{corpusErr: domain.ErrNotFound, body: body}
	pipeline := newPipeline(store, &fakeVectorStore{}, &fakeEmbedder{})
	document := testDocument()
	document.Attempts = MaxAttempts
	if _, err := pipeline.Index(context.Background(), document); err == nil {
		t.Fatal("a document in a missing corpus was indexed")
	}
	if len(store.failed) != 1 {
		t.Fatalf("failed = %v", store.failed)
	}
}

func TestIndexDoesNotMarkFailedOnCancellation(t *testing.T) {
	t.Parallel()
	// Shutting down is not the document's fault; it goes back untouched.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &fakeStore{corpus: testCorpus(), body: body}
	embedder := &fakeEmbedder{err: context.Canceled}
	pipeline := newPipeline(store, &fakeVectorStore{}, embedder)
	if _, err := pipeline.Index(ctx, testDocument()); err == nil {
		t.Fatal("Index succeeded on a cancelled context")
	}
	if len(store.failed) != 0 {
		t.Fatalf("a cancelled document was marked failed: %v", store.failed)
	}
	if len(store.released) != 1 {
		t.Fatalf("released = %v, want the document put back", store.released)
	}
}

func TestRunWorksTheQueueAndStops(t *testing.T) {
	t.Parallel()
	store := &fakeStore{corpus: testCorpus(), body: body, claim: [][]domain.Document{
		{testDocument()},
	}}
	pipeline := New(store, fakeVectors{&fakeVectorStore{}}, fakeEmbedders{&fakeEmbedder{}},
		Options{Interval: time.Millisecond}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		pipeline.Run(ctx)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for {
		store.mutex.Lock()
		indexed := len(store.ready)
		store.mutex.Unlock()
		if indexed == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the worker did not index the queued document")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker did not stop when its context ended")
	}
}
