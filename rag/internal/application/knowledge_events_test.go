package application

import (
	"context"
	"errors"
	"testing"
	"time"

	knowledgeeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/knowledgeclient"
)

// testDocumentPublished is a document a knowledge source has just extracted:
// member-scoped, real text, a URL a citation could point back at. Tests
// override only what they are testing.
func testDocumentPublished(overrides func(*knowledgeeventsv1.DocumentPublished)) knowledgeeventsv1.DocumentPublished {
	event := knowledgeeventsv1.DocumentPublished{
		DocumentID:     "doc_1",
		OrganizationID: testOrganization,
		SourceID:       "src_1",
		Provider:       "github",
		Scope:          "member",
		MemberID:       "user-1",
		ExternalID:     "42",
		Kind:           "issue",
		Title:          "Fix the bug",
		URL:            "https://github.com/acme/repo/issues/42",
		MIME:           "text/markdown",
		ContentHash:    "hash-1",
		Body:           "# Fix the bug\n\nDetails here.",
		ExtractedAt:    testClock,
	}
	if overrides != nil {
		overrides(&event)
	}
	return event
}

// newHarnessWithBedrockKBDefault is a harness whose knowledge bridge creates
// bedrock-kb corpora, for testing what happens to a knowledge document that
// carries only its original bytes.
func newHarnessWithBedrockKBDefault(t *testing.T) *harness {
	t.Helper()
	h := newHarness()
	h.service = New(h.store, fakeDirectory{}, h.teams, h.files, h.documents, h.vectors, h.retriever, h.generator,
		h.knowledgeBase, h.authzEngine, h.authzStore, Defaults{Backend: domain.BackendBedrockKB}, nil)
	h.service.now = func() time.Time { return testClock }
	return h
}

func TestKnowledgeBillingDocumentCannotDowngradeItsACL(t *testing.T) {
	h := newHarness()
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.Provider = "files"
		event.Scope = "organization"
		event.MemberID = ""
		event.Kind = "file"
		event.ExternalID = "file_invoice"
		event.Metadata = map[string]string{"purpose": "billing.attachment"}
		event.Access = "members" // even a malformed event cannot downgrade Files
	})
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, event.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	document, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID)
	if err != nil || document.Access != domain.AccessAdmins {
		t.Fatalf("billing access = %+v, %v", document, err)
	}
}

func TestKnowledgeRestrictedSharedFileCannotDowngradeItsACL(t *testing.T) {
	h := newHarness()
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.Provider = "files"
		event.Scope = "organization"
		event.MemberID = ""
		event.Kind = "file"
		event.ExternalID = "file_restricted"
		event.Metadata = map[string]string{"purpose": "upload", "access": "admins"}
		event.Access = "members"
	})
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, event.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	document, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID)
	if err != nil || document.Access != domain.AccessAdmins {
		t.Fatalf("restricted access = %+v, %v", document, err)
	}
}

// TestDocumentPublishedWithAnOriginalIndexesItOnABedrockKBCorpus: a document
// with no extracted text — a PDF, an Office file — is still recorded, with
// no body and the location of its own bytes instead, so the ingest worker
// can hand that location straight to the knowledge base.
func TestDocumentPublishedWithAnOriginalIndexesItOnABedrockKBCorpus(t *testing.T) {
	h := newHarnessWithBedrockKBDefault(t)
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.Body = ""
		event.MIME = "application/pdf"
		event.Original = &knowledgeeventsv1.Original{Bucket: "oryxa-knowledge-originals", Key: "org/doc/handbook.pdf", Size: 4096, SHA256: "original-sha"}
		event.ContentHash = "original-sha"
	})
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("DocumentPublished() error = %v", err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	document, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if !document.HasOriginal() {
		t.Fatalf("HasOriginal() is false: %+v", document)
	}
	if document.OriginalBucket != "oryxa-knowledge-originals" || document.OriginalKey != "org/doc/handbook.pdf" {
		t.Fatalf("original location did not reach the document: %+v", document)
	}
	if document.ContentSHA256 != "original-sha" || document.ByteSize != 4096 {
		t.Fatalf("content hash/size = %q/%d, want the original's", document.ContentSHA256, document.ByteSize)
	}
	corpus, err := h.store.Corpus(context.Background(), testOrganization, document.CorpusID)
	if err != nil {
		t.Fatalf("Corpus: %v", err)
	}
	if corpus.Backend != domain.BackendBedrockKB {
		t.Fatalf("corpus backend = %q, want bedrock-kb", corpus.Backend)
	}
}

// TestDocumentPublishedWithAnOriginalSkipsANonBedrockKBCorpus: without a
// knowledge base to parse them, a document's own bytes are not something
// this bridge can turn into anything searchable, so it is skipped rather
// than indexed as a title with nothing inside it.
func TestDocumentPublishedWithAnOriginalSkipsANonBedrockKBCorpus(t *testing.T) {
	h := newHarness() // pgvector by default
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.Body = ""
		event.MIME = "application/pdf"
		event.Original = &knowledgeeventsv1.Original{Bucket: "b", Key: "k", Size: 10, SHA256: "sha-1"}
		event.ContentHash = "sha-1"
	})
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("DocumentPublished() error = %v", err)
	}
	if len(h.store.documents) != 0 {
		t.Fatalf("a document with only an original was indexed on a corpus that cannot parse it: %v", h.store.documents)
	}
	if _, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("a mapping was recorded for a document that was skipped")
	}
}

