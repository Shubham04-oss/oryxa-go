package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/generation"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
)

// wantErr checks an outcome against a sentinel, or against success when want
// is nil, which is most of what these tests do.
func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestCallerOutsideTheOrganizationIsNotFound(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "org-a", Scope: domain.ScopeOrganization})
	// A caller auth does not recognise in this organization is refused before
	// anything about the corpus is looked at, and reads exactly like a caller
	// asking about an organization that does not exist: auth conceals which
	// organizations exist, and rag conceals the same way.
	if _, err := h.service.Corpus(context.Background(), creds("user-stranger"), testOrganization, corpus.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

type wrongOrganizationDirectory struct{}

func (wrongOrganizationDirectory) Membership(context.Context, string, authclient.Credentials) (domain.OrgMembership, error) {
	return domain.OrgMembership{OrganizationID: "org_2", UserID: "user-other", Role: domain.OrgRoleAdmin}, nil
}

func TestMembershipForAnotherOrganizationCannotReadACorpus(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "org-a", Scope: domain.ScopeOrganization})
	h.service.directory = wrongOrganizationDirectory{}
	if _, err := h.service.Corpus(context.Background(), creds("user-other"), testOrganization, corpus.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign organization's member read = %v, want ErrNotFound", err)
	}
}

func TestCallerWithAnUnrecognisedRoleIsForbidden(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "org-a", Scope: domain.ScopeOrganization})
	// auth answered a membership, so this caller is not a stranger to the
	// organization; its role is just not one this service knows what to do
	// with, which is a different refusal from not being a member at all.
	if _, err := h.service.Corpus(context.Background(), creds("user-badrole"), testOrganization, corpus.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
}

