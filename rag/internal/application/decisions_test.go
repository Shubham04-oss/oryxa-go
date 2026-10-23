package application

import (
	"context"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/access"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// fakeRecorder keeps every decision it is handed. Which are worth keeping is
// access.Log's call (Decision.Worth), so it keeps a member's own reads too.
type fakeRecorder struct {
	mu        sync.Mutex
	decisions []access.Decision
}

func (recorder *fakeRecorder) Record(_ context.Context, decision access.Decision) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.decisions = append(recorder.decisions, decision)
}

func (recorder *fakeRecorder) taken() []access.Decision {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]access.Decision(nil), recorder.decisions...)
}

// decisionsHarness seeds a member corpus of user-creator's, a team_eng corpus
// user-creator made, and an organization corpus, each with one members
// document, plus an admin-only document in the organization corpus.
type decisionsHarness struct {
	*harness
	recorder                    *fakeRecorder
	member, team, organization  domain.Corpus
	memberDocument, teamDoc     domain.Document
	organizationDoc, adminsOnly domain.Document
}

func newDecisionsHarness() *decisionsHarness {
	h := newHarness()
	recorder := &fakeRecorder{}
	h.service.WithDecisions(recorder)
	member := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator", CreatedBy: "user-creator"})
	team := seedCorpus(h.store, domain.Corpus{Slug: "t1", Scope: domain.ScopeTeam, TeamID: "team_eng", CreatedBy: "user-creator"})
	organization := seedCorpus(h.store, domain.Corpus{Slug: "o1", Scope: domain.ScopeOrganization, CreatedBy: "user-admin"})
	document := func(corpus domain.Corpus, access domain.DocumentAccess) domain.Document {
		return seedDocument(h.store, domain.Document{CorpusID: corpus.ID, Source: domain.Source{Kind: domain.SourceInline},
			Title: "Note", Access: access, Status: domain.StatusReady})
	}
	return &decisionsHarness{
		harness: h, recorder: recorder, member: member, team: team, organization: organization,
		memberDocument: document(member, domain.AccessMembers), teamDoc: document(team, domain.AccessMembers),
		organizationDoc: document(organization, domain.AccessMembers), adminsOnly: document(organization, domain.AccessAdmins),
	}
}