// TestDocumentPublishedWithAnOriginalOnANonKBCorpusRemovesAPreviouslyIndexedVersion:
// a document that used to carry text and now carries only its original bytes
// is removed from a corpus that cannot parse them, rather than left
// answering from text that no longer matches what the source has —
// symmetric with the same rule for a body too large to carry.
func TestDocumentPublishedWithAnOriginalOnANonKBCorpusRemovesAPreviouslyIndexedVersion(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(nil)
	if err := h.service.DocumentPublished(context.Background(), first); err != nil {
		t.Fatalf("first DocumentPublished() error = %v", err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	h.store.chunkIDs[mapping.DocumentID] = []string{"chk_1"}

	becameOriginalOnly := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.Body = ""
		event.Original = &knowledgeeventsv1.Original{Bucket: "b", Key: "k", Size: 10, SHA256: "sha-2"}
		event.ContentHash = "sha-2"
		event.ExtractedAt = testClock.Add(time.Hour)
	})
	if err := h.service.DocumentPublished(context.Background(), becameOriginalOnly); err != nil {
		t.Fatalf("second DocumentPublished() error = %v", err)
	}
	if _, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("the previously indexed document survived becoming original-only on a non-KB corpus")
	}
	if _, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("the mapping survived")
	}
	if got := h.vectors.store.deleted[mapping.CorpusID]; len(got) != 1 || got[0] != "chk_1" {
		t.Fatalf("vectors deleted for %s = %v, want the removed document's one chunk", mapping.CorpusID, got)
	}
}

func TestDocumentPublishedCreatesTheOwnersCorpusAndIndexesTheDocument(t *testing.T) {
	h := newHarness()
	event := testDocumentPublished(nil)
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("DocumentPublished() error = %v", err)
	}

	slug, err := knowledgeCorpusSlug(domain.ScopeMember, "", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := h.store.CorpusBySlug(context.Background(), testOrganization, slug)
	if err != nil {
		t.Fatalf("the member's corpus was not created: %v", err)
	}
	if corpus.Scope != domain.ScopeMember || corpus.MemberID != "user-1" || corpus.TeamID != "" {
		t.Fatalf("corpus placement = %+v", corpus)
	}
	if corpus.Name != knowledgeCorpusName {
		t.Fatalf("corpus name = %q, want %q", corpus.Name, knowledgeCorpusName)
	}
	if corpus.Metadata["origin"] != "knowledge" {
		t.Fatalf("corpus metadata = %v, want origin=knowledge so it reads as what it is", corpus.Metadata)
	}
	if corpus.CreatedBy != "user-1" {
		t.Fatalf("corpus created_by = %q, want the member", corpus.CreatedBy)
	}

	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	if mapping.CorpusID != corpus.ID || mapping.ContentHash != "hash-1" {
		t.Fatalf("mapping = %+v", mapping)
	}

	document, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if document.CorpusID != corpus.ID {
		t.Fatalf("document landed in corpus %s, want %s", document.CorpusID, corpus.ID)
	}
	if document.Source.Kind != domain.SourceExternal || document.Source.Ref != event.URL {
		t.Fatalf("document source = %+v, want external at the event's URL", document.Source)
	}
	if document.Title != "Fix the bug" {
		t.Fatalf("document title = %q", document.Title)
	}
	if document.ContentType != "text/markdown" {
		t.Fatalf("document content type = %q", document.ContentType)
	}
	if document.Metadata["knowledge_document_id"] != "doc_1" || document.Metadata["knowledge_source_id"] != "src_1" ||
		document.Metadata["provider"] != "github" || document.Metadata["document_kind"] != "issue" {
		t.Fatalf("document metadata = %v", document.Metadata)
	}
	if document.CreatedBy != "user-1" {
		t.Fatalf("document created_by = %q, want the member", document.CreatedBy)
	}
	if document.Status != domain.StatusPending {
		t.Fatalf("document status = %q, want pending: the ingest worker, not this handler, does the rest", document.Status)
	}
}

func TestKnowledgeCategoryChangeUpdatesSameContent(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.Metadata = map[string]string{"knowledge_category": "work_material"}
	})
	if err := h.service.DocumentPublished(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	updated := first
	updated.ExtractedAt = first.ExtractedAt.Add(time.Minute)
	updated.Metadata = map[string]string{"knowledge_category": "support_case"}
	if err := h.service.DocumentPublished(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	after, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID)
	if err != nil || after.Metadata["category"] != "support_case" || after.ContentSHA256 != before.ContentSHA256 {
		t.Fatalf("same-content category update = %+v, %v", after, err)
	}
}

func TestDocumentPublishedWithTheSameHashDoesNothing(t *testing.T) {
	h := newHarness()
	event := testDocumentPublished(nil)
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("first DocumentPublished() error = %v", err)
	}
	if len(h.store.corpora) != 1 || len(h.store.documents) != 1 {
		t.Fatalf("after the first publish: %d corpora, %d documents, want 1 and 1", len(h.store.corpora), len(h.store.documents))
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}

	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("re-delivered DocumentPublished() error = %v", err)
	}
	if len(h.store.corpora) != 1 || len(h.store.documents) != 1 {
		t.Fatalf("after re-delivery: %d corpora, %d documents, want no new ones", len(h.store.corpora), len(h.store.documents))
	}
	again, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil || again.DocumentID != mapping.DocumentID {
		t.Fatalf("mapping changed on a re-delivery of the same content: %+v (was %+v), err=%v", again, mapping, err)
	}
}

