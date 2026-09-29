package agentworkflows

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

const (
	maxNameRunes        = 80
	maxDescriptionRunes = 1000
	maxAssignments      = 8
	maxTaskRunes        = 10_000
	maxContextRunes     = 10_000
	maxExpressionRunes  = 256
)

type Repository interface {
	Create(context.Context, Scope, Workflow) (Workflow, error)
	List(context.Context, Scope) ([]Workflow, error)
	Get(context.Context, Scope, string) (Workflow, error)
	Update(context.Context, Scope, string, UpdateInput) (Workflow, error)
	Delete(context.Context, Scope, string) error
	CreateExecution(context.Context, Scope, Execution) (Execution, error)
	GetExecution(context.Context, Scope, string) (Execution, error)
	ListExecutions(context.Context, Scope, string, int) ([]Execution, error)
	MarkExecutionQueued(context.Context, Scope, string, string) (Execution, error)
	ClaimExecution(context.Context, Scope, string, string, time.Duration) (Execution, bool, error)
	FinishExecution(context.Context, Scope, string, ExecutionStatus, json.RawMessage, string) (Execution, error)
}

type TeamRegistry interface {
	Get(context.Context, agentteams.Scope, string) (agentteams.Team, error)
}

type PrimaryRegistry interface {
	Primary(context.Context, managedagents.Scope) (managedagents.Definition, error)
}

type Delegator interface {
	Delegate(context.Context, primaryagents.AgentSystemActor, primaryagents.SystemTeamDelegateInput) (primaryagents.SystemDelegation, error)
}

type Orchestrator interface {
	Start(context.Context, StartRequest) (string, error)
}

type Scheduler interface {
	Upsert(context.Context, Workflow) error
	Delete(context.Context, Workflow) error
}

type Config struct {
	Repository     Repository
	Teams          TeamRegistry
	Agents         PrimaryRegistry
	Delegator      Delegator
	Orchestrator   Orchestrator
	Scheduler      Scheduler
	ExecutionLease time.Duration
}

type Service struct {
	repository     Repository
	teams          TeamRegistry
	agents         PrimaryRegistry
	delegator      Delegator
	orchestrator   Orchestrator
	scheduler      Scheduler
	executionLease time.Duration
}

type forgettingRepository interface {
	OrganizationWorkflows(context.Context, string, string) ([]Workflow, error)
	Forget(context.Context, string, string) error
}

func New(config Config) (*Service, error) {
	if config.Repository == nil || config.Teams == nil {
		return nil, errors.New("agent workflows: a repository and team registry are required")
	}
	return &Service{
		repository: config.Repository, teams: config.Teams, agents: config.Agents,
		delegator: config.Delegator, orchestrator: config.Orchestrator, scheduler: config.Scheduler,
		executionLease: config.ExecutionLease,
	}, nil
}

func (service *Service) Create(ctx context.Context, scope Scope, input CreateInput) (Workflow, error) {
	workflow := Workflow{
		Name: cleanLine(input.Name), Description: strings.TrimSpace(input.Description),
		TeamID: strings.TrimSpace(input.TeamID), Assignments: cleanAssignments(input.Assignments),
		Schedule: cleanSchedule(input.Schedule), Status: StatusActive, Revision: 1,
	}
	if err := service.validate(ctx, scope, workflow); err != nil {
		return Workflow{}, err
	}
	id, err := newID("awf")
	if err != nil {
		return Workflow{}, err
	}
	workflow.ID = id
	created, err := service.repository.Create(ctx, scope, workflow)
	if err != nil {
		return Workflow{}, err
	}
	if created.Schedule != nil {
		if service.scheduler == nil {
			_ = service.repository.Delete(context.WithoutCancel(ctx), scope, created.ID)
			return Workflow{}, fmt.Errorf("%w: EventBridge Scheduler is not configured", ErrUnavailable)
		}
		if err := service.scheduler.Upsert(ctx, created); err != nil {
			_ = service.repository.Delete(context.WithoutCancel(ctx), scope, created.ID)
			_ = service.scheduler.Delete(context.WithoutCancel(ctx), created)
			return Workflow{}, fmt.Errorf("create workflow schedule: %w", err)
		}
	}
	return created, nil
}

func (service *Service) List(ctx context.Context, scope Scope) ([]Workflow, error) {
	return service.repository.List(ctx, scope)
}

func (service *Service) Get(ctx context.Context, scope Scope, id string) (Workflow, error) {
	if strings.TrimSpace(id) == "" {
		return Workflow{}, ErrNotFound
	}
	return service.repository.Get(ctx, scope, id)
}

