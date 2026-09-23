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
	"github.com/Shubham04-oss/oryxa-go/rag/internal/knowledgebase"
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
	// MarkKnowledgeBaseSubmitted is used only for a bedrock-kb corpus's
	// documents; see indexKnowledgeBase.
	MarkKnowledgeBaseSubmitted(ctx context.Context, documentID, contentSHA256 string, at time.Time) error
	// RecheckDocumentAt leaves a document ingesting, claimable again at at,
	// without counting the claim as an attempt.
	RecheckDocumentAt(ctx context.Context, documentID string, at time.Time) error
	// RetryDocumentAt puts a document whose attempt failed back in the
	// queue, claimable again at at. The attempt still counts.
	RetryDocumentAt(ctx context.Context, documentID string, at time.Time) error
}

// knowledgeBaseRecheck is how soon a document handed to the knowledge base is
// asked about again. Bedrock indexes a document in seconds; holding the whole
// lease would leave one waiting five minutes to be called ready.
const knowledgeBaseRecheck = 10 * time.Second

// knowledgeBaseStall is how long a submission may stay unfinished before it
// is given up on and submitted again. Indexing takes seconds, and minutes for
// a managed knowledge base reading a document's pictures; an hour is far past
// either, so a document still unfinished then is one the knowledge base lost,
// or left in a state this worker does not know to be final. Polls do not
// count as attempts, so without this such a document was polled forever; a
// resubmission does count, so one that never finishes ends up failed.
const knowledgeBaseStall = time.Hour

// Vectors routes a corpus to the store holding its vectors.
type Vectors interface {
	For(corpus domain.Corpus) (vectorstore.Store, error)
}

// Embedders hands out the embedder for a corpus's pinned spec.
type Embedders interface {
	For(ctx context.Context, spec domain.EmbeddingSpec) (embedding.Embedder, error)
}

// KnowledgeBase is what indexes a document in a bedrock-kb corpus, in place
// of chunking.Cut, Embedders and Vectors above: Bedrock does the chunking,
// embedding and, for a document with no extracted text, the parsing, so
// there is nothing here to cut or embed — only to submit and poll.
type KnowledgeBase interface {
	Submit(ctx context.Context, corpus domain.Corpus, document domain.Document, text string) error
	Statuses(ctx context.Context, documentIDs []string) (map[string]knowledgebase.DocumentStatus, error)
	DeleteStaged(ctx context.Context, corpus domain.Corpus, document domain.Document) error
}

// MaxAttempts is how many times a document is tried before it is left failed.
//
// A failure that repeats is usually the document rather than the weather: a
// model that will not take it, or a corpus whose spec no longer resolves.
// Retrying forever would spend money on the same rejection and hide the
// document behind a queue that never drains.
const MaxAttempts = 5

// firstRetry is how long a document waits after its first failed attempt.
// Each later wait is four times the one before, so the attempts MaxAttempts
// allows span about three quarters of an hour rather than a few seconds.
const firstRetry = 30 * time.Second

// retryDelay is how long a document waits after failing attempt number
// attempts.
func retryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	return firstRetry << (2 * (attempts - 1))
}

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
	store         Store
	vectors       Vectors
	embedders     Embedders
	knowledgeBase KnowledgeBase
	options       Options
	logger        *slog.Logger
	now           func() time.Time
}