func TestSameContentAtALaterExtractionAdvancesTheMappedVersion(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(nil)
	if err := h.service.DocumentPublished(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	later := first
	later.ExtractedAt = first.ExtractedAt.Add(time.Hour)
	if err := h.service.DocumentPublished(context.Background(), later); err != nil {
		t.Fatal(err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
	if err != nil || !mapping.ExtractedAt.Equal(later.ExtractedAt) {
		t.Fatalf("mapped version = %+v, %v", mapping, err)
	}
	if len(h.store.documents) != 1 {
		t.Fatalf("same content created %d documents", len(h.store.documents))
	}
}

func TestDocumentPublishedWithANewHashReplacesTheText(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(nil)
	if err := h.service.DocumentPublished(context.Background(), first); err != nil {
		t.Fatalf("first DocumentPublished() error = %v", err)
	}
	before, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	beforeDocument, err := h.store.Document(context.Background(), testOrganization, before.DocumentID)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}

	second := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.ContentHash = "hash-2"
		event.Body = "# Fix the bug\n\nA longer explanation replaces the old one."
		event.ExtractedAt = testClock.Add(time.Hour)
	})
	if err := h.service.DocumentPublished(context.Background(), second); err != nil {
		t.Fatalf("second DocumentPublished() error = %v", err)
	}

	if len(h.store.corpora) != 1 {
		t.Fatalf("a content change created a second corpus: %d", len(h.store.corpora))
	}
	after, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	if after.DocumentID != before.DocumentID {
		t.Fatalf("a content change replaced the document instead of updating it in place: %s then %s", before.DocumentID, after.DocumentID)
	}
	if after.ContentHash != "hash-2" {
		t.Fatalf("mapping content hash = %q, want the new one", after.ContentHash)
	}
	afterDocument, err := h.store.Document(context.Background(), testOrganization, after.DocumentID)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if afterDocument.ContentSHA256 == beforeDocument.ContentSHA256 {
		t.Fatal("the document's own content hash did not change even though the text did")
	}
}

func TestDocumentPublishedIgnoresAnEventOlderThanWhatIsMapped(t *testing.T) {
	h := newHarness()
	newer := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.ContentHash = "hash-new"
		event.Body = "the current text"
		event.ExtractedAt = testClock
	})
	if err := h.service.DocumentPublished(context.Background(), newer); err != nil {
		t.Fatalf("DocumentPublished(newer) error = %v", err)
	}

	older := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.ContentHash = "hash-old"
		event.Body = "a stale, out-of-order redelivery"
		event.ExtractedAt = testClock.Add(-time.Hour)
	})
	if err := h.service.DocumentPublished(context.Background(), older); err != nil {
		t.Fatalf("DocumentPublished(older) error = %v", err)
	}

	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	if mapping.ContentHash != "hash-new" {
		t.Fatalf("an older event overwrote a newer one: mapping content hash = %q, want hash-new", mapping.ContentHash)
	}
}

func TestDocumentPublishedMovesToANewDocumentWhenTheReferenceChanges(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.URL = "https://github.com/acme/repo/issues/42"
	})
	if err := h.service.DocumentPublished(context.Background(), first); err != nil {
		t.Fatalf("first DocumentPublished() error = %v", err)
	}
	before, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}

	moved := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		// The issue was transferred to a different repository, so knowledge
		// hands it a new URL for the same logical document.
		event.URL = "https://github.com/acme/repo-renamed/issues/42"
		event.ContentHash = "hash-2"
		event.Body = "the same issue, moved"
		event.ExtractedAt = testClock.Add(time.Hour)
	})
	if err := h.service.DocumentPublished(context.Background(), moved); err != nil {
		t.Fatalf("moved DocumentPublished() error = %v", err)
	}

	if _, err := h.store.Document(context.Background(), testOrganization, before.DocumentID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the document at the old reference survived: %v", err)
	}
	after, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	if after.DocumentID == before.DocumentID {
		t.Fatal("the mapping still points at the document under the old reference")
	}
	newDocument, err := h.store.Document(context.Background(), testOrganization, after.DocumentID)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if newDocument.Source.Ref != "https://github.com/acme/repo-renamed/issues/42" {
		t.Fatalf("new document ref = %q", newDocument.Source.Ref)
	}
}

func TestAnOmittedBodyThatCannotBeReadBackIsNotIndexed(t *testing.T) {
	h := newHarness()
	// Nothing to read back: a deployment with no way into knowledge's
	// internal API, which is what an empty fake stands for.
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.BodyOmitted = true
		event.Body = ""
	})
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("DocumentPublished() error = %v", err)
	}
	if len(h.store.corpora) != 0 || len(h.store.documents) != 0 {
		t.Fatalf("an omitted body with no prior version indexed something: %d corpora, %d documents", len(h.store.corpora), len(h.store.documents))
	}
	if _, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a mapping was recorded for a document never indexed: %v", err)
	}
}

