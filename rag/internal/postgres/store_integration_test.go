package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// openTestStore connects to the database the integration tests run against.
// Without RAG_TEST_DATABASE_URL they skip, so the ordinary unit run needs no
// PostgreSQL.
func openTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("RAG_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RAG_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Running the migrations twice is how a restarted service starts, so it
	// has to be as uneventful as the first time.
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	return store, ctx
}

func testEvent(t *testing.T, detailType string) domain.Event {
	t.Helper()
	id, err := domain.NewID(domain.EventIDPrefix)
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: time.Now().UTC(), Data: json.RawMessage(`{}`)}
}

func testCorpus(t *testing.T, store *Store, ctx context.Context, organizationID string, spec domain.EmbeddingSpec) domain.Corpus {
	t.Helper()
	id, err := domain.NewID(domain.CorpusIDPrefix)
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	corpus := domain.Corpus{
		ID:             id,
		OrganizationID: organizationID,
		Slug:           "handbook-" + strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(id[4:14])),
		Name:           "Handbook",
		Scope:          domain.ScopeOrganization,
		Spec:           spec,
		Backend:        domain.BackendPgvector,
		Chunking:       domain.DefaultChunkPolicy(spec),
		Metadata:       domain.Metadata{"team": "platform"},
		CreatedBy:      "usr_integration",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := store.CreateCorpus(ctx, corpus, testEvent(t, "rag.corpus.created.v1")); err != nil {
		t.Fatalf("CreateCorpus: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM rag_corpora WHERE organization_id = $1`, organizationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM rag_organizations WHERE organization_id = $1`, organizationID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM rag_outbox WHERE published_at IS NULL AND detail_type LIKE 'rag.%'`)
	})
	return corpus
}

func uniqueOrganization() string {
	return "org-it-" + time.Now().UTC().Format("20060102150405.000000000")
}

func titanSpec() domain.EmbeddingSpec {
	return domain.EmbeddingSpec{
		Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v2:0",
		Dimensions: 1024, Metric: domain.MetricCosine,
	}
}

func TestCorpusLifecycle(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	read, err := store.Corpus(ctx, organizationID, corpus.ID)
	if err != nil {
		t.Fatalf("Corpus: %v", err)
	}
	if !read.Spec.Equal(corpus.Spec) {
		t.Fatalf("spec came back as %v, want %v", read.Spec, corpus.Spec)
	}
	if read.Chunking != corpus.Chunking || read.Backend != corpus.Backend {
		t.Fatalf("corpus = %+v", read)
	}
	if read.Metadata["team"] != "platform" {
		t.Fatalf("metadata = %v", read.Metadata)
	}

	bySlug, err := store.CorpusBySlug(ctx, organizationID, strings.ToUpper(corpus.Slug))
	if err != nil || bySlug.ID != corpus.ID {
		t.Fatalf("CorpusBySlug = (%v, %v)", bySlug.ID, err)
	}

	// Another organization must not see it, and must not be told it exists.
	if _, err := store.Corpus(ctx, "org-someone-else", corpus.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a corpus in another organization read as %v, want ErrNotFound", err)
	}

	corpora, err := store.Corpora(ctx, organizationID, CorpusQuery{MemberID: "usr_integration"})
	if err != nil {
		t.Fatalf("Corpora: %v", err)
	}
	if len(corpora) != 1 || corpora[0].ID != corpus.ID {
		t.Fatalf("Corpora returned %d corpora", len(corpora))
	}
}

func TestDocumentsCannotCrossOrganizationBoundaries(t *testing.T) {
	store, ctx := openTestStore(t)
	first := testCorpus(t, store, ctx, uniqueOrganization(), titanSpec())
	second := testCorpus(t, store, ctx, uniqueOrganization(), titanSpec())
	otherCorpusInFirst := testCorpus(t, store, ctx, first.OrganizationID, titanSpec())

	firstBody := "Only the first organization may read this."
	firstDocument, _, err := store.UpsertDocument(ctx,
		newDocument(first, domain.Source{Kind: domain.SourceExternal, Ref: "https://example.test/first"}, "First", firstBody),
		firstBody, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("write first organization's document: %v", err)
	}
	secondBody := "Only the second organization may read this."
	secondDocument, _, err := store.UpsertDocument(ctx,
		newDocument(second, domain.Source{Kind: domain.SourceInline}, "Second", secondBody),
		secondBody, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("write second organization's document: %v", err)
	}
	otherBody := "A different collection in the first organization."
	otherDocument, _, err := store.UpsertDocument(ctx,
		newDocument(otherCorpusInFirst, domain.Source{Kind: domain.SourceInline}, "Other", otherBody),
		otherBody, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("write other corpus's document: %v", err)
	}

	// Even an internal writer must not attach a document claiming one
	// organization to a corpus owned by another.
	wrong := newDocument(first, domain.Source{Kind: domain.SourceInline}, "Wrong", "cross-organization")
	wrong.OrganizationID = second.OrganizationID
	if _, _, err := store.UpsertDocument(ctx, wrong, "cross-organization", testEvent(t, "rag.document.received.v1")); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-organization document write = %v, want ErrConflict", err)
	}
	wrongUpdate := firstDocument
	wrongUpdate.OrganizationID = second.OrganizationID
	wrongUpdate.ContentSHA256 = domain.Hash("changed by foreign organization")
	if _, _, err := store.UpsertDocument(ctx, wrongUpdate, "changed by foreign organization", testEvent(t, "rag.document.received.v1")); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-organization document update = %v, want ErrConflict", err)
	}
	unchanged, err := store.Document(ctx, first.OrganizationID, firstDocument.ID)
	if err != nil || unchanged.ContentSHA256 != firstDocument.ContentSHA256 {
		t.Fatalf("foreign update changed first organization's document: %+v, %v", unchanged, err)
	}
	wrongChunkParent := firstDocument
	wrongChunkParent.OrganizationID = second.OrganizationID
	if _, err := store.SyncChunks(ctx, wrongChunkParent, []domain.Chunk{{
		Ordinal: 0, Text: firstBody, TokenCount: 7, SHA256: domain.Hash(firstBody),
		StartByte: 0, EndByte: len(firstBody),
	}}, time.Now().UTC()); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-organization chunk write = %v, want ErrConflict", err)
	}
	now := time.Now().UTC()
	if err := store.UpsertKnowledgeDocumentMapping(ctx, domain.KnowledgeDocumentMapping{
		KnowledgeDocumentID: "kdoc_foreign", OrganizationID: second.OrganizationID,
		CorpusID: first.ID, DocumentID: firstDocument.ID,
		ContentHash: "hash", ExtractedAt: now, UpdatedAt: now,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("cross-organization knowledge mapping = %v, want ErrConflict", err)
	}

	// Bedrock results are hydrated from PostgreSQL. A result carrying a
	// foreign document id must be discarded even if Bedrock returned it.
	refs, err := store.DocumentRefs(ctx, first.OrganizationID, first.ID,
		[]string{firstDocument.ID, secondDocument.ID, otherDocument.ID}, false)
	if err != nil {
		t.Fatalf("read first organization's references: %v", err)
	}
	if len(refs) != 1 || refs[firstDocument.ID].ID != firstDocument.ID {
		t.Fatalf("references from the first organization = %+v", refs)
	}
	if _, err := store.Document(ctx, first.OrganizationID, secondDocument.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign document read = %v, want ErrNotFound", err)
	}
}

