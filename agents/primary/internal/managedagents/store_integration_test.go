package managedagents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestStoreLifecycle needs the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/managedagents
func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-managed-agents-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	scope := Scope{OrganizationID: fmt.Sprintf("org_managed_test_%d", suffix), UserID: fmt.Sprintf("user_managed_test_%d", suffix)}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'user_id'=$1`, scope.UserID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1 AND owner_user_id=$2`, scope.OrganizationID, scope.UserID)
	}()

	primary, err := store.EnsurePrimary(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.EnsurePrimary(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if primary.ID != again.ID || primary.Kind != KindPrimary || primary.Name == "" {
		t.Fatalf("primary=%+v again=%+v", primary, again)
	}

	specialist, err := store.Create(ctx, scope, Definition{Name: "Sales", Description: "Owns pipeline", Instructions: "Review CRM."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, scope, Definition{Name: "sales", Instructions: "Duplicate."}); !errors.Is(err, ErrConflict) {
		t.Fatalf("case-insensitive duplicate error = %v", err)
	}
	name := "Revenue"
	updated, err := store.Update(ctx, scope, specialist.ID, UpdateInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.Revision != 2 {
		t.Fatalf("updated=%+v", updated)
	}
	// A specialist reads every app until its owner grants otherwise; the
	// grant set survives a later change of anything else; the primary has none.
	if primary.Grant != nil || !reflect.DeepEqual(updated.SpecialistGrant(), runprofile.DefaultGrant()) || updated.Grant == nil {
		t.Fatalf("primary grant=%+v specialist grant=%+v", primary.Grant, updated.Grant)
	}
	grant := runprofile.Grant{Apps: []string{"gmail"}, Tools: []string{}, WriteApps: []string{"gmail"}, WriteTools: []string{},
		Corpora: []string{"cor_a"}, Files: []string{"member", "team_a"}, Knowledge: []string{}}
	if _, err := store.Update(ctx, scope, specialist.ID, UpdateInput{Grant: &grant}); err != nil {
		t.Fatal(err)
	}
	description := "Owns revenue"
	if _, err := store.Update(ctx, scope, specialist.ID, UpdateInput{Description: &description}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, scope, specialist.ID); err != nil || !reflect.DeepEqual(*got.Grant, grant) || got.Revision != 4 {
		t.Fatalf("granted=%+v, %v", got, err)
	}
	if _, err := store.Update(ctx, scope, primary.ID, UpdateInput{Grant: &grant}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a grant for the primary: error = %v", err)
	}
	// A specialist answers on lite until its owner moves it, and keeps the
	// tier set through later changes; the primary is always on main.
	if primary.ModelTier != modelrouter.Main || updated.ModelTier != modelrouter.Lite {
		t.Fatalf("primary tier=%q specialist tier=%q", primary.ModelTier, updated.ModelTier)
	}
	mainTier := modelrouter.Main
	if _, err := store.Update(ctx, scope, specialist.ID, UpdateInput{ModelTier: &mainTier}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Update(ctx, scope, specialist.ID, UpdateInput{Description: &description}); err != nil || got.ModelTier != modelrouter.Main {
		t.Fatalf("tier after another change=%+v, %v", got, err)
	}
	if _, err := store.Update(ctx, scope, primary.ID, UpdateInput{ModelTier: &mainTier}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a tier for the primary: error = %v", err)
	}
	listed, err := store.List(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Kind != KindPrimary {
		t.Fatalf("listed=%+v", listed)
	}
	handles, err := store.OrganizationAgents(ctx, scope.OrganizationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 2 || handles[0].ID != primary.ID || handles[1].ID != specialist.ID || handles[1].Name != name {
		t.Fatalf("organization handles=%+v", handles)
	}
	disabled := StatusDisabled
	if _, err := store.Update(ctx, scope, specialist.ID, UpdateInput{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	handles, err = store.OrganizationAgents(ctx, scope.OrganizationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(handles) != 1 || handles[0].ID != primary.ID {
		t.Fatalf("handles with disabled specialist=%+v", handles)
	}
	if err := store.Delete(ctx, scope, primary.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delete primary error = %v", err)
	}
	if err := store.Delete(ctx, scope, specialist.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, scope, specialist.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted specialist error = %v", err)
	}
	var lifecycleEvents int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox WHERE detail->>'user_id'=$1`, scope.UserID).Scan(&lifecycleEvents); err != nil {
		t.Fatal(err)
	}
	if lifecycleEvents != 9 {
		t.Fatalf("lifecycle events=%d, want 9", lifecycleEvents)
	}
	if err := store.Forget(ctx, scope.OrganizationID, scope.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, scope, primary.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgotten primary error=%v", err)
	}
}

// TestPrimaryNamesArePlainAndDistinct gives two members of one organization
// their primary agents: each is a friendly name as it is, and never the same
// as the other's.
func TestPrimaryNamesArePlainAndDistinct(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-managed-agents-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	organization := fmt.Sprintf("org_managed_names_%d", time.Now().UnixNano())
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organization)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, organization)
	}()
	first, err := store.EnsurePrimary(ctx, Scope{OrganizationID: organization, UserID: "user_names_a"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnsurePrimary(ctx, Scope{OrganizationID: organization, UserID: "user_names_b"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{first.Name, second.Name} {
		if !slices.Contains(friendlyNames, name) {
			t.Fatalf("name = %q, want a friendly name as it is", name)
		}
	}
	if first.Name == second.Name {
		t.Fatalf("both members' primary agents are %q", first.Name)
	}
}

// TestRenameSuffixedPrimaries renames primary agents an earlier build named
// "Nia-WETP": to their plain name, or numbered where an agent in the
// organization has it, with the copies of the name and an event; and it
// leaves every other name, and the agents it renamed, alone when run again.
func TestRenameSuffixedPrimaries(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-managed-agents-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	organization := fmt.Sprintf("org_managed_rename_%d", time.Now().UnixNano())
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->'data'->>'organization_id'=$1 OR detail->>'organization_id'=$1`, organization)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, organization)
	}()
	insert := func(id, owner, kind, name string) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, `INSERT INTO agents_primary.member_agents (id, organization_id, owner_user_id, kind, name)
			VALUES ($1,$2,$3,$4,$5)`, id, organization, owner, kind, name); err != nil {
			t.Fatal(err)
		}
	}
	// Someone already named a specialist Nia, so the two suffixed Nias are
	// numbered; the Cleo is free and goes plain; a primary its member
	// renamed, and a specialist that only looks suffixed, stay.
	insert(organization+"_spec", "user_a", "specialist", "Nia")
	insert(organization+"_a", "user_a", "primary", "Nia-WETP")
	insert(organization+"_b", "user_b", "primary", "Nia--W_P")
	insert(organization+"_c", "user_c", "primary", "Cleo-AB12")
	insert(organization+"_d", "user_d", "primary", "Morgan")
	insert(organization+"_e", "user_d", "specialist", "Kai-TEAM")

	if _, err := store.RenameSuffixedPrimaries(ctx); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	revisions := map[string]int64{}
	rows, err := database.Pool.Query(ctx, `SELECT id, name, revision FROM agents_primary.member_agents WHERE organization_id=$1`, organization)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, name string
		var revision int64
		if err := rows.Scan(&id, &name, &revision); err != nil {
			t.Fatal(err)
		}
		names[id], revisions[id] = name, revision
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	nias := []string{names[organization+"_a"], names[organization+"_b"]}
	slices.Sort(nias)
	if !reflect.DeepEqual(nias, []string{"Nia 2", "Nia 3"}) {
		t.Fatalf("the suffixed Nias are %v, want Nia 2 and Nia 3", nias)
	}
	if names[organization+"_c"] != "Cleo" || revisions[organization+"_c"] != 2 {
		t.Fatalf("the suffixed Cleo is %q at revision %d, want Cleo at 2", names[organization+"_c"], revisions[organization+"_c"])
	}
	for id, want := range map[string]string{"_spec": "Nia", "_d": "Morgan", "_e": "Kai-TEAM"} {
		if names[organization+id] != want || revisions[organization+id] != 1 {
			t.Fatalf("%s is %q at revision %d, want %q untouched", id, names[organization+id], revisions[organization+id], want)
		}
	}
	var updates int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail_type=$1 AND detail::text LIKE '%'||$2||'%'`, "agents.agent.updated.v1", organization).Scan(&updates); err != nil {
		t.Fatal(err)
	}
	if updates != 3 {
		t.Fatalf("updated events = %d, want one for each of the 3 renamed agents", updates)
	}
	if again, err := store.RenameSuffixedPrimaries(ctx); err != nil || again != 0 {
		t.Fatalf("run again: renamed %d, error %v; want nothing left to rename", again, err)
	}
}
