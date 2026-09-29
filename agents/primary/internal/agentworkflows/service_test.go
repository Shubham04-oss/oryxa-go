package agentworkflows

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

type fakeRepository struct {
	workflows  map[string]Workflow
	executions map[string]Execution
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{workflows: map[string]Workflow{}, executions: map[string]Execution{}}
}

func (repo *fakeRepository) Create(_ context.Context, scope Scope, workflow Workflow) (Workflow, error) {
	workflow.OrganizationID, workflow.OwnerUserID = scope.OrganizationID, scope.UserID
	workflow.CreatedAt, workflow.UpdatedAt = time.Unix(1, 0).UTC(), time.Unix(1, 0).UTC()
	repo.workflows[workflow.ID] = workflow
	return workflow, nil
}
func (repo *fakeRepository) List(_ context.Context, scope Scope) ([]Workflow, error) {
	result := []Workflow{}
	for _, workflow := range repo.workflows {
		if workflow.OrganizationID == scope.OrganizationID && workflow.OwnerUserID == scope.UserID {
			result = append(result, workflow)
		}
	}
	return result, nil
}
func (repo *fakeRepository) Get(_ context.Context, scope Scope, id string) (Workflow, error) {
	workflow, ok := repo.workflows[id]
	if !ok || workflow.OrganizationID != scope.OrganizationID || workflow.OwnerUserID != scope.UserID {
		return Workflow{}, ErrNotFound
	}
	return workflow, nil
}
func (repo *fakeRepository) Update(_ context.Context, scope Scope, id string, input UpdateInput) (Workflow, error) {
	workflow, err := repo.Get(context.Background(), scope, id)
	if err != nil {
		return Workflow{}, err
	}
	if input.Name != nil {
		workflow.Name = *input.Name
	}
	if input.Description != nil {
		workflow.Description = *input.Description
	}
	if input.TeamID != nil {
		workflow.TeamID = *input.TeamID
	}
	if input.Status != nil {
		workflow.Status = *input.Status
	}
	if input.Assignments != nil {
		workflow.Assignments = *input.Assignments
	}
	if input.Schedule != nil {
		workflow.Schedule = input.Schedule
	}
	if input.ClearSchedule {
		workflow.Schedule = nil
	}
	workflow.Revision++
	repo.workflows[id] = workflow
	return workflow, nil
}
func (repo *fakeRepository) Delete(_ context.Context, scope Scope, id string) error {
	if _, err := repo.Get(context.Background(), scope, id); err != nil {
		return err
	}
	delete(repo.workflows, id)
	return nil
}
func (repo *fakeRepository) CreateExecution(_ context.Context, scope Scope, execution Execution) (Execution, error) {
	for _, existing := range repo.executions {
		if execution.ExternalID != "" && existing.ExternalID == execution.ExternalID {
			return existing, nil
		}
	}
	execution.OrganizationID, execution.OwnerUserID = scope.OrganizationID, scope.UserID
	execution.CreatedAt = time.Unix(2, 0).UTC()
	repo.executions[execution.ID] = execution
	return execution, nil
}
func (repo *fakeRepository) GetExecution(_ context.Context, scope Scope, id string) (Execution, error) {
	execution, ok := repo.executions[id]
	if !ok || execution.OrganizationID != scope.OrganizationID || execution.OwnerUserID != scope.UserID {
		return Execution{}, ErrNotFound
	}
	return execution, nil
}
func (repo *fakeRepository) ListExecutions(_ context.Context, scope Scope, workflowID string, _ int) ([]Execution, error) {
	result := []Execution{}
	for _, execution := range repo.executions {
		if execution.OrganizationID == scope.OrganizationID && execution.OwnerUserID == scope.UserID && execution.WorkflowID == workflowID {
			result = append(result, execution)
		}
	}
	return result, nil
}
func (repo *fakeRepository) MarkExecutionQueued(_ context.Context, scope Scope, id, externalID string) (Execution, error) {
	execution, err := repo.GetExecution(context.Background(), scope, id)
	if err != nil {
		return Execution{}, err
	}
	execution.Status, execution.ExternalID = ExecutionQueued, externalID
	repo.executions[id] = execution
	return execution, nil
}
func (repo *fakeRepository) ClaimExecution(_ context.Context, scope Scope, id, externalID string, _ time.Duration) (Execution, bool, error) {
	execution, err := repo.GetExecution(context.Background(), scope, id)
	if err != nil {
		return Execution{}, false, err
	}
	if execution.Status != ExecutionQueued {
		return execution, false, nil
	}
	execution.Status, execution.ExternalID = ExecutionRunning, externalID
	execution.StartedAt = time.Unix(3, 0).UTC()
	repo.executions[id] = execution
	return execution, true, nil
}
func (repo *fakeRepository) FinishExecution(_ context.Context, scope Scope, id string, status ExecutionStatus, result json.RawMessage, errorText string) (Execution, error) {
	execution, err := repo.GetExecution(context.Background(), scope, id)
	if err != nil {
		return Execution{}, err
	}
	execution.Status, execution.Result, execution.Error = status, result, errorText
	execution.CompletedAt = time.Unix(4, 0).UTC()
	repo.executions[id] = execution
	return execution, nil
}