// TestCorporaCountsWhatEachHolds is the regression for a listing that said
// every corpus was empty: Krude searches only the corpora a listing says hold
// documents, so it searched none.
func TestCorporaCountsWhatEachHolds(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	full := testCorpus(t, store, ctx, organizationID, titanSpec())
	empty := testCorpus(t, store, ctx, organizationID, titanSpec())

	body := "alpha bravo"
	document, _, err := store.UpsertDocument(ctx, newDocument(full, domain.Source{Kind: domain.SourceInline}, "Doc", body), body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	if _, err := store.SyncChunks(ctx, document, []domain.Chunk{
		{Ordinal: 0, Text: "alpha", TokenCount: 1, SHA256: domain.Hash("alpha"), StartByte: 0, EndByte: 5},
		{Ordinal: 1, Text: "bravo", TokenCount: 1, SHA256: domain.Hash("bravo"), StartByte: 6, EndByte: 11},
	}, time.Now().UTC()); err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}

	corpora, err := store.Corpora(ctx, organizationID, CorpusQuery{MemberID: "usr_integration"})
	if err != nil {
		t.Fatalf("Corpora: %v", err)
	}
	counts := map[string][2]int64{}
	for _, corpus := range corpora {
		counts[corpus.ID] = [2]int64{corpus.DocumentCount, corpus.ChunkCount}
	}
	if counts[full.ID] != [2]int64{1, 2} || counts[empty.ID] != [2]int64{0, 0} || len(counts) != 2 {
		t.Fatalf("listed counts = %v, want %s holding 1 document of 2 chunks and %s nothing", counts, full.ID, empty.ID)
	}
	documents, chunks, err := store.CorpusCounts(ctx, full.ID, true)
	if err != nil || [2]int64{documents, chunks} != counts[full.ID] {
		t.Fatalf("CorpusCounts = (%d, %d, %v), want what the listing said", documents, chunks, err)
	}
}

func TestCorpusSlugIsUniquePerOrganization(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	id, _ := domain.NewID(domain.CorpusIDPrefix)
	duplicate := corpus
	duplicate.ID = id
	err := store.CreateCorpus(ctx, duplicate, testEvent(t, "rag.corpus.created.v1"))
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a duplicate slug was accepted: %v", err)
	}
}

func TestCorpusRefusesAnUnsearchableCombination(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	// S3 Vectors cannot index on inner product. The database enforces it as
	// well as the domain, so a corpus that could accept documents and never
	// answer a query cannot be recorded at all.
	id, _ := domain.NewID(domain.CorpusIDPrefix)
	bad := corpus
	bad.ID = id
	bad.Slug = corpus.Slug + "x"
	bad.Backend = domain.BackendS3Vectors
	bad.Spec.Metric = domain.MetricInnerProduct
	if err := store.CreateCorpus(ctx, bad, testEvent(t, "rag.corpus.created.v1")); err == nil {
		t.Fatal("an inner-product corpus on S3 Vectors was recorded")
	}
}

