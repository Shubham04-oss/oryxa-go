// Package retrieval finds the passages a question should be answered from.
//
// It runs two retrievers and fuses them, because they fail differently.
// Vectors miss an exact identifier, error code or surname that appears
// verbatim in one chunk; full-text search misses a question worded nothing
// like the passage that answers it. Running both and fusing by rank covers
// each one's blind spot without needing their scores to mean the same thing.
//
// After fusion the candidates can be reranked by a cross-encoder, which reads
// the question and the passage together and is far better at telling a
// passage that is about the subject from one that answers the question. It is
// opt-in because it is the largest latency cost in the path.
package retrieval

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/embedding"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

const (
	denseRetriever   = "dense"
	lexicalRetriever = "lexical"
)

// MaxRerankPassages is how many candidates go to the cross-encoder in one
// call. A larger candidate pool is reranked down to this and the rest keeps
// its fused order.
const MaxRerankPassages = 100

// Chunks is what retrieval needs from PostgreSQL: the lexical half of the
// search, and the text and provenance of whatever either half found.
type Chunks interface {
	LexicalSearch(ctx context.Context, corpus domain.Corpus, text string, limit int, filter domain.Filter) ([]domain.ScoredChunk, error)
	Chunks(ctx context.Context, organizationID, corpusID string, ids []string) ([]domain.Chunk, error)
	DocumentRefs(ctx context.Context, organizationID string, ids []string) (map[string]domain.Ref, error)
}

// Vectors routes a corpus to the store holding its vectors.
type Vectors interface {
	For(corpus domain.Corpus) (vectorstore.Store, error)
}

// Embedders hands out the embedder for a corpus's pinned spec.
type Embedders interface {
	For(ctx context.Context, spec domain.EmbeddingSpec) (embedding.Embedder, error)
}

// Results is what one search produced.
type Results struct {
	Matches []domain.Match
	Usage   domain.Usage
	// Reranked says whether a cross-encoder ordered these. It is reported
	// rather than assumed, because a reranker that is configured but failed
	// leaves results that are still usable and a caller that should know.
	Reranked bool
}

type Retriever struct {
	chunks        Chunks
	vectors       Vectors
	embedders     Embedders
	reranker      Reranker
	knowledgeBase *KnowledgeBase
	logger        *slog.Logger
}

// New builds a retriever. knowledgeBase is nil in a deployment with no
// Bedrock knowledge base configured, in which case a query against a
// bedrock-kb corpus fails by name rather than reaching a nil client — the
// same shape of failure Vectors.For already gives a corpus on a backend this
// deployment cannot serve.
func New(chunks Chunks, vectors Vectors, embedders Embedders, reranker Reranker, knowledgeBase *KnowledgeBase, logger *slog.Logger) *Retriever {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Retriever{
		chunks: chunks, vectors: vectors, embedders: embedders, reranker: reranker,
		knowledgeBase: knowledgeBase, logger: logger,
	}
}

// Search runs a query against one corpus and returns the passages to answer
// from, best first.
func (retriever *Retriever) Search(ctx context.Context, corpus domain.Corpus, query domain.Query) (Results, error) {
	query, err := query.Normalize()
	if err != nil {
		return Results{}, err
	}
	// A bedrock-kb corpus has no chunk rows to run a lexical search over and
	// no vectorstore.Store to hold a dense one: Bedrock did that chunking and
	// embedding itself, so this takes a wholly different, shorter path to a
	// Results rather than the dense-then-lexical-then-fuse below.
	if corpus.Backend == domain.BackendBedrockKB {
		return retriever.searchKnowledgeBase(ctx, corpus, query)
	}
	var (
		results    Results
		candidates = query.Candidates()
		lists      []ranked
	)

	if query.Mode.UsesDense() {
		hits, usage, err := retriever.dense(ctx, corpus, query, candidates)
		if err != nil {
			return Results{}, err
		}
		results.Usage.Add(usage)
		lists = append(lists, ranked{name: denseRetriever, hits: hits})
	}
	if query.Mode.UsesLexical() {
		hits, err := retriever.chunks.LexicalSearch(ctx, corpus, query.Text, candidates, query.Filter)
		if err != nil {
			return Results{}, err
		}
		lists = append(lists, ranked{name: lexicalRetriever, hits: hits})
	}

	combined := fuse(lists)
	if len(combined) == 0 {
		return results, nil
	}
	// A single retriever has a score that already means something, and fusion
	// would throw that away in favour of a position. Dense keeps its
	// similarity; lexical is rank-derived either way, so fusion's own scale is
	// the honest one for it.
	if len(lists) == 1 && lists[0].name == denseRetriever {
		for index := range combined {
			if combined[index].dense != nil {
				combined[index].score = *combined[index].dense
			}
		}
		sortFused(combined)
	}

	matches, err := retriever.hydrate(ctx, corpus, combined)
	if err != nil {
		return Results{}, err
	}
	if len(matches) == 0 {
		return results, nil
	}

	if query.Rerank && retriever.reranker != nil {
		reranked, err := retriever.rerank(ctx, query, matches)
		if err != nil {
			// A reranker that is down must not take the whole query with it.
			// Fused order is worse than reranked order and much better than
			// an error page.
			retriever.logger.WarnContext(ctx, "reranking failed; falling back to the fused order",
				"corpus_id", corpus.ID, "error", err)
		} else {
			matches, results.Reranked = reranked, true
		}
	}

	results.Matches = retriever.finalize(matches, query)
	return results, nil
}