func TestAnOmittedBodyThatCannotBeReadBackRemovesAPreviouslyIndexedVersion(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(nil)
	if err := h.service.DocumentPublished(context.Background(), first); err != nil {
		t.Fatalf("first DocumentPublished() error = %v", err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	h.store.chunkIDs[mapping.DocumentID] = []string{"chk_1"}

	grewTooLarge := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.BodyOmitted = true
		event.Body = ""
		event.ContentHash = "hash-2"
		event.ExtractedAt = testClock.Add(time.Hour)
	})
	if err := h.service.DocumentPublished(context.Background(), grewTooLarge); err != nil {
		t.Fatalf("DocumentPublished(body omitted) error = %v", err)
	}

	// With no way to read it back, the stale text is removed rather than
	// left to answer questions the source no longer backs.
	if _, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the previously indexed document survived an omitted-body republish: %v", err)
	}
	if _, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("the mapping survived an omitted-body republish")
	}
	if got := h.vectors.store.deleted[mapping.CorpusID]; len(got) != 1 || got[0] != "chk_1" {
		t.Fatalf("vectors deleted for %s = %v, want the removed document's one chunk", mapping.CorpusID, got)
	}
}

func TestDocumentPublishedRefusesAMemberScopedEventWithNoMember(t *testing.T) {
	h := newHarness()
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.MemberID = ""
	})
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("DocumentPublished() error = %v, want a silent refusal rather than a failure to retry", err)
	}
	if len(h.store.corpora) != 0 || len(h.store.documents) != 0 {
		t.Fatalf("a member-scoped event with no member was indexed anyway: %d corpora, %d documents", len(h.store.corpora), len(h.store.documents))
	}
}

func TestDocumentPublishedRefusesATeamScopedEventWithNoTeam(t *testing.T) {
	h := newHarness()
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.Scope = "team"
		event.MemberID = ""
		event.TeamID = ""
	})
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("DocumentPublished() error = %v, want a silent refusal rather than a failure to retry", err)
	}
	if len(h.store.corpora) != 0 || len(h.store.documents) != 0 {
		t.Fatalf("a team-scoped event with no team was indexed anyway: %d corpora, %d documents", len(h.store.corpora), len(h.store.documents))
	}
}

func TestDocumentPublishedGivesEachMemberTheirOwnCorpus(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.DocumentID, event.MemberID = "doc_1", "user-1"
	})
	second := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.DocumentID, event.MemberID = "doc_2", "user-2"
	})
	if err := h.service.DocumentPublished(context.Background(), first); err != nil {
		t.Fatalf("DocumentPublished(user-1) error = %v", err)
	}
	if err := h.service.DocumentPublished(context.Background(), second); err != nil {
		t.Fatalf("DocumentPublished(user-2) error = %v", err)
	}

	if len(h.store.corpora) != 2 {
		t.Fatalf("two different members produced %d corpora, want 2", len(h.store.corpora))
	}
	slugOne, err := knowledgeCorpusSlug(domain.ScopeMember, "", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	slugTwo, err := knowledgeCorpusSlug(domain.ScopeMember, "", "user-2")
	if err != nil {
		t.Fatal(err)
	}
	corpusOne, err := h.store.CorpusBySlug(context.Background(), testOrganization, slugOne)
	if err != nil {
		t.Fatalf("user-1's corpus: %v", err)
	}
	corpusTwo, err := h.store.CorpusBySlug(context.Background(), testOrganization, slugTwo)
	if err != nil {
		t.Fatalf("user-2's corpus: %v", err)
	}
	if corpusOne.ID == corpusTwo.ID {
		t.Fatal("two different members were placed in the same corpus")
	}
	if corpusOne.MemberID != "user-1" || corpusTwo.MemberID != "user-2" {
		t.Fatalf("corpus owners = %q, %q", corpusOne.MemberID, corpusTwo.MemberID)
	}
}