func TestCorpusThatDoesNotExistIsNotFound(t *testing.T) {
	h := newHarness()
	if _, err := h.service.Corpus(context.Background(), creds("user-creator"), testOrganization, "cor_never-created"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestTeamForbiddenFromTeamsIsConcealedAsNotFound(t *testing.T) {
	h := newHarness()
	h.teams.err = domain.ErrForbidden
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "t1", Scope: domain.ScopeTeam, TeamID: "team_eng"})
	// teams refusing outright must read exactly like teams saying the team is
	// invisible: a forbidden that leaked through here would tell the caller
	// the team exists, which is what concealment exists to prevent.
	if _, err := h.service.Corpus(context.Background(), creds("user-teammate"), testOrganization, corpus.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestTeamScopeIsUnavailableWithoutATeamsService(t *testing.T) {
	store := newFakeStore()
	service := New(store, fakeDirectory{}, nil, &fakeFiles{}, nil, newFakeVectors(), &fakeRetriever{}, &fakeGenerator{}, nil, Defaults{}, nil)
	if _, err := service.CreateCorpus(context.Background(), creds("user-creator"), testOrganization, CreateCorpusInput{
		Name: "Handbook", Scope: domain.ScopeTeam, TeamID: "team_eng",
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

// TestCreateCorpusByScopeAndRole is the table for who may create a corpus at
// each scope, matching the placement table in rag/README.md.
func TestCreateCorpusByScopeAndRole(t *testing.T) {
	tests := []struct {
		name    string
		user    string
		scope   domain.Scope
		teamID  string
		wantErr error
	}{
		{"no scope named defaults to member", "user-creator", "", "", nil},
		{"member: any member may create their own", "user-creator", domain.ScopeMember, "", nil},
		{"member: an org admin may also create their own", "user-admin", domain.ScopeMember, "", nil},
		{"team: anyone who can see the team may create", "user-teammate", domain.ScopeTeam, "team_eng", nil},
		{"team: the team's manager may create", "user-teamlead", domain.ScopeTeam, "team_eng", nil},
		{"team: someone who cannot see the team may not", "user-outsider", domain.ScopeTeam, "team_eng", domain.ErrNotFound},
		{"organization: an org admin may create", "user-owner", domain.ScopeOrganization, "", nil},
		{"organization: an ordinary member may not", "user-creator", domain.ScopeOrganization, "", domain.ErrForbidden},
		{"a caller outside the organization does not even learn it exists", "user-stranger", domain.ScopeMember, "", domain.ErrNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness()
			corpus, err := h.service.CreateCorpus(context.Background(), creds(test.user), testOrganization, CreateCorpusInput{
				Name: "Handbook", Scope: test.scope, TeamID: test.teamID,
			})
			wantErr(t, err, test.wantErr)
			if test.wantErr != nil {
				return
			}
			switch test.scope {
			case domain.ScopeTeam:
				if corpus.TeamID != test.teamID || corpus.MemberID != "" {
					t.Fatalf("placement = %+v, want it to hang on the team alone", corpus)
				}
			case domain.ScopeOrganization:
				if corpus.TeamID != "" || corpus.MemberID != "" {
					t.Fatalf("placement = %+v, want it to hang on the organization alone", corpus)
				}
			default: // "" and ScopeMember both default to a member corpus.
				if corpus.MemberID != test.user || corpus.TeamID != "" {
					t.Fatalf("placement = %+v, want it to hang on its creator, %s", corpus, test.user)
				}
			}
			if _, err := h.store.Corpus(context.Background(), testOrganization, corpus.ID); err != nil {
				t.Fatalf("the corpus was not recorded: %v", err)
			}
		})
	}
}

// TestCreateCorpusRejectsAMismatchedBedrockKBSpec: a knowledge base embeds
// with whatever model it was created with, not with whatever a caller asks
// for, so a corpus naming a different one is refused before it is recorded —
// the database would refuse it too (see the postgres package's own
// integration test), but the service is what a caller actually sees.
func TestCreateCorpusRejectsAMismatchedBedrockKBSpec(t *testing.T) {
	h := newHarness()
	_, err := h.service.CreateCorpus(context.Background(), creds("user-creator"), testOrganization, CreateCorpusInput{
		Name: "Handbook", Backend: domain.BackendBedrockKB, Dimensions: 512,
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "knowledge base's own embedding spec") {
		t.Fatalf("error = %v, want it to name what a bedrock-kb corpus must use", err)
	}
}

// TestCreateCorpusOnBedrockKBWithTheRightSpecSucceeds is the positive case:
// the harness's own defaults already resolve to Titan v2 at 1024 dimensions,
// cosine, so naming no embedding configuration at all is what a caller who
// just wants a knowledge base corpus actually does.
func TestCreateCorpusOnBedrockKBWithTheRightSpecSucceeds(t *testing.T) {
	h := newHarness()
	corpus, err := h.service.CreateCorpus(context.Background(), creds("user-creator"), testOrganization, CreateCorpusInput{
		Name: "Handbook", Backend: domain.BackendBedrockKB,
	})
	if err != nil {
		t.Fatalf("CreateCorpus: %v", err)
	}
	if corpus.Backend != domain.BackendBedrockKB {
		t.Fatalf("backend = %q", corpus.Backend)
	}
	if corpus.Spec != domain.BedrockKnowledgeBaseEmbeddingSpec {
		t.Fatalf("spec = %+v, want the knowledge base's own %+v", corpus.Spec, domain.BedrockKnowledgeBaseEmbeddingSpec)
	}
}

func TestCreateCorpusRejectsADuplicateSlug(t *testing.T) {
	h := newHarness()
	seedCorpus(h.store, domain.Corpus{Slug: "handbook", Scope: domain.ScopeOrganization})
	if _, err := h.service.CreateCorpus(context.Background(), creds("user-owner"), testOrganization, CreateCorpusInput{
		Name: "Handbook", Slug: "handbook", Scope: domain.ScopeOrganization,
	}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

// TestCorpusReadAccessByScope is the table for who may read (and by the same
// gate, search and answer from) a corpus at each scope.
func TestCorpusReadAccessByScope(t *testing.T) {
	tests := []struct {
		name    string
		user    string
		corpus  domain.Corpus
		wantErr error
	}{
		{"member: the owner may read", "user-creator", domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"}, nil},
		{"member: an org admin may read someone else's", "user-admin", domain.Corpus{Slug: "m2", Scope: domain.ScopeMember, MemberID: "user-creator"}, nil},
		{"member: another ordinary member may not", "user-teammate", domain.Corpus{Slug: "m3", Scope: domain.ScopeMember, MemberID: "user-creator"}, domain.ErrNotFound},
		{"team: a plain team member may read", "user-teammate", domain.Corpus{Slug: "t1", Scope: domain.ScopeTeam, TeamID: "team_eng"}, nil},
		{"team: the team's manager may read too", "user-teamlead", domain.Corpus{Slug: "t2", Scope: domain.ScopeTeam, TeamID: "team_eng"}, nil},
		{"team: someone who cannot see the team may not", "user-outsider", domain.Corpus{Slug: "t3", Scope: domain.ScopeTeam, TeamID: "team_eng"}, domain.ErrNotFound},
		{"team: an org admin may read even without being on the team", "user-owner", domain.Corpus{Slug: "t4", Scope: domain.ScopeTeam, TeamID: "team_eng"}, nil},
		{"organization: any member may read", "user-teammate", domain.Corpus{Slug: "o1", Scope: domain.ScopeOrganization}, nil},
		{"organization: an admin may read too", "user-admin", domain.Corpus{Slug: "o2", Scope: domain.ScopeOrganization}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness()
			corpus := seedCorpus(h.store, test.corpus)
			_, err := h.service.Corpus(context.Background(), creds(test.user), testOrganization, corpus.ID)
			wantErr(t, err, test.wantErr)
		})
	}
}

func TestOrganizationAdminReadsTeamCorpusWithoutTeamMembershipLookup(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "team-eng", Scope: domain.ScopeTeam, TeamID: "team_eng"})
	h.service.teams = nil
	if _, err := h.service.Corpus(context.Background(), creds("user-admin"), testOrganization, corpus.ID); err != nil {
		t.Fatalf("org admin read team corpus = %v", err)
	}
	listed, err := h.service.Corpora(context.Background(), creds("user-admin"), testOrganization,
		ListFilter{Scope: domain.ScopeTeam, TeamID: "team_eng"})
	if err != nil || len(listed) != 1 || listed[0].ID != corpus.ID {
		t.Fatalf("org admin list team corpus = %+v, %v", listed, err)
	}
	h.retriever.results = retrieval.Results{Matches: []domain.Match{{Chunk: domain.Chunk{ID: "chk_team"}, Score: 0.9}}}
	results, err := h.service.Search(context.Background(), creds("user-admin"), testOrganization, corpus.ID, domain.Query{Text: "team notes"})
	if err != nil || len(results.Matches) != 1 {
		t.Fatalf("org admin search team corpus = %+v, %v", results, err)
	}
}

// TestSearchAndAnswerHonorTheReadGate spot-checks that Search and Answer
// apply the same gate TestCorpusReadAccessByScope proved for Corpus, rather
// than each re-deriving it.
func TestSearchAndAnswerHonorTheReadGate(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	h.retriever.results = retrieval.Results{Matches: []domain.Match{{Chunk: domain.Chunk{ID: "chk_1"}, Score: 0.9}}}
	h.generator.response = generation.Response{Text: "Every ninety days [1]."}

	if _, err := h.service.Search(context.Background(), creds("user-teammate"), testOrganization, corpus.ID, domain.Query{Text: "q"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an outsider's Search() error = %v, want ErrNotFound", err)
	}
	if _, err := h.service.Answer(context.Background(), creds("user-teammate"), testOrganization, corpus.ID, domain.Query{Text: "q"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an outsider's Answer() error = %v, want ErrNotFound", err)
	}

	results, err := h.service.Search(context.Background(), creds("user-creator"), testOrganization, corpus.ID, domain.Query{Text: "q"})
	if err != nil || len(results.Matches) != 1 {
		t.Fatalf("the owner's Search() = %+v, %v", results, err)
	}
	answered, err := h.service.Answer(context.Background(), creds("user-creator"), testOrganization, corpus.ID, domain.Query{Text: "q"})
	if err != nil || !answered.Answer.Grounded || len(answered.Matches) != 1 {
		t.Fatalf("the owner's Answer() = %+v, %v", answered, err)
	}
}

func TestAnswerWithoutAGeneratorConfigured(t *testing.T) {
	h := newHarness()
	h.service = New(h.store, fakeDirectory{}, h.teams, h.files, h.documents, h.vectors, h.retriever, nil, h.knowledgeBase, Defaults{}, nil)
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})

	// This check runs before the corpus is even looked up: a deployment with
	// no answering model configured says so to any caller, including one who
	// could not have read the corpus anyway. That leaks nothing about the
	// corpus itself, only about the deployment.
	if _, err := h.service.Answer(context.Background(), creds("user-outsider"), testOrganization, corpus.ID, domain.Query{Text: "q"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestAnswerWithNothingRetrievedDoesNotCallTheModel(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	h.generator.err = errors.New("the generator must not be called when nothing was retrieved")

	answered, err := h.service.Answer(context.Background(), creds("user-creator"), testOrganization, corpus.ID, domain.Query{Text: "q"})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if answered.Answer.Grounded {
		t.Fatalf("answer = %+v, want ungrounded", answered.Answer)
	}
}

// TestDocumentsHonorsTheReadGateAndForwardsPagination covers the listing:
// the same read gate as Corpus, and the cursor pagination added alongside
// this test — status and cursor forwarded unchanged, the limit clamped to
// the page cap exactly as it was before a cursor existed, and the store's
// page and next_cursor passed straight back.
func TestDocumentsHonorsTheReadGateAndForwardsPagination(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})

	if _, err := h.service.Documents(context.Background(), creds("user-teammate"), testOrganization, corpus.ID, "", "", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an outsider's Documents() error = %v, want ErrNotFound", err)
	}

	if _, err := h.service.Documents(context.Background(), creds("user-creator"), testOrganization, corpus.ID, "not-a-status", "", 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an unknown status error = %v, want ErrInvalid", err)
	}

	h.store.documentsPage = []domain.Document{{ID: "doc_1"}}
	h.store.documentsNext = "opaque-cursor"
	for _, limit := range []struct {
		asked, want int
	}{
		{0, MaxDocumentsPerPage},    // unset defaults to the page size
		{9999, MaxDocumentsPerPage}, // too large is clamped to the cap
		{5, 5},                      // anything else passes through unchanged
	} {
		page, err := h.service.Documents(context.Background(), creds("user-creator"), testOrganization, corpus.ID, domain.StatusReady, "prior-cursor", limit.asked)
		if err != nil {
			t.Fatalf("Documents(limit=%d) error = %v", limit.asked, err)
		}
		if h.store.lastDocumentsCall.limit != limit.want {
			t.Fatalf("Documents(limit=%d) asked the store for %d, want %d", limit.asked, h.store.lastDocumentsCall.limit, limit.want)
		}
		if h.store.lastDocumentsCall.status != domain.StatusReady || h.store.lastDocumentsCall.cursor != "prior-cursor" {
			t.Fatalf("status/cursor forwarded = %q/%q, want %q/%q",
				h.store.lastDocumentsCall.status, h.store.lastDocumentsCall.cursor, domain.StatusReady, "prior-cursor")
		}
		if len(page.Documents) != 1 || page.Documents[0].ID != "doc_1" || page.NextCursor != "opaque-cursor" {
			t.Fatalf("page = %+v, want the store's page and next_cursor passed straight through", page)
		}
	}
}

func TestDocumentHonorsTheReadGate(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	document := seedDocument(h.store, domain.Document{CorpusID: corpus.ID, Status: domain.StatusReady})

	if _, err := h.service.Document(context.Background(), creds("user-teammate"), testOrganization, corpus.ID, document.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an outsider's Document() error = %v, want ErrNotFound", err)
	}
	got, err := h.service.Document(context.Background(), creds("user-creator"), testOrganization, corpus.ID, document.ID)
	if err != nil || got.ID != document.ID {
		t.Fatalf("Document() = %+v, %v", got, err)
	}

	// A document that belongs to a different corpus reads as not found, even
	// to a caller who could see this other corpus fine.
	otherCorpus := seedCorpus(h.store, domain.Corpus{Slug: "m2", Scope: domain.ScopeMember, MemberID: "user-creator"})
	if _, err := h.service.Document(context.Background(), creds("user-creator"), testOrganization, otherCorpus.ID, document.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a document read through the wrong corpus = %v, want ErrNotFound", err)
	}
}

// TestWriteAccessByScope is the table for who may add or remove documents at
// each scope: note that team scope needs only visibility, not canManage,
// unlike deleting the whole corpus, and that an organization corpus is
// readable by any member but writable only by an admin.
func TestWriteAccessByScope(t *testing.T) {
	tests := []struct {
		name    string
		user    string
		corpus  domain.Corpus
		wantErr error
	}{
		{"member: the owner may add a document", "user-creator", domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"}, nil},
		{"member: an org admin may add to someone else's", "user-admin", domain.Corpus{Slug: "m2", Scope: domain.ScopeMember, MemberID: "user-creator"}, nil},
		{"member: another ordinary member may not", "user-teammate", domain.Corpus{Slug: "m3", Scope: domain.ScopeMember, MemberID: "user-creator"}, domain.ErrNotFound},
		{"team: seeing the team is enough, canManage is not required", "user-teammate", domain.Corpus{Slug: "t1", Scope: domain.ScopeTeam, TeamID: "team_eng"}, nil},
		{"team: someone who cannot see the team may not", "user-outsider", domain.Corpus{Slug: "t2", Scope: domain.ScopeTeam, TeamID: "team_eng"}, domain.ErrNotFound},
		{"team: an org admin may write even without being on the team", "user-admin", domain.Corpus{Slug: "t3", Scope: domain.ScopeTeam, TeamID: "team_eng"}, nil},
		{"organization: an admin may add", "user-owner", domain.Corpus{Slug: "o1", Scope: domain.ScopeOrganization}, nil},
		{"organization: an ordinary member may see it but not write to it", "user-teammate", domain.Corpus{Slug: "o2", Scope: domain.ScopeOrganization}, domain.ErrForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness()
			corpus := seedCorpus(h.store, test.corpus)
			document, err := h.service.AddDocument(context.Background(), creds(test.user), testOrganization, corpus.ID, given(AddDocumentInput{
				Source: domain.Source{Kind: domain.SourceInline}, Text: "some body text about rotation.", ContentType: "text/plain",
			}))
			wantErr(t, err, test.wantErr)
			if test.wantErr == nil && document.CorpusID != corpus.ID {
				t.Fatalf("document = %+v, want it filed under %s", document, corpus.ID)
			}
		})
	}
}

func TestAddDocumentFetchesFromFilesAsTheCaller(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	h.files.content = domain.SourceContent{ID: "file_1", Filename: "notes.md", ContentType: "text/markdown", Body: "# Notes\n\nSome text.", Scope: domain.ScopeMember, MemberID: "user-creator"}

	document, err := h.service.AddDocument(context.Background(), creds("user-creator"), testOrganization, corpus.ID, given(AddDocumentInput{
		Source: domain.Source{Kind: domain.SourceFile, Ref: "file_1"},
	}))
	if err != nil {
		t.Fatalf("AddDocument() error = %v", err)
	}
	if h.files.seen.organizationID != testOrganization || h.files.seen.fileID != "file_1" {
		t.Fatalf("files was asked for org=%q file=%q", h.files.seen.organizationID, h.files.seen.fileID)
	}
	if h.files.seen.credentials != creds("user-creator") {
		// The ingest worker that indexes this later must never need to act as
		// anyone, which only holds if the fetch happened as this caller, now.
		t.Fatalf("files was not fetched as the caller: %+v", h.files.seen.credentials)
	}
	if document.Title != "notes.md" || document.ContentType != "text/markdown" {
		t.Fatalf("document = %+v, want the title and type files reported", document)
	}
}

func TestOrganizationDocumentACL(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "org-acl", Scope: domain.ScopeOrganization})
	adminDocument, err := h.service.AddDocument(context.Background(), creds("user-admin"), testOrganization, corpus.ID, given(AddDocumentInput{
		Source: domain.Source{Kind: domain.SourceInline}, Text: "Payroll numbers", ContentType: "text/plain", Access: domain.AccessAdmins,
	}))
	if err != nil || adminDocument.Access != domain.AccessAdmins {
		t.Fatalf("admin document = %+v, %v", adminDocument, err)
	}
	generalDocument, err := h.service.AddDocument(context.Background(), creds("user-admin"), testOrganization, corpus.ID, given(AddDocumentInput{
		Source: domain.Source{Kind: domain.SourceInline}, Text: "Public handbook", ContentType: "text/plain",
	}))
	if err != nil || generalDocument.Access != domain.AccessMembers {
		t.Fatalf("general document = %+v, %v", generalDocument, err)
	}
	if _, err := h.service.Document(context.Background(), creds("user-teammate"), testOrganization, corpus.ID, adminDocument.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("member read admin document = %v", err)
	}
	if _, err := h.service.Document(context.Background(), creds("user-teammate"), testOrganization, corpus.ID, generalDocument.ID); err != nil {
		t.Fatalf("member read general document = %v", err)
	}
	if _, err := h.service.Document(context.Background(), creds("user-admin"), testOrganization, corpus.ID, adminDocument.ID); err != nil {
		t.Fatalf("admin read admin document = %v", err)
	}
	if _, err := h.service.Search(context.Background(), creds("user-teammate"), testOrganization, corpus.ID, domain.Query{Text: "payroll", IncludeAdminOnly: true}); err != nil || h.retriever.seen.query.IncludeAdminOnly {
		t.Fatalf("member search accepted admin access: %v, %+v", err, h.retriever.seen.query)
	}
	if _, err := h.service.Search(context.Background(), creds("user-admin"), testOrganization, corpus.ID, domain.Query{Text: "payroll"}); err != nil || !h.retriever.seen.query.IncludeAdminOnly {
		t.Fatalf("admin search lost admin access: %v, %+v", err, h.retriever.seen.query)
	}
	if _, err := h.service.AddDocument(context.Background(), creds("user-teammate"), testOrganization, corpus.ID, given(AddDocumentInput{
		Source: domain.Source{Kind: domain.SourceInline}, Text: "hidden", ContentType: "text/plain", Access: domain.AccessAdmins,
	})); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member creating admin document = %v", err)
	}
}

func TestFileCannotBeCopiedIntoWiderCorpus(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "org-files", Scope: domain.ScopeOrganization})
	h.files.content = domain.SourceContent{ID: "file_private", Scope: domain.ScopeMember, MemberID: "user-admin", Body: "private notes", ContentType: "text/plain"}
	if _, err := h.service.AddDocument(context.Background(), creds("user-admin"), testOrganization, corpus.ID, given(AddDocumentInput{
		Source: domain.Source{Kind: domain.SourceFile, Ref: "file_private"},
	})); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("private file copied to org corpus = %v", err)
	}
	h.files.content = domain.SourceContent{ID: "file_invoice", Scope: domain.ScopeOrganization, Purpose: "billing.attachment", Body: "amount due", ContentType: "text/plain"}
	document, err := h.service.AddDocument(context.Background(), creds("user-admin"), testOrganization, corpus.ID, given(AddDocumentInput{
		Source: domain.Source{Kind: domain.SourceFile, Ref: "file_invoice"}, Access: domain.AccessMembers,
	}))
	if err != nil || document.Access != domain.AccessAdmins {
		t.Fatalf("billing file access = %+v, %v", document, err)
	}
	h.files.content = domain.SourceContent{ID: "file_general", Scope: domain.ScopeOrganization, Purpose: "upload", Access: domain.AccessMembers, Body: "general", ContentType: "text/plain"}
	if _, err := h.service.AddDocument(context.Background(), creds("user-admin"), testOrganization, corpus.ID, given(AddDocumentInput{
		Source: domain.Source{Kind: domain.SourceFile, Ref: "file_general"}, Access: domain.AccessAdmins,
	})); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RAG tried to restrict a general file = %v", err)
	}
	h.files.content = domain.SourceContent{ID: "file_restricted", Scope: domain.ScopeOrganization, Purpose: "upload", Access: domain.AccessAdmins, Body: "restricted", ContentType: "text/plain"}
	document, err = h.service.AddDocument(context.Background(), creds("user-admin"), testOrganization, corpus.ID, given(AddDocumentInput{
		Source: domain.Source{Kind: domain.SourceFile, Ref: "file_restricted"}, Access: domain.AccessMembers,
	}))
	if err != nil || document.Access != domain.AccessAdmins {
		t.Fatalf("restricted file access = %+v, %v", document, err)
	}
}