// TestEachSingleDecisionRecordsItsRule: every single read, search, write and
// delete records one decision naming the resource, whose it is, the caller
// (member, and agent where one acted) and the most specific rule the check
// needed.
func TestEachSingleDecisionRecordsItsRule(t *testing.T) {
	ctx := context.Background()
	text := AddDocumentInput{Source: domain.Source{Kind: domain.SourceInline}, Text: "A note.", ContentType: "text/plain"}
	query := domain.Query{Text: "plans"}
	type operation func(h *decisionsHarness, who authclient.Credentials) (resourceID string, err error)
	readCorpus := func(corpus func(*decisionsHarness) domain.Corpus) operation {
		return func(h *decisionsHarness, who authclient.Credentials) (string, error) {
			c := corpus(h)
			_, err := h.service.Corpus(ctx, who, testOrganization, c.ID)
			return c.ID, err
		}
	}
	readDocument := func(corpus func(*decisionsHarness) domain.Corpus, document func(*decisionsHarness) domain.Document) operation {
		return func(h *decisionsHarness, who authclient.Credentials) (string, error) {
			d := document(h)
			_, err := h.service.Document(ctx, who, testOrganization, corpus(h).ID, d.ID)
			return d.ID, err
		}
	}
	search := func(corpus func(*decisionsHarness) domain.Corpus) operation {
		return func(h *decisionsHarness, who authclient.Credentials) (string, error) {
			c := corpus(h)
			_, err := h.service.Search(ctx, who, testOrganization, c.ID, query)
			return c.ID, err
		}
	}
	answer := func(corpus func(*decisionsHarness) domain.Corpus) operation {
		return func(h *decisionsHarness, who authclient.Credentials) (string, error) {
			c := corpus(h)
			_, err := h.service.Answer(ctx, who, testOrganization, c.ID, query)
			return c.ID, err
		}
	}
	addDocument := func(corpus func(*decisionsHarness) domain.Corpus) operation {
		return func(h *decisionsHarness, who authclient.Credentials) (string, error) {
			document, err := h.service.AddDocument(ctx, who, testOrganization, corpus(h).ID, given(text))
			return document.ID, err
		}
	}
	deleteDocument := func(corpus func(*decisionsHarness) domain.Corpus, document func(*decisionsHarness) domain.Document) operation {
		return func(h *decisionsHarness, who authclient.Credentials) (string, error) {
			d := document(h)
			return d.ID, h.service.DeleteDocument(ctx, who, testOrganization, corpus(h).ID, d.ID)
		}
	}
	deleteCorpus := func(corpus func(*decisionsHarness) domain.Corpus) operation {
		return func(h *decisionsHarness, who authclient.Credentials) (string, error) {
			c := corpus(h)
			return c.ID, h.service.DeleteCorpus(ctx, who, testOrganization, c.ID)
		}
	}
	member := func(h *decisionsHarness) domain.Corpus { return h.member }
	team := func(h *decisionsHarness) domain.Corpus { return h.team }
	organization := func(h *decisionsHarness) domain.Corpus { return h.organization }
	memberDocument := func(h *decisionsHarness) domain.Document { return h.memberDocument }
	teamDocument := func(h *decisionsHarness) domain.Document { return h.teamDoc }
	organizationDocument := func(h *decisionsHarness) domain.Document { return h.organizationDoc }
	adminsOnly := func(h *decisionsHarness) domain.Document { return h.adminsOnly }
	// grantFor is a specialist of user-creator's whose grant names the corpus.
	grantFor := func(corpus func(*decisionsHarness) domain.Corpus) func(*decisionsHarness) authclient.Credentials {
		return func(h *decisionsHarness) authclient.Credentials { return grantedCreds("user-creator", corpus(h).ID) }
	}
	as := func(credentials authclient.Credentials) func(*decisionsHarness) authclient.Credentials {
		return func(*decisionsHarness) authclient.Credentials { return credentials }
	}

	cases := []struct {
		name      string
		who       func(*decisionsHarness) authclient.Credentials
		do        operation
		action    string
		kind      string
		owner     string
		actor     string
		agentRole string
		rule      string
	}{
		// read: Corpus
		{"the owner reads their own corpus", as(creds("user-creator")), readCorpus(member), access.ActionRead, "corpus", "user-creator", "user-creator", "", access.ReasonOwner},
		{"an org admin reads a member's corpus", as(creds("user-admin")), readCorpus(member), access.ActionRead, "corpus", "user-creator", "user-admin", "", access.ReasonOrgAdmin},
		{"the owner's primary reads their corpus", as(roleCreds("user-creator", access.AgentPrimary, nil)), readCorpus(member), access.ActionRead, "corpus", "user-creator", "user-creator", access.AgentPrimary, access.ReasonOwner},
		{"a specialist granted all reads its member's corpus", as(grantedCreds("user-creator", access.All)), readCorpus(member), access.ActionRead, "corpus", "user-creator", "user-creator", access.AgentSpecialist, access.ReasonOwner},
		{"a specialist whose grant names it reads its member's corpus", grantFor(member), readCorpus(member), access.ActionRead, "corpus", "user-creator", "user-creator", access.AgentSpecialist, access.ReasonGrant},
		{"a team member reads a team corpus", as(creds("user-teammate")), readCorpus(team), access.ActionRead, "corpus", "", "user-teammate", "", access.ReasonTeamMember},
		{"a team admin reads a team corpus as a member", as(creds("user-teamlead")), readCorpus(team), access.ActionRead, "corpus", "", "user-teamlead", "", access.ReasonTeamMember},
		{"an org admin off the team reads a team corpus", as(creds("user-owner")), readCorpus(team), access.ActionRead, "corpus", "", "user-owner", "", access.ReasonOrgAdmin},
		{"a team member's agent reads a team corpus", as(agentCreds("user-teammate")), readCorpus(team), access.ActionRead, "corpus", "", "user-teammate", access.AgentSpecialist, access.ReasonTeamMember},
		{"a specialist whose grant names it reads a team corpus", grantFor(team), readCorpus(team), access.ActionRead, "corpus", "", "user-creator", access.AgentSpecialist, access.ReasonGrant},
		{"a member reads an organization corpus", as(creds("user-teammate")), readCorpus(organization), access.ActionRead, "corpus", "", "user-teammate", "", access.ReasonOrganization},
		{"an org admin reads an organization corpus", as(creds("user-admin")), readCorpus(organization), access.ActionRead, "corpus", "", "user-admin", "", access.ReasonOrganization},
		{"a specialist whose grant names it reads an organization corpus", grantFor(organization), readCorpus(organization), access.ActionRead, "corpus", "", "user-creator", access.AgentSpecialist, access.ReasonGrant},

		// read: Document
		{"the owner reads their own document", as(creds("user-creator")), readDocument(member, memberDocument), access.ActionRead, "document", "user-creator", "user-creator", "", access.ReasonOwner},
		{"an org admin reads a member's document", as(creds("user-admin")), readDocument(member, memberDocument), access.ActionRead, "document", "user-creator", "user-admin", "", access.ReasonOrgAdmin},
		{"a team member's agent reads a team document", as(agentCreds("user-teammate")), readDocument(team, teamDocument), access.ActionRead, "document", "", "user-teammate", access.AgentSpecialist, access.ReasonTeamMember},
		{"a member reads an organization document", as(creds("user-teammate")), readDocument(organization, organizationDocument), access.ActionRead, "document", "", "user-teammate", "", access.ReasonOrganization},
		{"an org admin reads an admin-only document", as(creds("user-admin")), readDocument(organization, adminsOnly), access.ActionRead, "document", "", "user-admin", "", access.ReasonOrgAdmin},

		// search: Search and Answer, on the corpus
		{"an org admin searches a member's corpus", as(creds("user-admin")), search(member), access.ActionSearch, "corpus", "user-creator", "user-admin", "", access.ReasonOrgAdmin},
		{"the owner's primary searches their corpus", as(roleCreds("user-creator", access.AgentPrimary, nil)), search(member), access.ActionSearch, "corpus", "user-creator", "user-creator", access.AgentPrimary, access.ReasonOwner},
		{"a specialist whose grant names it searches a team corpus", grantFor(team), search(team), access.ActionSearch, "corpus", "", "user-creator", access.AgentSpecialist, access.ReasonGrant},
		{"a team member answers from a team corpus", as(creds("user-teammate")), answer(team), access.ActionSearch, "corpus", "", "user-teammate", "", access.ReasonTeamMember},
		{"an organization agent answers from an organization corpus", as(roleCreds("user-teammate", access.AgentOrganization, nil)), answer(organization), access.ActionSearch, "corpus", "", "user-teammate", access.AgentOrganization, access.ReasonOrganization},

		// write: AddDocument, on the document it made
		{"the owner adds to their own corpus", as(creds("user-creator")), addDocument(member), access.ActionWrite, "document", "user-creator", "user-creator", "", access.ReasonOwner},
		{"an org admin adds to a member's corpus", as(creds("user-admin")), addDocument(member), access.ActionWrite, "document", "user-creator", "user-admin", "", access.ReasonOrgAdmin},
		{"a specialist whose grant names it adds to its member's corpus", grantFor(member), addDocument(member), access.ActionWrite, "document", "user-creator", "user-creator", access.AgentSpecialist, access.ReasonGrant},
		{"a team member adds to a team corpus", as(creds("user-teammate")), addDocument(team), access.ActionWrite, "document", "", "user-teammate", "", access.ReasonTeamMember},
		{"an org admin adds to an organization corpus", as(creds("user-admin")), addDocument(organization), access.ActionWrite, "document", "", "user-admin", "", access.ReasonOrgAdmin},

		// delete: DeleteDocument and DeleteCorpus
		{"the owner's agent deletes their document", as(agentCreds("user-creator")), deleteDocument(member, memberDocument), access.ActionDelete, "document", "user-creator", "user-creator", access.AgentSpecialist, access.ReasonOwner},
		{"a team member deletes a team document", as(creds("user-teammate")), deleteDocument(team, teamDocument), access.ActionDelete, "document", "", "user-teammate", "", access.ReasonTeamMember},
		{"an org admin deletes an organization document", as(creds("user-admin")), deleteDocument(organization, organizationDocument), access.ActionDelete, "document", "", "user-admin", "", access.ReasonOrgAdmin},
		{"the owner deletes their own corpus", as(creds("user-creator")), deleteCorpus(member), access.ActionDelete, "corpus", "user-creator", "user-creator", "", access.ReasonOwner},
		{"an org admin deletes a member's corpus", as(creds("user-admin")), deleteCorpus(member), access.ActionDelete, "corpus", "user-creator", "user-admin", "", access.ReasonOrgAdmin},
		{"a team corpus's maker deletes it", as(creds("user-creator")), deleteCorpus(team), access.ActionDelete, "corpus", "", "user-creator", "", access.ReasonOwner},
		{"a team admin deletes a team corpus they did not make", as(creds("user-teamlead")), deleteCorpus(team), access.ActionDelete, "corpus", "", "user-teamlead", "", access.ReasonTeamAdmin},
		{"an org admin deletes a team corpus", as(creds("user-owner")), deleteCorpus(team), access.ActionDelete, "corpus", "", "user-owner", "", access.ReasonOrgAdmin},
		{"an org admin deletes an organization corpus", as(creds("user-admin")), deleteCorpus(organization), access.ActionDelete, "corpus", "", "user-admin", "", access.ReasonOrgAdmin},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newDecisionsHarness()
			resourceID, err := c.do(h, c.who(h))
			if err != nil {
				t.Fatalf("allowed operation failed: %v", err)
			}
			got := h.recorder.taken()
			if len(got) != 1 {
				t.Fatalf("recorded %d decisions, want 1: %+v", len(got), got)
			}
			want := access.Decision{
				OrganizationID: testOrganization, Service: "rag", Action: c.action,
				ResourceKind: c.kind, ResourceID: resourceID, OwnerID: c.owner,
				ActorUserID: c.actor, ActorAgentRole: c.agentRole, Reason: c.rule,
			}
			if c.agentRole != "" {
				want.ActorAgentID = "agt_" + c.actor
			}
			if got[0] != want {
				t.Errorf("decision\n got %+v\nwant %+v", got[0], want)
			}
		})
	}
}

