package teamspaces

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
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/teamspaces
func TestStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-teamspaces-test", Schema: "public"})
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
	organizationID := fmt.Sprintf("org_teamspace_test_%d", suffix)
	owner := managedagents.Scope{OrganizationID: organizationID, UserID: fmt.Sprintf("user_owner_%d", suffix)}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.teamspaces WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	}()

	specialist, err := agentStore.Create(ctx, owner, managedagents.Definition{Name: "Reviewer", Description: "Reviews architecture", Instructions: "Return bounded findings."})
	if err != nil {
		t.Fatal(err)
	}
	organizationAgent, err := store.EnsureOrganizationAgent(ctx, organizationID, "Integration Org", owner.UserID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.EnsureOrganizationAgent(ctx, organizationID, "A different label", owner.UserID)
	if err != nil || again.ID != organizationAgent.ID {
		t.Fatalf("singleton organization agent=%+v again=%+v error=%v", organizationAgent, again, err)
	}
	space, err := store.Create(ctx, Space{
		ID: fmt.Sprintf("tsp_%d", suffix), OrganizationID: organizationID, TeamID: fmt.Sprintf("team_%d", suffix),
		Name: "Engineering Space", CreatedByUserID: owner.UserID,
		OrganizationAgentID: organizationAgent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if space.OrganizationAgent.ID != organizationAgent.ID {
		t.Fatalf("space organization agent=%+v", space.OrganizationAgent)
	}
	space, err = store.AddContribution(ctx, Actor{OrganizationID: organizationID, UserID: owner.UserID}, space.ID, ContributionInput{
		AgentID: specialist.ID, Role: "Review architecture", DelegationMode: DelegationAutomatic,
	})
	if err != nil || len(space.Contributions) != 1 || space.Revision != 2 {
		t.Fatalf("contributed=%+v error=%v", space, err)
	}
	entry, err := store.PublishEntry(ctx, Entry{
		ID: fmt.Sprintf("tse_%d", suffix), TeamspaceID: space.ID, OrganizationID: organizationID,
		AuthorUserID: owner.UserID, AuthorAgentID: specialist.ID, AuthorAgentName: specialist.Name,
		Kind: EntryResult, Title: "Architecture review", Summary: "Ready with one follow-up.",
		ToolsUsed: []string{"github.search"}, Artifacts: []EntryArtifact{{Name: "Review", URI: "https://example.test/review", MediaType: "text/markdown"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListEntries(ctx, organizationID, space.ID, 10)
	if err != nil || len(entries) != 1 || entries[0].ID != entry.ID || len(entries[0].Artifacts) != 1 {
		t.Fatalf("entries=%+v error=%v", entries, err)
	}
	if err := agentStore.Delete(ctx, owner, specialist.ID); !errors.Is(err, managedagents.ErrInUse) {
		t.Fatalf("delete contributed specialist error=%v", err)
	}

	request := DelegationRequest
	updated, err := store.UpdateContribution(ctx, Actor{OrganizationID: organizationID, UserID: owner.UserID}, space.ID, specialist.ID, ContributionUpdate{DelegationMode: &request})
	if err != nil || updated.Contributions[0].DelegationMode != DelegationRequest || updated.Revision != 3 {
		t.Fatalf("updated=%+v error=%v", updated, err)
	}
	if err := store.ForgetTeamMember(ctx, organizationID, space.TeamID, owner.UserID); err != nil {
		t.Fatal(err)
	}
	remaining, err := store.Get(ctx, organizationID, space.ID)
	if err != nil || len(remaining.Contributions) != 0 || remaining.Revision != 4 {
		t.Fatalf("member cleanup space=%+v error=%v", remaining, err)
	}
	if err := agentStore.Delete(ctx, owner, specialist.ID); err != nil {
		t.Fatalf("delete specialist after contribution withdrawal: %v", err)
	}
	if err := store.ForgetTeam(ctx, organizationID, space.TeamID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, organizationID, space.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted human team left space: %v", err)
	}
	var events int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail->>'organization_id'=$1 AND detail_type LIKE 'agents.teamspace.%'`, organizationID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 6 {
		t.Fatalf("teamspace lifecycle events=%d, want 6", events)
	}
	var organizationAgentEvents int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail->>'organization_id'=$1 AND detail_type LIKE 'agents.organization-agent.%'`, organizationID).Scan(&organizationAgentEvents); err != nil {
		t.Fatal(err)
	}
	if organizationAgentEvents != 1 {
		t.Fatalf("organization agent lifecycle events=%d, want 1", organizationAgentEvents)
	}
}
