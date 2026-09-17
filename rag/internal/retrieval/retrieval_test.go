package retrieval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/embedding"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

func testCorpus() domain.Corpus {
	return domain.Corpus{
		ID: "cor_1", OrganizationID: "org_1", Backend: domain.BackendPgvector,
		Spec: domain.EmbeddingSpec{
			Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v2:0",
			Dimensions: 4, Metric: domain.MetricCosine,
		},
	}
}

// fakeChunks answers both the lexical search and the hydration reads.
type fakeChunks struct {
	lexical     []domain.ScoredChunk
	lexicalErr  error
	texts       map[string]string
	documents   map[string]string
	missing     map[string]bool
	lastFilter  domain.Filter
	lexicalCall int
}

func (fake *fakeChunks) LexicalSearch(_ context.Context, _ domain.Corpus, _ string, _ int, filter domain.Filter) ([]domain.ScoredChunk, error) {
	fake.lexicalCall++
	fake.lastFilter = filter
	return fake.lexical, fake.lexicalErr
}

func (fake *fakeChunks) Chunks(_ context.Context, _, _ string, ids []string) ([]domain.Chunk, error) {
	chunks := make([]domain.Chunk, 0, len(ids))
	for _, id := range ids {
		if fake.missing[id] {
			continue
		}
		text := fake.texts[id]
		if text == "" {
			text = "text for " + id
		}
		document := fake.documents[id]
		if document == "" {
			document = "doc_1"
		}
		chunks = append(chunks, domain.Chunk{
			ID: id, DocumentID: document, CorpusID: "cor_1", OrganizationID: "org_1", Text: text,
		})
	}
	return chunks, nil
}

func (fake *fakeChunks) DocumentRefs(_ context.Context, _ string, ids []string) (map[string]domain.Ref, error) {
	refs := map[string]domain.Ref{}
	for _, id := range ids {
		refs[id] = domain.Ref{ID: id, Title: "Title " + id, Source: domain.Source{Kind: domain.SourceInline}}
	}
	return refs, nil
}

// fakeVectorStore answers the dense half.
type fakeVectorStore struct {
	hits       []vectorstore.Hit
	err        error
	lastSearch vectorstore.Search
	calls      int
}

func (fake *fakeVectorStore) Upsert(context.Context, domain.Corpus, []domain.Vector) error {
	return nil
}
func (fake *fakeVectorStore) Delete(context.Context, domain.Corpus, []string) error { return nil }
func (fake *fakeVectorStore) DropCorpus(context.Context, domain.Corpus) error       { return nil }
func (fake *fakeVectorStore) Backend() domain.Backend                               { return domain.BackendPgvector }

func (fake *fakeVectorStore) Search(_ context.Context, _ domain.Corpus, search vectorstore.Search) ([]vectorstore.Hit, error) {
	fake.calls++
	fake.lastSearch = search
	return fake.hits, fake.err
}

type fakeVectors struct{ store *fakeVectorStore }

func (fake fakeVectors) For(domain.Corpus) (vectorstore.Store, error) { return fake.store, nil }

// fakeEmbedder records what kind of input it was asked for, which is the
// difference between a question and a passage.
type fakeEmbedder struct {
	kinds []domain.InputKind
	err   error
}

func (fake *fakeEmbedder) Spec() domain.EmbeddingSpec { return testCorpus().Spec }

func (fake *fakeEmbedder) Embed(_ context.Context, kind domain.InputKind, texts []string) (embedding.Result, error) {
	if fake.err != nil {
		return embedding.Result{}, fake.err
	}
	fake.kinds = append(fake.kinds, kind)
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vectors[index] = []float32{1, 0, 0, 0}
	}
	return embedding.Result{Vectors: vectors, Usage: domain.Usage{EmbeddedTexts: len(texts), EmbeddingTokens: 5}}, nil
}

type fakeEmbedders struct{ embedder *fakeEmbedder }

func (fake fakeEmbedders) For(context.Context, domain.EmbeddingSpec) (embedding.Embedder, error) {
	return fake.embedder, nil
}

type fakeReranker struct {
	scores []Scored
	err    error
	calls  int
}

func (fake *fakeReranker) Rerank(context.Context, string, []string, int) ([]Scored, error) {
	fake.calls++
	return fake.scores, fake.err
}

func chunkIDs(matches []domain.Match) []string {
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match.Chunk.ID)
	}
	return ids
}