func TestKnowledgeCorpusKeepsCollidingMemberIDsSeparate(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.DocumentID, event.MemberID = "doc_upper", "user_A"
	})
	second := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.DocumentID, event.MemberID = "doc_lower", "user-a"
	})
	for _, event := range []knowledgeeventsv1.DocumentPublished{first, second} {
		if err := h.service.DocumentPublished(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	firstMapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	secondMapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, second.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if firstMapping.CorpusID == secondMapping.CorpusID {
		t.Fatal("member ids that collide after slug conversion shared a corpus")
	}
	for _, tc := range []struct{ id, corpusID string }{{first.MemberID, firstMapping.CorpusID}, {second.MemberID, secondMapping.CorpusID}} {
		corpus, err := h.store.Corpus(context.Background(), testOrganization, tc.corpusID)
		if err != nil || corpus.MemberID != tc.id {
			t.Fatalf("member %s corpus = %+v, %v", tc.id, corpus, err)
		}
	}
}

func TestKnowledgeCorpusKeepsCollidingTeamIDsSeparate(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.DocumentID, event.Scope, event.MemberID, event.TeamID = "doc_upper", "team", "", "team_A"
	})
	second := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.DocumentID, event.Scope, event.MemberID, event.TeamID = "doc_lower", "team", "", "team-a"
	})
	for _, event := range []knowledgeeventsv1.DocumentPublished{first, second} {
		if err := h.service.DocumentPublished(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	firstMapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	secondMapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, second.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if firstMapping.CorpusID == secondMapping.CorpusID {
		t.Fatal("team ids that collide after slug conversion shared a corpus")
	}
	for _, tc := range []struct{ id, corpusID string }{{first.TeamID, firstMapping.CorpusID}, {second.TeamID, secondMapping.CorpusID}} {
		corpus, err := h.store.Corpus(context.Background(), testOrganization, tc.corpusID)
		if err != nil || corpus.TeamID != tc.id {
			t.Fatalf("team %s corpus = %+v, %v", tc.id, corpus, err)
		}
	}
}

func TestKnowledgeCorpusReusesOnlyMatchingLegacyOwner(t *testing.T) {
	h := newHarness()
	legacySlug, err := legacyKnowledgeCorpusSlug(domain.ScopeMember, "", "user_A")
	if err != nil {
		t.Fatal(err)
	}
	legacy := seedCorpus(h.store, domain.Corpus{
		ID: "cor_legacy", Slug: legacySlug, Scope: domain.ScopeMember,
		MemberID: "user_A", Metadata: domain.Metadata{"origin": knowledgeCorpusOrigin},
	})
	for _, tc := range []struct{ id, member string }{{"doc_upper", "user_A"}, {"doc_lower", "user-a"}} {
		event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
			event.DocumentID, event.MemberID = tc.id, tc.member
		})
		if err := h.service.DocumentPublished(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	upper, _ := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_upper")
	lower, _ := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_lower")
	if upper.CorpusID != legacy.ID || lower.CorpusID == legacy.ID {
		t.Fatalf("legacy corpus reused across owners: upper=%s lower=%s legacy=%s", upper.CorpusID, lower.CorpusID, legacy.ID)
	}
}

func TestKnowledgeCorpusRefusesAnOccupiedSlugWithoutKnowledgeOrigin(t *testing.T) {
	h := newHarness()
	slug, err := knowledgeCorpusSlug(domain.ScopeMember, "", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	seedCorpus(h.store, domain.Corpus{ID: "cor_handmade", Slug: slug, Scope: domain.ScopeMember, MemberID: "user-1"})
	if err := h.service.DocumentPublished(context.Background(), testDocumentPublished(nil)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("publish into a member-created corpus = %v, want conflict", err)
	}
	if len(h.store.documents) != 0 {
		t.Fatal("a knowledge document was indexed into a corpus it did not create")
	}
}

func TestDocumentPublishedMovesSameContentToNewMember(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(nil)
	if err := h.service.DocumentPublished(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	before, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	h.store.chunkIDs[before.DocumentID] = []string{"chk_old"}
	moved := first
	moved.MemberID = "user-2"
	moved.ExtractedAt = first.ExtractedAt.Add(time.Hour)
	if err := h.service.DocumentPublished(context.Background(), moved); err != nil {
		t.Fatal(err)
	}
	after, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if after.CorpusID == before.CorpusID || after.DocumentID == before.DocumentID {
		t.Fatalf("document did not move to the new member: before=%+v after=%+v", before, after)
	}
	if _, err := h.store.Document(context.Background(), testOrganization, before.DocumentID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("old member's indexed document survived the move")
	}
	if got := h.vectors.store.deleted[before.CorpusID]; len(got) != 1 || got[0] != "chk_old" {
		t.Fatalf("old member's vectors were not removed: %v", got)
	}
	newCorpus, err := h.store.Corpus(context.Background(), testOrganization, after.CorpusID)
	if err != nil || newCorpus.MemberID != "user-2" {
		t.Fatalf("new corpus = %+v, %v", newCorpus, err)
	}
}

func TestDocumentPublishedMovesSameContentToNewTeam(t *testing.T) {
	h := newHarness()
	first := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.Scope, event.MemberID, event.TeamID = "team", "", "team_eng"
	})
	if err := h.service.DocumentPublished(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	before, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	moved := first
	moved.TeamID = "team_design"
	moved.ExtractedAt = first.ExtractedAt.Add(time.Hour)
	if err := h.service.DocumentPublished(context.Background(), moved); err != nil {
		t.Fatal(err)
	}
	after, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if after.CorpusID == before.CorpusID || after.DocumentID == before.DocumentID {
		t.Fatalf("team document did not move: before=%+v after=%+v", before, after)
	}
	if _, err := h.store.Document(context.Background(), testOrganization, before.DocumentID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("old team's indexed document survived the move")
	}
	newCorpus, err := h.store.Corpus(context.Background(), testOrganization, after.CorpusID)
	if err != nil || newCorpus.TeamID != "team_design" {
		t.Fatalf("new team corpus = %+v, %v", newCorpus, err)
	}
}

func TestDocumentPublishedTeamAndOrganizationScopes(t *testing.T) {
	h := newHarness()
	team := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.DocumentID, event.Scope, event.MemberID, event.TeamID = "doc_team", "team", "", "team_eng"
	})
	if err := h.service.DocumentPublished(context.Background(), team); err != nil {
		t.Fatalf("DocumentPublished(team) error = %v", err)
	}
	teamSlug, err := knowledgeCorpusSlug(domain.ScopeTeam, "team_eng", "")
	if err != nil {
		t.Fatal(err)
	}
	teamCorpus, err := h.store.CorpusBySlug(context.Background(), testOrganization, teamSlug)
	if err != nil {
		t.Fatalf("the team's corpus was not created: %v", err)
	}
	if teamCorpus.Scope != domain.ScopeTeam || teamCorpus.TeamID != "team_eng" || teamCorpus.MemberID != "" {
		t.Fatalf("team corpus placement = %+v", teamCorpus)
	}
	// No member is acting for a team-scoped source, so rag's own schema-legal
	// service creator is used, not a fabricated member id — see
	// knowledgeServiceCreator.
	if teamCorpus.CreatedBy != knowledgeServiceCreator {
		t.Fatalf("team corpus created_by = %q, want %q", teamCorpus.CreatedBy, knowledgeServiceCreator)
	}

	org := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.DocumentID, event.Scope, event.MemberID, event.TeamID = "doc_org", "organization", "", ""
	})
	if err := h.service.DocumentPublished(context.Background(), org); err != nil {
		t.Fatalf("DocumentPublished(organization) error = %v", err)
	}
	orgCorpus, err := h.store.CorpusBySlug(context.Background(), testOrganization, "knowledge-organization")
	if err != nil {
		t.Fatalf("the organization's corpus was not created: %v", err)
	}
	if orgCorpus.Scope != domain.ScopeOrganization || orgCorpus.TeamID != "" || orgCorpus.MemberID != "" {
		t.Fatalf("organization corpus placement = %+v", orgCorpus)
	}
	if orgCorpus.CreatedBy != knowledgeServiceCreator {
		t.Fatalf("organization corpus created_by = %q, want %q", orgCorpus.CreatedBy, knowledgeServiceCreator)
	}
	if teamCorpus.ID == orgCorpus.ID {
		t.Fatal("the team and organization documents landed in the same corpus")
	}
}

