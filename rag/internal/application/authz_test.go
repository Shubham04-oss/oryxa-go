package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/authz"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
)

// confidentialForbidText is doc.go's own worked example: a document tagged
// confidential is unreadable except by an org admin. It is used throughout
// this file exactly as internal/authz's own engine_test.go uses it, so a
// failure here points at the wiring in this package rather than at Cedar
// itself, which already has its own coverage.
const confidentialForbidText = `forbid (
	principal,
	action == Oryxa::Action::"read",
	resource is Oryxa::Document
) when {
	resource.metadata has confidential &&
	resource.metadata.confidential == "true"
} unless {
	context.acts_as_admin
};`

// searchAdminsOnlyForbidText forbids search alone, leaving read untouched —
// internal/authz's own TestStoredForbidRestrictsSearchToAdmins reproduced
// here to prove rag actually asks the engine for the search action it claims
// to, not just for read.
const searchAdminsOnlyForbidText = `forbid (principal, action == Oryxa::Action::"search", resource is Oryxa::KnowledgeBase) unless { context.acts_as_admin };`

// TestDocumentLevelForbidHidesOneDocumentFromSearchAndDirectRead is the
// "Search hits" and "Documents" requirements together: a stored forbid keyed
// on metadata hides the document it names from a search's results and from a
// direct read of it, while a document it does not name keeps appearing.
func TestDocumentLevelForbidHidesOneDocumentFromSearchAndDirectRead(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "org-docs", Scope: domain.ScopeOrganization})
	secret := seedDocument(h.store, domain.Document{
		CorpusID: corpus.ID, Metadata: domain.Metadata{"confidential": "true"},
	})
	ordinary := seedDocument(h.store, domain.Document{CorpusID: corpus.ID})
	h.authzStore.seed(authz.Policy{
		OrganizationID: testOrganization,
		Scope:          authz.Scope{Type: knowledgeBaseScopeType, ID: corpus.ID},
		Name:           "hide confidential documents",
		Text:           confidentialForbidText,
	})

	// Direct reads: the secret document reads as not found, the ordinary one
	// reads fine, for a plain member.
	if _, err := h.service.Document(context.Background(), creds("user-creator"), testOrganization, corpus.ID, secret.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member reading the confidential document: err = %v, want ErrNotFound", err)
	}
	if _, err := h.service.Document(context.Background(), creds("user-creator"), testOrganization, corpus.ID, ordinary.ID); err != nil {
		t.Fatalf("member reading the ordinary document: err = %v, want nil", err)
	}
	// The forbid's own unless clause exempts an org admin, exactly as it does
	// in internal/authz's own tests.
	if _, err := h.service.Document(context.Background(), creds("user-admin"), testOrganization, corpus.ID, secret.ID); err != nil {
		t.Fatalf("admin reading the confidential document: err = %v, want nil", err)
	}

	// Search hits: both documents' chunks come back from retrieval, only the
	// ordinary one should survive for a plain member.
	h.retriever.results = retrieval.Results{Matches: []domain.Match{
		{Chunk: domain.Chunk{ID: "chk_secret", DocumentID: secret.ID}, Score: 0.9},
		{Chunk: domain.Chunk{ID: "chk_ordinary", DocumentID: ordinary.ID}, Score: 0.8},
	}}
	results, err := h.service.Search(context.Background(), creds("user-creator"), testOrganization, corpus.ID, domain.Query{Text: "q"})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results.Matches) != 1 || results.Matches[0].Chunk.DocumentID != ordinary.ID {
		t.Fatalf("Search() matches = %+v, want only the ordinary document", results.Matches)
	}
}

// TestKnowledgeBaseSearchForbidActsAsNotFoundForMembersOnly is the
// "Corpus reads and searches" requirement: a forbid on the search action
// alone makes search answer not found for a member but leaves an org admin's
// search — and everyone's plain read of the corpus — untouched.
func TestKnowledgeBaseSearchForbidActsAsNotFoundForMembersOnly(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "org-search-gate", Scope: domain.ScopeOrganization})
	h.authzStore.seed(authz.Policy{
		OrganizationID: testOrganization,
		Scope:          authz.Scope{Type: knowledgeBaseScopeType, ID: corpus.ID},
		Name:           "search is admins only",
		Text:           searchAdminsOnlyForbidText,
	})
	h.retriever.results = retrieval.Results{Matches: []domain.Match{{Chunk: domain.Chunk{ID: "chk_1"}, Score: 0.9}}}

	if _, err := h.service.Search(context.Background(), creds("user-creator"), testOrganization, corpus.ID, domain.Query{Text: "q"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member Search() = %v, want ErrNotFound", err)
	}
	if _, err := h.service.Search(context.Background(), creds("user-admin"), testOrganization, corpus.ID, domain.Query{Text: "q"}); err != nil {
		t.Fatalf("admin Search() = %v, want nil", err)
	}
	if _, err := h.service.Corpus(context.Background(), creds("user-creator"), testOrganization, corpus.ID); err != nil {
		t.Fatalf("member Corpus() = %v, want nil: a search-only forbid must leave read alone", err)
	}
}