func TestHybridFusesBothRetrievers(t *testing.T) {
	t.Parallel()
	// The dense side likes a, b, c. The lexical side likes c, d, a. Chunk c is
	// respectable on both and should come out on top of chunks that only one
	// retriever liked — that is the whole reason for running both.
	chunks := &fakeChunks{lexical: []domain.ScoredChunk{
		{ChunkID: "chk_c", Score: 0.9}, {ChunkID: "chk_d", Score: 0.5}, {ChunkID: "chk_a", Score: 0.2},
	}}
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{
		{ChunkID: "chk_a", Score: 0.95}, {ChunkID: "chk_b", Score: 0.9}, {ChunkID: "chk_c", Score: 0.85},
	}}
	embedder := &fakeEmbedder{}
	retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{embedder}, nil, nil)

	results, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "how does rotation work?", TopK: 4})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results.Matches) != 4 {
		t.Fatalf("got %d matches, want 4", len(results.Matches))
	}
	top := results.Matches[0]
	if top.Chunk.ID != "chk_a" && top.Chunk.ID != "chk_c" {
		t.Fatalf("top match is %s; the chunks both retrievers found should lead", top.Chunk.ID)
	}
	// Both retrievers' opinions are kept, so a result can be explained.
	var withBoth int
	for _, match := range results.Matches {
		if match.Dense != nil && match.Lexical != nil {
			withBoth++
			if match.DenseRank == 0 || match.LexicalRank == 0 {
				t.Fatalf("match %s has both scores but a missing rank: %+v", match.Chunk.ID, match)
			}
		}
	}
	if withBoth != 2 {
		t.Fatalf("%d matches carry both retrievers' scores, want 2", withBoth)
	}
	// A chunk only one retriever found still carries only that one.
	for _, match := range results.Matches {
		if match.Chunk.ID == "chk_b" && match.Lexical != nil {
			t.Fatal("chk_b was never returned by the lexical side but carries a lexical score")
		}
	}
	if results.Usage.EmbeddedTexts != 1 {
		t.Fatalf("usage = %+v, want one embedded query", results.Usage)
	}
}

func TestFusedScoreIsOneWhenEveryRetrieverRanksItFirst(t *testing.T) {
	t.Parallel()
	// The score has to mean something fixed across queries, or min_score is
	// not a dial anyone can set.
	chunks := &fakeChunks{lexical: []domain.ScoredChunk{{ChunkID: "chk_a", Score: 0.9}}}
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{{ChunkID: "chk_a", Score: 0.95}}}
	retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)

	results, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := results.Matches[0].Score; got < 0.999 || got > 1.001 {
		t.Fatalf("a chunk ranked first by both retrievers scored %v, want 1", got)
	}
}

func TestQueryIsEmbeddedAsAQuestionNotAPassage(t *testing.T) {
	t.Parallel()
	embedder := &fakeEmbedder{}
	retriever := New(&fakeChunks{}, fakeVectors{&fakeVectorStore{}}, fakeEmbedders{embedder}, nil, nil)
	if _, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", Mode: domain.ModeDense}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(embedder.kinds) != 1 || embedder.kinds[0] != domain.InputQuery {
		t.Fatalf("the query was embedded as %v, want %q", embedder.kinds, domain.InputQuery)
	}
}

func TestModesRunOnlyWhatTheyNeed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode        domain.SearchMode
		wantDense   int
		wantLexical int
	}{
		{domain.ModeHybrid, 1, 1},
		{domain.ModeDense, 1, 0},
		// Lexical mode costs nothing per query because nothing is embedded.
		{domain.ModeLexical, 0, 1},
	}
	for _, testCase := range cases {
		t.Run(string(testCase.mode), func(t *testing.T) {
			t.Parallel()
			chunks := &fakeChunks{lexical: []domain.ScoredChunk{{ChunkID: "chk_a", Score: 1}}}
			vectors := &fakeVectorStore{hits: []vectorstore.Hit{{ChunkID: "chk_a", Score: 1}}}
			embedder := &fakeEmbedder{}
			retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{embedder}, nil, nil)
			if _, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", Mode: testCase.mode}); err != nil {
				t.Fatalf("Search: %v", err)
			}
			if vectors.calls != testCase.wantDense {
				t.Fatalf("dense ran %d times, want %d", vectors.calls, testCase.wantDense)
			}
			if chunks.lexicalCall != testCase.wantLexical {
				t.Fatalf("lexical ran %d times, want %d", chunks.lexicalCall, testCase.wantLexical)
			}
			if len(embedder.kinds) != testCase.wantDense {
				t.Fatalf("embedded %d times, want %d", len(embedder.kinds), testCase.wantDense)
			}
		})
	}
}

