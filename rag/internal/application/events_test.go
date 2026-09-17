package application

import (
	"context"
	"errors"
	"testing"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	fileseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// What rag does when something it depends on goes away. An index outliving
// what it was built from is the failure mode that matters here, so these
// tests check both that the right corpora and documents disappear and that
// the wrong ones do not.

func TestOrganizationCreatedAndUpdatedRecordTheOrganization(t *testing.T) {
	h := newHarness()
	if err := h.service.OrganizationCreated(context.Background(), autheventsv1.OrganizationCreated{OrganizationID: testOrganization, Name: "Acme"}); err != nil {
		t.Fatalf("OrganizationCreated() error = %v", err)
	}
	if h.store.orgs[testOrganization] != "Acme" {
		t.Fatalf("orgs = %v, want Acme recorded", h.store.orgs)
	}
	if err := h.service.OrganizationUpdated(context.Background(), autheventsv1.OrganizationUpdated{OrganizationID: testOrganization, Name: "Acme Inc"}); err != nil {
		t.Fatalf("OrganizationUpdated() error = %v", err)
	}
	if h.store.orgs[testOrganization] != "Acme Inc" {
		t.Fatalf("orgs = %v, want the rename recorded", h.store.orgs)
	}
}

func TestOrganizationDeletedWipesEveryCorpusAndItsVectors(t *testing.T) {
	h := newHarness()
	member := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	team := seedCorpus(h.store, domain.Corpus{Slug: "t1", Scope: domain.ScopeTeam, TeamID: "team_eng"})
	org := seedCorpus(h.store, domain.Corpus{Slug: "o1", Scope: domain.ScopeOrganization})

	if err := h.service.OrganizationDeleted(context.Background(), autheventsv1.OrganizationDeleted{OrganizationID: testOrganization, Name: "Acme"}); err != nil {
		t.Fatalf("OrganizationDeleted() error = %v", err)
	}
	for _, corpus := range []domain.Corpus{member, team, org} {
		if _, err := h.store.Corpus(context.Background(), testOrganization, corpus.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("corpus %s (scope %s) survived organization deletion: %v", corpus.ID, corpus.Scope, err)
		}
	}
	// Vectors go before the rows: a drop that failed would otherwise leave
	// them orphaned with nothing left pointing at them.
	if len(h.vectors.store.dropped) != 3 {
		t.Fatalf("vectors dropped = %v, want all three corpora", h.vectors.store.dropped)
	}
}

func TestMemberRemovedDeletesOnlyThatMembersScopedCorpora(t *testing.T) {
	h := newHarness()
	personal := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	team := seedCorpus(h.store, domain.Corpus{Slug: "t1", Scope: domain.ScopeTeam, TeamID: "team_eng", CreatedBy: "user-creator"})
	org := seedCorpus(h.store, domain.Corpus{Slug: "o1", Scope: domain.ScopeOrganization, CreatedBy: "user-creator"})

	if err := h.service.MemberRemoved(context.Background(), autheventsv1.MemberRemoved{
		OrganizationID: testOrganization, UserID: "user-creator", Reason: autheventsv1.MemberRemovedReasonLeft,
	}); err != nil {
		t.Fatalf("MemberRemoved() error = %v", err)
	}
	if _, err := h.store.Corpus(context.Background(), testOrganization, personal.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the member's personal corpus survived: %v", err)
	}
	// Team and organization corpora belong to the team or the organization,
	// not to whoever happened to create them, so they stay — exactly as a
	// departing member's own team and organization files do.
	if _, err := h.store.Corpus(context.Background(), testOrganization, team.ID); err != nil {
		t.Fatalf("the team corpus was removed when its creator left: %v", err)
	}
	if _, err := h.store.Corpus(context.Background(), testOrganization, org.ID); err != nil {
		t.Fatalf("the organization corpus was removed when its creator left: %v", err)
	}
}

func TestMemberRemovedForOrganizationDeletionIsANoOp(t *testing.T) {
	h := newHarness()
	personal := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	// The organization going away already wipes everything under it via its
	// own event; acting on this one too would be doing the same work twice,
	// against a corpus (and vectors) that may already be gone.
	if err := h.service.MemberRemoved(context.Background(), autheventsv1.MemberRemoved{
		OrganizationID: testOrganization, UserID: "user-creator", Reason: autheventsv1.MemberRemovedReasonOrganizationDeleted,
	}); err != nil {
		t.Fatalf("MemberRemoved() error = %v", err)
	}
	if _, err := h.store.Corpus(context.Background(), testOrganization, personal.ID); err != nil {
		t.Fatalf("MemberRemoved(organization_deleted) touched the corpus: %v", err)
	}
	if len(h.vectors.store.dropped) != 0 {
		t.Fatalf("vectors dropped = %v, want none", h.vectors.store.dropped)
	}
}

func TestTeamDeletedWipesTeamCorpora(t *testing.T) {
	h := newHarness()
	team := seedCorpus(h.store, domain.Corpus{Slug: "t1", Scope: domain.ScopeTeam, TeamID: "team_eng"})
	personal := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})

	if err := h.service.TeamDeleted(context.Background(), teamseventsv1.TeamDeleted{
		OrganizationID: testOrganization, TeamID: "team_eng", Name: "Engineering",
	}); err != nil {
		t.Fatalf("TeamDeleted() error = %v", err)
	}
	if _, err := h.store.Corpus(context.Background(), testOrganization, team.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the team corpus survived: %v", err)
	}
	if _, err := h.store.Corpus(context.Background(), testOrganization, personal.ID); err != nil {
		t.Fatalf("an unrelated member corpus was removed: %v", err)
	}
}