func (service *Service) Update(ctx context.Context, scope Scope, id string, input UpdateInput) (Workflow, error) {
	if input.Name == nil && input.Description == nil && input.TeamID == nil && input.Status == nil && input.Assignments == nil && input.Schedule == nil && !input.ClearSchedule {
		return Workflow{}, fmt.Errorf("%w: send at least one field to change", ErrInvalid)
	}
	if input.Schedule != nil && input.ClearSchedule {
		return Workflow{}, fmt.Errorf("%w: schedule and clear_schedule cannot be sent together", ErrInvalid)
	}
	current, err := service.Get(ctx, scope, id)
	if err != nil {
		return Workflow{}, err
	}
	prospective := current
	if input.Name != nil {
		value := cleanLine(*input.Name)
		input.Name, prospective.Name = &value, value
	}
	if input.Description != nil {
		value := strings.TrimSpace(*input.Description)
		input.Description, prospective.Description = &value, value
	}
	if input.TeamID != nil {
		value := strings.TrimSpace(*input.TeamID)
		input.TeamID, prospective.TeamID = &value, value
	}
	if input.Status != nil {
		prospective.Status = *input.Status
	}
	if input.Assignments != nil {
		value := cleanAssignments(*input.Assignments)
		input.Assignments, prospective.Assignments = &value, value
	}
	if input.Schedule != nil {
		value := cleanSchedule(input.Schedule)
		input.Schedule, prospective.Schedule = value, value
	}
	if input.ClearSchedule {
		prospective.Schedule = nil
	}
	if err := service.validate(ctx, scope, prospective); err != nil {
		return Workflow{}, err
	}
	if !schedulesEqual(current.Schedule, prospective.Schedule) || current.Status != prospective.Status {
		if service.scheduler == nil && (current.Schedule != nil || prospective.Schedule != nil) {
			return Workflow{}, fmt.Errorf("%w: EventBridge Scheduler is not configured", ErrUnavailable)
		}
		if prospective.Schedule == nil {
			if current.Schedule != nil {
				if err := service.scheduler.Delete(ctx, current); err != nil {
					return Workflow{}, fmt.Errorf("delete workflow schedule: %w", err)
				}
			}
		} else if err := service.scheduler.Upsert(ctx, prospective); err != nil {
			return Workflow{}, fmt.Errorf("update workflow schedule: %w", err)
		}
	}
	updated, err := service.repository.Update(ctx, scope, id, input)
	if err != nil {
		// Restore the previous external schedule as a best-effort compensation.
		if service.scheduler != nil {
			if current.Schedule == nil {
				_ = service.scheduler.Delete(context.WithoutCancel(ctx), prospective)
			} else {
				_ = service.scheduler.Upsert(context.WithoutCancel(ctx), current)
			}
		}
		return Workflow{}, err
	}
	return updated, nil
}

func (service *Service) Delete(ctx context.Context, scope Scope, id string) error {
	current, err := service.Get(ctx, scope, id)
	if err != nil {
		return err
	}
	if current.Schedule != nil {
		if service.scheduler == nil {
			return fmt.Errorf("%w: EventBridge Scheduler is not configured", ErrUnavailable)
		}
		if err := service.scheduler.Delete(ctx, current); err != nil {
			return fmt.Errorf("delete workflow schedule: %w", err)
		}
	}
	return service.repository.Delete(ctx, scope, id)
}

func (service *Service) Start(ctx context.Context, scope Scope, workflowID string) (Execution, error) {
	workflow, err := service.Get(ctx, scope, workflowID)
	if err != nil {
		return Execution{}, err
	}
	if workflow.Status != StatusActive {
		return Execution{}, ErrDisabled
	}
	if service.orchestrator == nil {
		return Execution{}, ErrUnavailable
	}
	id, err := newID("awx")
	if err != nil {
		return Execution{}, err
	}
	execution, err := service.repository.CreateExecution(ctx, scope, Execution{
		ID: id, WorkflowID: workflow.ID, StartedBy: StartedByAPI, Status: ExecutionQueued,
	})
	if err != nil {
		return Execution{}, err
	}
	externalID, err := service.orchestrator.Start(ctx, StartRequest{
		ExecutionID: execution.ID, WorkflowID: workflow.ID, OrganizationID: scope.OrganizationID,
		OwnerUserID: scope.UserID, StartedBy: StartedByAPI,
	})
	if err != nil {
		failed, finishErr := service.repository.FinishExecution(context.WithoutCancel(ctx), scope, execution.ID, ExecutionFailed, nil, err.Error())
		return failed, errors.Join(fmt.Errorf("start Step Functions execution: %w", err), finishErr)
	}
	return service.repository.MarkExecutionQueued(ctx, scope, execution.ID, externalID)
}