func TestAddDocumentFromAFileNeedsFilesConfigured(t *testing.T) {
	h := newHarness()
	h.service = New(h.store, fakeDirectory{}, h.teams, nil, nil, h.vectors, h.retriever, h.generator, h.knowledgeBase, Defaults{}, nil)
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	if _, err := h.service.AddDocument(context.Background(), creds("user-creator"), testOrganization, corpus.ID, given(AddDocumentInput{
		Source: domain.Source{Kind: domain.SourceFile, Ref: "file_1"},
	})); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

// given is a document already in hand, as a caller with nothing to decode
// would supply it.
func given(input AddDocumentInput) DocumentReader {
	return func() (AddDocumentInput, error) { return input, nil }
}

func TestAddDocumentDoesNotReadTheBodyOfACallerWhoMayNotWrite(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "o1", Scope: domain.ScopeOrganization})
	read := func() (AddDocumentInput, error) {
		t.Fatal("the document was read for a caller who may not add to this corpus")
		return AddDocumentInput{}, nil
	}
	// An ordinary member may see an organization corpus but not write to it.
	if _, err := h.service.AddDocument(context.Background(), creds("user-teammate"), testOrganization, corpus.ID, read); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("error = %v, want forbidden", err)
	}
	// And a stranger to the organization is turned away just as early.
	if _, err := h.service.AddDocument(context.Background(), creds("user-nobody"), testOrganization, corpus.ID, read); err == nil {
		t.Fatal("a caller the directory does not know was admitted")
	}
}

