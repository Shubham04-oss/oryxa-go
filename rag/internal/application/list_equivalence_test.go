package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// These tests hold each listing to the single read beside it
// (docs/design/permissions §6 step 2): for every caller and every fixture, a
// listed item is one the single read allows, and an item the single read
// allows is listed whenever the listing's filter selects it. They run here
// against the fake store, and again against PostgreSQL in
// list_equivalence_integration_test.go, so the SQL pre-filters are held to
// the same answer.

type listCaller struct {
	name        string
	userID      string
	credentials authclient.Credentials
}

// listFixtures is a corpus in every placement, each holding documents at
// every access level.
type listFixtures struct {
	corpora   []domain.Corpus
	documents map[string][]domain.Document // by corpus id
}

func (fixtures listFixtures) corpus(name string) domain.Corpus {
	for _, corpus := range fixtures.corpora {
		if corpus.Name == name {
			return corpus
		}
	}
	panic("no fixture corpus " + name)
}

// listCallers is every kind of caller: members on and off team_eng, its team
// admin, org admins in their own session, their primary and organization
// agents, and specialists whose grants are all ("*"), absent, some corpora,
// and none.
func listCallers(fixtures listFixtures) []listCaller {
	some := []string{fixtures.corpus("creator's").ID, fixtures.corpus("eng one").ID, fixtures.corpus("handbook").ID}
	return []listCaller{
		{"member on team_eng", "user-creator", creds("user-creator")},
		{"team_eng's team admin", "user-teamlead", creds("user-teamlead")},
		{"member on no team", "user-outsider", creds("user-outsider")},
		{"org admin", "user-admin", creds("user-admin")},
		{"org owner", "user-owner", creds("user-owner")},
		{"member's primary agent", "user-creator", roleCreds("user-creator", "primary", nil)},
		{"org admin's primary agent", "user-admin", roleCreds("user-admin", "primary", nil)},
		{"org owner's primary agent", "user-owner", roleCreds("user-owner", "primary", nil)},
		{"organization agent (admin)", "user-admin", roleCreds("user-admin", "organization", nil)},
		{"specialist, grant *", "user-creator", roleCreds("user-creator", "specialist", []string{"*"})},
		{"specialist, no grant field", "user-creator", roleCreds("user-creator", "specialist", nil)},
		{"specialist, some corpora", "user-creator", roleCreds("user-creator", "specialist", some)},
		{"specialist, grant []", "user-creator", roleCreds("user-creator", "specialist", []string{})},
		{"admin's specialist, grant *", "user-admin", roleCreds("user-admin", "specialist", []string{"*"})},
		{"admin's specialist, some corpora", "user-admin", roleCreds("user-admin", "specialist", some)},
	}
}

// seedListFixtures writes the fixtures straight to the store, as nothing a
// caller does should decide what exists.
func seedListFixtures(t *testing.T, ctx context.Context, store Store, organizationID string) listFixtures {
	t.Helper()
	spec, err := domain.ResolveSpec(domain.ProviderBedrock, "amazon.titan-embed-text-v2:0", 0, domain.MetricCosine)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	placements := []struct {
		name           string
		scope          domain.Scope
		teamID, member string
	}{
		{"handbook", domain.ScopeOrganization, "", ""},
		{"creator's", domain.ScopeMember, "", "user-creator"},
		{"admin's", domain.ScopeMember, "", "user-admin"},
		{"outsider's", domain.ScopeMember, "", "user-outsider"},
		{"eng one", domain.ScopeTeam, "team_eng", ""},
		{"eng two", domain.ScopeTeam, "team_eng", ""},
		{"eng three", domain.ScopeTeam, "team_eng", ""},
		{"ops", domain.ScopeTeam, "team_ops", ""},
		{"ghost team's", domain.ScopeTeam, "team_ghost", ""},
	}
	fixtures := listFixtures{documents: map[string][]domain.Document{}}
	for i, placement := range placements {
		id, err := domain.NewID(domain.CorpusIDPrefix)
		if err != nil {
			t.Fatal(err)
		}
		at := base.Add(time.Duration(i) * time.Minute)
		corpus := domain.Corpus{
			ID: id, OrganizationID: organizationID, Slug: fmt.Sprintf("list-%d", i), Name: placement.name,
			Scope: placement.scope, TeamID: placement.teamID, MemberID: placement.member,
			Spec: spec, Backend: domain.BackendPgvector, Chunking: domain.DefaultChunkPolicy(spec),
			Metadata: domain.Metadata{}, CreatedBy: "user-creator", CreatedAt: at, UpdatedAt: at,
		}
		if err := store.CreateCorpus(ctx, corpus, listEvent(t)); err != nil {
			t.Fatalf("CreateCorpus %s: %v", placement.name, err)
		}
		fixtures.corpora = append(fixtures.corpora, corpus)
		// Five documents, admin-only ones among them, two at the same instant
		// so the cursor's tie-break on id is exercised.
		for j, access := range []domain.DocumentAccess{
			domain.AccessMembers, domain.AccessAdmins, domain.AccessMembers, domain.AccessAdmins, domain.AccessMembers,
		} {
			documentID, err := domain.NewID(domain.DocumentIDPrefix)
			if err != nil {
				t.Fatal(err)
			}
			created := at.Add(-time.Duration(min(j, 3)) * time.Second)
			body := fmt.Sprintf("%s document %d", placement.name, j)
			document := domain.Document{
				ID: documentID, CorpusID: corpus.ID, OrganizationID: organizationID,
				Source: domain.Source{Kind: domain.SourceExternal, Ref: fmt.Sprintf("ref-%d-%d", i, j)},
				Title:  body, ContentType: "text/plain", ContentSHA256: domain.Hash(body), ByteSize: int64(len(body)),
				Metadata: domain.Metadata{}, Access: access, Status: domain.StatusPending,
				CreatedBy: "user-creator", CreatedAt: created, UpdatedAt: created,
			}
			stored, _, err := store.UpsertDocument(ctx, document, body, listEvent(t))
			if err != nil {
				t.Fatalf("UpsertDocument: %v", err)
			}
			fixtures.documents[corpus.ID] = append(fixtures.documents[corpus.ID], stored)
		}
	}
	return fixtures
}