func TestDocumentPublishedFallsBackTitleAndContentType(t *testing.T) {
	h := newHarness()
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.Title = ""
		event.MIME = "application/pdf" // not one rag accepts
	})
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("DocumentPublished() error = %v", err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	document, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if document.Title != event.ExternalID {
		t.Fatalf("title = %q, want the external id %q as a fallback", document.Title, event.ExternalID)
	}
	if document.ContentType != "text/plain" {
		t.Fatalf("content type = %q, want text/plain for a mime rag does not accept", document.ContentType)
	}
}

func TestDocumentRemovedDeletesTheDocumentAndForgetsTheMapping(t *testing.T) {
	h := newHarness()
	if err := h.service.DocumentPublished(context.Background(), testDocumentPublished(nil)); err != nil {
		t.Fatalf("DocumentPublished() error = %v", err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("KnowledgeDocumentMapping: %v", err)
	}
	h.store.chunkIDs[mapping.DocumentID] = []string{"chk_5"}

	removal := knowledgeeventsv1.DocumentRemoved{
		DocumentID: "doc_1", OrganizationID: testOrganization, SourceID: "src_1",
		Provider: "github", ExternalID: "42", Reason: "deleted", RemovedAt: testClock.Add(time.Hour),
	}
	if err := h.service.DocumentRemoved(context.Background(), removal); err != nil {
		t.Fatalf("DocumentRemoved() error = %v", err)
	}

	if _, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the rag document survived its knowledge document's removal: %v", err)
	}
	if _, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("the mapping survived its own document's removal")
	}
	if got := h.vectors.store.deleted[mapping.CorpusID]; len(got) != 1 || got[0] != "chk_5" {
		t.Fatalf("vectors deleted for %s = %v, want the removed document's one chunk", mapping.CorpusID, got)
	}

	// Delivered again — the bus promises at least once, not exactly once —
	// this must stay a no-op rather than fail.
	if err := h.service.DocumentRemoved(context.Background(), removal); err != nil {
		t.Fatalf("a repeated DocumentRemoved() error = %v, want idempotent success", err)
	}
}

func TestDocumentRemovedForADocumentNeverIndexedIsANoOp(t *testing.T) {
	h := newHarness()
	removal := knowledgeeventsv1.DocumentRemoved{
		DocumentID: "doc_never-indexed", OrganizationID: testOrganization, SourceID: "src_1",
		Provider: "github", ExternalID: "1", Reason: "deleted", RemovedAt: testClock,
	}
	if err := h.service.DocumentRemoved(context.Background(), removal); err != nil {
		t.Fatalf("DocumentRemoved() error = %v", err)
	}
	if len(h.store.corpora) != 0 || len(h.store.documents) != 0 {
		t.Fatal("DocumentRemoved for an unknown document touched the store")
	}
}

func TestRemovedKnowledgeDocumentCannotBeResurrectedByAnOlderPublish(t *testing.T) {
	h := newHarness()
	removal := knowledgeeventsv1.DocumentRemoved{
		DocumentID: "doc_1", OrganizationID: testOrganization, SourceID: "src_1",
		RemovedAt: testClock.Add(time.Hour),
	}
	if err := h.service.DocumentRemoved(context.Background(), removal); err != nil {
		t.Fatal(err)
	}
	if err := h.service.DocumentPublished(context.Background(), testDocumentPublished(nil)); err != nil {
		t.Fatal(err)
	}
	if len(h.store.documents) != 0 || len(h.store.corpora) != 0 {
		t.Fatal("an older publish recreated a removed member document")
	}

	newer := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.ExtractedAt = removal.RemovedAt.Add(time.Hour)
	})
	if err := h.service.DocumentPublished(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	if len(h.store.documents) != 1 {
		t.Fatal("a genuinely newer publish was not indexed")
	}
}