func TestDeleteDocumentHonorsTheWriteGateAndDeletesVectorsBeforeRows(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"})
	document := seedDocument(h.store, domain.Document{CorpusID: corpus.ID, Status: domain.StatusReady})
	h.store.chunkIDs[document.ID] = []string{"chk_1", "chk_2"}

	if err := h.service.DeleteDocument(context.Background(), creds("user-teammate"), testOrganization, corpus.ID, document.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member's DeleteDocument() error = %v, want ErrNotFound", err)
	}
	if err := h.service.DeleteDocument(context.Background(), creds("user-creator"), testOrganization, corpus.ID, document.ID); err != nil {
		t.Fatalf("DeleteDocument() error = %v", err)
	}
	if _, err := h.store.Document(context.Background(), testOrganization, document.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the document still exists: %v", err)
	}
	if got := h.vectors.store.deleted[corpus.ID]; len(got) != 2 {
		t.Fatalf("vectors deleted for %s = %v, want the document's two chunks", corpus.ID, got)
	}
}

// TestDeleteDocumentOnBedrockKBUsesTheKnowledgeBaseNotVectors: a bedrock-kb
// corpus has no chunk rows for ChunkIDs to find, so deletion must go through
// the knowledge base by document id instead of silently doing nothing the
// way an empty chunk list would for vectorstore.Store.Delete.
func TestDeleteDocumentOnBedrockKBUsesTheKnowledgeBaseNotVectors(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{
		Slug: "kb1", Scope: domain.ScopeMember, MemberID: "user-creator",
		Backend: domain.BackendBedrockKB, Spec: domain.BedrockKnowledgeBaseEmbeddingSpec,
	})
	document := seedDocument(h.store, domain.Document{CorpusID: corpus.ID, Status: domain.StatusReady, ContentSHA256: "hash-1"})

	if err := h.service.DeleteDocument(context.Background(), creds("user-creator"), testOrganization, corpus.ID, document.ID); err != nil {
		t.Fatalf("DeleteDocument() error = %v", err)
	}
	if _, err := h.store.Document(context.Background(), testOrganization, document.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the document still exists: %v", err)
	}
	if got := h.knowledgeBase.deleted; len(got) != 1 || got[0] != document.ID {
		t.Fatalf("knowledge base deletions = %v, want exactly [%s]", got, document.ID)
	}
	if len(h.knowledgeBase.deletedStaged) != 1 || h.knowledgeBase.deletedStaged[0] != document.ID {
		t.Fatalf("staged text was not cleaned up: %v", h.knowledgeBase.deletedStaged)
	}
	if got := h.vectors.store.deleted[corpus.ID]; len(got) != 0 {
		t.Fatalf("vectorstore.Store.Delete was called for a bedrock-kb corpus: %v", got)
	}
}