// TestAgentActingForAnAdminIsRefusedThePolicyRoutes is the "principal and
// context" requirement: an agent token never carries acts_as_admin, even
// acting for an organization admin, so it gets a plain member's reach and the
// administer check the policy routes require refuses it.
func TestAgentActingForAnAdminIsRefusedThePolicyRoutes(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "org-agent-admin", Scope: domain.ScopeOrganization})

	if _, err := h.service.ListPolicies(context.Background(), agentCreds("user-admin"), testOrganization, corpus.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("agent-for-admin ListPolicies() = %v, want ErrForbidden", err)
	}
	if _, err := h.service.AddPolicy(context.Background(), agentCreds("user-admin"), testOrganization, corpus.ID, AddPolicyInput{
		Name: "x", Text: searchAdminsOnlyForbidText,
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("agent-for-admin AddPolicy() = %v, want ErrForbidden", err)
	}
	// The admin's own session, not acting through an agent, is unaffected.
	if _, err := h.service.ListPolicies(context.Background(), creds("user-admin"), testOrganization, corpus.ID); err != nil {
		t.Fatalf("admin ListPolicies() = %v, want nil", err)
	}
}

// TestPolicyRoutesEndToEnd is the "policy routes" requirement in one pass:
// admin list/create/delete, a member's 403, an unknown corpus's 404, invalid
// Cedar's 400-equivalent with the validator's own message, and a duplicate
// name's 409-equivalent.
func TestPolicyRoutesEndToEnd(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "kb-policy-routes", Scope: domain.ScopeOrganization})

	policies, err := h.service.ListPolicies(context.Background(), creds("user-admin"), testOrganization, corpus.ID)
	if err != nil || len(policies) != 0 {
		t.Fatalf("admin ListPolicies() on a fresh corpus = %+v, %v, want an empty list", policies, err)
	}

	if _, err := h.service.ListPolicies(context.Background(), creds("user-creator"), testOrganization, corpus.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member ListPolicies() = %v, want ErrForbidden", err)
	}

	if _, err := h.service.ListPolicies(context.Background(), creds("user-admin"), testOrganization, "cor_never-created"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ListPolicies() on an unknown corpus = %v, want ErrNotFound", err)
	}

	_, err = h.service.AddPolicy(context.Background(), creds("user-admin"), testOrganization, corpus.ID, AddPolicyInput{
		Name: "not cedar", Text: "this is not a policy",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("AddPolicy() with invalid Cedar = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "does not parse as Cedar") {
		t.Fatalf("AddPolicy() error = %q, want the validator's own message", err.Error())
	}

	created, err := h.service.AddPolicy(context.Background(), creds("user-admin"), testOrganization, corpus.ID, AddPolicyInput{
		Name: "search is admins only", Text: searchAdminsOnlyForbidText,
	})
	if err != nil || created.ID == "" {
		t.Fatalf("AddPolicy() = %+v, %v, want a stored policy", created, err)
	}

	if _, err := h.service.AddPolicy(context.Background(), creds("user-admin"), testOrganization, corpus.ID, AddPolicyInput{
		Name: "search is admins only", Text: confidentialForbidText,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("AddPolicy() with a duplicate name = %v, want ErrConflict", err)
	}

	policies, err = h.service.ListPolicies(context.Background(), creds("user-admin"), testOrganization, corpus.ID)
	if err != nil || len(policies) != 1 || policies[0].ID != created.ID {
		t.Fatalf("ListPolicies() after AddPolicy = %+v, %v", policies, err)
	}

	if err := h.service.RemovePolicy(context.Background(), creds("user-creator"), testOrganization, corpus.ID, created.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member RemovePolicy() = %v, want ErrForbidden", err)
	}
	if err := h.service.RemovePolicy(context.Background(), creds("user-admin"), testOrganization, corpus.ID, "pol_never-created"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RemovePolicy() of an unknown id = %v, want ErrNotFound", err)
	}
	if err := h.service.RemovePolicy(context.Background(), creds("user-admin"), testOrganization, corpus.ID, created.ID); err != nil {
		t.Fatalf("RemovePolicy() = %v, want nil", err)
	}

	policies, err = h.service.ListPolicies(context.Background(), creds("user-admin"), testOrganization, corpus.ID)
	if err != nil || len(policies) != 0 {
		t.Fatalf("ListPolicies() after RemovePolicy = %+v, %v, want an empty list", policies, err)
	}
}

// TestPolicyCreateAndDeleteInvalidateTheCacheImmediately checks that the next
// decision after AddPolicy or RemovePolicy sees the change right away,
// instead of waiting out Engine's own cache TTL (30 seconds by default).
func TestPolicyCreateAndDeleteInvalidateTheCacheImmediately(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "kb-cache", Scope: domain.ScopeOrganization})
	secret := seedDocument(h.store, domain.Document{CorpusID: corpus.ID, Metadata: domain.Metadata{"confidential": "true"}})
	h.retriever.results = retrieval.Results{Matches: []domain.Match{{Chunk: domain.Chunk{ID: "chk_1", DocumentID: secret.ID}, Score: 0.9}}}

	// Primes the engine's cache for this organization with nothing stored.
	before, err := h.service.Search(context.Background(), creds("user-creator"), testOrganization, corpus.ID, domain.Query{Text: "q"})
	if err != nil || len(before.Matches) != 1 {
		t.Fatalf("Search() before any policy = %+v, %v, want the one match", before, err)
	}

	policy, err := h.service.AddPolicy(context.Background(), creds("user-admin"), testOrganization, corpus.ID, AddPolicyInput{
		Name: "hide confidential", Text: confidentialForbidText,
	})
	if err != nil {
		t.Fatalf("AddPolicy() error = %v", err)
	}
	after, err := h.service.Search(context.Background(), creds("user-creator"), testOrganization, corpus.ID, domain.Query{Text: "q"})
	if err != nil || len(after.Matches) != 0 {
		t.Fatalf("Search() right after AddPolicy = %+v, %v, want the match gone already", after, err)
	}

	if err := h.service.RemovePolicy(context.Background(), creds("user-admin"), testOrganization, corpus.ID, policy.ID); err != nil {
		t.Fatalf("RemovePolicy() error = %v", err)
	}
	restored, err := h.service.Search(context.Background(), creds("user-creator"), testOrganization, corpus.ID, domain.Query{Text: "q"})
	if err != nil || len(restored.Matches) != 1 {
		t.Fatalf("Search() right after RemovePolicy = %+v, %v, want the match back already", restored, err)
	}
}

