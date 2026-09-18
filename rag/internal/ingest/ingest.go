// Package ingest turns an accepted document into searchable chunks.
//
// It runs as a worker over documents already recorded in PostgreSQL, not in
// the request that submitted them. Two things follow from that and both are
// deliberate. A submission returns as soon as the text is safely stored, so a
// caller is never waiting on an embedding provider; and the text is fetched
// while the caller's credentials are still to hand, so this worker never needs
// to act as anyone.
//
// The expensive part is embedding, and most of the work here is about not
// doing it: a re-delivered document with the same bytes is recognised before
// this package is reached, and a changed document only re-embeds the chunks
// whose text actually changed.
package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	rageventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/rag/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/chunking"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/embedding"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

// Store is what ingest needs from PostgreSQL.
type Store interface {
	Corpus(ctx context.Context, organizationID, corpusID string) (domain.Corpus, error)
	DocumentBody(ctx context.Context, documentID string) (string, error)
	SyncChunks(ctx context.Context, document domain.Document, chunks []domain.Chunk, at time.Time) (domain.ChunkSync, error)
	MarkChunksEmbedded(ctx context.Context, chunkIDs []string, at time.Time) error
	ClaimDocuments(ctx context.Context, limit int, lease time.Duration) ([]domain.Document, error)
	MarkDocumentReady(ctx context.Context, documentID string, chunkCount int, at time.Time, event domain.Event) error
	MarkDocumentFailed(ctx context.Context, documentID, reason string, at time.Time, event domain.Event) error
	ReleaseDocument(ctx context.Context, documentID string) error
}

// Vectors routes a corpus to the store holding its vectors.
type Vectors interface {
	For(corpus domain.Corpus) (vectorstore.Store, error)
}

// Embedders hands out the embedder for a corpus's pinned spec.
type Embedders interface {
	For(ctx context.Context, spec domain.EmbeddingSpec) (embedding.Embedder, error)
}

// MaxAttempts is how many times a document is tried before it is left failed.
//
// A failure that repeats is usually the document rather than the weather: a
// model that will not take it, or a corpus whose spec no longer resolves.
// Retrying forever would spend money on the same rejection and hide the
// document behind a queue that never drains.
const MaxAttempts = 5

// Options tune the worker loop.
type Options struct {
	// Batch is how many documents one pass claims.
	Batch int
	// Lease is how long a claim is held before another worker may take the
	// document. It bounds how long a dead worker's work stays stuck.
	Lease time.Duration
	// Interval is how long the loop waits when there was nothing to do.
	Interval time.Duration
}

func (options Options) withDefaults() Options {
	if options.Batch <= 0 {
		options.Batch = 4
	}
	if options.Lease <= 0 {
		options.Lease = 5 * time.Minute
	}
	if options.Interval <= 0 {
		options.Interval = 2 * time.Second
	}
	return options
}

// Result is what indexing one document did, and what it cost.
type Result struct {
	Sync  domain.ChunkSync
	Usage domain.Usage
}

type Pipeline struct {
	store     Store
	vectors   Vectors
	embedders Embedders
	options   Options
	logger    *slog.Logger
	now       func() time.Time
}

func New(store Store, vectors Vectors, embedders Embedders, options Options, logger *slog.Logger) *Pipeline {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Pipeline{
		store: store, vectors: vectors, embedders: embedders,
		options: options.withDefaults(), logger: logger, now: time.Now,
	}
}

