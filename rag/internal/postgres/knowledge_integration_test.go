package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// mappingsEqual compares two mappings for the same meaning rather than the
// same time.Time representation: a timestamptz round trip through whatever
// timezone the session is in changes the Location on the way back without
// changing the instant, which a plain == would wrongly treat as a mismatch.
func mappingsEqual(a, b domain.KnowledgeDocumentMapping) bool {
	return a.KnowledgeDocumentID == b.KnowledgeDocumentID &&
		a.OrganizationID == b.OrganizationID &&
		a.CorpusID == b.CorpusID &&
		a.DocumentID == b.DocumentID &&
		a.ContentHash == b.ContentHash &&
		a.ExtractedAt.Equal(b.ExtractedAt) &&
		a.UpdatedAt.Equal(b.UpdatedAt)
}

// testKnowledgeDocument gives a test a corpus and a rag document already
// indexed under it, which is what a knowledge document mapping always points
// at.
func testKnowledgeDocument(t *testing.T, store *Store, ctx context.Context, organizationID string) (domain.Corpus, domain.Document) {
	t.Helper()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())
	body := "the text knowledge extracted"
	document, _, err := store.UpsertDocument(ctx,
		newDocument(corpus, domain.Source{Kind: domain.SourceExternal, Ref: "https://example.com/doc"}, "Doc", body),
		body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument: %v", err)
	}
	return corpus, document
}

func TestKnowledgeDocumentMappingRoundTrip(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus, document := testKnowledgeDocument(t, store, ctx, organizationID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	mapping := domain.KnowledgeDocumentMapping{
		KnowledgeDocumentID: "kdoc_1", OrganizationID: organizationID,
		CorpusID: corpus.ID, DocumentID: document.ID,
		ContentHash: "hash-1", ExtractedAt: now, UpdatedAt: now,
	}
	if err := store.UpsertKnowledgeDocumentMapping(ctx, mapping); err != nil {
		t.Fatalf("UpsertKnowledgeDocumentMapping: %v", err)
	}

	read, err := store.KnowledgeDocumentMapping(ctx, organizationID, "kdoc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	if !mappingsEqual(read, mapping) {
		t.Fatalf("read mapping = %+v, want %+v", read, mapping)
	}

	// Another organization must not see it, exactly as a corpus or a document
	// in one organization is invisible from another.
	if _, err := store.KnowledgeDocumentMapping(ctx, "org-someone-else", "kdoc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a mapping read from another organization = %v, want ErrNotFound", err)
	}
}

func TestKnowledgeDocumentMappingNotFound(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	if _, err := store.KnowledgeDocumentMapping(ctx, organizationID, "kdoc_never-existed"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("KnowledgeDocumentMapping for an unknown id = %v, want ErrNotFound", err)
	}
}