// TestListsAndRefusalsRecordNothing: a listing runs mayRead per corpus and
// VisibleTo per document but is no decision about any one of them, and a
// refusal is not an allow.
func TestListsAndRefusalsRecordNothing(t *testing.T) {
	ctx := context.Background()
	h := newDecisionsHarness()
	for _, who := range []authclient.Credentials{creds("user-creator"), creds("user-admin"), agentCreds("user-teammate"), grantedCreds("user-creator", h.member.ID)} {
		for _, filter := range []ListFilter{{}, {Scope: domain.ScopeMember}, {Scope: domain.ScopeOrganization}} {
			if _, err := h.service.Corpora(ctx, who, testOrganization, filter); err != nil {
				t.Fatalf("Corpora(%+v): %v", filter, err)
			}
		}
	}
	if _, err := h.service.Corpora(ctx, creds("user-teammate"), testOrganization, ListFilter{Scope: domain.ScopeTeam, TeamID: "team_eng"}); err != nil {
		t.Fatalf("Corpora(team): %v", err)
	}
	for _, corpus := range []domain.Corpus{h.member, h.team, h.organization} {
		if _, err := h.service.Documents(ctx, creds("user-admin"), testOrganization, corpus.ID, "", "", 10); err != nil {
			t.Fatalf("Documents(%s): %v", corpus.ID, err)
		}
	}
	// Refused: another member's corpus, a team they are not on, a corpus
	// outside a specialist's grant, and writing to the organization's.
	if _, err := h.service.Corpus(ctx, creds("user-teammate"), testOrganization, h.member.ID); err == nil {
		t.Fatal("a member read another member's corpus")
	}
	if _, err := h.service.Search(ctx, creds("user-outsider"), testOrganization, h.team.ID, domain.Query{Text: "plans"}); err == nil {
		t.Fatal("an outsider searched a team corpus")
	}
	if _, err := h.service.Corpus(ctx, grantedCreds("user-creator", h.team.ID), testOrganization, h.member.ID); err == nil {
		t.Fatal("a specialist read a corpus outside its grant")
	}
	if _, err := h.service.AddDocument(ctx, creds("user-teammate"), testOrganization, h.organization.ID, given(AddDocumentInput{})); err == nil {
		t.Fatal("a member wrote to an organization corpus")
	}
	if got := h.recorder.taken(); len(got) != 0 {
		t.Errorf("lists and refusals recorded %d decisions: %+v", len(got), got)
	}
}