func TestOlderRemovalDoesNotDeleteNewerMemberDocument(t *testing.T) {
	h := newHarness()
	newer := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.ExtractedAt = testClock.Add(2 * time.Hour)
	})
	if err := h.service.DocumentPublished(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	before, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, newer.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	removal := knowledgeeventsv1.DocumentRemoved{
		DocumentID: newer.DocumentID, OrganizationID: testOrganization, SourceID: newer.SourceID,
		RemovedAt: testClock.Add(time.Hour),
	}
	if err := h.service.DocumentRemoved(context.Background(), removal); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Document(context.Background(), testOrganization, before.DocumentID); err != nil {
		t.Fatalf("older removal deleted newer document: %v", err)
	}
	if err := h.service.DocumentPublished(context.Background(), testDocumentPublished(nil)); err != nil {
		t.Fatal(err)
	}
	after, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, newer.DocumentID)
	if err != nil || after.DocumentID != before.DocumentID {
		t.Fatalf("older publish displaced newer member document: %+v, %v", after, err)
	}
}

// A document over knowledge's event budget travels without its body. Reading
// it back is what keeps the largest documents an organization has — the
// handbook, the specification, the long thread — from being the ones it
// cannot search.

func TestABodyTooLargeToCarryIsReadBackAndIndexed(t *testing.T) {
	h := newHarness()
	h.documents.body = "# The handbook\n\nEverything about how we work."
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.BodyOmitted = true
		event.Body = ""
	})

	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("DocumentPublished() error = %v", err)
	}
	if len(h.documents.calls) != 1 || h.documents.calls[0] != testOrganization+"/doc_1" {
		t.Fatalf("read back %v, want this document in this organization", h.documents.calls)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil {
		t.Fatalf("nothing was indexed for a body that could be read back: %v", err)
	}
	stored, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID)
	if err != nil {
		t.Fatalf("Document() error = %v", err)
	}
	if stored.ByteSize == 0 {
		t.Fatal("the document was indexed with no text")
	}
}

// TestADocumentThatChangedWhileBeingReadBackWaits covers the race the fetch
// opens: knowledge may read the document again between publishing an event
// and answering for it, and indexing that under this event's hash would
// leave the two disagreeing about what is stored.
func TestADocumentThatChangedWhileBeingReadBackWaits(t *testing.T) {
	h := newHarness()
	h.documents.body = "a newer version"
	h.documents.hash = "hash-2"
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.BodyOmitted = true
		event.Body = ""
		event.ContentHash = "hash-1"
	})

	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("DocumentPublished() error = %v", err)
	}
	if len(h.store.documents) != 0 {
		t.Fatalf("indexed a version the event did not describe: %d documents", len(h.store.documents))
	}
}

// TestKnowledgeBeingUnreachableIsWorthAnotherDelivery separates what a retry
// may fix from what it cannot, since the bus redelivers what is returned as
// an error.
func TestKnowledgeBeingUnreachableIsWorthAnotherDelivery(t *testing.T) {
	h := newHarness()
	h.documents.err = knowledgeclient.ErrUnavailable
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.BodyOmitted = true
		event.Body = ""
	})

	if err := h.service.DocumentPublished(context.Background(), event); !errors.Is(err, knowledgeclient.ErrUnavailable) {
		t.Fatalf("error = %v, want it reported so the bus redelivers", err)
	}

	// A document knowledge no longer holds will not appear on a retry.
	h.documents.err = knowledgeclient.ErrNotFound
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("a document knowledge no longer holds was reported as a failure: %v", err)
	}

	// Nor will a deployment knowledge does not admit.
	h.documents.err = knowledgeclient.ErrNotConfigured
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("a deployment knowledge does not admit was reported as a failure: %v", err)
	}
}

// Knowledge republishes a document it had excluded when a change of kinds
// re-includes it, and it does so with the ExtractedAt of when it last read
// it: before the exclusion that removed it. Ordered by that, the republish
// read as older than its removal and the document never came back.
func TestAReincludedKnowledgeDocumentReturnsAfterItsRemoval(t *testing.T) {
	h := newHarness()
	extractedAt := testClock.Add(-24 * time.Hour)
	first := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.ExtractedAt = extractedAt
	})
	if err := h.service.DocumentPublishedAt(context.Background(), first, extractedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	excluded := knowledgeeventsv1.DocumentRemoved{
		DocumentID: first.DocumentID, OrganizationID: testOrganization, SourceID: first.SourceID,
		Reason: knowledgeeventsv1.RemovedReasonExcluded, RemovedAt: testClock,
	}
	if err := h.service.DocumentRemoved(context.Background(), excluded); err != nil {
		t.Fatal(err)
	}
	if len(h.store.documents) != 0 {
		t.Fatal("the exclusion did not remove the document")
	}

	// Re-included: the same ExtractedAt, published after the exclusion.
	if err := h.service.DocumentPublishedAt(context.Background(), first, testClock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
	if err != nil {
		t.Fatalf("the re-included document was not indexed again: %v", err)
	}
	if _, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID); err != nil {
		t.Fatalf("the re-included document is missing: %v", err)
	}

	// The exclusion delivered again must not take it away a second time: it
	// is older than the publish that brought the document back.
	if err := h.service.DocumentRemoved(context.Background(), excluded); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID); err != nil {
		t.Fatalf("a redelivered exclusion removed the re-included document: %v", err)
	}
}