func (service *Service) ListExecutions(ctx context.Context, scope Scope, workflowID string, limit int) ([]Execution, error) {
	if _, err := service.Get(ctx, scope, workflowID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	return service.repository.ListExecutions(ctx, scope, workflowID, limit)
}

func (service *Service) GetExecution(ctx context.Context, scope Scope, id string) (Execution, error) {
	return service.repository.GetExecution(ctx, scope, strings.TrimSpace(id))
}

// Forget removes a departed member's or deleted organization's schedules
// before deleting their workflow rows. It is deliberately all-or-nothing from
// the consumer's perspective: a scheduler outage makes the auth event retry
// rather than leaving an orphan schedule invoking deleted work forever.
func (service *Service) Forget(ctx context.Context, organizationID, userID string) error {
	repository, ok := service.repository.(forgettingRepository)
	if !ok {
		return errors.New("agent workflow repository cannot forget organization state")
	}
	workflows, err := repository.OrganizationWorkflows(ctx, organizationID, userID)
	if err != nil {
		return err
	}
	for _, workflow := range workflows {
		if workflow.Schedule == nil {
			continue
		}
		if service.scheduler == nil {
			return fmt.Errorf("%w: cannot remove workflow schedule", ErrUnavailable)
		}
		if err := service.scheduler.Delete(ctx, workflow); err != nil {
			return fmt.Errorf("delete workflow schedule while forgetting member: %w", err)
		}
	}
	return repository.Forget(ctx, organizationID, userID)
}

// Execute handles one callback task delivered by Step Functions. It creates a
// ledger row for scheduled executions, then delegates every stored assignment
// under the workflow origin. Writes still stop at the ordinary confirmation
// boundary because no member token or confirmation answer is manufactured.
func (service *Service) Execute(ctx context.Context, dispatch Dispatch) (Execution, error) {
	if service.agents == nil || service.delegator == nil {
		return Execution{}, ErrUnavailable
	}
	scope := Scope{OrganizationID: strings.TrimSpace(dispatch.OrganizationID), UserID: strings.TrimSpace(dispatch.OwnerUserID)}
	workflow, err := service.Get(ctx, scope, strings.TrimSpace(dispatch.WorkflowID))
	if err != nil {
		return Execution{}, err
	}
	if workflow.Status != StatusActive {
		return Execution{}, ErrDisabled
	}
	executionID := strings.TrimSpace(dispatch.ExecutionID)
	if executionID == "" {
		executionID, err = newID("awx")
		if err != nil {
			return Execution{}, err
		}
		created, createErr := service.repository.CreateExecution(ctx, scope, Execution{
			ID: executionID, WorkflowID: workflow.ID, StartedBy: StartedBySchedule,
			Status: ExecutionQueued, ExternalID: strings.TrimSpace(dispatch.ExternalID),
		})
		if createErr != nil {
			return Execution{}, createErr
		}
		executionID = created.ID
	}
	lease := service.executionLease
	if lease <= 0 {
		lease = 15 * time.Minute
	}
	running, claimed, err := service.repository.ClaimExecution(ctx, scope, executionID, strings.TrimSpace(dispatch.ExternalID), lease)
	if err != nil {
		return Execution{}, err
	}
	if running.Status == ExecutionSucceeded || running.Status == ExecutionNeedsConfirmation || running.Status == ExecutionFailed {
		return running, nil
	}
	if !claimed {
		return running, ErrInProgress
	}
	primary, err := service.agents.Primary(ctx, scope)
	if err != nil {
		return service.fail(ctx, scope, executionID, err)
	}
	actor := primaryagents.AgentSystemActor{
		OrganizationID: scope.OrganizationID, UserID: scope.UserID,
		AgentID: primary.ID, AgentName: primary.Name, AgentKind: string(primary.Kind),
	}
	delegationCtx := memberauth.WithRunOrigin(ctx, agentsv1.StartedByWorkflow)
	delegation, err := service.delegator.Delegate(delegationCtx, actor, primaryagents.SystemTeamDelegateInput{
		TeamID: workflow.TeamID, Assignments: workflow.Assignments,
	})
	if err != nil {
		return service.fail(ctx, scope, executionID, err)
	}
	encoded, err := json.Marshal(delegation)
	if err != nil {
		return service.fail(ctx, scope, executionID, err)
	}
	status := ExecutionSucceeded
	for _, result := range delegation.Results {
		if result.Status == "needs_confirmation" {
			status = ExecutionNeedsConfirmation
			break
		}
		// Anything but completed is work that was not done: failed, and also
		// blocked, needs_input, or a status this does not know.
		if result.Status != "completed" {
			status = ExecutionFailed
		}
	}
	errorText := ""
	if status == ExecutionFailed {
		errorText = "one or more workflow assignments did not complete"
	}
	return service.repository.FinishExecution(ctx, scope, executionID, status, encoded, errorText)
}

func (service *Service) fail(ctx context.Context, scope Scope, id string, cause error) (Execution, error) {
	failed, err := service.repository.FinishExecution(context.WithoutCancel(ctx), scope, id, ExecutionFailed, nil, cause.Error())
	return failed, errors.Join(cause, err)
}

func (service *Service) validate(ctx context.Context, scope Scope, workflow Workflow) error {
	switch {
	case strings.TrimSpace(scope.OrganizationID) == "" || strings.TrimSpace(scope.UserID) == "":
		return fmt.Errorf("%w: organization and user are required", ErrInvalid)
	case workflow.Name == "":
		return fmt.Errorf("%w: name is required", ErrInvalid)
	case utf8.RuneCountInString(workflow.Name) > maxNameRunes:
		return fmt.Errorf("%w: name holds at most %d characters", ErrInvalid, maxNameRunes)
	case utf8.RuneCountInString(workflow.Description) > maxDescriptionRunes:
		return fmt.Errorf("%w: description holds at most %d characters", ErrInvalid, maxDescriptionRunes)
	case workflow.Status != StatusActive && workflow.Status != StatusDisabled:
		return fmt.Errorf("%w: status must be active or disabled", ErrInvalid)
	case workflow.TeamID == "":
		return fmt.Errorf("%w: team_id is required", ErrInvalid)
	case len(workflow.Assignments) == 0 || len(workflow.Assignments) > maxAssignments:
		return fmt.Errorf("%w: workflow needs between 1 and %d assignments", ErrInvalid, maxAssignments)
	}
	team, err := service.teams.Get(ctx, scope, workflow.TeamID)
	if err != nil {
		return err
	}
	members := make(map[string]bool, len(team.Members))
	for _, member := range team.Members {
		members[member.AgentID] = true
	}
	seen := map[string]bool{}
	for _, assignment := range workflow.Assignments {
		switch {
		case assignment.AgentID == "":
			return fmt.Errorf("%w: every assignment needs an agent_id", ErrInvalid)
		case !members[assignment.AgentID]:
			return fmt.Errorf("%w: agent %q is not in team %q", ErrInvalid, assignment.AgentID, workflow.TeamID)
		case seen[assignment.AgentID]:
			return fmt.Errorf("%w: agent %q is assigned twice", ErrInvalid, assignment.AgentID)
		case assignment.Task == "":
			return fmt.Errorf("%w: every assignment needs a task", ErrInvalid)
		case utf8.RuneCountInString(assignment.Task) > maxTaskRunes:
			return fmt.Errorf("%w: an assignment task holds at most %d characters", ErrInvalid, maxTaskRunes)
		case utf8.RuneCountInString(assignment.Context) > maxContextRunes:
			return fmt.Errorf("%w: assignment context holds at most %d characters", ErrInvalid, maxContextRunes)
		}
		seen[assignment.AgentID] = true
	}
	return validateSchedule(workflow.Schedule)
}

func validateSchedule(schedule *Schedule) error {
	if schedule == nil {
		return nil
	}
	switch {
	case schedule.State != ScheduleEnabled && schedule.State != ScheduleDisabled:
		return fmt.Errorf("%w: schedule state must be enabled or disabled", ErrInvalid)
	case utf8.RuneCountInString(schedule.Expression) > maxExpressionRunes:
		return fmt.Errorf("%w: schedule expression is too long", ErrInvalid)
	case !(strings.HasPrefix(schedule.Expression, "rate(") || strings.HasPrefix(schedule.Expression, "cron(") || strings.HasPrefix(schedule.Expression, "at(")) || !strings.HasSuffix(schedule.Expression, ")"):
		return fmt.Errorf("%w: schedule expression must use rate(...), cron(...) or at(...)", ErrInvalid)
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return fmt.Errorf("%w: schedule timezone is not an IANA timezone", ErrInvalid)
	}
	return nil
}

func cleanAssignments(assignments []Assignment) []Assignment {
	result := make([]Assignment, 0, len(assignments))
	for _, assignment := range assignments {
		result = append(result, Assignment{
			AgentID: strings.TrimSpace(assignment.AgentID), Task: strings.TrimSpace(assignment.Task), Context: strings.TrimSpace(assignment.Context),
		})
	}
	return result
}

func cleanSchedule(schedule *Schedule) *Schedule {
	if schedule == nil {
		return nil
	}
	result := *schedule
	result.Expression = strings.TrimSpace(result.Expression)
	result.Timezone = strings.TrimSpace(result.Timezone)
	if result.Timezone == "" {
		result.Timezone = "UTC"
	}
	if result.State == "" {
		result.State = ScheduleEnabled
	}
	return &result
}

func schedulesEqual(left, right *Schedule) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cleanLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func newID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create %s id: %w", prefix, err)
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(value), nil
}