func TestDenseOnlyKeepsItsOwnSimilarity(t *testing.T) {
	t.Parallel()
	// With one retriever there is nothing to fuse, and a cosine similarity is
	// a better number than a position.
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{
		{ChunkID: "chk_a", Score: 0.91}, {ChunkID: "chk_b", Score: 0.42},
	}}
	retriever := New(&fakeChunks{}, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)
	results, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", Mode: domain.ModeDense})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if results.Matches[0].Score != 0.91 || results.Matches[1].Score != 0.42 {
		t.Fatalf("scores = %v, %v; want the similarities the store returned",
			results.Matches[0].Score, results.Matches[1].Score)
	}
}

func TestFilterReachesBothRetrievers(t *testing.T) {
	t.Parallel()
	// A filter that only narrowed one half would leak through the other.
	chunks := &fakeChunks{}
	vectors := &fakeVectorStore{}
	retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)
	filter := domain.Filter{Conditions: []domain.Condition{
		{Key: "team", Operator: domain.OperatorEqual, Values: []string{"platform"}},
	}}
	if _, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", Filter: filter}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(vectors.lastSearch.Filter.Conditions) != 1 {
		t.Fatal("the filter did not reach the vector store")
	}
	if len(chunks.lastFilter.Conditions) != 1 {
		t.Fatal("the filter did not reach the lexical search")
	}
}

func TestOverFetchAsksForMoreThanIsWanted(t *testing.T) {
	t.Parallel()
	// Fusing two lists of exactly TopK would discard the chunks only one
	// retriever ranked well, which is the point of running both.
	vectors := &fakeVectorStore{}
	retriever := New(&fakeChunks{}, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)
	if _, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", TopK: 5, OverFetch: 4}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if vectors.lastSearch.TopK != 20 {
		t.Fatalf("asked the store for %d candidates, want 20", vectors.lastSearch.TopK)
	}
}

func TestRerankReordersAndReplacesTheScore(t *testing.T) {
	t.Parallel()
	chunks := &fakeChunks{}
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{
		{ChunkID: "chk_a", Score: 0.9}, {ChunkID: "chk_b", Score: 0.8}, {ChunkID: "chk_c", Score: 0.7},
	}}
	// The cross-encoder disagrees with the fused order: the third candidate
	// is the one that actually answers the question.
	reranker := &fakeReranker{scores: []Scored{{Index: 2, Score: 0.99}, {Index: 0, Score: 0.4}, {Index: 1, Score: 0.1}}}
	retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, reranker, nil)

	results, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", Mode: domain.ModeDense, Rerank: true, TopK: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !results.Reranked {
		t.Fatal("Reranked is false after a reranker ran")
	}
	if got := chunkIDs(results.Matches); got[0] != "chk_c" {
		t.Fatalf("order = %v, want the reranker's choice first", got)
	}
	if results.Matches[0].Rerank == nil || *results.Matches[0].Rerank != 0.99 {
		t.Fatalf("the rerank score was not kept: %+v", results.Matches[0])
	}
	if results.Matches[0].Score != 0.99 {
		t.Fatalf("Score = %v, want the reranker's relevance", results.Matches[0].Score)
	}
	// The retriever's own opinion is kept alongside, so a result stays
	// explainable after reranking.
	if results.Matches[0].Dense == nil {
		t.Fatal("the dense score was dropped when reranking")
	}
}

func TestRerankIsOptInAndNotUsedOtherwise(t *testing.T) {
	t.Parallel()
	reranker := &fakeReranker{}
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{{ChunkID: "chk_a", Score: 0.9}}}
	retriever := New(&fakeChunks{}, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, reranker, nil)
	results, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", Mode: domain.ModeDense})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if reranker.calls != 0 {
		t.Fatal("the reranker ran without being asked for")
	}
	if results.Reranked {
		t.Fatal("Reranked is true without a reranker having run")
	}
}