// A publish that went out before its removal and arrives after it, such as
// a redelivery, is still refused: it was published before the removal.
func TestAPublishSentBeforeItsRemovalStaysRefused(t *testing.T) {
	h := newHarness()
	publish := testDocumentPublished(nil)
	publishedAt := testClock.Add(time.Minute)
	if err := h.service.DocumentPublishedAt(context.Background(), publish, publishedAt); err != nil {
		t.Fatal(err)
	}
	removal := knowledgeeventsv1.DocumentRemoved{
		DocumentID: publish.DocumentID, OrganizationID: testOrganization, SourceID: publish.SourceID,
		RemovedAt: testClock.Add(time.Hour),
	}
	if err := h.service.DocumentRemoved(context.Background(), removal); err != nil {
		t.Fatal(err)
	}
	if err := h.service.DocumentPublishedAt(context.Background(), publish, publishedAt); err != nil {
		t.Fatal(err)
	}
	if len(h.store.documents) != 0 {
		t.Fatal("a publish sent before its removal brought the removed document back")
	}
	if _, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, publish.DocumentID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a refused publish left a mapping: %v", err)
	}
}

// A publish is ordered by when it was published, not only by when knowledge
// read the document: an older publish delivered late cannot displace the
// version a later one indexed, even when both read the document at the same
// time.
func TestAnOlderPublishOfTheSameReadCannotDisplaceALaterOne(t *testing.T) {
	h := newHarness()
	later := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.ContentHash = "hash-2"
		event.Body = "# Fix the bug\n\nThe later version."
	})
	if err := h.service.DocumentPublishedAt(context.Background(), later, testClock.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	earlier := testDocumentPublished(nil)
	if err := h.service.DocumentPublishedAt(context.Background(), earlier, testClock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, later.DocumentID)
	if err != nil || mapping.ContentHash != "hash-2" || !mapping.ExtractedAt.Equal(testClock.Add(2*time.Hour)) {
		t.Fatalf("mapping = %+v, %v; want the later publish's version", mapping, err)
	}
}

// A redelivery of what is already indexed is recognised before its body is
// read back, and never costs the document what it holds.
func TestARedeliveredOmittedBodyIsNeitherFetchedNorRemoved(t *testing.T) {
	h := newHarness()
	h.documents.body = "# The handbook\n\nEverything about how we work."
	h.documents.hash = "hash-1"
	event := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.BodyOmitted = true
		event.Body = ""
	})
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, event.DocumentID)
	if err != nil {
		t.Fatalf("not indexed: %v", err)
	}
	if len(h.documents.calls) != 1 {
		t.Fatalf("read back %d times for the first delivery, want 1", len(h.documents.calls))
	}

	// Knowledge has since read a newer version; this is the old event again.
	h.documents.body = "# The handbook\n\nEverything, revised."
	h.documents.hash = "hash-2"
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(h.documents.calls) != 1 {
		t.Fatalf("a redelivery read the body back again: %d reads", len(h.documents.calls))
	}
	if _, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID); err != nil {
		t.Fatalf("a redelivery removed the document it had indexed: %v", err)
	}
}

// A newer event whose body has already been replaced at knowledge, or that
// knowledge no longer holds, leaves what is indexed alone: the event for the
// newer version, or the removal, follows it.
func TestAnOmittedBodyThatWasReplacedOrRemovedKeepsTheIndexedVersion(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeDocuments)
	}{
		{"replaced", func(documents *fakeDocuments) {
			documents.body, documents.hash = "a version newer still", "hash-3"
		}},
		{"removed", func(documents *fakeDocuments) { documents.err = knowledgeclient.ErrNotFound }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			first := testDocumentPublished(nil)
			if err := h.service.DocumentPublished(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(h.documents)
			grew := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
				event.BodyOmitted = true
				event.Body = ""
				event.ContentHash = "hash-2"
				event.ExtractedAt = testClock.Add(time.Hour)
			})
			if err := h.service.DocumentPublished(context.Background(), grew); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.Document(context.Background(), testOrganization, mapping.DocumentID); err != nil {
				t.Fatalf("the indexed version was removed: %v", err)
			}
			kept, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, first.DocumentID)
			if err != nil || kept.ContentHash != "hash-1" {
				t.Fatalf("mapping = %+v, %v; want the indexed version's", kept, err)
			}
		})
	}
}

// Every corpus created since collision-resistant slugs is found by its
// current slug, so that is asked for first; a legacy corpus the same owner
// also holds is used only when there is none.
func TestKnowledgeCorpusPrefersTheCurrentSlugOverALegacyOne(t *testing.T) {
	h := newHarness()
	legacySlug, err := legacyKnowledgeCorpusSlug(domain.ScopeMember, "", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	slug, err := knowledgeCorpusSlug(domain.ScopeMember, "", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	seedCorpus(h.store, domain.Corpus{
		ID: "cor_legacy", Slug: legacySlug, Scope: domain.ScopeMember,
		MemberID: "user-1", Metadata: domain.Metadata{"origin": knowledgeCorpusOrigin},
	})
	current := seedCorpus(h.store, domain.Corpus{
		ID: "cor_current", Slug: slug, Scope: domain.ScopeMember,
		MemberID: "user-1", Metadata: domain.Metadata{"origin": knowledgeCorpusOrigin},
	})
	if err := h.service.DocumentPublished(context.Background(), testDocumentPublished(nil)); err != nil {
		t.Fatal(err)
	}
	mapping, err := h.store.KnowledgeDocumentMapping(context.Background(), testOrganization, "doc_1")
	if err != nil || mapping.CorpusID != current.ID {
		t.Fatalf("mapping = %+v, %v; want corpus %s", mapping, err, current.ID)
	}
}