// TestDeleteCorpusByScope is the table for who may delete a whole corpus,
// which the README calls a larger act than removing one document: a team
// corpus needs its creator, a manager, or an org admin, not mere visibility.
func TestDeleteCorpusByScope(t *testing.T) {
	tests := []struct {
		name    string
		user    string
		corpus  domain.Corpus
		wantErr error
	}{
		{"member: the owner may delete", "user-creator", domain.Corpus{Slug: "m1", Scope: domain.ScopeMember, MemberID: "user-creator"}, nil},
		{"member: an org admin may delete someone else's", "user-admin", domain.Corpus{Slug: "m2", Scope: domain.ScopeMember, MemberID: "user-creator"}, nil},
		{"member: another ordinary member may not, and does not learn it exists", "user-teammate", domain.Corpus{Slug: "m3", Scope: domain.ScopeMember, MemberID: "user-creator"}, domain.ErrNotFound},
		{"team: the creator may delete even without canManage", "user-creator", domain.Corpus{Slug: "t1", Scope: domain.ScopeTeam, TeamID: "team_eng", CreatedBy: "user-creator"}, nil},
		{"team: canManage may delete even when not the creator", "user-teamlead", domain.Corpus{Slug: "t2", Scope: domain.ScopeTeam, TeamID: "team_eng", CreatedBy: "user-creator"}, nil},
		{"team: seeing the team is not enough on its own", "user-teammate", domain.Corpus{Slug: "t3", Scope: domain.ScopeTeam, TeamID: "team_eng", CreatedBy: "user-creator"}, domain.ErrForbidden},
		{"team: an org admin may delete regardless", "user-owner", domain.Corpus{Slug: "t4", Scope: domain.ScopeTeam, TeamID: "team_eng", CreatedBy: "user-creator"}, nil},
		{"team: someone who cannot see the team may not, and does not learn it exists", "user-outsider", domain.Corpus{Slug: "t5", Scope: domain.ScopeTeam, TeamID: "team_eng", CreatedBy: "user-creator"}, domain.ErrNotFound},
		{"organization: an admin may delete", "user-owner", domain.Corpus{Slug: "o1", Scope: domain.ScopeOrganization}, nil},
		{"organization: an ordinary member may see it but not delete it", "user-teammate", domain.Corpus{Slug: "o2", Scope: domain.ScopeOrganization}, domain.ErrForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness()
			corpus := seedCorpus(h.store, test.corpus)
			err := h.service.DeleteCorpus(context.Background(), creds(test.user), testOrganization, corpus.ID)
			wantErr(t, err, test.wantErr)
			if test.wantErr != nil {
				return
			}
			if _, err := h.store.Corpus(context.Background(), testOrganization, corpus.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("the corpus still exists after delete: %v", err)
			}
			// The vectors go first: DeleteCorpus must have dropped them before
			// the row disappeared, or a failed drop would orphan them.
			if got := h.vectors.store.dropped; len(got) != 1 || got[0] != corpus.ID {
				t.Fatalf("vectors dropped = %v, want exactly [%s]", got, corpus.ID)
			}
		})
	}
}