func TestUpsertKnowledgeDocumentMappingUpdatesInPlace(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	_, firstDocument := testKnowledgeDocument(t, store, ctx, organizationID)
	secondCorpus, secondDocument := testKnowledgeDocument(t, store, ctx, organizationID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	first := domain.KnowledgeDocumentMapping{
		KnowledgeDocumentID: "kdoc_1", OrganizationID: organizationID,
		CorpusID: secondCorpus.ID, DocumentID: firstDocument.ID,
		ContentHash: "hash-1", ExtractedAt: now, UpdatedAt: now,
	}
	if err := store.UpsertKnowledgeDocumentMapping(ctx, first); err != nil {
		t.Fatalf("first UpsertKnowledgeDocumentMapping: %v", err)
	}

	// A content change replaces the same rag document in place (the ref did
	// not change), so what this bridge writes back is the same corpus and
	// document with a new hash — never a second row for the same knowledge
	// document id, which the primary key would refuse anyway.
	later := now.Add(time.Hour)
	second := domain.KnowledgeDocumentMapping{
		KnowledgeDocumentID: "kdoc_1", OrganizationID: organizationID,
		CorpusID: secondCorpus.ID, DocumentID: secondDocument.ID,
		ContentHash: "hash-2", ExtractedAt: later, UpdatedAt: later,
	}
	if err := store.UpsertKnowledgeDocumentMapping(ctx, second); err != nil {
		t.Fatalf("second UpsertKnowledgeDocumentMapping: %v", err)
	}

	read, err := store.KnowledgeDocumentMapping(ctx, organizationID, "kdoc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	if !mappingsEqual(read, second) {
		t.Fatalf("read mapping = %+v, want the second upsert %+v", read, second)
	}
}

func TestDeleteKnowledgeDocumentMapping(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus, document := testKnowledgeDocument(t, store, ctx, organizationID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.UpsertKnowledgeDocumentMapping(ctx, domain.KnowledgeDocumentMapping{
		KnowledgeDocumentID: "kdoc_1", OrganizationID: organizationID,
		CorpusID: corpus.ID, DocumentID: document.ID,
		ContentHash: "hash-1", ExtractedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertKnowledgeDocumentMapping: %v", err)
	}

	if err := store.DeleteKnowledgeDocumentMapping(ctx, organizationID, "kdoc_1"); err != nil {
		t.Fatalf("DeleteKnowledgeDocumentMapping: %v", err)
	}
	if _, err := store.KnowledgeDocumentMapping(ctx, organizationID, "kdoc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a deleted mapping still reads back: %v", err)
	}
	// Deleting it again is what a repeated removal event does: it must report
	// not found rather than erroring, exactly like DeleteDocument and
	// DeleteCorpus do for the same reason.
	if err := store.DeleteKnowledgeDocumentMapping(ctx, organizationID, "kdoc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleting an already-deleted mapping = %v, want ErrNotFound", err)
	}
}

// TestKnowledgeDocumentMappingCascadesWithItsDocument backs
// Service.removeKnowledgeDocument's isMissing branch: when a document goes
// away some other way, the mapping must not be left pointing at nothing.
func TestKnowledgeDocumentMappingCascadesWithItsDocument(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus, document := testKnowledgeDocument(t, store, ctx, organizationID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.UpsertKnowledgeDocumentMapping(ctx, domain.KnowledgeDocumentMapping{
		KnowledgeDocumentID: "kdoc_1", OrganizationID: organizationID,
		CorpusID: corpus.ID, DocumentID: document.ID,
		ContentHash: "hash-1", ExtractedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertKnowledgeDocumentMapping: %v", err)
	}

	if _, err := store.DeleteDocument(ctx, organizationID, document.ID, testEvent(t, "rag.document.removed.v1")); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	if _, err := store.KnowledgeDocumentMapping(ctx, organizationID, "kdoc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the mapping survived its document being deleted directly: %v", err)
	}
}

// TestKnowledgeDocumentMappingCascadesWithItsCorpus backs the same
// isMissing branch for a whole corpus wipe — an organization, a team or a
// member removed — which is the far more common way a knowledge document's
// mapping should disappear without this bridge doing anything itself.
func TestKnowledgeDocumentMappingCascadesWithItsCorpus(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus, document := testKnowledgeDocument(t, store, ctx, organizationID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.UpsertKnowledgeDocumentMapping(ctx, domain.KnowledgeDocumentMapping{
		KnowledgeDocumentID: "kdoc_1", OrganizationID: organizationID,
		CorpusID: corpus.ID, DocumentID: document.ID,
		ContentHash: "hash-1", ExtractedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertKnowledgeDocumentMapping: %v", err)
	}

	if err := store.DeleteCorpus(ctx, organizationID, corpus.ID, testEvent(t, "rag.corpus.deleted.v1")); err != nil {
		t.Fatalf("DeleteCorpus: %v", err)
	}
	if _, err := store.KnowledgeDocumentMapping(ctx, organizationID, "kdoc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the mapping survived its corpus being deleted: %v", err)
	}
}