// Run works the queue until the context ends.
func (pipeline *Pipeline) Run(ctx context.Context) {
	for {
		worked, err := pipeline.once(ctx)
		if err != nil && ctx.Err() == nil {
			pipeline.logger.ErrorContext(ctx, "claiming documents to index failed", "error", err)
		}
		if ctx.Err() != nil {
			return
		}
		if worked {
			// There may be more waiting; only idle when the queue is empty.
			continue
		}
		timer := time.NewTimer(pipeline.options.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (pipeline *Pipeline) once(ctx context.Context) (bool, error) {
	documents, err := pipeline.store.ClaimDocuments(ctx, pipeline.options.Batch, pipeline.options.Lease)
	if err != nil {
		return false, err
	}
	if len(documents) == 0 {
		return false, nil
	}
	for _, document := range documents {
		if ctx.Err() != nil {
			// Shutting down mid-document: put it back rather than leaving it
			// leased until the lease expires.
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := pipeline.store.ReleaseDocument(releaseCtx, document.ID); err != nil {
				pipeline.logger.Error("releasing a document on shutdown failed", "document_id", document.ID, "error", err)
			}
			cancel()
			continue
		}
		if _, err := pipeline.Index(ctx, document); err != nil {
			pipeline.logger.ErrorContext(ctx, "indexing a document failed",
				"document_id", document.ID, "corpus_id", document.CorpusID, "attempt", document.Attempts, "error", err)
		}
	}
	return true, nil
}

// Index cuts a document into chunks, embeds what changed, and writes the
// vectors. It records the outcome either way, so a document is never left
// looking like it is still being worked on.
func (pipeline *Pipeline) Index(ctx context.Context, document domain.Document) (Result, error) {
	result, err := pipeline.index(ctx, document)
	if err == nil {
		return result, nil
	}
	// A cancelled run is not the document's fault; put it back untouched.
	if ctx.Err() != nil {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if releaseErr := pipeline.store.ReleaseDocument(releaseCtx, document.ID); releaseErr != nil {
			pipeline.logger.Error("releasing a cancelled document failed", "document_id", document.ID, "error", releaseErr)
		}
		return Result{}, err
	}
	if document.Attempts < MaxAttempts && !errors.Is(err, domain.ErrInvalid) {
		// Worth another go later. Release it rather than failing it, so the
		// attempt count is what decides when to give up.
		if releaseErr := pipeline.store.ReleaseDocument(ctx, document.ID); releaseErr != nil {
			pipeline.logger.ErrorContext(ctx, "releasing a document for retry failed", "document_id", document.ID, "error", releaseErr)
		}
		return Result{}, err
	}
	failure, buildErr := pipeline.event(rageventsv1.DocumentFailedDetailType, rageventsv1.DocumentFailed{
		DocumentID: document.ID, CorpusID: document.CorpusID, OrganizationID: document.OrganizationID,
		Reason: err.Error(), Attempts: document.Attempts, FailedAt: pipeline.now().UTC(),
	})
	if buildErr != nil {
		return Result{}, errors.Join(err, buildErr)
	}
	if markErr := pipeline.store.MarkDocumentFailed(ctx, document.ID, err.Error(), pipeline.now().UTC(), failure); markErr != nil {
		return Result{}, errors.Join(err, markErr)
	}
	return Result{}, err
}

func (pipeline *Pipeline) index(ctx context.Context, document domain.Document) (Result, error) {
	corpus, err := pipeline.store.Corpus(ctx, document.OrganizationID, document.CorpusID)
	if err != nil {
		return Result{}, fmt.Errorf("read the corpus: %w", err)
	}
	body, err := pipeline.store.DocumentBody(ctx, document.ID)
	if err != nil {
		return Result{}, fmt.Errorf("read the document text: %w", err)
	}

	// The body was normalized before it was stored, so the offsets a chunk
	// carries address exactly this text.
	pieces := chunking.Cut(body, document.ContentType, corpus.Chunking)
	if len(pieces) == 0 {
		return Result{}, fmt.Errorf("the document produced no chunks: %w", domain.ErrInvalid)
	}
	chunks := make([]domain.Chunk, 0, len(pieces))
	for ordinal, piece := range pieces {
		chunks = append(chunks, domain.Chunk{
			DocumentID:     document.ID,
			CorpusID:       document.CorpusID,
			OrganizationID: document.OrganizationID,
			Ordinal:        ordinal,
			Text:           piece.Text,
			TokenCount:     piece.TokenCount,
			HeadingPath:    piece.HeadingPath,
			StartByte:      piece.StartByte,
			EndByte:        piece.EndByte,
			SHA256:         domain.Hash(piece.Text),
			Metadata:       document.Metadata,
		})
	}

	at := pipeline.now().UTC()
	sync, err := pipeline.store.SyncChunks(ctx, document, chunks, at)
	if err != nil {
		return Result{}, err
	}

	vectors, err := pipeline.vectors.For(corpus)
	if err != nil {
		return Result{}, err
	}
	// Removals go first. A chunk whose text changed keeps neither its row nor
	// its vector, and leaving the old vector behind would let a search return
	// a passage the document no longer contains.
	if len(sync.Removed) > 0 {
		if err := vectors.Delete(ctx, corpus, sync.Removed); err != nil {
			return Result{}, err
		}
	}

	result := Result{Sync: sync}
	if len(sync.Fresh) > 0 {
		usage, err := pipeline.embed(ctx, corpus, document, vectors, sync.Fresh, at)
		if err != nil {
			return Result{}, err
		}
		result.Usage = usage
	}

	indexed, err := pipeline.event(rageventsv1.DocumentIndexedDetailType, rageventsv1.DocumentIndexed{
		DocumentID: document.ID, CorpusID: document.CorpusID, OrganizationID: document.OrganizationID,
		ChunkCount: len(chunks), EmbeddedChunks: len(sync.Fresh), ReusedChunks: len(sync.Reused),
		RemovedChunks: len(sync.Removed), Provider: string(corpus.Spec.Provider), Model: corpus.Spec.Model,
		Tokens: result.Usage.EmbeddingTokens, IndexedAt: at,
	})
	if err != nil {
		return Result{}, err
	}
	if err := pipeline.store.MarkDocumentReady(ctx, document.ID, len(chunks), at, indexed); err != nil {
		return Result{}, err
	}
	pipeline.logger.InfoContext(ctx, "indexed a document",
		"document_id", document.ID, "corpus_id", corpus.ID, "chunks", len(chunks),
		"embedded", len(sync.Fresh), "reused", len(sync.Reused), "removed", len(sync.Removed))
	return result, nil
}

// sliceChunks is how many changed chunks are embedded and written at a time. A
// document of thousands of chunks takes many embedding calls, and one that
// fails near the end should not cost the rest: each slice is kept as soon as
// its vectors are written, so the retry reuses it rather than paying again.
const sliceChunks = 256

// embed turns the changed chunks into vectors and writes them, a slice at a
// time.
func (pipeline *Pipeline) embed(ctx context.Context, corpus domain.Corpus, document domain.Document, store vectorstore.Store, chunks []domain.Chunk, at time.Time) (domain.Usage, error) {
	embedder, err := pipeline.embedders.For(ctx, corpus.Spec)
	if err != nil {
		return domain.Usage{}, err
	}
	var usage domain.Usage
	for start := 0; start < len(chunks); start += sliceChunks {
		sliceUsage, err := pipeline.embedSlice(ctx, embedder, corpus, document, store, chunks[start:min(start+sliceChunks, len(chunks))], at)
		usage.Add(sliceUsage)
		if err != nil {
			return usage, err
		}
	}
	return usage, nil
}

// embedSlice embeds one slice of chunks, writes their vectors, and only then
// marks them embedded. Recording it before the vectors landed would let a
// retry after a failed embed skip them and mark the document searchable
// holding nothing.
func (pipeline *Pipeline) embedSlice(ctx context.Context, embedder embedding.Embedder, corpus domain.Corpus, document domain.Document, store vectorstore.Store, chunks []domain.Chunk, at time.Time) (domain.Usage, error) {
	texts := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		// The heading trail is embedded with the text: a passage read alone is
		// often missing the subject it is about.
		texts = append(texts, chunk.EmbedText())
	}
	embedded, err := embedder.Embed(ctx, domain.InputDocument, texts)
	if err != nil {
		return domain.Usage{}, fmt.Errorf("embed %d chunks: %w", len(texts), err)
	}
	if len(embedded.Vectors) != len(chunks) {
		return embedded.Usage, fmt.Errorf("embedding returned %d vectors for %d chunks", len(embedded.Vectors), len(chunks))
	}
	vectors := make([]domain.Vector, 0, len(chunks))
	for index, chunk := range chunks {
		vectors = append(vectors, domain.Vector{
			ChunkID:    chunk.ID,
			DocumentID: document.ID,
			Values:     embedded.Vectors[index],
			// Provenance is written by the pipeline and laid over the caller's
			// metadata, which cannot contain those keys, so a citation cannot
			// be made to point somewhere else.
			Metadata: chunk.Metadata.Merge(chunk.Provenance(document)),
		})
	}
	if err := store.Upsert(ctx, corpus, vectors); err != nil {
		return embedded.Usage, err
	}
	ids := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		ids = append(ids, chunk.ID)
	}
	if err := pipeline.store.MarkChunksEmbedded(ctx, ids, at); err != nil {
		return embedded.Usage, err
	}
	return embedded.Usage, nil
}

func (pipeline *Pipeline) event(detailType string, payload any) (domain.Event, error) {
	id, err := domain.NewID(domain.EventIDPrefix)
	if err != nil {
		return domain.Event{}, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: pipeline.now().UTC(), Data: data}, nil
}