func listEvent(t *testing.T) domain.Event {
	t.Helper()
	id, err := domain.NewID(domain.EventIDPrefix)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Event{ID: id, DetailType: "rag.test.v1", OccurredAt: time.Now().UTC(), Data: []byte(`{}`)}
}

// selects is what a corpus listing's filter asks for, apart from who may
// read it: see Service.Corpora.
func selects(filter ListFilter, callerID string, corpus domain.Corpus) bool {
	switch filter.Scope {
	case "":
		return corpus.Scope == domain.ScopeOrganization || (corpus.Scope == domain.ScopeMember && corpus.MemberID == callerID)
	case domain.ScopeMember:
		return corpus.Scope == domain.ScopeMember && corpus.MemberID == callerID
	case domain.ScopeOrganization:
		return corpus.Scope == domain.ScopeOrganization
	case domain.ScopeTeam:
		return corpus.Scope == domain.ScopeTeam && corpus.TeamID == filter.TeamID
	default:
		return false
	}
}

// singleRead is whether Corpus lets the caller read it, failing the test on
// anything but an allow or a not found.
func singleRead(t *testing.T, ctx context.Context, service *Service, caller listCaller, organizationID, corpusID string) (domain.Corpus, bool) {
	t.Helper()
	corpus, err := service.Corpus(ctx, caller.credentials, organizationID, corpusID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("%s: Corpus(%s): %v", caller.name, corpusID, err)
	}
	return corpus, err == nil
}

func checkCorporaMatchCorpus(t *testing.T, ctx context.Context, service *Service, organizationID string, fixtures listFixtures) {
	filters := []ListFilter{
		{}, {Scope: domain.ScopeMember}, {Scope: domain.ScopeOrganization},
		{Scope: domain.ScopeTeam, TeamID: "team_eng"}, {Scope: domain.ScopeTeam, TeamID: "team_ops"},
		{Scope: domain.ScopeTeam, TeamID: "team_ghost"}, {Scope: domain.ScopeTeam},
	}
	for _, caller := range listCallers(fixtures) {
		for _, filter := range filters {
			t.Run(fmt.Sprintf("%s/%s %s", caller.name, cmpOr(string(filter.Scope), "default"), filter.TeamID), func(t *testing.T) {
				read := map[string]domain.Corpus{}
				var want []string
				for _, corpus := range fixtures.corpora {
					single, allowed := singleRead(t, ctx, service, caller, organizationID, corpus.ID)
					if allowed {
						read[corpus.ID] = single
						if selects(filter, caller.userID, corpus) {
							want = append(want, corpus.ID)
						}
					}
				}
				listed, err := service.Corpora(ctx, caller.credentials, organizationID, filter)
				if err != nil {
					// A team the caller cannot see is refused outright; then
					// none of its corpora may be readable one at a time either.
					if !errors.Is(err, domain.ErrNotFound) || len(want) != 0 {
						t.Fatalf("Corpora: %v, while the single read allows %v", err, names(fixtures, want))
					}
					return
				}
				var got []string
				for _, corpus := range listed {
					single, allowed := read[corpus.ID]
					if !allowed {
						t.Errorf("listed %q, which Corpus refuses", corpus.Name)
						continue
					}
					if corpus.DocumentCount != single.DocumentCount || corpus.ChunkCount != single.ChunkCount {
						t.Errorf("%q counts %d/%d listed, %d/%d read", corpus.Name,
							corpus.DocumentCount, corpus.ChunkCount, single.DocumentCount, single.ChunkCount)
					}
					got = append(got, corpus.ID)
				}
				slices.Sort(got)
				slices.Sort(want)
				if !slices.Equal(got, want) {
					t.Errorf("listed %v, single read allows %v", names(fixtures, got), names(fixtures, want))
				}
			})
		}
	}
}

