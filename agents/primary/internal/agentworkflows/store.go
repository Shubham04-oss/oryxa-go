package agentworkflows

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationsAdvisoryLock int64 = 7263510708

type Store struct{ database *postgreskit.Database }

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agent-workflows", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

func (store *Store) Create(ctx context.Context, scope Scope, workflow Workflow) (Workflow, error) {
	assignments, err := json.Marshal(workflow.Assignments)
	if err != nil {
		return Workflow{}, err
	}
	now := time.Now().UTC()
	workflow.OrganizationID, workflow.OwnerUserID = scope.OrganizationID, scope.UserID
	workflow.Revision, workflow.CreatedAt, workflow.UpdatedAt = 1, now, now
	expression, timezone, state := scheduleColumns(workflow.Schedule)
	var result Workflow
	err = pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if err := scanWorkflow(tx.QueryRow(ctx, `
			INSERT INTO agents_primary.member_agent_workflows
				(id, organization_id, owner_user_id, name, description, team_id, status, assignments,
				 schedule_expression, schedule_timezone, schedule_state, revision, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
			RETURNING `+workflowColumns,
			workflow.ID, scope.OrganizationID, scope.UserID, workflow.Name, workflow.Description,
			workflow.TeamID, workflow.Status, assignments, expression, timezone, state, workflow.Revision, now), &result); err != nil {
			return err
		}
		return recordWorkflowLifecycle(ctx, tx, agentsv1.AgentWorkflowCreatedDetailType, result)
	})
	if isWorkflowNameConflict(err) {
		return Workflow{}, ErrConflict
	}
	if err != nil {
		return Workflow{}, fmt.Errorf("create agent workflow: %w", err)
	}
	return result, nil
}

func (store *Store) List(ctx context.Context, scope Scope) ([]Workflow, error) {
	rows, err := store.database.Pool.Query(ctx, `SELECT `+workflowColumns+`
		FROM agents_primary.member_agent_workflows
		WHERE organization_id=$1 AND owner_user_id=$2 ORDER BY lower(name), id`, scope.OrganizationID, scope.UserID)
	if err != nil {
		return nil, fmt.Errorf("list agent workflows: %w", err)
	}
	defer rows.Close()
	result := []Workflow{}
	for rows.Next() {
		var workflow Workflow
		if err := scanWorkflow(rows, &workflow); err != nil {
			return nil, fmt.Errorf("scan agent workflow: %w", err)
		}
		result = append(result, workflow)
	}
	return result, rows.Err()
}

func (store *Store) Get(ctx context.Context, scope Scope, id string) (Workflow, error) {
	var workflow Workflow
	err := scanWorkflow(store.database.Pool.QueryRow(ctx, `SELECT `+workflowColumns+`
		FROM agents_primary.member_agent_workflows
		WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3`, scope.OrganizationID, scope.UserID, id), &workflow)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workflow{}, ErrNotFound
	}
	if err != nil {
		return Workflow{}, fmt.Errorf("get agent workflow: %w", err)
	}
	return workflow, nil
}

func (store *Store) Update(ctx context.Context, scope Scope, id string, input UpdateInput) (Workflow, error) {
	var result Workflow
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getWorkflowForUpdate(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if input.Name != nil {
			current.Name = *input.Name
		}
		if input.Description != nil {
			current.Description = *input.Description
		}
		if input.TeamID != nil {
			current.TeamID = *input.TeamID
		}
		if input.Status != nil {
			current.Status = *input.Status
		}
		if input.Assignments != nil {
			current.Assignments = *input.Assignments
		}
		if input.Schedule != nil {
			current.Schedule = input.Schedule
		}
		if input.ClearSchedule {
			current.Schedule = nil
		}
		assignments, err := json.Marshal(current.Assignments)
		if err != nil {
			return err
		}
		expression, timezone, state := scheduleColumns(current.Schedule)
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		if err := scanWorkflow(tx.QueryRow(ctx, `
			UPDATE agents_primary.member_agent_workflows
			SET name=$4, description=$5, team_id=$6, status=$7, assignments=$8,
				schedule_expression=$9, schedule_timezone=$10, schedule_state=$11,
				revision=$12, updated_at=$13
			WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3
			RETURNING `+workflowColumns,
			scope.OrganizationID, scope.UserID, id, current.Name, current.Description, current.TeamID,
			current.Status, assignments, expression, timezone, state, current.Revision, current.UpdatedAt), &result); err != nil {
			return err
		}
		return recordWorkflowLifecycle(ctx, tx, agentsv1.AgentWorkflowUpdatedDetailType, result)
	})
	if isWorkflowNameConflict(err) {
		return Workflow{}, ErrConflict
	}
	if err != nil {
		return Workflow{}, fmt.Errorf("update agent workflow: %w", err)
	}
	return result, nil
}

