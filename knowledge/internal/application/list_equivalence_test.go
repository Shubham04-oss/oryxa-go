package application

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/postgres"
)

// A list route answers exactly what the matching single read allows, decided
// by the same check (docs/design/permissions, gap G3 and §6 step 2). Each
// test below is a matrix of callers × sources: everything a list answers
// must pass the single read, and everything the single read allows must be
// listed under the filter that names it.

// equivalenceCallers is every kind of caller the rules tell apart: a member
// and an admin in their own sessions, the agents acting for them, and a
// specialist under each shape of knowledge grant.
func equivalenceCallers() map[string]domain.OrgMembership {
	as := func(role domain.OrgRole, agentRole string, grant []string) domain.OrgMembership {
		caller := domain.OrgMembership{OrganizationID: "org_1", UserID: "user-1", Role: role}
		if agentRole != "" {
			caller.Agent, caller.AgentRole, caller.AgentKnowledge = true, agentRole, grant
		}
		return caller
	}
	some := []string{"src_mine_files", "src_other_files", "src_mine_gh", "src_mine_removed"}
	return map[string]domain.OrgMembership{
		"member":                                 as(domain.OrgRoleMember, "", nil),
		"admin":                                  as(domain.OrgRoleAdmin, "", nil),
		"owner":                                  as(domain.OrgRoleOwner, "", nil),
		"primary agent of a member":              as(domain.OrgRoleMember, "primary", nil),
		"primary agent of an admin":              as(domain.OrgRoleAdmin, "primary", nil),
		"organization agent of an admin":         as(domain.OrgRoleAdmin, "organization", nil),
		"specialist of an admin, no grant field": as(domain.OrgRoleAdmin, "specialist", nil),
		"specialist of an admin, grant *":        as(domain.OrgRoleAdmin, "specialist", []string{"*"}),
		"specialist of an admin, grant some":     as(domain.OrgRoleAdmin, "specialist", some),
		"specialist of an admin, grant []":       as(domain.OrgRoleAdmin, "specialist", []string{}),
		"specialist of a member, grant some":     as(domain.OrgRoleMember, "specialist", some),
		// A grant naming a source someone else made reaches it no further
		// than its member could.
		"specialist of a member, grant others": as(domain.OrgRoleMember, "specialist", []string{"src_other_files"}),
	}
}

// equivalenceSources is a source of each scope and status, made by the
// caller (user-1) and by someone else (user-2) on the same team.
func equivalenceSources() []domain.Source {
	source := func(id, createdBy, provider, connection string, scope domain.Scope, place string, status domain.Status, target map[string]string) domain.Source {
		made := domain.Source{
			ID: id, OrganizationID: "org_1", Provider: provider, ConnectionID: connection, Target: target,
			Scope: scope, CreatedBy: createdBy, Status: status, Interval: time.Hour, SyncAt: &testClock,
			CreatedAt: testClock, UpdatedAt: testClock,
		}
		switch scope {
		case domain.ScopeTeam:
			made.TeamID = place
		case domain.ScopeMember:
			made.MemberID = place
		}
		switch status {
		case domain.StatusPaused:
			made.Reason = "needs_reconnect"
		case domain.StatusRemoved:
			made.Reason, made.SyncAt = "source_removed", nil
		}
		if connection != "" {
			made.ComposioAccountID = "ca_" + id
		}
		return made
	}
	folder := func(name string) map[string]string { return map[string]string{"folder": name} }
	repository := func(name string) map[string]string { return map[string]string{"repository": name} }
	return []domain.Source{
		source("src_mine_files", "user-1", "files", "", domain.ScopeOrganization, "", domain.StatusActive, folder("mine")),
		source("src_mine_team", "user-1", "files", "", domain.ScopeTeam, "team_1", domain.StatusActive, map[string]string{"team": "team_1"}),
		source("src_mine_self", "user-1", "files", "", domain.ScopeMember, "user-1", domain.StatusActive, map[string]string{"member": "self"}),
		source("src_mine_gh", "user-1", "github", "con_1", domain.ScopeMember, "user-1", domain.StatusActive, repository("acme/api")),
		source("src_mine_paused", "user-1", "github", "con_2", domain.ScopeMember, "user-1", domain.StatusPaused, repository("acme/web")),
		source("src_mine_removed", "user-1", "files", "", domain.ScopeOrganization, "", domain.StatusRemoved, folder("old")),
		source("src_other_files", "user-2", "files", "", domain.ScopeOrganization, "", domain.StatusActive, folder("theirs")),
		source("src_other_team", "user-2", "files", "", domain.ScopeTeam, "team_1", domain.StatusActive, map[string]string{"team": "team_1", "folder": "x"}),
		source("src_other_self", "user-2", "files", "", domain.ScopeMember, "user-2", domain.StatusActive, map[string]string{"member": "self"}),
		source("src_other_gh", "user-2", "github", "con_3", domain.ScopeMember, "user-2", domain.StatusActive, repository("acme/api")),
		source("src_other_removed", "user-2", "files", "", domain.ScopeOrganization, "", domain.StatusRemoved, folder("gone")),
	}
}

