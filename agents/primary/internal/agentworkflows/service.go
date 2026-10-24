package agentworkflows

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	// AbandonExecution fails, in one step, an execution still queued or on
	// an expired lease, and reports whether it did.
	AbandonExecution(context.Context, Scope, string, string) (Execution, bool, error)
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
	// touched records what was done to the external schedule, so a failed
	// row update undoes exactly that and nothing else.
	var touched scheduleChange
	if !schedulesEqual(current.Schedule, prospective.Schedule) || current.Status != prospective.Status {
		if service.scheduler == nil && (current.Schedule != nil || prospective.Schedule != nil) {
			return Workflow{}, fmt.Errorf("%w: EventBridge Scheduler is not configured", ErrUnavailable)
		}
		if prospective.Schedule == nil {
			if current.Schedule != nil {
				if err := service.scheduler.Delete(ctx, current); err != nil {
					return Workflow{}, fmt.Errorf("delete workflow schedule: %w", err)
				}
				touched = scheduleDeleted
			}
		} else {
			if err := service.scheduler.Upsert(ctx, prospective); err != nil {
				return Workflow{}, fmt.Errorf("update workflow schedule: %w", err)
			}
			touched = scheduleUpserted
		}
	}
	updated, err := service.repository.Update(ctx, scope, id, input)
	if err != nil {
		service.restoreSchedule(context.WithoutCancel(ctx), touched, current, prospective)
		return Workflow{}, err
	}
	return updated, nil
}

// scheduleChange is what Update did to a workflow's external schedule.
type scheduleChange int

const (
	scheduleUntouched scheduleChange = iota
	scheduleDeleted
	scheduleUpserted
)

// restoreSchedule is Update's best-effort compensation when the row update
// fails after the external schedule changed: it puts back the schedule as
// it was, and leaves it alone when nothing was changed.
func (service *Service) restoreSchedule(ctx context.Context, touched scheduleChange, current, prospective Workflow) {
	var err error
	switch {
	case touched == scheduleUntouched:
		return
	case current.Schedule == nil:
		// The update created a schedule the workflow never had.
		err = service.scheduler.Delete(ctx, prospective)
	default:
		// The update deleted or replaced the workflow's schedule.
		err = service.scheduler.Upsert(ctx, current)
	}
	if err != nil {
		slog.WarnContext(ctx, "restore a workflow schedule after a failed update", "workflow_id", current.ID, "error", err)
	}
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
	executionID := strings.TrimSpace(dispatch.ExecutionID)
	workflow, err := service.Get(ctx, scope, strings.TrimSpace(dispatch.WorkflowID))
	if errors.Is(err, ErrNotFound) {
		return service.abandon(ctx, scope, executionID, err, "the workflow was deleted before this execution ran")
	}
	if err != nil {
		return Execution{}, err
	}
	if workflow.Status != StatusActive {
		return service.abandon(ctx, scope, executionID, ErrDisabled, "the workflow was disabled before this execution ran")
	}
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
	running, claimed, err := service.repository.ClaimExecution(ctx, scope, executionID, strings.TrimSpace(dispatch.ExternalID), service.lease())
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
	status, errorText := executionOutcome(delegation.Results)
	return service.repository.FinishExecution(ctx, scope, executionID, status, encoded, errorText)
}

// executionOutcome folds the assignments' results into the execution's
// status. Any assignment that did not complete (failed, and also blocked,
// needs_input, or a status this does not know) fails the execution, even
// when others await confirmation: needs_confirmation is reported only when
// every other assignment completed, so a waiting confirmation never hides a
// failure. A failed execution's error names the assignments still awaiting
// confirmation, and the stored result keeps every assignment's own status.
func executionOutcome(results []primaryagents.SystemDelegationResult) (ExecutionStatus, string) {
	var failed, confirming []string
	for _, result := range results {
		switch result.Status {
		case "completed":
		case "needs_confirmation":
			confirming = append(confirming, result.AgentID)
		default:
			failed = append(failed, result.AgentID)
		}
	}
	switch {
	case len(failed) > 0:
		text := fmt.Sprintf("%d of %d workflow assignments did not complete (%s)", len(failed), len(results), strings.Join(failed, ", "))
		if len(confirming) > 0 {
			text += fmt.Sprintf("; %d await confirmation (%s)", len(confirming), strings.Join(confirming, ", "))
		}
		return ExecutionFailed, text
	case len(confirming) > 0:
		return ExecutionNeedsConfirmation, ""
	default:
		return ExecutionSucceeded, ""
	}
}

// abandon settles an execution whose workflow can no longer run: disabled,
// or deleted, after Start (or a schedule) queued it. Left alone it would
// stay queued for good. It fails the execution in one step, without
// claiming it, so work that never ran records no attempt, start time or
// started event, and one another delivery holds on a live lease is left to
// it. It returns cause so the caller (the Step Functions worker) reports the
// matching failure code. A scheduled dispatch that never created a row has
// nothing to settle.
func (service *Service) abandon(ctx context.Context, scope Scope, executionID string, cause error, reason string) (Execution, error) {
	if executionID == "" {
		return Execution{}, cause
	}
	current, settled, err := service.repository.AbandonExecution(context.WithoutCancel(ctx), scope, executionID, reason)
	if errors.Is(err, ErrNotFound) {
		// A deleted workflow takes its executions with it.
		return Execution{}, cause
	}
	if err != nil {
		return Execution{}, errors.Join(cause, err)
	}
	if !settled {
		if current.Status == ExecutionRunning {
			return current, ErrInProgress
		}
		// Already settled, by an earlier delivery: report what it was.
		return current, nil
	}
	return current, cause
}

// lease is how long one delivery holds an execution it claimed.
func (service *Service) lease() time.Duration {
	if service.executionLease <= 0 {
		return 15 * time.Minute
	}
	return service.executionLease
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
