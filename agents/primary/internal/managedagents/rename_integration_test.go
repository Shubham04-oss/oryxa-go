package managedagents_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestARenameReachesTheRequestsAnAgentMadeAndWasAsked: the agent's own row
// is the one source of its name, so a rename rewrites the copies requests
// keep, on both sides, and leaves another agent's alone.
func TestARenameReachesTheRequestsAnAgentMadeAndWasAsked(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-managed-agents-rename-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := runstore.New(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := managedagents.NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := interactions.NewStore(database).Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	scope := managedagents.Scope{OrganizationID: fmt.Sprintf("org_rename_%d", suffix), UserID: fmt.Sprintf("user_rename_%d", suffix)}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agent_interactions WHERE organization_id=$1`, scope.OrganizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'user_id'=$1`, scope.UserID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, scope.OrganizationID)
	}()
	specialist, err := store.Create(ctx, scope, managedagents.Definition{Name: "Sales", Instructions: "Sell."})
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id, requesterAgent, requesterName, targetAgent, targetName string) {
		t.Helper()
		if _, err := database.Pool.Exec(ctx, `INSERT INTO agents_primary.agent_interactions
			(id, organization_id, requester_user_id, requester_agent_id, requester_agent_name, target_user_id, target_agent_id, target_agent_name,
			 purpose, task, status, idempotency_key, expires_at)
			VALUES ($1,$2,$3,$4,$5,'user_other',$6,$7,'p','t','pending',$1,now()+interval '1 day')`,
			id, scope.OrganizationID, scope.UserID, requesterAgent, requesterName, targetAgent, targetName); err != nil {
			t.Fatal(err)
		}
	}
	made, asked := fmt.Sprintf("irq_made_%d", suffix), fmt.Sprintf("irq_asked_%d", suffix)
	insert(made, specialist.ID, "Sales", "agt_other", "Other")
	insert(asked, "agt_other", "Other", specialist.ID, "Sales")

	name := "Revenue"
	if _, err := store.Update(ctx, scope, specialist.ID, managedagents.UpdateInput{Name: &name}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][2]string{made: {"Revenue", "Other"}, asked: {"Other", "Revenue"}} {
		var requester, target string
		if err := database.Pool.QueryRow(ctx, `SELECT requester_agent_name, target_agent_name FROM agents_primary.agent_interactions WHERE id=$1`, id).Scan(&requester, &target); err != nil {
			t.Fatal(err)
		}
		if requester != want[0] || target != want[1] {
			t.Fatalf("%s: requester=%q target=%q, want %q and %q", id, requester, target, want[0], want[1])
		}
	}
}