func TestRerankFailureFallsBackToTheFusedOrder(t *testing.T) {
	t.Parallel()
	// A reranker that is down must not take the whole query with it: fused
	// order is worse than reranked order and far better than an error.
	reranker := &fakeReranker{err: errors.New("throttled")}
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{
		{ChunkID: "chk_a", Score: 0.9}, {ChunkID: "chk_b", Score: 0.8},
	}}
	retriever := New(&fakeChunks{}, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, reranker, nil)
	results, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", Mode: domain.ModeDense, Rerank: true})
	if err != nil {
		t.Fatalf("Search returned an error when only the reranker failed: %v", err)
	}
	if results.Reranked {
		t.Fatal("Reranked is true after the reranker failed")
	}
	if got := chunkIDs(results.Matches); got[0] != "chk_a" {
		t.Fatalf("order = %v, want the fused order", got)
	}
}

func TestRerankRefusesAnIndexOutsideTheBatch(t *testing.T) {
	t.Parallel()
	// An out-of-range index would otherwise read the wrong chunk and attach a
	// citation to a passage the answer never saw.
	reranker := &fakeReranker{scores: []Scored{{Index: 9, Score: 1}}}
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{{ChunkID: "chk_a", Score: 0.9}}}
	retriever := New(&fakeChunks{}, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, reranker, nil)
	results, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", Mode: domain.ModeDense, Rerank: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if results.Reranked {
		t.Fatal("a nonsense rerank response was accepted")
	}
}

func TestDiversifySuppressesNearDuplicates(t *testing.T) {
	t.Parallel()
	duplicate := "Credentials must be rotated every ninety days without exception."
	chunks := &fakeChunks{texts: map[string]string{
		"chk_a": duplicate,
		"chk_b": duplicate + " ",
		"chk_c": duplicate,
		"chk_d": "Holidays accrue monthly and carry over into the following year.",
	}}
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{
		{ChunkID: "chk_a", Score: 0.95}, {ChunkID: "chk_b", Score: 0.94},
		{ChunkID: "chk_c", Score: 0.93}, {ChunkID: "chk_d", Score: 0.4},
	}}
	retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)

	// Pure relevance takes the three copies of the same passage.
	plain, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", Mode: domain.ModeDense, TopK: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := chunkIDs(plain.Matches); got[1] == "chk_d" {
		t.Fatalf("relevance-only returned %v; the near-duplicate should have won on score", got)
	}

	// Asking for spread brings in the passage that says something else.
	diverse, err := retriever.Search(context.Background(), testCorpus(),
		domain.Query{Text: "q", Mode: domain.ModeDense, TopK: 2, Diversity: pointerTo(0.3)})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := chunkIDs(diverse.Matches)
	if len(got) != 2 {
		t.Fatalf("got %d matches, want 2", len(got))
	}
	if got[0] != "chk_a" {
		t.Fatalf("diversification changed the top result to %s; it should still be the best match", got[0])
	}
	if got[1] != "chk_d" {
		t.Fatalf("second result is %s, want the passage that is not a duplicate", got[1])
	}
}

func TestMinScoreDropsWeakMatches(t *testing.T) {
	t.Parallel()
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{
		{ChunkID: "chk_a", Score: 0.8}, {ChunkID: "chk_b", Score: 0.2},
	}}
	retriever := New(&fakeChunks{}, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)
	results, err := retriever.Search(context.Background(), testCorpus(),
		domain.Query{Text: "q", Mode: domain.ModeDense, MinScore: 0.5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Answering nothing is better than answering from a passage that does not
	// contain the answer.
	if len(results.Matches) != 1 || results.Matches[0].Chunk.ID != "chk_a" {
		t.Fatalf("matches = %v", chunkIDs(results.Matches))
	}
}

func TestChunkDeletedBetweenIndexAndReadIsDropped(t *testing.T) {
	t.Parallel()
	// The index can be briefly ahead of the source of truth. That should cost
	// one result, not the whole query.
	chunks := &fakeChunks{missing: map[string]bool{"chk_b": true}}
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{
		{ChunkID: "chk_a", Score: 0.9}, {ChunkID: "chk_b", Score: 0.85}, {ChunkID: "chk_c", Score: 0.8},
	}}
	retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)
	results, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", Mode: domain.ModeDense})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := chunkIDs(results.Matches); len(got) != 2 || got[0] != "chk_a" || got[1] != "chk_c" {
		t.Fatalf("matches = %v, want chk_a and chk_c", got)
	}
}