// TestCorpusRefusesAWrongEmbeddingSpecOnBedrockKB: a Bedrock knowledge base
// embeds with whatever model it was created with, not with whatever a
// corpus asks for, so the database refuses one that disagrees with it
// exactly as it refuses an unsearchable S3 Vectors combination above — and
// accepts the one spec a knowledge base corpus can actually have.
func TestCorpusRefusesAWrongEmbeddingSpecOnBedrockKB(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	wrongID, _ := domain.NewID(domain.CorpusIDPrefix)
	wrong := corpus
	wrong.ID = wrongID
	wrong.Slug = corpus.Slug + "-kb-wrong"
	wrong.Backend = domain.BackendBedrockKB
	wrong.Spec = domain.EmbeddingSpec{Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v2:0", Dimensions: 512, Metric: domain.MetricCosine}
	if err := store.CreateCorpus(ctx, wrong, testEvent(t, "rag.corpus.created.v1")); err == nil {
		t.Fatal("a bedrock-kb corpus pinning a spec other than the knowledge base's own was recorded")
	}

	rightID, _ := domain.NewID(domain.CorpusIDPrefix)
	right := corpus
	right.ID = rightID
	right.Slug = corpus.Slug + "-kb-right"
	right.Backend = domain.BackendBedrockKB
	right.Spec = domain.BedrockKnowledgeBaseEmbeddingSpec
	if err := store.CreateCorpus(ctx, right, testEvent(t, "rag.corpus.created.v1")); err != nil {
		t.Fatalf("a bedrock-kb corpus pinning the knowledge base's own spec was refused: %v", err)
	}
}

// TestDocumentOriginalAndKnowledgeBaseColumnsRoundTrip covers what migration
// 007 added to rag_documents: a document indexed from an original object
// rather than text, the constraint that a document must carry one or the
// other, and kb_synced_hash surviving a claim untouched — which is what lets
// the ingest worker tell "never submitted" from "submission outcome still
// unknown" across many claims of the same document.
func TestDocumentOriginalAndKnowledgeBaseColumnsRoundTrip(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	document := newDocument(corpus, domain.Source{Kind: domain.SourceExternal, Ref: "https://example.com/handbook.pdf"}, "Handbook", "")
	document.ContentSHA256 = "original-sha-1"
	document.ByteSize = 4096
	document.OriginalBucket = "oryxa-knowledge-originals"
	document.OriginalKey = "org/doc/original-sha-1.pdf"
	stored, outcome, err := store.UpsertDocument(ctx, document, "", testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument with an original and no body: %v", err)
	}
	if outcome != UpsertCreated {
		t.Fatalf("outcome = %v, want UpsertCreated", outcome)
	}
	if stored.OriginalBucket != document.OriginalBucket || stored.OriginalKey != document.OriginalKey {
		t.Fatalf("original location did not round-trip: %+v", stored)
	}
	if !stored.HasOriginal() {
		t.Fatal("HasOriginal() is false for a document with an original bucket and key")
	}
	if stored.KBSyncedHash != "" {
		t.Fatalf("kb_synced_hash = %q on a document never submitted, want empty", stored.KBSyncedHash)
	}

	// Neither text nor an original: the database refuses it rather than
	// recording a document nothing could ever index.
	empty := newDocument(corpus, domain.Source{Kind: domain.SourceExternal, Ref: "https://example.com/empty"}, "Empty", "")
	if _, _, err := store.UpsertDocument(ctx, empty, "", testEvent(t, "rag.document.received.v1")); err == nil {
		t.Fatal("a document with neither body nor an original was recorded")
	}

	at := time.Now().UTC().Truncate(time.Microsecond)
	marker := "v2:marker-of-the-submission"
	if err := store.MarkKnowledgeBaseSubmitted(ctx, stored.ID, marker, at); err != nil {
		t.Fatalf("MarkKnowledgeBaseSubmitted: %v", err)
	}
	afterSubmit, err := store.Document(ctx, organizationID, stored.ID)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if afterSubmit.KBSyncedHash != marker {
		t.Fatalf("kb_synced_hash = %q after submission, want %q", afterSubmit.KBSyncedHash, marker)
	}
	if afterSubmit.KBSubmittedAt == nil || !afterSubmit.KBSubmittedAt.Equal(at) {
		t.Fatalf("kb_submitted_at = %v after submission, want %v", afterSubmit.KBSubmittedAt, at)
	}

	// A claim is how the ingest worker later polls this document's outcome;
	// it must not disturb kb_synced_hash, which is the only thing telling
	// the next claim that this content was already submitted, nor
	// kb_submitted_at, which is how long it has been waiting.
	claimed, err := store.ClaimDocuments(ctx, 10, time.Minute)
	if err != nil {
		t.Fatalf("ClaimDocuments: %v", err)
	}
	var found bool
	for _, candidate := range claimed {
		if candidate.ID != stored.ID {
			continue
		}
		found = true
		if candidate.KBSyncedHash != marker {
			t.Fatalf("claiming the document changed kb_synced_hash to %q, want %q", candidate.KBSyncedHash, marker)
		}
		if candidate.KBSubmittedAt == nil || !candidate.KBSubmittedAt.Equal(at) {
			t.Fatalf("claiming the document changed kb_submitted_at to %v, want %v", candidate.KBSubmittedAt, at)
		}
	}
	if !found {
		t.Fatal("ClaimDocuments did not claim the pending document")
	}

	// Giving a submission up forgets when it was made along with what it was,
	// since nothing is outstanding any more.
	if err := store.MarkKnowledgeBaseSubmitted(ctx, stored.ID, "", at.Add(time.Minute)); err != nil {
		t.Fatalf("MarkKnowledgeBaseSubmitted with no marker: %v", err)
	}
	afterReset, err := store.Document(ctx, organizationID, stored.ID)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if afterReset.KBSyncedHash != "" || afterReset.KBSubmittedAt != nil {
		t.Fatalf("after a reset kb_synced_hash = %q and kb_submitted_at = %v, want both empty",
			afterReset.KBSyncedHash, afterReset.KBSubmittedAt)
	}

	ids, err := store.DocumentIDsForCorpus(ctx, corpus.ID)
	if err != nil {
		t.Fatalf("DocumentIDsForCorpus: %v", err)
	}
	if !slices.Contains(ids, stored.ID) {
		t.Fatalf("DocumentIDsForCorpus = %v, want it to include %s", ids, stored.ID)
	}
}

func newDocument(corpus domain.Corpus, source domain.Source, title, body string) domain.Document {
	id, _ := domain.NewID(domain.DocumentIDPrefix)
	now := time.Now().UTC().Truncate(time.Microsecond)
	return domain.Document{
		ID:             id,
		CorpusID:       corpus.ID,
		OrganizationID: corpus.OrganizationID,
		Source:         source,
		Title:          title,
		ContentType:    "text/markdown",
		ContentSHA256:  domain.Hash(body),
		ByteSize:       int64(len(body)),
		Metadata:       domain.Metadata{"team": "platform"},
		CreatedBy:      "usr_integration",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func TestDocumentUpsertIsIdempotentOnContent(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	body := "# Rotation\n\nCredentials rotate every ninety days.\n"
	source := domain.Source{Kind: domain.SourceFile, Ref: "file_abc123"}

	stored, outcome, err := store.UpsertDocument(ctx, newDocument(corpus, source, "Runbook", body), body, testEvent(t, "rag.document.received.v1"))
	if err != nil || outcome != UpsertCreated {
		t.Fatalf("first upsert = (%v, %v)", outcome, err)
	}
	if stored.Status != domain.StatusPending {
		t.Fatalf("a new document is %q, want pending", stored.Status)
	}

	// Not ingested yet, so the same bytes are still work to do.
	_, outcome, err = store.UpsertDocument(ctx, newDocument(corpus, source, "Runbook", body), body, testEvent(t, "rag.document.received.v1"))
	if err != nil || outcome != UpsertChanged {
		t.Fatalf("re-offering a pending document = (%v, %v), want changed", outcome, err)
	}

	if err := store.MarkDocumentReady(ctx, stored.ID, 3, time.Now().UTC(), testEvent(t, "rag.document.indexed.v1")); err != nil {
		t.Fatalf("MarkDocumentReady: %v", err)
	}

	// Now the same bytes are a no-op, which is what lets a pipeline
	// re-deliver everything it has without paying to re-embed it.
	again, outcome, err := store.UpsertDocument(ctx, newDocument(corpus, source, "Runbook", body), body, testEvent(t, "rag.document.received.v1"))
	if err != nil || outcome != UpsertUnchanged {
		t.Fatalf("re-offering identical content = (%v, %v), want unchanged", outcome, err)
	}
	if again.ID != stored.ID {
		t.Fatalf("a second document was created: %s then %s", stored.ID, again.ID)
	}

	// Different bytes from the same source replace it and need ingesting.
	changedBody := body + "\nA rotation that fails pages the on-call engineer.\n"
	changed, outcome, err := store.UpsertDocument(ctx, newDocument(corpus, source, "Runbook", changedBody), changedBody, testEvent(t, "rag.document.received.v1"))
	if err != nil || outcome != UpsertChanged {
		t.Fatalf("changed content = (%v, %v)", outcome, err)
	}
	if changed.ID != stored.ID {
		t.Fatalf("changed content made a second document: %s then %s", stored.ID, changed.ID)
	}
	if changed.Status != domain.StatusPending {
		t.Fatalf("a changed document is %q, want pending", changed.Status)
	}
	if changed.IngestedAt != nil {
		t.Fatal("a changed document kept the previous ingest time")
	}
}

func TestMetadataChangeRequeuesDocumentAndReplacesVectorMetadata(t *testing.T) {
	store, ctx := openTestStore(t)
	corpus := testCorpus(t, store, ctx, uniqueOrganization(), titanSpec())
	body := "An invoice for the September service."
	source := domain.Source{Kind: domain.SourceExternal, Ref: "https://example.com/invoice"}
	initial := newDocument(corpus, source, "Invoice", body)
	initial.Metadata = domain.Metadata{"provider": "gmail", "category": "financial_record"}
	stored, _, err := store.UpsertDocument(ctx, initial, body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := domain.Chunk{DocumentID: stored.ID, CorpusID: corpus.ID, OrganizationID: corpus.OrganizationID,
		Ordinal: 0, Text: body, TokenCount: 7, SHA256: domain.Hash(body), StartByte: 0, EndByte: len(body), Metadata: initial.Metadata}
	first, err := store.SyncChunks(ctx, stored, []domain.Chunk{chunk}, time.Now().UTC())
	if err != nil || len(first.Fresh) != 1 {
		t.Fatalf("initial chunk sync = %+v, %v", first, err)
	}
	if err := store.MarkChunksEmbedded(ctx, []string{first.Fresh[0].ID}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDocumentReady(ctx, stored.ID, 1, time.Now().UTC(), testEvent(t, "rag.document.indexed.v1")); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkKnowledgeBaseSubmitted(ctx, stored.ID, "v2:submitted", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	updated := newDocument(corpus, source, "Invoice", body)
	updated.Metadata = domain.Metadata{"provider": "gmail", "category": "account_record"}
	changed, outcome, err := store.UpsertDocument(ctx, updated, body, testEvent(t, "rag.document.received.v1"))
	if err != nil || outcome != UpsertChanged || changed.Status != domain.StatusPending || changed.KBSyncedHash != "" {
		t.Fatalf("metadata update = %+v, %v, %v", changed, outcome, err)
	}
	refs, err := store.DocumentRefs(ctx, corpus.OrganizationID, corpus.ID, []string{changed.ID}, false)
	if err != nil || refs[changed.ID].Metadata["category"] != "account_record" {
		t.Fatalf("updated document provenance = %+v, %v", refs, err)
	}
	chunk.Metadata = updated.Metadata
	second, err := store.SyncChunks(ctx, changed, []domain.Chunk{chunk}, time.Now().UTC())
	if err != nil || len(second.Fresh) != 1 || len(second.Removed) != 1 || len(second.Reused) != 0 {
		t.Fatalf("changed metadata chunk sync = %+v, %v", second, err)
	}
}

func TestDocumentACLFiltersPagesAndReferences(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())
	general := newDocument(corpus, domain.Source{Kind: domain.SourceExternal, Ref: "general"}, "Handbook", "handbook")
	general.Access = domain.AccessMembers
	general, _, err := store.UpsertDocument(ctx, general, "handbook", testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatal(err)
	}
	admin := newDocument(corpus, domain.Source{Kind: domain.SourceExternal, Ref: "restricted"}, "Payroll", "payroll")
	admin.Access = domain.AccessAdmins
	admin, _, err = store.UpsertDocument(ctx, admin, "payroll", testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		document domain.Document
		body     string
	}{{general, "handbook"}, {admin, "payroll"}} {
		_, err := store.SyncChunks(ctx, entry.document, []domain.Chunk{{
			Text: entry.body, TokenCount: 1, SHA256: domain.Hash(entry.body), StartByte: 0, EndByte: len(entry.body),
		}}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
	}
	if hits, err := store.LexicalSearch(ctx, corpus, "payroll", 10, domain.Filter{}, false); err != nil || len(hits) != 0 {
		t.Fatalf("member payroll hits = %+v, %v", hits, err)
	}
	if hits, err := store.LexicalSearch(ctx, corpus, "payroll", 10, domain.Filter{}, true); err != nil || len(hits) != 1 {
		t.Fatalf("admin payroll hits = %+v, %v", hits, err)
	}
	memberPage, next, err := store.Documents(ctx, organizationID, corpus.ID, "", "", 1, false)
	if err != nil || len(memberPage) != 1 || memberPage[0].ID != general.ID || next != "" {
		t.Fatalf("member page = %+v, %q, %v", memberPage, next, err)
	}
	adminPage, _, err := store.Documents(ctx, organizationID, corpus.ID, "", "", 2, true)
	if err != nil || len(adminPage) != 2 {
		t.Fatalf("admin page = %+v, %v", adminPage, err)
	}
	ids := []string{general.ID, admin.ID}
	memberRefs, err := store.DocumentRefs(ctx, organizationID, corpus.ID, ids, false)
	if err != nil || len(memberRefs) != 1 || memberRefs[general.ID].ID != general.ID {
		t.Fatalf("member refs = %+v, %v", memberRefs, err)
	}
	adminRefs, err := store.DocumentRefs(ctx, organizationID, corpus.ID, ids, true)
	if err != nil || len(adminRefs) != 2 {
		t.Fatalf("admin refs = %+v, %v", adminRefs, err)
	}
	visibleDocuments, _, err := store.CorpusCounts(ctx, corpus.ID, false)
	if err != nil || visibleDocuments != 1 {
		t.Fatalf("member count = %d, %v", visibleDocuments, err)
	}
	allDocuments, _, err := store.CorpusCounts(ctx, corpus.ID, true)
	if err != nil || allDocuments != 2 {
		t.Fatalf("admin count = %d, %v", allDocuments, err)
	}
	listed, err := store.Corpora(ctx, organizationID, CorpusQuery{Scope: domain.ScopeOrganization})
	if err != nil || len(listed) != 1 || listed[0].DocumentCount != 1 {
		t.Fatalf("member corpora = %+v, %v", listed, err)
	}
	listed, err = store.Corpora(ctx, organizationID, CorpusQuery{Scope: domain.ScopeOrganization, Admin: true})
	if err != nil || len(listed) != 1 || listed[0].DocumentCount != 2 {
		t.Fatalf("admin corpora = %+v, %v", listed, err)
	}
	if err := store.MarkDocumentReady(ctx, admin.ID, 0, time.Now().UTC(), testEvent(t, "rag.document.indexed.v1")); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkKnowledgeBaseSubmitted(ctx, admin.ID, "v2:submitted", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	reclassified := newDocument(corpus, domain.Source{Kind: domain.SourceExternal, Ref: "restricted"}, "Payroll", "payroll")
	reclassified.Access = domain.AccessMembers
	reclassified, outcome, err := store.UpsertDocument(ctx, reclassified, "payroll", testEvent(t, "rag.document.received.v1"))
	if err != nil || outcome != UpsertChanged || reclassified.ID != admin.ID || reclassified.Status != domain.StatusPending || reclassified.KBSyncedHash != "" {
		t.Fatalf("ACL update = %+v, %v, %v", reclassified, outcome, err)
	}
}

func TestSyncChunksReusesUnchangedChunks(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	body := "one two three"
	source := domain.Source{Kind: domain.SourceInline}
	document, _, err := store.UpsertDocument(ctx, newDocument(corpus, source, "Doc", body), body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}

	chunks := func(texts ...string) []domain.Chunk {
		out := make([]domain.Chunk, 0, len(texts))
		for index, text := range texts {
			out = append(out, domain.Chunk{
				Ordinal: index, Text: text, TokenCount: 1, SHA256: domain.Hash(text),
				HeadingPath: []string{"Top"}, StartByte: index * 10, EndByte: index*10 + len(text),
			})
		}
		return out
	}

	now := time.Now().UTC()
	// The pipeline records a chunk as embedded only once its vector is
	// written, and only those are reusable, so this does the same.
	embed := func(sync domain.ChunkSync) {
		t.Helper()
		ids := make([]string, 0, len(sync.Fresh))
		for _, chunk := range sync.Fresh {
			ids = append(ids, chunk.ID)
		}
		if err := store.MarkChunksEmbedded(ctx, ids, now); err != nil {
			t.Fatalf("MarkChunksEmbedded: %v", err)
		}
	}

	first, err := store.SyncChunks(ctx, document, chunks("alpha", "bravo", "charlie"), now)
	if err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}
	embed(first)
	if len(first.Fresh) != 3 || len(first.Reused) != 0 || len(first.Removed) != 0 {
		t.Fatalf("first sync = %d fresh, %d reused, %d removed", len(first.Fresh), len(first.Reused), len(first.Removed))
	}
	originalIDs := map[string]string{}
	for _, chunk := range first.Fresh {
		originalIDs[chunk.Text] = chunk.ID
	}

	// One paragraph edited. Only that chunk should need embedding again —
	// this is the difference between one call and three on every edit.
	second, err := store.SyncChunks(ctx, document, chunks("alpha", "BRAVO edited", "charlie"), now)
	if err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}
	embed(second)
	if len(second.Fresh) != 1 || len(second.Reused) != 2 {
		t.Fatalf("second sync = %d fresh, %d reused, want 1 and 2", len(second.Fresh), len(second.Reused))
	}
	if second.Fresh[0].Text != "BRAVO edited" {
		t.Fatalf("the wrong chunk was re-embedded: %q", second.Fresh[0].Text)
	}
	if len(second.Removed) != 1 || second.Removed[0] != originalIDs["bravo"] {
		t.Fatalf("removed = %v, want the old bravo chunk", second.Removed)
	}
	for _, chunk := range second.Reused {
		if originalIDs[chunk.Text] != chunk.ID {
			t.Fatalf("reused chunk %q got a new id, so its vector would be orphaned", chunk.Text)
		}
	}

	// A paragraph that only moved keeps its vector, even at a new ordinal.
	third, err := store.SyncChunks(ctx, document, chunks("charlie", "alpha"), now)
	if err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}
	embed(third)
	if len(third.Fresh) != 0 || len(third.Reused) != 2 {
		t.Fatalf("reordering = %d fresh, %d reused, want 0 and 2", len(third.Fresh), len(third.Reused))
	}
	if len(third.Removed) != 1 {
		t.Fatalf("removed = %v, want the edited chunk", third.Removed)
	}
	for _, chunk := range third.Reused {
		if originalIDs[chunk.Text] != chunk.ID {
			t.Fatalf("chunk %q changed id when it only moved", chunk.Text)
		}
	}

	stored, err := store.Chunks(ctx, organizationID, corpus.ID, []string{originalIDs["charlie"], originalIDs["alpha"]})
	if err != nil {
		t.Fatalf("Chunks: %v", err)
	}
	if len(stored) != 2 || stored[0].Text != "charlie" || stored[1].Text != "alpha" {
		t.Fatalf("Chunks did not preserve the order it was asked in: %+v", stored)
	}
	if stored[0].Ordinal != 0 || stored[1].Ordinal != 1 {
		t.Fatalf("ordinals were not rewritten: %d and %d", stored[0].Ordinal, stored[1].Ordinal)
	}
	if len(stored[0].HeadingPath) != 1 || stored[0].HeadingPath[0] != "Top" {
		t.Fatalf("heading path = %v", stored[0].HeadingPath)
	}
}

func TestLexicalSearchFindsExactTerms(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	body := "irrelevant"
	document, _, err := store.UpsertDocument(ctx, newDocument(corpus, domain.Source{Kind: domain.SourceInline}, "Doc", body), body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	texts := []string{
		"The error ERR_ROTATION_4471 means the credential could not be replaced.",
		"Holidays are accrued monthly and carry over to the next year.",
		"Rotation happens every ninety days without intervention.",
	}
	chunks := make([]domain.Chunk, 0, len(texts))
	for index, text := range texts {
		chunks = append(chunks, domain.Chunk{
			Ordinal: index, Text: text, TokenCount: 12, SHA256: domain.Hash(text),
			StartByte: index * 100, EndByte: index*100 + len(text),
			Metadata: domain.Metadata{"team": map[bool]string{true: "platform", false: "people"}[index != 1]},
		})
	}
	if _, err := store.SyncChunks(ctx, document, chunks, time.Now().UTC()); err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}

	// An exact identifier is precisely what a dense retriever misses and what
	// the lexical half is here for.
	hits, err := store.LexicalSearch(ctx, corpus, "ERR_ROTATION_4471", 10, domain.Filter{}, true)
	if err != nil {
		t.Fatalf("LexicalSearch: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("the exact error code was not found")
	}
	if hits[0].ChunkID != chunks[0].ID {
		t.Fatalf("the top hit was not the chunk containing the code")
	}

	// A metadata filter must narrow the lexical side as well as the dense one,
	// or a filtered query would leak through one half of the hybrid.
	hits, err = store.LexicalSearch(ctx, corpus, "rotation", 10, domain.Filter{
		Conditions: []domain.Condition{{Key: "team", Operator: domain.OperatorEqual, Values: []string{"people"}}},
	}, true)
	if err != nil {
		t.Fatalf("LexicalSearch: %v", err)
	}
	for _, hit := range hits {
		if hit.ChunkID == chunks[0].ID || hit.ChunkID == chunks[2].ID {
			t.Fatalf("a chunk outside the filter was returned: %s", hit.ChunkID)
		}
	}

	// A question worded as a person asks it carries words no passage holds.
	// Requiring every word found nothing; the passage with the most of them
	// is what the lexical half should return.
	hits, err = store.LexicalSearch(ctx, corpus, "When does the rotation happen, and is it automatic?", 10, domain.Filter{}, true)
	if err != nil {
		t.Fatalf("LexicalSearch: %v", err)
	}
	if len(hits) == 0 || hits[0].ChunkID != chunks[2].ID {
		t.Fatalf("a natural question found %v, want the rotation schedule first", hits)
	}

	// Punctuation must not blow up the query parser.
	for _, query := range []string{"what's the (rotation) policy?", `"ninety days"`, "rotation -holidays", "!!!", "a & b | c", "'quoted' :*"} {
		if _, err := store.LexicalSearch(ctx, corpus, query, 5, domain.Filter{}, true); err != nil {
			t.Fatalf("LexicalSearch(%q): %v", query, err)
		}
	}
}

func TestDeleteDocumentReturnsItsChunksAndCascades(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	body := "body"
	document, _, err := store.UpsertDocument(ctx, newDocument(corpus, domain.Source{Kind: domain.SourceInline}, "Doc", body), body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	sync, err := store.SyncChunks(ctx, document, []domain.Chunk{
		{Ordinal: 0, Text: "a", TokenCount: 1, SHA256: domain.Hash("a"), StartByte: 0, EndByte: 1},
		{Ordinal: 1, Text: "b", TokenCount: 1, SHA256: domain.Hash("b"), StartByte: 1, EndByte: 2},
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}

	// The ids come back before the rows go, because S3 Vectors has no cascade
	// and the caller needs the keys to delete there.
	chunkIDs, err := store.DeleteDocument(ctx, organizationID, document.ID, testEvent(t, "rag.document.removed.v1"))
	if err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	if len(chunkIDs) != len(sync.Fresh) {
		t.Fatalf("DeleteDocument returned %d chunk ids, want %d", len(chunkIDs), len(sync.Fresh))
	}
	remaining, err := store.Chunks(ctx, organizationID, corpus.ID, chunkIDs)
	if err != nil {
		t.Fatalf("Chunks: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("%d chunks survived their document", len(remaining))
	}
	if _, err := store.Document(ctx, organizationID, document.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the document read as %v after deletion", err)
	}
}

// A document whose attempt failed waits out its retry rather than being
// claimed straight back, and the attempt it failed still counts.
func TestRetryDocumentAtHoldsADocumentUntilItsTime(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())
	document, _, err := store.UpsertDocument(ctx,
		newDocument(corpus, domain.Source{Kind: domain.SourceInline}, "Doc", "retry me"), "retry me",
		testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}

	// The worker claims across every organization, so this looks for its own
	// document among whatever else is queued.
	claimMine := func() (domain.Document, bool) {
		t.Helper()
		for pass := 0; pass < 20; pass++ {
			claimed, err := store.ClaimDocuments(ctx, 10, time.Minute)
			if err != nil {
				t.Fatalf("ClaimDocuments: %v", err)
			}
			if len(claimed) == 0 {
				return domain.Document{}, false
			}
			for _, candidate := range claimed {
				if candidate.ID == document.ID {
					return candidate, true
				}
			}
		}
		return domain.Document{}, false
	}

	first, ok := claimMine()
	if !ok {
		t.Fatal("the document was never claimed")
	}
	if err := store.RetryDocumentAt(ctx, document.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("RetryDocumentAt: %v", err)
	}
	waiting, err := store.Document(ctx, organizationID, document.ID)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if waiting.Status != domain.StatusPending {
		t.Fatalf("status = %q while waiting to retry, want pending", waiting.Status)
	}
	if _, ok := claimMine(); ok {
		t.Fatal("a document waiting on its retry was claimed straight back")
	}

	// Once its time comes it is claimed again, one attempt further on.
	if _, err := store.db.ExecContext(ctx,
		`UPDATE rag_documents SET locked_until = now() - interval '1 second' WHERE id = $1`, document.ID); err != nil {
		t.Fatalf("bring the retry forward: %v", err)
	}
	second, ok := claimMine()
	if !ok {
		t.Fatal("a document whose retry was due was not claimed")
	}
	if second.Attempts != first.Attempts+1 {
		t.Fatalf("attempts = %d after %d, want the failed attempt counted", second.Attempts, first.Attempts)
	}
}

func TestClaimDocumentsLeasesWorkOnce(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	// The worker claims across every organization, because that is its job.
	// So this asserts on its own documents rather than on counts: another
	// test's pending documents are legitimately in the same queue.
	mine := map[string]bool{}
	for index := range 3 {
		body := fmt.Sprintf("document %d", index)
		document, _, err := store.UpsertDocument(ctx,
			newDocument(corpus, domain.Source{Kind: domain.SourceInline}, "Doc", body), body,
			testEvent(t, "rag.document.received.v1"))
		if err != nil {
			t.Fatalf("UpsertDocument: %v", err)
		}
		mine[document.ID] = true
	}

	claimOwn := func() []domain.Document {
		t.Helper()
		var own []domain.Document
		for pass := 0; pass < 20 && len(own) < len(mine); pass++ {
			claimed, err := store.ClaimDocuments(ctx, 10, time.Minute)
			if err != nil {
				t.Fatalf("ClaimDocuments: %v", err)
			}
			if len(claimed) == 0 {
				break
			}
			for _, document := range claimed {
				if mine[document.ID] {
					own = append(own, document)
				}
			}
		}
		return own
	}

	claimed := claimOwn()
	if len(claimed) != len(mine) {
		t.Fatalf("claimed %d of my %d documents", len(claimed), len(mine))
	}
	// Two workers must never embed the same document twice.
	held := map[string]bool{}
	for _, document := range claimed {
		if held[document.ID] {
			t.Fatalf("document %s was claimed twice", document.ID)
		}
		held[document.ID] = true
		if document.Status != domain.StatusIngesting {
			t.Fatalf("a claimed document is %q, want ingesting", document.Status)
		}
	}
	// Everything of mine is leased now, so another pass finds none of it.
	for _, document := range mustClaim(t, store, ctx) {
		if mine[document.ID] {
			t.Fatalf("document %s was claimed while another worker held its lease", document.ID)
		}
	}

	// A worker that stops mid-ingest puts its document back without counting
	// the attempt as a failure.
	if err := store.ReleaseDocument(ctx, claimed[0].ID); err != nil {
		t.Fatalf("ReleaseDocument: %v", err)
	}
	var returned bool
	for _, document := range mustClaim(t, store, ctx) {
		if document.ID == claimed[0].ID {
			returned = true
		}
	}
	if !returned {
		t.Fatal("the released document did not come back")
	}
}

func mustClaim(t *testing.T, store *Store, ctx context.Context) []domain.Document {
	t.Helper()
	claimed, err := store.ClaimDocuments(ctx, 25, time.Minute)
	if err != nil {
		t.Fatalf("ClaimDocuments: %v", err)
	}
	return claimed
}

// TestOutboxRoundTrip proves RecordEvents wrote the event; claiming, marking
// and retrying are internal/outbox's own concern now, covered by its
// table_integration_test.go.
func TestOutboxRoundTrip(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	testCorpus(t, store, ctx, organizationID, titanSpec())

	event := testEvent(t, "rag.corpus.created.v1")
	if err := store.RecordEvents(ctx, []domain.Event{event}); err != nil {
		t.Fatalf("RecordEvents: %v", err)
	}
	var pending bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rag_outbox WHERE id = $1 AND published_at IS NULL)`,
		event.ID).Scan(&pending); err != nil || !pending {
		t.Fatalf("the recorded event was not in the outbox: found = %v, err = %v", pending, err)
	}
}

func TestDeleteCorpusTakesEverythingWithIt(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	body := "content"
	document, _, err := store.UpsertDocument(ctx, newDocument(corpus, domain.Source{Kind: domain.SourceInline}, "Doc", body), body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	if _, err := store.SyncChunks(ctx, document, []domain.Chunk{
		{Ordinal: 0, Text: "a", TokenCount: 1, SHA256: domain.Hash("a"), StartByte: 0, EndByte: 1},
	}, time.Now().UTC()); err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}
	documents, chunks, err := store.CorpusCounts(ctx, corpus.ID, true)
	if err != nil || documents != 1 || chunks != 1 {
		t.Fatalf("CorpusCounts = (%d, %d, %v)", documents, chunks, err)
	}

	if err := store.DeleteCorpus(ctx, organizationID, corpus.ID, testEvent(t, "rag.corpus.deleted.v1")); err != nil {
		t.Fatalf("DeleteCorpus: %v", err)
	}
	if _, err := store.Document(ctx, organizationID, document.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a document survived its corpus: %v", err)
	}
	if err := store.DeleteCorpus(ctx, organizationID, corpus.ID, testEvent(t, "rag.corpus.deleted.v1")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleting a corpus twice returned %v, want ErrNotFound", err)
	}
}

// TestSyncChunksWillNotReuseAChunkThatWasNeverEmbedded is the regression for
// the worst failure this service could have: a document that looks searchable
// and holds no vectors.
//
// The first attempt writes every chunk row and then fails while embedding. On
// the retry those rows match by hash. If matching by hash were enough they
// would come back as reused, nothing would be embedded, and the document would
// be marked ready holding nothing at all.
func TestSyncChunksWillNotReuseAChunkThatWasNeverEmbedded(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	body := "body"
	document, _, err := store.UpsertDocument(ctx,
		newDocument(corpus, domain.Source{Kind: domain.SourceInline}, "Doc", body), body,
		testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	chunks := []domain.Chunk{
		{Ordinal: 0, Text: "alpha", TokenCount: 1, SHA256: domain.Hash("alpha"), StartByte: 0, EndByte: 5},
		{Ordinal: 1, Text: "bravo", TokenCount: 1, SHA256: domain.Hash("bravo"), StartByte: 5, EndByte: 10},
	}
	now := time.Now().UTC()

	// Attempt one: rows written, embedding then fails, nothing marked.
	first, err := store.SyncChunks(ctx, document, chunks, now)
	if err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}
	if len(first.Fresh) != 2 {
		t.Fatalf("first attempt = %d fresh, want 2", len(first.Fresh))
	}

	// The retry must embed them again, not treat them as already done.
	second, err := store.SyncChunks(ctx, document, chunks, now)
	if err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}
	if len(second.Fresh) != 2 || len(second.Reused) != 0 {
		t.Fatalf("after a failed embed the retry found %d fresh and %d reused; "+
			"reusing an unembedded chunk would mark the document searchable with no vectors",
			len(second.Fresh), len(second.Reused))
	}

	// Once the vectors are recorded, the saving comes back.
	embedded := make([]string, 0, len(second.Fresh))
	for _, chunk := range second.Fresh {
		embedded = append(embedded, chunk.ID)
	}
	if err := store.MarkChunksEmbedded(ctx, embedded, now); err != nil {
		t.Fatalf("MarkChunksEmbedded: %v", err)
	}
	third, err := store.SyncChunks(ctx, document, chunks, now)
	if err != nil {
		t.Fatalf("SyncChunks: %v", err)
	}
	if len(third.Fresh) != 0 || len(third.Reused) != 2 {
		t.Fatalf("after the vectors were recorded = %d fresh, %d reused, want 0 and 2",
			len(third.Fresh), len(third.Reused))
	}
}

// TestDocumentsPaginatesWithACursor backs the listing's cursor: every
// document is reachable across pages, in the same newest-first order an
// unpaged read gives, with no repeat and no skip at a page boundary — and a
// caller who names no cursor gets exactly the first page it always got.
func TestDocumentsPaginatesWithACursor(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	// Explicit, strictly increasing timestamps: pagination is ordered by
	// created_at, and the wall clock's resolution is not reliably finer than
	// what five inserts in a row would need to stay distinct.
	base := time.Now().UTC().Truncate(time.Microsecond)
	created := make([]domain.Document, 0, 5)
	for index := range 5 {
		body := fmt.Sprintf("document %d", index)
		document := newDocument(corpus, domain.Source{Kind: domain.SourceInline}, fmt.Sprintf("Doc %d", index), body)
		document.CreatedAt = base.Add(time.Duration(index) * time.Second)
		document.UpdatedAt = document.CreatedAt
		stored, _, err := store.UpsertDocument(ctx, document, body, testEvent(t, "rag.document.received.v1"))
		if err != nil {
			t.Fatalf("UpsertDocument: %v", err)
		}
		created = append(created, stored)
	}
	newestFirst := func(index int) string { return created[len(created)-1-index].ID }

	// No cursor must return exactly the first page an unpaged caller gets
	// today: the newest three, with a cursor to continue.
	firstPage, next, err := store.Documents(ctx, organizationID, corpus.ID, "", "", 3, true)
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if len(firstPage) != 3 || next == "" {
		t.Fatalf("first page = %d documents, next_cursor = %q, want 3 and a cursor", len(firstPage), next)
	}
	for index, document := range firstPage {
		if document.ID != newestFirst(index) {
			t.Fatalf("first page order = %+v", firstPage)
		}
	}

	// Walking the cursor reaches every document exactly once, newest first
	// throughout, with a page size never larger than asked for.
	var walked []domain.Document
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > len(created) {
			t.Fatal("Documents did not terminate: the cursor is not making progress")
		}
		page, next, err := store.Documents(ctx, organizationID, corpus.ID, "", cursor, 2, true)
		if err != nil {
			t.Fatalf("Documents: %v", err)
		}
		if len(page) > 2 {
			t.Fatalf("page held %d documents, want at most 2", len(page))
		}
		walked = append(walked, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(walked) != len(created) {
		t.Fatalf("walked %d documents across pages, want %d", len(walked), len(created))
	}
	for index, document := range walked {
		if document.ID != newestFirst(index) {
			t.Fatalf("page %d: got %s, want %s (newest-first order was not preserved across the boundary)",
				index, document.ID, newestFirst(index))
		}
	}

	// A page that holds everything carries no cursor, so a caller knows to
	// stop asking.
	_, lastNext, err := store.Documents(ctx, organizationID, corpus.ID, "", "", len(created), true)
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if lastNext != "" {
		t.Fatalf("next_cursor = %q on a page holding everything, want none", lastNext)
	}

	// A cursor that was not handed back unmodified is refused rather than
	// silently treated as the start.
	if _, _, err := store.Documents(ctx, organizationID, corpus.ID, "", "not-a-real-cursor", 2, true); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a malformed cursor returned %v, want ErrInvalid", err)
	}
}

// TestDocumentsBySourceFindsEveryCopy backs removing what a deleted file left
// behind. Reading each corpus's newest page instead would miss the documents
// in a corpus holding more than one.
func TestDocumentsBySourceFindsEveryCopy(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())

	const fileID = "file_shared"
	body := "the same file, indexed once"
	wanted, _, err := store.UpsertDocument(ctx,
		newDocument(corpus, domain.Source{Kind: domain.SourceFile, Ref: fileID}, "Shared", body), body,
		testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	// Plenty of other documents, so a page-limited scan would not reach it.
	for index := range 5 {
		other := fmt.Sprintf("another document %d", index)
		if _, _, err := store.UpsertDocument(ctx,
			newDocument(corpus, domain.Source{Kind: domain.SourceInline}, "Other", other), other,
			testEvent(t, "rag.document.received.v1")); err != nil {
			t.Fatalf("UpsertDocument: %v", err)
		}
	}

	found, err := store.DocumentsBySource(ctx, organizationID, domain.SourceFile, fileID)
	if err != nil {
		t.Fatalf("DocumentsBySource: %v", err)
	}
	if len(found) != 1 || found[0].ID != wanted.ID {
		t.Fatalf("DocumentsBySource returned %d documents, want the one indexed from that file", len(found))
	}
	none, err := store.DocumentsBySource(ctx, organizationID, domain.SourceFile, "file_never_indexed")
	if err != nil {
		t.Fatalf("DocumentsBySource: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("a file that was never indexed matched %d documents", len(none))
	}

	ids, err := store.ChunkIDs(ctx, organizationID, wanted.ID)
	if err != nil {
		t.Fatalf("ChunkIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("an un-ingested document has %d chunks", len(ids))
	}
}