// TestDeleteCorpusOnBedrockKBRemovesFromTheKnowledgeBase: there is no
// per-corpus index for a bedrock-kb corpus the way S3 Vectors has one — one
// knowledge base serves the whole deployment — so deleting the corpus means
// naming every document it owned to the knowledge base instead of dropping
// one thing.
func TestDeleteCorpusOnBedrockKBRemovesFromTheKnowledgeBase(t *testing.T) {
	h := newHarness()
	corpus := seedCorpus(h.store, domain.Corpus{
		Slug: "kb1", Scope: domain.ScopeOrganization,
		Backend: domain.BackendBedrockKB, Spec: domain.BedrockKnowledgeBaseEmbeddingSpec,
	})
	first := seedDocument(h.store, domain.Document{CorpusID: corpus.ID, Status: domain.StatusReady, ContentSHA256: "h1"})
	second := seedDocument(h.store, domain.Document{CorpusID: corpus.ID, Status: domain.StatusIngesting, ContentSHA256: "h2"})

	if err := h.service.DeleteCorpus(context.Background(), creds("user-owner"), testOrganization, corpus.ID); err != nil {
		t.Fatalf("DeleteCorpus() error = %v", err)
	}
	if _, err := h.store.Corpus(context.Background(), testOrganization, corpus.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the corpus still exists after delete: %v", err)
	}
	got := h.knowledgeBase.deleted
	if len(got) != 2 {
		t.Fatalf("knowledge base deletions = %v, want both documents", got)
	}
	for _, id := range []string{first.ID, second.ID} {
		if !slices.Contains(got, id) {
			t.Fatalf("knowledge base deletions = %v, missing %s", got, id)
		}
	}
	if len(h.vectors.store.dropped) != 0 {
		t.Fatalf("vectorstore.Store.DropCorpus was called for a bedrock-kb corpus: %v", h.vectors.store.dropped)
	}
}