func TestEmptyCorpusReturnsNothingNotAnError(t *testing.T) {
	t.Parallel()
	retriever := New(&fakeChunks{}, fakeVectors{&fakeVectorStore{}}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)
	results, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results.Matches) != 0 {
		t.Fatalf("got %d matches from an empty corpus", len(results.Matches))
	}
}

func TestSearchRejectsAnUnusableQuery(t *testing.T) {
	t.Parallel()
	retriever := New(&fakeChunks{}, fakeVectors{&fakeVectorStore{}}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)
	if _, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "   "}); err == nil {
		t.Fatal("an empty query was accepted")
	}
}

func TestResultsAreDeterministic(t *testing.T) {
	t.Parallel()
	// Ties are common when only one retriever found a chunk. An unstable
	// order there makes a result set look like it is changing when it is not.
	chunks := &fakeChunks{lexical: []domain.ScoredChunk{
		{ChunkID: "chk_d", Score: 1}, {ChunkID: "chk_e", Score: 1},
	}}
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{
		{ChunkID: "chk_a", Score: 0.9}, {ChunkID: "chk_b", Score: 0.9}, {ChunkID: "chk_c", Score: 0.9},
	}}
	retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)
	first, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", TopK: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := strings.Join(chunkIDs(first.Matches), ",")
	for attempt := range 20 {
		again, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q", TopK: 5})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got := strings.Join(chunkIDs(again.Matches), ","); got != want {
			t.Fatalf("attempt %d returned %s, want %s", attempt, got, want)
		}
	}
}

func TestJaccard(t *testing.T) {
	t.Parallel()
	same := tokenSet("credentials rotate every ninety days")
	if got := jaccard(same, same); got != 1 {
		t.Fatalf("a set against itself scored %v, want 1", got)
	}
	other := tokenSet("holidays accrue monthly and carry over")
	if got := jaccard(same, other); got != 0 {
		t.Fatalf("unrelated text scored %v, want 0", got)
	}
	if got := jaccard(same, map[string]struct{}{}); got != 0 {
		t.Fatalf("an empty set scored %v, want 0", got)
	}
	partial := tokenSet("credentials rotate every year")
	if got := jaccard(same, partial); got <= 0 || got >= 1 {
		t.Fatalf("partly shared text scored %v, want something between 0 and 1", got)
	}
}

func TestFuseRewardsAgreement(t *testing.T) {
	t.Parallel()
	// Fusion's whole purpose: a chunk both retrievers placed respectably
	// beats one a single retriever placed first.
	combined := fuse([]ranked{
		{name: denseRetriever, hits: []domain.ScoredChunk{
			{ChunkID: "only-dense"}, {ChunkID: "both"}, {ChunkID: "filler"},
		}},
		{name: lexicalRetriever, hits: []domain.ScoredChunk{
			{ChunkID: "only-lexical"}, {ChunkID: "both"},
		}},
	})
	if combined[0].chunkID != "both" {
		t.Fatalf("fusion put %q first; the chunk both retrievers found should lead", combined[0].chunkID)
	}
	for _, entry := range combined {
		if entry.score < 0 || entry.score > 1 {
			t.Fatalf("%s scored %v, outside [0,1]", entry.chunkID, entry.score)
		}
	}
}

func TestFuseOfNothing(t *testing.T) {
	t.Parallel()
	if got := fuse(nil); got != nil {
		t.Fatalf("fuse(nil) = %v", got)
	}
	if got := fuse([]ranked{{name: denseRetriever}}); len(got) != 0 {
		t.Fatalf("fuse of an empty list = %v", got)
	}
}

func TestDenseFailureIsReported(t *testing.T) {
	t.Parallel()
	// Unlike a reranker, a retriever that fails means the answer would be
	// built from the wrong passages. That has to surface.
	vectors := &fakeVectorStore{err: fmt.Errorf("index unavailable")}
	retriever := New(&fakeChunks{}, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)
	if _, err := retriever.Search(context.Background(), testCorpus(), domain.Query{Text: "q"}); err == nil {
		t.Fatal("a failing vector store was swallowed")
	}
}

func pointerTo[T any](value T) *T { return &value }