// finalize applies what a query asked for after the candidates are found,
// regardless of which path found them: spread them by diversity, cut to
// top_k, and drop anything under min_score. Writing it once here is what
// makes those three settings mean the same thing whichever backend a corpus
// happens to be on.
func (retriever *Retriever) finalize(matches []domain.Match, query domain.Query) []domain.Match {
	// Diversification runs on more candidates than are wanted, so it has
	// something to choose between, and the cut to TopK happens after.
	matches = diversify(matches, *query.Diversity, query.TopK)
	if len(matches) > query.TopK {
		matches = matches[:query.TopK]
	}
	if query.MinScore > 0 {
		kept := matches[:0]
		for _, match := range matches {
			if match.Score >= query.MinScore {
				kept = append(kept, match)
			}
		}
		matches = kept
	}
	return matches
}

// searchKnowledgeBase answers a query for a corpus whose vectors live in a
// Bedrock knowledge base. It asks Bedrock for this one corpus's passages,
// scoped by TenantFilter, then hydrates each with its document's title and
// source the same way the pgvector and S3 Vectors path does — from
// PostgreSQL, which still holds every document's row for a bedrock-kb corpus
// even though it holds no chunks for one.
func (retriever *Retriever) searchKnowledgeBase(ctx context.Context, corpus domain.Corpus, query domain.Query) (Results, error) {
	if retriever.knowledgeBase == nil {
		return Results{}, fmt.Errorf("this deployment cannot reach its knowledge base: %w", domain.ErrInvalid)
	}
	passages, err := retriever.knowledgeBase.retrieve(ctx, corpus.OrganizationID, []string{corpus.ID}, query, query.Candidates())
	if err != nil {
		return Results{}, err
	}
	if len(passages) == 0 {
		return Results{}, nil
	}
	documentIDs := make([]string, 0, len(passages))
	seen := make(map[string]bool, len(passages))
	for _, passage := range passages {
		if !seen[passage.documentID] {
			seen[passage.documentID] = true
			documentIDs = append(documentIDs, passage.documentID)
		}
	}
	refs, err := retriever.chunks.DocumentRefs(ctx, corpus.OrganizationID, documentIDs)
	if err != nil {
		return Results{}, err
	}
	matches := make([]domain.Match, 0, len(passages))
	for _, passage := range passages {
		matches = append(matches, domain.Match{
			Chunk: domain.Chunk{
				ID: kbChunkID(passage.documentID, passage.text), DocumentID: passage.documentID,
				CorpusID: corpus.ID, OrganizationID: corpus.OrganizationID,
				Text: passage.text, SHA256: domain.Hash(passage.text),
			},
			Document: refs[passage.documentID],
			Score:    passage.score,
		})
	}
	// Bedrock's own reranking, when it ran, already reordered and scored
	// these; the cross-encoder Reranker below is never invoked for a
	// knowledge base corpus, since it has nothing here of its own to send it.
	reranked := query.Rerank && retriever.knowledgeBase.reranks()
	return Results{Matches: retriever.finalize(matches, query), Reranked: reranked}, nil
}