// TestCorporaDefaultsToTheCallersOwnPlusTheOrganization backs the listing
// endpoint's own default, mirroring how files lists.
func TestCorporaDefaultsToTheCallersOwnPlusTheOrganization(t *testing.T) {
	h := newHarness()
	own := seedCorpus(h.store, domain.Corpus{Slug: "mine", Scope: domain.ScopeMember, MemberID: "user-creator"})
	seedCorpus(h.store, domain.Corpus{Slug: "someone-elses", Scope: domain.ScopeMember, MemberID: "user-teammate"})
	org := seedCorpus(h.store, domain.Corpus{Slug: "shared", Scope: domain.ScopeOrganization})
	team := seedCorpus(h.store, domain.Corpus{Slug: "team", Scope: domain.ScopeTeam, TeamID: "team_eng"})

	corpora, err := h.service.Corpora(context.Background(), creds("user-creator"), testOrganization, ListFilter{})
	if err != nil {
		t.Fatalf("Corpora() error = %v", err)
	}
	seen := map[string]bool{}
	for _, corpus := range corpora {
		seen[corpus.ID] = true
	}
	if !seen[own.ID] || !seen[org.ID] || seen[team.ID] {
		t.Fatalf("default listing ids = %v, want the caller's own and the organization's but no team corpus", seen)
	}

	teamList, err := h.service.Corpora(context.Background(), creds("user-teammate"), testOrganization, ListFilter{Scope: domain.ScopeTeam, TeamID: "team_eng"})
	if err != nil || len(teamList) != 1 || teamList[0].ID != team.ID {
		t.Fatalf("team listing = %+v, %v", teamList, err)
	}

	if _, err := h.service.Corpora(context.Background(), creds("user-outsider"), testOrganization, ListFilter{Scope: domain.ScopeTeam, TeamID: "team_eng"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("listing a team the caller cannot see = %v, want ErrNotFound", err)
	}
	if _, err := h.service.Corpora(context.Background(), creds("user-stranger"), testOrganization, ListFilter{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a caller outside the organization = %v, want ErrNotFound", err)
	}
}