func (store *Store) Delete(ctx context.Context, scope Scope, id string) error {
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getWorkflowForUpdate(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.member_agent_workflows
			WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3`, scope.OrganizationID, scope.UserID, id); err != nil {
			return fmt.Errorf("delete agent workflow: %w", err)
		}
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		return recordWorkflowLifecycle(ctx, tx, agentsv1.AgentWorkflowDeletedDetailType, current)
	})
}

func (store *Store) Forget(ctx context.Context, organizationID, userID string) error {
	query := `DELETE FROM agents_primary.member_agent_workflows WHERE organization_id=$1`
	args := []any{organizationID}
	if userID != "" {
		query += ` AND owner_user_id=$2`
		args = append(args, userID)
	}
	_, err := store.database.Pool.Exec(ctx, query, args...)
	return err
}

func (store *Store) OrganizationWorkflows(ctx context.Context, organizationID, userID string) ([]Workflow, error) {
	query := `SELECT ` + workflowColumns + ` FROM agents_primary.member_agent_workflows WHERE organization_id=$1`
	args := []any{organizationID}
	if userID != "" {
		query += ` AND owner_user_id=$2`
		args = append(args, userID)
	}
	query += ` ORDER BY owner_user_id, id`
	rows, err := store.database.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list organization workflows: %w", err)
	}
	defer rows.Close()
	result := []Workflow{}
	for rows.Next() {
		var workflow Workflow
		if err := scanWorkflow(rows, &workflow); err != nil {
			return nil, err
		}
		result = append(result, workflow)
	}
	return result, rows.Err()
}

func (store *Store) CreateExecution(ctx context.Context, scope Scope, execution Execution) (Execution, error) {
	now := time.Now().UTC()
	execution.OrganizationID, execution.OwnerUserID, execution.CreatedAt = scope.OrganizationID, scope.UserID, now
	var result Execution
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if err := scanExecution(tx.QueryRow(ctx, `
			INSERT INTO agents_primary.member_agent_workflow_executions
				(id, workflow_id, organization_id, owner_user_id, started_by, status, external_id, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			RETURNING `+executionColumns,
			execution.ID, execution.WorkflowID, scope.OrganizationID, scope.UserID,
			execution.StartedBy, execution.Status, execution.ExternalID, now), &result); err != nil {
			return err
		}
		return recordExecutionLifecycle(ctx, tx, agentsv1.AgentWorkflowExecutionQueuedDetailType, result, now)
	})
	if err == nil {
		return result, nil
	}
	if execution.ExternalID != "" && isExternalExecutionConflict(err) {
		err = scanExecution(store.database.Pool.QueryRow(ctx, `SELECT `+executionColumns+`
			FROM agents_primary.member_agent_workflow_executions
			WHERE organization_id=$1 AND owner_user_id=$2 AND external_id=$3`, scope.OrganizationID, scope.UserID, execution.ExternalID), &result)
		return result, err
	}
	return Execution{}, fmt.Errorf("create workflow execution: %w", err)
}

func (store *Store) GetExecution(ctx context.Context, scope Scope, id string) (Execution, error) {
	var execution Execution
	err := scanExecution(store.database.Pool.QueryRow(ctx, `SELECT `+executionColumns+`
		FROM agents_primary.member_agent_workflow_executions
		WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3`, scope.OrganizationID, scope.UserID, id), &execution)
	if errors.Is(err, pgx.ErrNoRows) {
		return Execution{}, ErrNotFound
	}
	if err != nil {
		return Execution{}, fmt.Errorf("get workflow execution: %w", err)
	}
	return execution, nil
}

func (store *Store) ListExecutions(ctx context.Context, scope Scope, workflowID string, limit int) ([]Execution, error) {
	rows, err := store.database.Pool.Query(ctx, `SELECT `+executionColumns+`
		FROM agents_primary.member_agent_workflow_executions
		WHERE organization_id=$1 AND owner_user_id=$2 AND workflow_id=$3
		ORDER BY created_at DESC, id DESC LIMIT $4`, scope.OrganizationID, scope.UserID, workflowID, limit)
	if err != nil {
		return nil, fmt.Errorf("list workflow executions: %w", err)
	}
	defer rows.Close()
	result := []Execution{}
	for rows.Next() {
		var execution Execution
		if err := scanExecution(rows, &execution); err != nil {
			return nil, err
		}
		result = append(result, execution)
	}
	return result, rows.Err()
}

func (store *Store) MarkExecutionQueued(ctx context.Context, scope Scope, id, externalID string) (Execution, error) {
	return store.markExecution(ctx, scope, id, ExecutionQueued, externalID, false)
}

func (store *Store) markExecution(ctx context.Context, scope Scope, id string, status ExecutionStatus, externalID string, started bool) (Execution, error) {
	var execution Execution
	var startedAt any
	if started {
		startedAt = time.Now().UTC()
	}
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		previous, err := getExecutionForUpdate(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if previous.Status != ExecutionQueued && previous.Status != ExecutionRunning {
			execution = previous
			return nil
		}
		if err := scanExecution(tx.QueryRow(ctx, `
			UPDATE agents_primary.member_agent_workflow_executions
			SET status=CASE WHEN status='queued' THEN $4 ELSE status END,
				external_id=CASE WHEN $5='' THEN external_id ELSE $5 END,
				started_at=CASE WHEN $6::timestamptz IS NULL THEN started_at ELSE COALESCE(started_at,$6) END
			WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3
			RETURNING `+executionColumns,
			scope.OrganizationID, scope.UserID, id, status, externalID, startedAt), &execution); err != nil {
			return err
		}
		if previous.Status == execution.Status {
			return nil
		}
		return recordExecutionLifecycle(ctx, tx, executionDetailType(execution.Status), execution, executionChangedAt(execution))
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GetExecution(ctx, scope, id)
	}
	if err != nil {
		return Execution{}, fmt.Errorf("mark workflow execution %s: %w", status, err)
	}
	return execution, nil
}

func (store *Store) ClaimExecution(ctx context.Context, scope Scope, id, externalID string, lease time.Duration) (Execution, bool, error) {
	var execution Execution
	now := time.Now().UTC()
	claimed := false
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if err := scanExecution(tx.QueryRow(ctx, `
			UPDATE agents_primary.member_agent_workflow_executions
			SET status='running', external_id=CASE WHEN $4='' THEN external_id ELSE $4 END,
				started_at=COALESCE(started_at,$5), lease_until=$5 + ($6 * interval '1 second'),
				attempts=attempts+1
			WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3
				AND (status='queued' OR (status='running' AND (lease_until IS NULL OR lease_until < $5)))
			RETURNING `+executionColumns,
			scope.OrganizationID, scope.UserID, id, externalID, now, lease.Seconds()), &execution); err != nil {
			return err
		}
		claimed = true
		return recordExecutionLifecycle(ctx, tx, agentsv1.AgentWorkflowExecutionStartedDetailType, execution, now)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		execution, err = store.GetExecution(ctx, scope, id)
		return execution, false, err
	}
	if err != nil {
		return Execution{}, false, fmt.Errorf("claim workflow execution: %w", err)
	}
	return execution, claimed, nil
}

func (store *Store) FinishExecution(ctx context.Context, scope Scope, id string, status ExecutionStatus, result json.RawMessage, errorText string) (Execution, error) {
	completed := time.Now().UTC()
	var payload any
	if len(result) > 0 {
		payload = string(result)
	}
	var execution Execution
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if err := scanExecution(tx.QueryRow(ctx, `
			UPDATE agents_primary.member_agent_workflow_executions
			SET status=$4, result=$5, error=$6, completed_at=$7, lease_until=NULL
			WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3
				AND status IN ('queued','running')
			RETURNING `+executionColumns,
			scope.OrganizationID, scope.UserID, id, status, payload, truncate(errorText, 2000), completed), &execution); err != nil {
			return err
		}
		return recordExecutionLifecycle(ctx, tx, executionDetailType(status), execution, completed)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GetExecution(ctx, scope, id)
	}
	if err != nil {
		return Execution{}, fmt.Errorf("finish workflow execution: %w", err)
	}
	return execution, nil
}

const workflowColumns = `id, organization_id, owner_user_id, name, description, team_id, status,
	assignments, schedule_expression, schedule_timezone, schedule_state, revision, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scanWorkflow(row scanner, workflow *Workflow) error {
	var assignments []byte
	var expression, timezone, state *string
	if err := row.Scan(&workflow.ID, &workflow.OrganizationID, &workflow.OwnerUserID, &workflow.Name,
		&workflow.Description, &workflow.TeamID, &workflow.Status, &assignments,
		&expression, &timezone, &state, &workflow.Revision, &workflow.CreatedAt, &workflow.UpdatedAt); err != nil {
		return err
	}
	if err := json.Unmarshal(assignments, &workflow.Assignments); err != nil {
		return fmt.Errorf("decode workflow assignments: %w", err)
	}
	if expression != nil {
		workflow.Schedule = &Schedule{Expression: *expression, Timezone: *timezone, State: ScheduleState(*state)}
	}
	return nil
}

const executionColumns = `id, workflow_id, organization_id, owner_user_id, started_by, status,
	external_id, result, error, created_at, started_at, completed_at`

func scanExecution(row scanner, execution *Execution) error {
	var result []byte
	var startedAt, completedAt *time.Time
	if err := row.Scan(&execution.ID, &execution.WorkflowID, &execution.OrganizationID, &execution.OwnerUserID,
		&execution.StartedBy, &execution.Status, &execution.ExternalID, &result, &execution.Error,
		&execution.CreatedAt, &startedAt, &completedAt); err != nil {
		return err
	}
	execution.Result = json.RawMessage(result)
	if startedAt != nil {
		execution.StartedAt = *startedAt
	}
	if completedAt != nil {
		execution.CompletedAt = *completedAt
	}
	return nil
}

func getWorkflowForUpdate(ctx context.Context, tx pgx.Tx, scope Scope, id string) (Workflow, error) {
	var workflow Workflow
	err := scanWorkflow(tx.QueryRow(ctx, `SELECT `+workflowColumns+`
		FROM agents_primary.member_agent_workflows
		WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3 FOR UPDATE`,
		scope.OrganizationID, scope.UserID, id), &workflow)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workflow{}, ErrNotFound
	}
	return workflow, err
}

func getExecutionForUpdate(ctx context.Context, tx pgx.Tx, scope Scope, id string) (Execution, error) {
	var execution Execution
	err := scanExecution(tx.QueryRow(ctx, `SELECT `+executionColumns+`
		FROM agents_primary.member_agent_workflow_executions
		WHERE organization_id=$1 AND owner_user_id=$2 AND id=$3 FOR UPDATE`,
		scope.OrganizationID, scope.UserID, id), &execution)
	if errors.Is(err, pgx.ErrNoRows) {
		return Execution{}, ErrNotFound
	}
	return execution, err
}

func recordWorkflowLifecycle(ctx context.Context, tx pgx.Tx, detailType string, workflow Workflow) error {
	scheduleEnabled := workflow.Schedule != nil && workflow.Schedule.State == ScheduleEnabled
	event, err := events.NewEvent(detailType, agentsv1.AgentWorkflowChanged{
		WorkflowID: workflow.ID, UserID: workflow.OwnerUserID, OrganizationID: workflow.OrganizationID,
		TeamID: workflow.TeamID, Name: workflow.Name, Status: string(workflow.Status),
		ScheduleEnabled: scheduleEnabled, Revision: workflow.Revision, ChangedAt: workflow.UpdatedAt,
	}, workflow.UpdatedAt)
	if err != nil {
		return err
	}
	return insertLifecycle(ctx, tx, event)
}

func recordExecutionLifecycle(ctx context.Context, tx pgx.Tx, detailType string, execution Execution, changedAt time.Time) error {
	event, err := events.NewEvent(detailType, agentsv1.AgentWorkflowExecutionChanged{
		ExecutionID: execution.ID, WorkflowID: execution.WorkflowID,
		UserID: execution.OwnerUserID, OrganizationID: execution.OrganizationID,
		StartedBy: execution.StartedBy, Status: string(execution.Status), ChangedAt: changedAt,
	}, changedAt)
	if err != nil {
		return err
	}
	return insertLifecycle(ctx, tx, event)
}

func insertLifecycle(ctx context.Context, tx pgx.Tx, event events.Event) error {
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	_, err := tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace)
	if err != nil {
		return fmt.Errorf("record agent workflow event: %w", err)
	}
	return nil
}

func executionDetailType(status ExecutionStatus) string {
	switch status {
	case ExecutionQueued:
		return agentsv1.AgentWorkflowExecutionQueuedDetailType
	case ExecutionRunning:
		return agentsv1.AgentWorkflowExecutionStartedDetailType
	case ExecutionFailed:
		return agentsv1.AgentWorkflowExecutionFailedDetailType
	default:
		return agentsv1.AgentWorkflowExecutionCompletedDetailType
	}
}

func executionChangedAt(execution Execution) time.Time {
	if !execution.CompletedAt.IsZero() {
		return execution.CompletedAt
	}
	if !execution.StartedAt.IsZero() {
		return execution.StartedAt
	}
	return execution.CreatedAt
}

func scheduleColumns(schedule *Schedule) (any, any, any) {
	if schedule == nil {
		return nil, nil, nil
	}
	return schedule.Expression, schedule.Timezone, schedule.State
}

func isWorkflowNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "member_agent_workflows_owner_name_idx"
}

func isExternalExecutionConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "member_agent_workflow_executions_external_idx"
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