// TestASearchRecordsOnce: a search is one decision about its corpus however
// many passages it finds, and so is an answer, which searches once.
func TestASearchRecordsOnce(t *testing.T) {
	ctx := context.Background()
	h := newDecisionsHarness()
	who := agentCreds("user-creator")
	if _, err := h.service.Search(ctx, who, testOrganization, h.member.ID, domain.Query{Text: "plans"}); err != nil {
		t.Fatal(err)
	}
	if got := h.recorder.taken(); len(got) != 1 || got[0].Action != access.ActionSearch || got[0].ResourceID != h.member.ID {
		t.Fatalf("after one search: %+v, want one search of %s", got, h.member.ID)
	}
	if _, err := h.service.Answer(ctx, who, testOrganization, h.member.ID, domain.Query{Text: "plans"}); err != nil {
		t.Fatal(err)
	}
	if got := h.recorder.taken(); len(got) != 2 || got[1].Action != access.ActionSearch {
		t.Fatalf("after a search and an answer: %+v, want two searches", got)
	}
}

// TestWithoutARecorderNothingIsRecorded: a service built without decisions
// (as every other test's is) still serves.
func TestWithoutARecorderNothingIsRecorded(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	if _, err := h.service.Corpus(context.Background(), creds("user-admin"), testOrganization, corpus.ID); err != nil {
		t.Fatal(err)
	}
}