type fakeTeams struct{ team agentteams.Team }

func (teams fakeTeams) Get(context.Context, agentteams.Scope, string) (agentteams.Team, error) {
	return teams.team, nil
}

type fakeAgents struct{ primary managedagents.Definition }

func (agents fakeAgents) Primary(context.Context, managedagents.Scope) (managedagents.Definition, error) {
	return agents.primary, nil
}

type fakeDelegator struct {
	result primaryagents.SystemDelegation
	origin string
	input  primaryagents.SystemTeamDelegateInput
}

func (delegator *fakeDelegator) Delegate(ctx context.Context, _ primaryagents.AgentSystemActor, input primaryagents.SystemTeamDelegateInput) (primaryagents.SystemDelegation, error) {
	delegator.origin, delegator.input = memberauth.RunOrigin(ctx), input
	return delegator.result, nil
}

type fakeOrchestrator struct{ request StartRequest }

func (orchestrator *fakeOrchestrator) Start(_ context.Context, request StartRequest) (string, error) {
	orchestrator.request = request
	return "arn:aws:states:region:account:execution:workflows:" + request.ExecutionID, nil
}

type fakeScheduler struct {
	upserts []Workflow
	deletes []Workflow
}

func (scheduler *fakeScheduler) Upsert(_ context.Context, workflow Workflow) error {
	scheduler.upserts = append(scheduler.upserts, workflow)
	return nil
}
func (scheduler *fakeScheduler) Delete(_ context.Context, workflow Workflow) error {
	scheduler.deletes = append(scheduler.deletes, workflow)
	return nil
}