// checkDocumentsMatchDocument pages through every corpus at a small page
// size, for every status filter, and holds the whole listing to Document.
// A page that comes back short while a cursor follows it fails the test: it
// would mean the SQL and the predicate disagree.
func checkDocumentsMatchDocument(t *testing.T, ctx context.Context, service *Service, organizationID string, fixtures listFixtures) {
	const limit = 2
	for _, caller := range listCallers(fixtures) {
		for _, corpus := range fixtures.corpora {
			for _, status := range []domain.IngestStatus{"", domain.StatusPending, domain.StatusReady} {
				t.Run(fmt.Sprintf("%s/%s/%s", caller.name, corpus.Name, cmpOr(string(status), "any")), func(t *testing.T) {
					var want []string
					for _, document := range fixtures.documents[corpus.ID] {
						_, err := service.Document(ctx, caller.credentials, organizationID, corpus.ID, document.ID)
						if err != nil && !errors.Is(err, domain.ErrNotFound) {
							t.Fatalf("Document: %v", err)
						}
						if err == nil && (status == "" || document.Status == status) {
							want = append(want, document.ID)
						}
					}
					_, corpusReadable := singleRead(t, ctx, service, caller, organizationID, corpus.ID)
					var got []string
					cursor := ""
					for pages := 0; ; pages++ {
						if pages > 10 {
							t.Fatal("the listing never ends")
						}
						page, err := service.Documents(ctx, caller.credentials, organizationID, corpus.ID, status, cursor, limit)
						if err != nil {
							if !errors.Is(err, domain.ErrNotFound) || corpusReadable {
								t.Fatalf("Documents: %v, while Corpus allows: %v", err, corpusReadable)
							}
							break
						}
						if page.NextCursor != "" && len(page.Documents) != limit {
							t.Errorf("page %d has %d documents of %d and a cursor after it", pages, len(page.Documents), limit)
						}
						for _, document := range page.Documents {
							if slices.Contains(got, document.ID) {
								t.Errorf("%s listed twice", document.Title)
							}
							got = append(got, document.ID)
						}
						if cursor = page.NextCursor; cursor == "" {
							break
						}
					}
					for _, id := range got {
						if !slices.Contains(want, id) {
							t.Errorf("listed %s, which Document refuses", id)
						}
					}
					for _, id := range want {
						if !slices.Contains(got, id) {
							t.Errorf("Document allows %s, which the listing left out", id)
						}
					}
				})
			}
		}
	}
}

func names(fixtures listFixtures, ids []string) []string {
	var out []string
	for _, corpus := range fixtures.corpora {
		if slices.Contains(ids, corpus.ID) {
			out = append(out, corpus.Name)
		}
	}
	return out
}

func cmpOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func TestCorporaListsExactlyWhatCorpusReads(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	fixtures := seedListFixtures(t, ctx, h.store, testOrganization)
	checkCorporaMatchCorpus(t, ctx, h.service, testOrganization, fixtures)
}

func TestDocumentsListsExactlyWhatDocumentReads(t *testing.T) {
	h := newHarness()
	h.store.pageDocuments = true
	ctx := context.Background()
	fixtures := seedListFixtures(t, ctx, h.store, testOrganization)
	checkDocumentsMatchDocument(t, ctx, h.service, testOrganization, fixtures)
}

// TestCorporaAsksTeamsOncePerTeam: a listing of three team_eng corpora asks
// teams about team_eng once, not once for the filter and again per corpus.
func TestCorporaAsksTeamsOncePerTeam(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	seedListFixtures(t, ctx, h.store, testOrganization)
	listed, err := h.service.Corpora(ctx, roleCreds("user-creator", "primary", nil), testOrganization,
		ListFilter{Scope: domain.ScopeTeam, TeamID: "team_eng"})
	if err != nil || len(listed) != 3 {
		t.Fatalf("listed %d, %v; want the three team_eng corpora", len(listed), err)
	}
	if calls := h.teams.calls["team_eng"]; calls != 1 {
		t.Errorf("teams asked about team_eng %d times, want 1", calls)
	}
}