// TestDeleteCorpusRemovesItsStoredPolicies is the "Cleanup" requirement for a
// single knowledge base: deleting it removes the Cedar policies stored on it.
func TestDeleteCorpusRemovesItsStoredPolicies(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "kb-cleanup", Scope: domain.ScopeMember, MemberID: "user-creator", CreatedBy: "user-creator"})
	scope := authz.Scope{Type: knowledgeBaseScopeType, ID: corpus.ID}
	h.authzStore.seed(authz.Policy{OrganizationID: testOrganization, Scope: scope, Name: "x", Text: searchAdminsOnlyForbidText})

	if err := h.service.DeleteCorpus(context.Background(), creds("user-creator"), testOrganization, corpus.ID); err != nil {
		t.Fatalf("DeleteCorpus() error = %v", err)
	}
	remaining, err := h.authzStore.Policies(context.Background(), testOrganization, scope)
	if err != nil {
		t.Fatalf("Policies() error = %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("policies remaining after DeleteCorpus = %+v, want none", remaining)
	}
}

// TestOrganizationDeletedRemovesEveryStoredPolicy is the "Cleanup" requirement
// for a whole organization: every policy it had, on any scope, is gone.
func TestOrganizationDeletedRemovesEveryStoredPolicy(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "kb-org-cleanup", Scope: domain.ScopeOrganization})
	h.authzStore.seed(authz.Policy{
		OrganizationID: testOrganization, Scope: authz.Scope{Type: knowledgeBaseScopeType, ID: corpus.ID},
		Name: "x", Text: searchAdminsOnlyForbidText,
	})
	h.authzStore.seed(authz.Policy{
		OrganizationID: testOrganization, Scope: authz.Scope{Type: "organization"},
		Name: "y", Text: searchAdminsOnlyForbidText,
	})

	if err := h.service.OrganizationDeleted(context.Background(), autheventsv1.OrganizationDeleted{OrganizationID: testOrganization}); err != nil {
		t.Fatalf("OrganizationDeleted() error = %v", err)
	}
	remaining, err := h.authzStore.OrganizationPolicies(context.Background(), testOrganization)
	if err != nil {
		t.Fatalf("OrganizationPolicies() error = %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("policies remaining after OrganizationDeleted = %+v, want none", remaining)
	}
}