// New builds a Pipeline. knowledgeBase is nil in a deployment with no
// Bedrock knowledge base configured, in which case a document in a
// bedrock-kb corpus fails by name rather than reaching a nil client — the
// same shape of failure Vectors.For already gives a corpus on a backend this
// deployment cannot serve.
func New(store Store, vectors Vectors, embedders Embedders, knowledgeBase KnowledgeBase, options Options, logger *slog.Logger) *Pipeline {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Pipeline{
		store: store, vectors: vectors, embedders: embedders, knowledgeBase: knowledgeBase,
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
		// Worth another go later, so the attempt count is what decides when
		// to give up - but later, not at once. A document put straight back
		// is claimed straight back, and a problem that takes minutes to clear
		// (a permission not granted yet, a throttled model) used every
		// attempt up in seconds.
		at := pipeline.now().Add(retryDelay(document.Attempts))
		if retryErr := pipeline.store.RetryDocumentAt(ctx, document.ID, at); retryErr != nil {
			pipeline.logger.ErrorContext(ctx, "scheduling a document's retry failed", "document_id", document.ID, "error", retryErr)
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
	// A bedrock-kb corpus is never chunked or embedded here: Bedrock does
	// both, so this document takes a wholly different, shorter path.
	if corpus.Backend == domain.BackendBedrockKB {
		return pipeline.indexKnowledgeBase(ctx, corpus, document)
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

// indexKnowledgeBase hands a document to a Bedrock knowledge base instead of
// chunking and embedding it. Ingestion there is asynchronous — a live test
// indexed three documents, one a PDF, in about nine seconds — so this claim
// either submits the document, if its current content was never submitted,
// or polls the outcome of a submission already running.
//
// It reuses the ordinary lease and claim: a submission leaves the document
// 'ingesting' with the lease ClaimDocuments already set, so the next poll
// happens on the next claim after that lease passes, and two replicas can
// never submit the same content twice at once, exactly as they can never
// both embed the same chunk at once on the pgvector or S3 Vectors path.
func (pipeline *Pipeline) indexKnowledgeBase(ctx context.Context, corpus domain.Corpus, document domain.Document) (Result, error) {
	if pipeline.knowledgeBase == nil {
		return Result{}, fmt.Errorf("this deployment cannot reach its knowledge base: %w", domain.ErrInvalid)
	}
	at := pipeline.now().UTC()

	// kb_synced_hash is the content_sha256 last handed to the knowledge base.
	// It differs from the document's own content_sha256 exactly when this
	// content has never been submitted, or was submitted under an id the
	// knowledge base then lost or only partially indexed — both handled by
	// submitting again, since re-ingesting the same custom document id
	// replaces whatever was there.
	if document.ContentSHA256 != document.KBSyncedHash {
		var text string
		if !document.HasOriginal() {
			body, err := pipeline.store.DocumentBody(ctx, document.ID)
			if err != nil {
				return Result{}, fmt.Errorf("read the document text: %w", err)
			}
			text = body
		}
		if err := pipeline.knowledgeBase.Submit(ctx, corpus, document, text); err != nil {
			return Result{}, err
		}
		if err := pipeline.store.MarkKnowledgeBaseSubmitted(ctx, document.ID, document.ContentSHA256, at); err != nil {
			return Result{}, err
		}
		pipeline.logger.InfoContext(ctx, "submitted a document to the knowledge base",
			"document_id", document.ID, "corpus_id", corpus.ID)
		return Result{}, pipeline.store.RecheckDocumentAt(ctx, document.ID, at.Add(knowledgeBaseRecheck))
	}

	statuses, err := pipeline.knowledgeBase.Statuses(ctx, []string{document.ID})
	if err != nil {
		return Result{}, err
	}
	status := statuses[document.ID]
	switch status.Code {
	case "INDEXED":
		return Result{}, pipeline.markKnowledgeBaseReady(ctx, corpus, document, status.Code, at)
	case "IGNORED":
		// IGNORED, measured against the validated behaviour, is what
		// re-ingesting content the knowledge base already holds unchanged
		// looks like — nothing rag's own dedup would call a failure either.
		// Bedrock also ignores a document it will not take at all, and says
		// why. That reason is logged, because such a document reads as ready
		// with nothing of it searchable.
		if status.Reason != "" {
			pipeline.logger.InfoContext(ctx, "the knowledge base ignored a document",
				"document_id", document.ID, "corpus_id", corpus.ID, "reason", status.Reason)
		}
		return Result{}, pipeline.markKnowledgeBaseReady(ctx, corpus, document, status.Code, at)
	case "PARTIALLY_INDEXED":
		// A finished outcome, not a step on the way: Bedrock lists it beside
		// INDEXED and FAILED as what became of the document. Waiting on it
		// only ever got the same answer back. What did index is searchable,
		// so the document is ready, and the shortfall is logged.
		pipeline.logger.WarnContext(ctx, "the knowledge base indexed only part of a document",
			"document_id", document.ID, "corpus_id", corpus.ID, "reason", status.Reason)
		return Result{}, pipeline.markKnowledgeBaseReady(ctx, corpus, document, status.Code, at)
	case "FAILED", "METADATA_UPDATE_FAILED":
		reason := status.Reason
		if reason == "" {
			reason = "the knowledge base reported " + status.Code
		}
		// A failure the knowledge base itself calls final is unlikely to
		// come out differently for trying again with identical content —
		// the same reasoning that fails a document which chunks to nothing
		// at once rather than spending every attempt on the same rejection.
		return Result{}, fmt.Errorf("%s: %w", reason, domain.ErrInvalid)
	case "METADATA_PARTIALLY_INDEXED", "NOT_FOUND", "DELETING", "DELETE_IN_PROGRESS":
		// The knowledge base has finished with this attempt — successfully
		// in part, not at all, or by losing the document some other way —
		// rather than still working on it, so polling again on the same
		// submission would only ask the same question and get the same
		// answer. Forgetting that this content was ever synced is what
		// makes the next claim submit it again instead, which tenant
		// isolation depends on actually completing: a corpus_id or
		// organization_id attribute that only partially landed is a risk to
		// it, not a success.
		if resetErr := pipeline.store.MarkKnowledgeBaseSubmitted(ctx, document.ID, "", at); resetErr != nil {
			return Result{}, resetErr
		}
		return Result{}, fmt.Errorf("the knowledge base reported %s for this document; resubmitting", status.Code)
	default:
		// STARTING, PENDING and IN_PROGRESS all mean "still working": leave
		// the document ingesting and ask again shortly. So does TEXT_INDEXED,
		// which a managed knowledge base reports, undocumented, between
		// taking a document's text and finishing with it: seen live, it
		// becomes INDEXED minutes later.
		//
		// Still working an hour after the submission is not working. It is
		// given up on the way a lost document is, by forgetting the
		// submission so the next claim makes it again; this claim is not
		// handed back as a poll, so it counts as an attempt.
		if document.KBSubmittedAt != nil {
			if waited := at.Sub(*document.KBSubmittedAt); waited > knowledgeBaseStall {
				if resetErr := pipeline.store.MarkKnowledgeBaseSubmitted(ctx, document.ID, "", at); resetErr != nil {
					return Result{}, resetErr
				}
				return Result{}, fmt.Errorf("the knowledge base still reported %s for this document %s after it was submitted; resubmitting",
					status.Code, waited.Round(time.Minute))
			}
		}
		return Result{}, pipeline.store.RecheckDocumentAt(ctx, document.ID, at.Add(knowledgeBaseRecheck))
	}
}

// markKnowledgeBaseReady records a document the knowledge base finished
// indexing. There is no chunk count of rag's own to report — Bedrock chunked
// it, not rag — so the field stays zero for a document on this backend,
// which is an honest answer rather than a guess. status is the knowledge
// base's own word for how it finished.
func (pipeline *Pipeline) markKnowledgeBaseReady(ctx context.Context, corpus domain.Corpus, document domain.Document, status string, at time.Time) error {
	indexed, err := pipeline.event(rageventsv1.DocumentIndexedDetailType, rageventsv1.DocumentIndexed{
		DocumentID: document.ID, CorpusID: document.CorpusID, OrganizationID: document.OrganizationID,
		Provider: string(corpus.Spec.Provider), Model: corpus.Spec.Model, IndexedAt: at,
	})
	if err != nil {
		return err
	}
	if err := pipeline.store.MarkDocumentReady(ctx, document.ID, 0, at, indexed); err != nil {
		return err
	}
	// Staged text left behind after indexing costs storage, not correctness,
	// so a failure to remove it is logged rather than failing the document.
	if err := pipeline.knowledgeBase.DeleteStaged(ctx, corpus, document); err != nil {
		pipeline.logger.WarnContext(ctx, "deleting a document's staged text failed",
			"document_id", document.ID, "corpus_id", corpus.ID, "error", err)
	}
	pipeline.logger.InfoContext(ctx, "a knowledge base finished indexing a document",
		"document_id", document.ID, "corpus_id", corpus.ID, "status", status)
	return nil
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
