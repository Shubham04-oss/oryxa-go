package agentworkflows

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestStoreAbandonsOnlyWorkNobodyIsRunning needs PostgreSQL, as
// TestStoreLifecycle does. An execution still queued, or running on a lease
// that ran out, is failed in one step with only the failed event: no
// attempt, start time or started event for work that never ran. One on a
// live lease, or already settled, is returned as it is.
func TestStoreAbandonsOnlyWorkNobodyIsRunning(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agent-workflows-test", Schema: "public"})
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
	teamStore := agentteams.NewStore(database)
	if err := teamStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UnixNano()
	scope := Scope{OrganizationID: fmt.Sprintf("org_abandon_test_%d", suffix), UserID: fmt.Sprintf("user_abandon_test_%d", suffix)}
	defer func() {
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agent_workflows WHERE organization_id=$1`, scope.OrganizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agent_teams WHERE organization_id=$1`, scope.OrganizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, scope.OrganizationID)
		_, _ = database.Pool.Exec(ctx, `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, scope.OrganizationID)
	}()
	if _, err := agentStore.EnsurePrimary(ctx, scope); err != nil {
		t.Fatal(err)
	}
	sales, err := agentStore.Create(ctx, scope, managedagents.Definition{Name: "Sales", Instructions: "Review pipeline."})
	if err != nil {
		t.Fatal(err)
	}
	teamService, _ := agentteams.New(teamStore)
	team, err := teamService.Create(ctx, scope, agentteams.CreateInput{Name: "Revenue", Members: []agentteams.MemberInput{{AgentID: sales.ID, Role: "Qualify"}}})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := store.Create(ctx, scope, Workflow{ID: fmt.Sprintf("awf_abandon_%d", suffix), Name: "Daily review", TeamID: team.ID, Status: StatusActive, Assignments: []Assignment{{AgentID: sales.ID, Task: "Review pipeline"}}})
	if err != nil {
		t.Fatal(err)
	}
	execution := func(name string) Execution {
		t.Helper()
		created, err := store.CreateExecution(ctx, scope, Execution{ID: fmt.Sprintf("awx_%s_%d", name, suffix), WorkflowID: workflow.ID, StartedBy: StartedByAPI, Status: ExecutionQueued})
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	events := func(id, detailType string) int {
		t.Helper()
		var count int
		if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
			WHERE detail->>'execution_id'=$1 AND detail_type=$2`, id, detailType).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	const reason = "the workflow was disabled before this execution ran"

	// Queued: failed in one step, never started.
	queued := execution("queued")
	abandoned, settled, err := store.AbandonExecution(ctx, scope, queued.ID, reason)
	if err != nil || !settled || abandoned.Status != ExecutionFailed || abandoned.Error != reason || !abandoned.StartedAt.IsZero() || abandoned.CompletedAt.IsZero() {
		t.Fatalf("queued: abandoned=%+v settled=%v err=%v", abandoned, settled, err)
	}
	var attempts int
	var leased bool
	if err := database.Pool.QueryRow(ctx, `SELECT attempts, lease_until IS NOT NULL FROM agents_primary.member_agent_workflow_executions WHERE id=$1`, queued.ID).Scan(&attempts, &leased); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || leased {
		t.Fatalf("queued: attempts=%d leased=%v, want 0 and no lease", attempts, leased)
	}
	if started, failed := events(queued.ID, agentsv1.AgentWorkflowExecutionStartedDetailType), events(queued.ID, agentsv1.AgentWorkflowExecutionFailedDetailType); started != 0 || failed != 1 {
		t.Fatalf("queued: started events=%d failed events=%d, want 0 and 1", started, failed)
	}
	// Settled: a second abandon changes nothing.
	again, settled, err := store.AbandonExecution(ctx, scope, queued.ID, "another reason")
	if err != nil || settled || again.Status != ExecutionFailed || again.Error != reason || events(queued.ID, agentsv1.AgentWorkflowExecutionFailedDetailType) != 1 {
		t.Fatalf("settled: again=%+v settled=%v err=%v", again, settled, err)
	}

	// Running on a live lease: left to the delivery that holds it.
	live := execution("live")
	if _, ok, err := store.ClaimExecution(ctx, scope, live.ID, "", time.Hour); err != nil || !ok {
		t.Fatalf("claim live: ok=%v err=%v", ok, err)
	}
	held, settled, err := store.AbandonExecution(ctx, scope, live.ID, reason)
	if err != nil || settled || held.Status != ExecutionRunning || events(live.ID, agentsv1.AgentWorkflowExecutionFailedDetailType) != 0 {
		t.Fatalf("live lease: held=%+v settled=%v err=%v", held, settled, err)
	}

	// Running on a lease that ran out: its delivery is gone, so it fails.
	expired := execution("expired")
	if _, ok, err := store.ClaimExecution(ctx, scope, expired.ID, "", time.Hour); err != nil || !ok {
		t.Fatalf("claim expired: ok=%v err=%v", ok, err)
	}
	if _, err := database.Pool.Exec(ctx, `UPDATE agents_primary.member_agent_workflow_executions SET lease_until=now() - interval '1 minute' WHERE id=$1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	failed, settled, err := store.AbandonExecution(ctx, scope, expired.ID, reason)
	if err != nil || !settled || failed.Status != ExecutionFailed || events(expired.ID, agentsv1.AgentWorkflowExecutionFailedDetailType) != 1 {
		t.Fatalf("expired lease: failed=%+v settled=%v err=%v", failed, settled, err)
	}

	// Missing: not found.
	if _, _, err := store.AbandonExecution(ctx, scope, "awx_missing", reason); err == nil {
		t.Fatal("abandoning a missing execution reported no error")
	}
}
