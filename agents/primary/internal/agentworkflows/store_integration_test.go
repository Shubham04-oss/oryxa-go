package agentworkflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestStoreLifecycle needs the shared local PostgreSQL:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/oryxa?sslmode=disable' go test ./agents/primary/internal/agentworkflows
func TestStoreLifecycle(t *testing.T) {
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
	scope := Scope{OrganizationID: fmt.Sprintf("org_workflow_test_%d", suffix), UserID: fmt.Sprintf("user_workflow_test_%d", suffix)}
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

	workflow, err := store.Create(ctx, scope, Workflow{ID: "awf_" + fmt.Sprint(suffix), Name: "Daily review", TeamID: team.ID, Status: StatusActive, Assignments: []Assignment{{AgentID: sales.ID, Task: "Review pipeline"}}, Schedule: &Schedule{Expression: "rate(1 day)", Timezone: "UTC", State: ScheduleEnabled}})
	if err != nil {
		t.Fatal(err)
	}
	if workflow.Revision != 1 || workflow.Schedule == nil || workflow.Schedule.Expression != "rate(1 day)" {
		t.Fatalf("workflow=%+v", workflow)
	}
	if _, err := store.Create(ctx, scope, Workflow{ID: "other", Name: "daily REVIEW", TeamID: team.ID, Status: StatusActive, Assignments: workflow.Assignments}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name error=%v", err)
	}

	execution, err := store.CreateExecution(ctx, scope, Execution{ID: "awx_" + fmt.Sprint(suffix), WorkflowID: workflow.ID, StartedBy: StartedByAPI, Status: ExecutionQueued})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimExecution(ctx, scope, execution.ID, "arn:execution:1", time.Minute)
	if err != nil || !ok || claimed.Status != ExecutionRunning {
		t.Fatalf("claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	if _, ok, err := store.ClaimExecution(ctx, scope, execution.ID, "arn:execution:1", time.Minute); err != nil || ok {
		t.Fatalf("second claim ok=%v err=%v", ok, err)
	}
	result := json.RawMessage(`{"team_id":"` + team.ID + `","results":[]}`)
	finished, err := store.FinishExecution(ctx, scope, execution.ID, ExecutionSucceeded, result, "")
	if err != nil || finished.Status != ExecutionSucceeded || !json.Valid(finished.Result) {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	listed, err := store.ListExecutions(ctx, scope, workflow.ID, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != execution.ID {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	renamed := "Daily pipeline review"
	if _, err := store.Update(ctx, scope, workflow.ID, UpdateInput{Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, scope, workflow.ID); err != nil {
		t.Fatal(err)
	}
	var lifecycleEvents, sensitiveEvents int
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail->>'organization_id'=$1 AND detail ? 'workflow_id'`, scope.OrganizationID).Scan(&lifecycleEvents); err != nil {
		t.Fatal(err)
	}
	if lifecycleEvents != 6 {
		t.Fatalf("workflow lifecycle events=%d, want 6", lifecycleEvents)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail->>'organization_id'=$1 AND detail ? 'workflow_id'
			AND (detail::text ILIKE '%Review pipeline%' OR detail::text ILIKE '%results%')`, scope.OrganizationID).Scan(&sensitiveEvents); err != nil {
		t.Fatal(err)
	}
	if sensitiveEvents != 0 {
		t.Fatalf("workflow outbox leaked assignments or results in %d events", sensitiveEvents)
	}
}