func workflowHarness(t *testing.T) (*Service, *fakeRepository, *fakeOrchestrator, *fakeScheduler, *fakeDelegator) {
	t.Helper()
	repo, orchestrator, schedules := newFakeRepository(), &fakeOrchestrator{}, &fakeScheduler{}
	delegator := &fakeDelegator{result: primaryagents.SystemDelegation{TeamID: "atm_1", Results: []primaryagents.SystemDelegationResult{{AgentID: "agt_sales", Status: "completed", Output: `{}`}}}}
	service, err := New(Config{
		Repository: repo,
		Teams:      fakeTeams{team: agentteams.Team{ID: "atm_1", Status: agentteams.StatusActive, Members: []agentteams.Member{{AgentID: "agt_sales", Status: managedagents.StatusActive}}}},
		Agents:     fakeAgents{primary: managedagents.Definition{ID: "agt_primary", Name: "Carl", Kind: managedagents.KindPrimary, Status: managedagents.StatusActive}},
		Delegator:  delegator, Orchestrator: orchestrator, Scheduler: schedules, ExecutionLease: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, repo, orchestrator, schedules, delegator
}

func TestWorkflowCreateScheduleAndManualStart(t *testing.T) {
	service, _, orchestrator, schedules, _ := workflowHarness(t)
	scope := Scope{OrganizationID: "org_1", UserID: "user_1"}
	created, err := service.Create(context.Background(), scope, CreateInput{
		Name: " Weekly   pipeline ", TeamID: "atm_1",
		Assignments: []Assignment{{AgentID: " agt_sales ", Task: " Review pipeline "}},
		Schedule:    &Schedule{Expression: "cron(0 9 ? * MON *)", Timezone: "Asia/Kolkata"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Weekly pipeline" || created.Schedule.State != ScheduleEnabled || len(schedules.upserts) != 1 {
		t.Fatalf("created=%+v schedules=%+v", created, schedules.upserts)
	}
	execution, err := service.Start(context.Background(), scope, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Status != ExecutionQueued || execution.ExternalID == "" || orchestrator.request.WorkflowID != created.ID || orchestrator.request.OwnerUserID != "user_1" {
		t.Fatalf("execution=%+v request=%+v", execution, orchestrator.request)
	}
}

func TestAnAssignmentThatDidNotCompleteFailsTheExecution(t *testing.T) {
	for _, test := range []struct {
		statuses []string
		want     ExecutionStatus
	}{
		{[]string{"completed"}, ExecutionSucceeded},
		{[]string{"completed", "blocked"}, ExecutionFailed},
		{[]string{"needs_input"}, ExecutionFailed},
		{[]string{"failed"}, ExecutionFailed},
		{[]string{"something_new"}, ExecutionFailed},
		{[]string{"blocked", "needs_confirmation"}, ExecutionNeedsConfirmation},
	} {
		service, _, _, _, delegator := workflowHarness(t)
		var results []primaryagents.SystemDelegationResult
		for _, status := range test.statuses {
			results = append(results, primaryagents.SystemDelegationResult{AgentID: "agt_sales", Status: status, Output: `{}`})
		}
		delegator.result = primaryagents.SystemDelegation{TeamID: "atm_1", Results: results}
		scope := Scope{OrganizationID: "org_1", UserID: "user_1"}
		workflow, err := service.Create(context.Background(), scope, CreateInput{Name: "Pipeline", TeamID: "atm_1", Assignments: []Assignment{{AgentID: "agt_sales", Task: "Review pipeline"}}})
		if err != nil {
			t.Fatal(err)
		}
		execution, err := service.Execute(context.Background(), Dispatch{WorkflowID: workflow.ID, OrganizationID: "org_1", OwnerUserID: "user_1", StartedBy: StartedBySchedule, ExternalID: "arn:execution:1"})
		if err != nil {
			t.Fatal(err)
		}
		if execution.Status != test.want {
			t.Errorf("statuses %v: execution status %q, want %q", test.statuses, execution.Status, test.want)
		}
	}
}

func TestScheduledExecutionDelegatesWithoutMemberAuthority(t *testing.T) {
	service, _, _, _, delegator := workflowHarness(t)
	scope := Scope{OrganizationID: "org_1", UserID: "user_1"}
	workflow, err := service.Create(context.Background(), scope, CreateInput{Name: "Pipeline", TeamID: "atm_1", Assignments: []Assignment{{AgentID: "agt_sales", Task: "Review pipeline"}}})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := service.Execute(context.Background(), Dispatch{WorkflowID: workflow.ID, OrganizationID: "org_1", OwnerUserID: "user_1", StartedBy: StartedBySchedule, ExternalID: "arn:execution:1"})
	if err != nil {
		t.Fatal(err)
	}
	if execution.Status != ExecutionSucceeded || delegator.origin != agentsv1.StartedByWorkflow || delegator.input.TeamID != "atm_1" {
		t.Fatalf("execution=%+v origin=%q input=%+v", execution, delegator.origin, delegator.input)
	}
}

func TestScheduledWritesRemainConfirmationBound(t *testing.T) {
	service, _, _, _, delegator := workflowHarness(t)
	delegator.result.Results[0].Status = "needs_confirmation"
	delegator.result.Results[0].Confirmations = []string{"call_1"}
	scope := Scope{OrganizationID: "org_1", UserID: "user_1"}
	workflow, _ := service.Create(context.Background(), scope, CreateInput{Name: "Pipeline", TeamID: "atm_1", Assignments: []Assignment{{AgentID: "agt_sales", Task: "Send follow-ups"}}})
	execution, err := service.Execute(context.Background(), Dispatch{WorkflowID: workflow.ID, OrganizationID: "org_1", OwnerUserID: "user_1", ExternalID: "arn:execution:2"})
	if err != nil {
		t.Fatal(err)
	}
	if execution.Status != ExecutionNeedsConfirmation || !json.Valid(execution.Result) {
		t.Fatalf("execution=%+v", execution)
	}
}

func TestWorkflowValidationKeepsSchedulesAndAssignmentsBounded(t *testing.T) {
	service, _, _, _, _ := workflowHarness(t)
	scope := Scope{OrganizationID: "org_1", UserID: "user_1"}
	for name, input := range map[string]CreateInput{
		"outside team": {Name: "Bad", TeamID: "atm_1", Assignments: []Assignment{{AgentID: "agt_legal", Task: "Review"}}},
		"bad schedule": {Name: "Bad", TeamID: "atm_1", Assignments: []Assignment{{AgentID: "agt_sales", Task: "Review"}}, Schedule: &Schedule{Expression: "every day"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Create(context.Background(), scope, input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