func TestObjectDeletedRemovesDocumentsIndexedFromTheFile(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	indexed := seedDocument(h.store, domain.Document{
		CorpusID: corpus.ID, Source: domain.Source{Kind: domain.SourceFile, Ref: "file_1"}, Status: domain.StatusReady,
	})
	other := seedDocument(h.store, domain.Document{
		CorpusID: corpus.ID, Source: domain.Source{Kind: domain.SourceInline}, Status: domain.StatusReady,
	})
	h.store.chunkIDs[indexed.ID] = []string{"chk_1"}

	if err := h.service.ObjectDeleted(context.Background(), fileseventsv1.ObjectDeleted{
		OrganizationID: testOrganization, FileID: "file_1", Reason: fileseventsv1.DeletedReasonDeleted,
	}); err != nil {
		t.Fatalf("ObjectDeleted() error = %v", err)
	}
	// Without this, a file a member deleted would go on answering questions
	// out of the index, quoting text nobody can check any more.
	if _, err := h.store.Document(context.Background(), testOrganization, indexed.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the document indexed from the deleted file survived: %v", err)
	}
	if _, err := h.store.Document(context.Background(), testOrganization, other.ID); err != nil {
		t.Fatalf("an unrelated document was removed: %v", err)
	}
	if got := h.vectors.store.deleted[corpus.ID]; len(got) != 1 || got[0] != "chk_1" {
		t.Fatalf("vectors deleted for %s = %v, want the removed document's one chunk", corpus.ID, got)
	}
}

func TestObjectDeletedSkipsADocumentWhoseCorpusIsAlreadyGone(t *testing.T) {
	h := newHarness()
	// This document points at a corpus id the store no longer holds, as if
	// the corpus had already been deleted by another event that arrived
	// first. ObjectDeleted must skip it rather than fail the whole pass on
	// one document it can no longer place.
	orphan := seedDocument(h.store, domain.Document{
		CorpusID: "cor_already-deleted", Source: domain.Source{Kind: domain.SourceFile, Ref: "file_1"}, Status: domain.StatusReady,
	})

	if err := h.service.ObjectDeleted(context.Background(), fileseventsv1.ObjectDeleted{
		OrganizationID: testOrganization, FileID: "file_1", Reason: fileseventsv1.DeletedReasonDeleted,
	}); err != nil {
		t.Fatalf("ObjectDeleted() error = %v", err)
	}
	// Nothing claimed to have removed it, so it must not have been silently
	// dropped either — its own corpus's deletion is what will have taken it,
	// or already has.
	if _, err := h.store.Document(context.Background(), testOrganization, orphan.ID); err != nil {
		t.Fatalf("the orphaned document was unexpectedly touched: %v", err)
	}
}