// TestRerankPoolLargerThanTheCeiling guards a silent failure: a big candidate
// pool used to exceed what the cross-encoder accepts, which errored, was
// swallowed as a warning, and left rerank:true doing nothing at all.
func TestRerankPoolLargerThanTheCeiling(t *testing.T) {
	t.Parallel()
	const candidates = MaxRerankPassages + 40
	hits := make([]vectorstore.Hit, 0, candidates)
	for index := range candidates {
		hits = append(hits, vectorstore.Hit{ChunkID: fmt.Sprintf("chk_%03d", index), Score: 1 - float64(index)/1000})
	}
	var asked int
	reranker := &fakeRerankerCounting{onRerank: func(passages []string) ([]Scored, error) {
		asked = len(passages)
		if len(passages) > MaxRerankPassages {
			return nil, fmt.Errorf("too many passages: %d", len(passages))
		}
		// Reverse the order, so the effect is unmistakable.
		scored := make([]Scored, 0, len(passages))
		for index := range passages {
			scored = append(scored, Scored{Index: index, Score: float64(index) / 1000})
		}
		return scored, nil
	}}
	retriever := New(&fakeChunks{}, fakeVectors{&fakeVectorStore{hits: hits}},
		fakeEmbedders{&fakeEmbedder{}}, reranker, nil)

	results, err := retriever.Search(context.Background(), testCorpus(),
		domain.Query{Text: "q", Mode: domain.ModeDense, TopK: 100, OverFetch: 2, Rerank: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !results.Reranked {
		t.Fatalf("a %d-candidate pool defeated reranking entirely", candidates)
	}
	if asked > MaxRerankPassages {
		t.Fatalf("sent %d passages to the reranker, over the ceiling of %d", asked, MaxRerankPassages)
	}
	// The reranker put the last of its batch first.
	if results.Matches[0].Chunk.ID != fmt.Sprintf("chk_%03d", MaxRerankPassages-1) {
		t.Fatalf("top match is %s; the reranker's order was not applied", results.Matches[0].Chunk.ID)
	}
	// The tail it never saw is still there, below what it judged.
	if len(results.Matches) != 100 {
		t.Fatalf("got %d matches, want the full top_k", len(results.Matches))
	}
}

type fakeRerankerCounting struct {
	onRerank func([]string) ([]Scored, error)
}

func (fake *fakeRerankerCounting) Rerank(_ context.Context, _ string, passages []string, _ int) ([]Scored, error) {
	return fake.onRerank(passages)
}

// TestFusionIgnoresARetrieverThatFoundNothing guards a threshold bug: a
// lexical index matching nothing used to halve every hybrid score, so
// min_score rejected results the same query in dense mode passed.
func TestFusionIgnoresARetrieverThatFoundNothing(t *testing.T) {
	t.Parallel()
	chunks := &fakeChunks{} // the lexical side matches nothing
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{{ChunkID: "chk_a", Score: 0.9}}}
	retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)

	results, err := retriever.Search(context.Background(), testCorpus(),
		domain.Query{Text: "q", Mode: domain.ModeHybrid, MinScore: 0.9})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results.Matches) != 1 {
		t.Fatalf("got %d matches; a silent lexical index must not halve the score and trip min_score",
			len(results.Matches))
	}
	if got := results.Matches[0].Score; got < 0.999 {
		t.Fatalf("score = %v, want 1: only the retriever that answered should set the scale", got)
	}
}

func TestDiversityZeroIsHonoured(t *testing.T) {
	t.Parallel()
	// 0 means maximum spread. If it were indistinguishable from unset it
	// would become 1, which is the exact opposite.
	duplicate := "Credentials must be rotated every ninety days without exception."
	chunks := &fakeChunks{texts: map[string]string{
		"chk_a": duplicate, "chk_b": duplicate, "chk_c": "Holidays accrue monthly and carry over.",
	}}
	vectors := &fakeVectorStore{hits: []vectorstore.Hit{
		{ChunkID: "chk_a", Score: 0.95}, {ChunkID: "chk_b", Score: 0.94}, {ChunkID: "chk_c", Score: 0.1},
	}}
	retriever := New(chunks, fakeVectors{vectors}, fakeEmbedders{&fakeEmbedder{}}, nil, nil)
	results, err := retriever.Search(context.Background(), testCorpus(),
		domain.Query{Text: "q", Mode: domain.ModeDense, TopK: 2, Diversity: pointerTo(0.0)})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := chunkIDs(results.Matches); got[1] != "chk_c" {
		t.Fatalf("matches = %v; maximum spread should have taken the passage that is not a duplicate", got)
	}
}
