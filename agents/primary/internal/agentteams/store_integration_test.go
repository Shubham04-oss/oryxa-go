package agentteams

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestStoreLifecycle needs the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/agentteams
func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agent-teams-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	agentStore := managedagents.NewStore(database)
	if err := agentStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	scope := Scope{OrganizationID: fmt.Sprintf("org_team_test_%d", suffix), UserID: fmt.Sprintf("user_team_test_%d", suffix)}
	other := Scope{OrganizationID: scope.OrganizationID, UserID: fmt.Sprintf("user_team_other_%d", suffix)}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agent_teams WHERE organization_id=$1`, scope.OrganizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, scope.OrganizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, scope.OrganizationID)
	}()

	primary, err := agentStore.EnsurePrimary(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	sales, err := agentStore.Create(ctx, scope, managedagents.Definition{Name: "Sales", Description: "Owns pipeline", Instructions: "Qualify opportunities."})
	if err != nil {
		t.Fatal(err)
	}
	engineering, err := agentStore.Create(ctx, scope, managedagents.Definition{Name: "Engineering", Description: "Answers technical questions", Instructions: "Review technical requirements."})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := agentStore.Create(ctx, other, managedagents.Definition{Name: "Legal", Instructions: "Review terms."})
	if err != nil {
		t.Fatal(err)
	}

	service, _ := New(store)
	team, err := service.Create(ctx, scope, CreateInput{Name: "Revenue", Description: "Move qualified deals", Members: []MemberInput{
		{AgentID: sales.ID, Role: "Qualify opportunities"},
		{AgentID: engineering.ID, Role: "Answer product feasibility questions"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if team.CoordinatorAgentID != primary.ID || team.CoordinatorName != primary.Name || len(team.Members) != 2 || team.Members[1].Name != engineering.Name {
		t.Fatalf("team=%+v", team)
	}
	if _, err := service.Create(ctx, scope, CreateInput{Name: "Foreign", Members: []MemberInput{{AgentID: foreign.ID, Role: "Review"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-user member error=%v", err)
	}

	disabled := StatusDisabled
	updated, err := service.Update(ctx, scope, team.ID, UpdateInput{Status: &disabled})
	if err != nil || updated.Revision != 2 || updated.Status != StatusDisabled {
		t.Fatalf("updated=%+v error=%v", updated, err)
	}
	listed, err := service.List(ctx, scope)
	if err != nil || len(listed) != 1 || listed[0].ID != team.ID {
		t.Fatalf("listed=%+v error=%v", listed, err)
	}
	if err := agentStore.Delete(ctx, scope, sales.ID); !errors.Is(err, managedagents.ErrInUse) {
		t.Fatalf("delete team member error=%v", err)
	}
	if err := service.Delete(ctx, scope, team.ID); err != nil {
		t.Fatal(err)
	}
	if err := agentStore.Delete(ctx, scope, sales.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, scope, team.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted team error=%v", err)
	}

	var lifecycleEvents int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail->>'user_id'=$1 AND detail_type LIKE 'agents.team.%'`, scope.UserID).Scan(&lifecycleEvents); err != nil {
		t.Fatal(err)
	}
	if lifecycleEvents != 3 {
		t.Fatalf("team lifecycle events=%d, want 3", lifecycleEvents)
	}
}