// dense embeds the question and asks the vector store.
func (retriever *Retriever) dense(ctx context.Context, corpus domain.Corpus, query domain.Query, candidates int) ([]domain.ScoredChunk, domain.Usage, error) {
	embedder, err := retriever.embedders.For(ctx, corpus.Spec)
	if err != nil {
		return nil, domain.Usage{}, err
	}
	// The query kind is what makes this a question rather than a passage. The
	// same words embedded as a document land somewhere else entirely.
	vector, usage, err := embedding.EmbedOne(ctx, embedder, domain.InputQuery, query.Text)
	if err != nil {
		return nil, domain.Usage{}, fmt.Errorf("embed the query: %w", err)
	}
	store, err := retriever.vectors.For(corpus)
	if err != nil {
		return nil, usage, err
	}
	hits, err := store.Search(ctx, corpus, vectorstore.Search{
		Vector: vector, TopK: candidates, Filter: query.Filter,
	})
	if err != nil {
		return nil, usage, err
	}
	scored := make([]domain.ScoredChunk, 0, len(hits))
	for _, hit := range hits {
		scored = append(scored, domain.ScoredChunk{ChunkID: hit.ChunkID, Score: hit.Score})
	}
	return scored, usage, nil
}

// hydrate turns fused chunk ids into matches with their text and provenance.
//
// A chunk that fusion named but PostgreSQL no longer holds is dropped rather
// than reported. That happens when a document was deleted between the index
// answering and this read, and an index that is briefly ahead of the source of
// truth should not turn into an error for the person asking a question.
func (retriever *Retriever) hydrate(ctx context.Context, corpus domain.Corpus, combined []fused) ([]domain.Match, error) {
	ids := make([]string, 0, len(combined))
	for _, entry := range combined {
		ids = append(ids, entry.chunkID)
	}
	chunks, err := retriever.chunks.Chunks(ctx, corpus.OrganizationID, corpus.ID, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]domain.Chunk, len(chunks))
	documentIDs := make([]string, 0, len(chunks))
	seenDocument := map[string]bool{}
	for _, chunk := range chunks {
		byID[chunk.ID] = chunk
		if !seenDocument[chunk.DocumentID] {
			seenDocument[chunk.DocumentID] = true
			documentIDs = append(documentIDs, chunk.DocumentID)
		}
	}
	refs, err := retriever.chunks.DocumentRefs(ctx, corpus.OrganizationID, documentIDs)
	if err != nil {
		return nil, err
	}

	matches := make([]domain.Match, 0, len(combined))
	for _, entry := range combined {
		chunk, found := byID[entry.chunkID]
		if !found {
			continue
		}
		matches = append(matches, domain.Match{
			Chunk:       chunk,
			Document:    refs[chunk.DocumentID],
			Score:       entry.score,
			Dense:       entry.dense,
			Lexical:     entry.lexical,
			DenseRank:   entry.denseRank,
			LexicalRank: entry.lexicalRank,
		})
	}
	return matches, nil
}

// rerank reorders candidates with a cross-encoder and replaces the fused score
// with its relevance, which is a better number and already on a fixed scale.
func (retriever *Retriever) rerank(ctx context.Context, query domain.Query, matches []domain.Match) ([]domain.Match, error) {
	// A cross-encoder reads every passage against the question, so there is a
	// ceiling on how many go in one call. Rerank the best candidates and leave
	// the tail in fused order below them: a pool larger than the ceiling would
	// otherwise fail the whole rerank and silently return the fused order for
	// everything, which is what a large top_k would have quietly done.
	tail := []domain.Match(nil)
	if len(matches) > MaxRerankPassages {
		tail = matches[MaxRerankPassages:]
		matches = matches[:MaxRerankPassages]
	}
	passages := make([]string, 0, len(matches))
	for _, match := range matches {
		// The heading trail goes to the reranker for the same reason it is
		// embedded: a passage read alone is often missing its subject.
		passages = append(passages, match.Chunk.EmbedText())
	}
	scores, err := retriever.reranker.Rerank(ctx, query.Text, passages, len(matches))
	if err != nil {
		return nil, err
	}
	reordered := make([]domain.Match, 0, len(matches))
	for _, scored := range scores {
		if scored.Index < 0 || scored.Index >= len(matches) {
			return nil, fmt.Errorf("the reranker returned index %d for %d passages", scored.Index, len(matches))
		}
		match := matches[scored.Index]
		relevance := scored.Score
		match.Rerank = &relevance
		match.Score = relevance
		reordered = append(reordered, match)
	}
	sort.SliceStable(reordered, func(first, second int) bool {
		return reordered[first].Score > reordered[second].Score
	})
	// The tail was not judged by the cross-encoder, so it keeps its fused
	// score and sits below everything that was.
	return append(reordered, tail...), nil
}
