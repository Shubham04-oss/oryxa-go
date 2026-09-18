package application

import (
	"context"
	"errors"
	"testing"
	"time"

	knowledgeeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
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

func TestDocumentPublishedCreatesTheOwnersCorpusAndIndexesTheDocument(t *testing.T) {
	h := newHarness()
	event := testDocumentPublished(nil)
	if err := h.service.DocumentPublished(context.Background(), event); err != nil {
		t.Fatalf("DocumentPublished() error = %v", err)
	}

	corpus, err := h.store.CorpusBySlug(context.Background(), testOrganization, "knowledge-member-user-1")
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
	if document.Metadata["knowledge_document_id"] != "doc_1" || document.Metadata["knowledge_source_id"] != "src_1" || document.Metadata["provider"] != "github" {
		t.Fatalf("document metadata = %v", document.Metadata)
	}
	if document.CreatedBy != "user-1" {
		t.Fatalf("document created_by = %q, want the member", document.CreatedBy)
	}
	if document.Status != domain.StatusPending {
		t.Fatalf("document status = %q, want pending: the ingest worker, not this handler, does the rest", document.Status)
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

func TestDocumentPublishedWithAnOmittedBodyIsNotIndexed(t *testing.T) {
	h := newHarness()
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

func TestDocumentPublishedWithAnOmittedBodyRemovesAPreviouslyIndexedVersion(t *testing.T) {
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

	// Rag cannot fetch the body itself, so the stale text is removed rather
	// than left to answer questions the source no longer backs.
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
	corpusOne, err := h.store.CorpusBySlug(context.Background(), testOrganization, "knowledge-member-user-1")
	if err != nil {
		t.Fatalf("user-1's corpus: %v", err)
	}
	corpusTwo, err := h.store.CorpusBySlug(context.Background(), testOrganization, "knowledge-member-user-2")
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

func TestDocumentPublishedTeamAndOrganizationScopes(t *testing.T) {
	h := newHarness()
	team := testDocumentPublished(func(event *knowledgeeventsv1.DocumentPublished) {
		event.DocumentID, event.Scope, event.MemberID, event.TeamID = "doc_team", "team", "", "team_eng"
	})
	if err := h.service.DocumentPublished(context.Background(), team); err != nil {
		t.Fatalf("DocumentPublished(team) error = %v", err)
	}
	teamCorpus, err := h.store.CorpusBySlug(context.Background(), testOrganization, "knowledge-team-team-eng")
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