// equivalenceQueries is every filter a list takes: none, the organization-
// wide listing an admin asks for, each narrowing alone and with it, and the
// removed sources a listing leaves out unless asked.
func equivalenceQueries() map[string]domain.SourceQuery {
	team := domain.Placement{Scope: domain.ScopeTeam, TeamID: "team_1"}
	self := domain.Placement{Scope: domain.ScopeMember, MemberID: "user-1"}
	other := domain.Placement{Scope: domain.ScopeMember, MemberID: "user-2"}
	organization := domain.Placement{Scope: domain.ScopeOrganization}
	return map[string]domain.SourceQuery{
		"mine":                            {},
		"organization":                    {Admin: true},
		"provider":                        {Provider: "github"},
		"organization, provider":          {Admin: true, Provider: "files"},
		"connection":                      {ConnectionID: "con_1"},
		"organization, connection":        {Admin: true, ConnectionID: "con_3"},
		"team placement":                  {Placement: &team},
		"organization, team placement":    {Admin: true, Placement: &team},
		"own placement":                   {Placement: &self},
		"organization, other placement":   {Admin: true, Placement: &other},
		"organization placement":          {Admin: true, Placement: &organization},
		"including removed":               {IncludeRemoved: true},
		"organization, including removed": {Admin: true, IncludeRemoved: true},
	}
}

// matches says whether a source is one the query's filters name, apart from
// who may see it: the filters a caller chooses, not the rules.
func matches(query domain.SourceQuery, callerID string, source domain.Source) bool {
	switch {
	case source.OrganizationID != "org_1",
		source.Status == domain.StatusRemoved && !query.IncludeRemoved,
		// Without the organization-wide listing a caller asks for their own.
		!query.Admin && source.CreatedBy != callerID,
		query.Provider != "" && source.Provider != query.Provider,
		query.ConnectionID != "" && source.ConnectionID != query.ConnectionID,
		query.Placement != nil && !atPlacement(source, *query.Placement):
		return false
	}
	return true
}

// lister is the service as a caller reaches it, over any store.
func lister(store Store, caller domain.OrgMembership) *Service {
	return New(Dependencies{
		Store:     store,
		Directory: &fakeDirectory{membership: caller},
		Clock:     func() time.Time { return testClock },
	}, Config{})
}

// wideStore answers every source a listing's other filters name, whoever
// made it: the SQL is a pre-filter, so a mistake in it must not show a
// caller what the single read would refuse them.
type wideStore struct{ *fakeStore }

func (store wideStore) Sources(ctx context.Context, organizationID string, query domain.SourceQuery) ([]domain.Source, error) {
	query.Admin = true
	return store.fakeStore.Sources(ctx, organizationID, query)
}

func equivalenceFake(t *testing.T) *fakeStore {
	t.Helper()
	store := newFakeStore()
	for _, source := range equivalenceSources() {
		store.sources[source.ID] = source
		store.runs = append(store.runs, domain.SyncRun{
			ID: "run_" + source.ID, OrganizationID: "org_1", SourceID: source.ID,
			Kind: "backfill", Status: domain.RunCompleted, StartedAt: testClock,
		})
	}
	return store
}

func TestSourceListsAnswerWhatTheSingleReadAllows(t *testing.T) {
	fake := equivalenceFake(t)
	stores := map[string]Store{"fake": fake, "wide pre-filter": wideStore{fake}}
	if store := equivalencePostgres(t); store != nil {
		stores["postgres"] = store
	}
	for storeName, store := range stores {
		t.Run(storeName, func(t *testing.T) { checkSourceLists(t, store) })
	}
}

func checkSourceLists(t *testing.T, store Store) {
	ctx := context.Background()
	credentials := authclient.Credentials{Authorization: "Bearer user-1"}
	fixtures := equivalenceSources()
	if _, ok := store.(*postgres.Store); ok {
		fixtures = append(fixtures, foreignSource())
	}
	for callerName, caller := range equivalenceCallers() {
		service := lister(store, caller)
		readable := map[string]bool{}
		for _, source := range fixtures {
			_, err := service.Source(ctx, credentials, "org_1", source.ID)
			switch {
			case err == nil:
				readable[source.ID] = true
			case !errors.Is(err, domain.ErrNotFound):
				t.Fatalf("%s: Source(%s) = %v; want it or not found", callerName, source.ID, err)
			}
		}
		for queryName, query := range equivalenceQueries() {
			listed, err := service.Sources(ctx, credentials, "org_1", query)
			if err != nil {
				t.Fatalf("%s, %s: Sources() = %v", callerName, queryName, err)
			}
			got := []string{}
			for _, source := range listed {
				got = append(got, source.ID)
				if !readable[source.ID] {
					t.Errorf("%s, %s: lists %s, which the single read refuses", callerName, queryName, source.ID)
				}
			}
			for _, source := range fixtures {
				if readable[source.ID] && matches(query, caller.UserID, source) && !slices.Contains(got, source.ID) {
					t.Errorf("%s, %s: does not list %s, which the single read allows", callerName, queryName, source.ID)
				}
			}
		}
	}
}

// A source's runs are listed for exactly the callers who may read it.
func TestRunListsAnswerWhatTheSingleReadAllows(t *testing.T) {
	stores := map[string]Store{"fake": equivalenceFake(t)}
	if store := equivalencePostgres(t); store != nil {
		stores["postgres"] = store
	}
	ctx := context.Background()
	credentials := authclient.Credentials{Authorization: "Bearer user-1"}
	for storeName, store := range stores {
		t.Run(storeName, func(t *testing.T) {
			for callerName, caller := range equivalenceCallers() {
				service := lister(store, caller)
				for _, source := range equivalenceSources() {
					_, readErr := service.Source(ctx, credentials, "org_1", source.ID)
					runs, err := service.Runs(ctx, credentials, "org_1", source.ID, 0)
					if !errors.Is(err, readErr) && (err != nil || readErr != nil) {
						t.Errorf("%s: Runs(%s) = %v; the single read answers %v", callerName, source.ID, err, readErr)
						continue
					}
					if err == nil && (len(runs) != 1 || runs[0].SourceID != source.ID) {
						t.Errorf("%s: Runs(%s) = %+v; want the source's one run", callerName, source.ID, runs)
					}
				}
			}
		})
	}
}

// foreignSource is the caller's own source in another organization, which
// the real store keeps apart by organization.
func foreignSource() domain.Source {
	return domain.Source{
		ID: "src_foreign", OrganizationID: "org_2", Provider: "files", Target: map[string]string{"folder": "abroad"},
		Scope: domain.ScopeOrganization, CreatedBy: "user-1", Status: domain.StatusActive,
		Interval: time.Hour, SyncAt: &testClock, CreatedAt: testClock, UpdatedAt: testClock,
	}
}

// equivalencePostgres is the real store holding the same sources and runs,
// in a schema of its own dropped afterwards, when KNOWLEDGE_TEST_DATABASE_URL
// names a database; nil otherwise, so the matrix still runs over the fake.
func equivalencePostgres(t *testing.T) *postgres.Store {
	t.Helper()
	databaseURL := os.Getenv("KNOWLEDGE_TEST_DATABASE_URL")
	if databaseURL == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schemaName := fmt.Sprintf("knowledge_test_lists_%d", time.Now().UnixNano())
	schema := pgx.Identifier{schemaName}.Sanitize()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		_ = admin.Close(cleanup)
	})
	scoped, err := url.Parse(databaseURL)
	if err != nil || scoped.Scheme == "" {
		t.Fatalf("KNOWLEDGE_TEST_DATABASE_URL must be a postgres:// URL")
	}
	values := scoped.Query()
	values.Set("search_path", schemaName+",public")
	scoped.RawQuery = values.Encode()
	store, err := postgres.Open(ctx, scoped.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, source := range append(equivalenceSources(), foreignSource()) {
		if err := store.CreateSource(ctx, source, nil); err != nil {
			t.Fatalf("CreateSource(%s) = %v", source.ID, err)
		}
		if err := store.StartRun(ctx, domain.SyncRun{
			ID: "run_" + source.ID, OrganizationID: source.OrganizationID, SourceID: source.ID,
			Kind: "backfill", Status: domain.RunRunning, StartedAt: testClock,
		}); err != nil {
			t.Fatalf("StartRun(%s) = %v", source.ID, err)
		}
	}
	return store
}
