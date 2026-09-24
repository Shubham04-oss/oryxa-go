package interactions

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationsAdvisoryLock int64 = 7263510703

type Store struct{ database *postgreskit.Database }

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agent-interactions", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

func (store *Store) Create(ctx context.Context, request agents.Interaction, idempotencyKey string) (agents.Interaction, error) {
	contextJSON, err := json.Marshal(request.Context)
	if err != nil {
		return agents.Interaction{}, err
	}
	toolsJSON, err := json.Marshal(request.RequestedTools)
	if err != nil {
		return agents.Interaction{}, err
	}
	permissionsJSON, err := json.Marshal(request.RequestedPermissions)
	if err != nil {
		return agents.Interaction{}, err
	}
	// A request that names no approval is decided by its agent, as every
	// request made before teamspaces was; the service always names one.
	if request.Approval == "" {
		request.Approval = ApprovalAgent
	}
	err = pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO agents_primary.agent_interactions
				(id, organization_id, requester_user_id, requester_agent_id, requester_agent_name,
				 target_user_id, target_agent_id, target_agent_name, target_user_email, target_user_display_name,
				 purpose, task, context, requested_tools, requested_permissions, status, idempotency_key,
				 revision, expires_at, created_at, updated_at, teamspace_id, approval)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$20,$21,$22)
			ON CONFLICT (organization_id, requester_agent_id, idempotency_key) DO NOTHING
			RETURNING `+selectColumns,
			request.ID, request.OrganizationID, request.Requester.UserID, request.Requester.AgentID, request.Requester.AgentName,
			request.Target.UserID, request.Target.AgentID, request.Target.AgentName, request.Target.UserEmail, request.Target.UserDisplayName,
			request.Purpose, request.Task, contextJSON, toolsJSON, permissionsJSON, request.Status, idempotencyKey,
			request.Revision, request.ExpiresAt, request.CreatedAt, request.TeamspaceID, request.Approval)
		if err := scanInteraction(row, &request); errors.Is(err, pgx.ErrNoRows) {
			return scanInteraction(tx.QueryRow(ctx, `SELECT `+selectColumns+` FROM agents_primary.agent_interactions
				WHERE organization_id=$1 AND requester_agent_id=$2 AND idempotency_key=$3`,
				request.OrganizationID, request.Requester.AgentID, idempotencyKey), &request)
		} else if err != nil {
			return err
		}
		return recordEvent(ctx, tx, agentsv1.InteractionRequestedDetailType, request)
	})
	if err != nil {
		return agents.Interaction{}, fmt.Errorf("create agent request: %w", err)
	}
	return request, nil
}

func (store *Store) Get(ctx context.Context, id string) (agents.Interaction, error) {
	var request agents.Interaction
	err := scanInteraction(store.database.Pool.QueryRow(ctx, `SELECT `+selectColumns+`
		FROM agents_primary.agent_interactions WHERE id=$1`, id), &request)
	if errors.Is(err, pgx.ErrNoRows) {
		return agents.Interaction{}, ErrNotFound
	}
	if err != nil {
		return agents.Interaction{}, fmt.Errorf("get agent request: %w", err)
	}
	return request, nil
}

// Inbox lists the requests addressed to one member's agents, newest first.
func (store *Store) Inbox(ctx context.Context, organizationID, userID string, limit int) ([]agents.Interaction, error) {
	return store.List(ctx, organizationID, userID, Query{Box: BoxInbox, Limit: limit})
}

// Sent lists the requests one member's agents sent, newest first.
func (store *Store) Sent(ctx context.Context, organizationID, userID string, limit int) ([]agents.Interaction, error) {
	return store.List(ctx, organizationID, userID, Query{Box: BoxSent, Limit: limit})
}

// List selects, newest first, the requests of one organization that userID
// is a party to: as the target (BoxInbox), the requester (BoxSent) or either
// (BoxAll), optionally only with one other member and only in one teamspace.
func (store *Store) List(ctx context.Context, organizationID, userID string, query Query) ([]agents.Interaction, error) {
	args := []any{organizationID, userID}
	asTarget, asRequester := `target_user_id=$2`, `requester_user_id=$2`
	if query.WithUserID != "" {
		args = append(args, query.WithUserID)
		asTarget += ` AND requester_user_id=$3`
		asRequester += ` AND target_user_id=$3`
	}
	var parties string
	switch query.Box {
	case BoxInbox:
		parties = asTarget
	case BoxSent:
		parties = asRequester
	case BoxAll:
		parties = `(` + asTarget + `) OR (` + asRequester + `)`
	default:
		return nil, fmt.Errorf("%w: unknown listing %q", ErrInvalid, query.Box)
	}
	where := `organization_id=$1 AND (` + parties + `)`
	if query.TeamspaceID != "" {
		args = append(args, query.TeamspaceID)
		where += fmt.Sprintf(` AND teamspace_id=$%d`, len(args))
	}
	args = append(args, clamp(query.Limit))
	rows, err := store.database.Pool.Query(ctx, `SELECT `+selectColumns+` FROM agents_primary.agent_interactions
		WHERE `+where+fmt.Sprintf(` ORDER BY updated_at DESC, id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list agent requests: %w", err)
	}
	defer rows.Close()
	requests := []agents.Interaction{}
	for rows.Next() {
		var request agents.Interaction
		if err := scanInteraction(rows, &request); err != nil {
			return nil, fmt.Errorf("scan agent request: %w", err)
		}
		requests = append(requests, request)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list agent requests: %w", err)
	}
	return requests, nil
}

func (store *Store) Decide(ctx context.Context, id string, input agents.InteractionDecisionInput, at time.Time) (agents.Interaction, error) {
	var result agents.Interaction
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Status != StatusPending {
			return ErrConflict
		}
		if !current.ExpiresAt.After(at) {
			return ErrExpired
		}
		decision := agents.InteractionDecision{
			Decision: input.Decision, Message: input.Message,
			AllowedTools: nonNilStrings(input.AllowedTools), GrantedPermissions: nonNilStrings(input.GrantedPermissions), DecidedAt: at,
		}
		decisionJSON, err := json.Marshal(decision)
		if err != nil {
			return err
		}
		status := StatusRejected
		if input.Decision == DecisionApproved {
			status = StatusApproved
		}
		err = scanInteraction(tx.QueryRow(ctx, `UPDATE agents_primary.agent_interactions
			SET status=$2, decision=$3, revision=revision+1, updated_at=$4 WHERE id=$1 RETURNING `+selectColumns,
			id, status, decisionJSON, at), &result)
		if err != nil {
			return err
		}
		return recordEvent(ctx, tx, agentsv1.InteractionDecidedDetailType, result)
	})
	if err != nil {
		return agents.Interaction{}, err
	}
	return result, nil
}

func (store *Store) Respond(ctx context.Context, id string, input agents.InteractionResponseInput, at time.Time) (agents.Interaction, error) {
	var result agents.Interaction
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Status != StatusApproved {
			return ErrConflict
		}
		if !current.ExpiresAt.After(at) {
			return ErrExpired
		}
		response := agents.InteractionResult{
			Status: input.Status, Summary: input.Summary, Content: input.Content,
			ToolsUsed: nonNilStrings(input.ToolsUsed), Artifacts: nonNilStrings(input.Artifacts), CompletedAt: at,
		}
		responseJSON, err := json.Marshal(response)
		if err != nil {
			return err
		}
		err = scanInteraction(tx.QueryRow(ctx, `UPDATE agents_primary.agent_interactions
			SET status=$2, result=$3, revision=revision+1, updated_at=$4 WHERE id=$1 RETURNING `+selectColumns,
			id, input.Status, responseJSON, at), &result)
		if err != nil {
			return err
		}
		return recordEvent(ctx, tx, agentsv1.InteractionCompletedDetailType, result)
	})
	if err != nil {
		return agents.Interaction{}, err
	}
	return result, nil
}

// Forget removes request content when a member leaves or an organization is
// deleted. Published routing events contain IDs and parties only, never task
// content, so no second content store survives this cleanup.
func (store *Store) Forget(ctx context.Context, organizationID, userID string) error {
	query := `DELETE FROM agents_primary.agent_interactions WHERE organization_id=$1`
	args := []any{organizationID}
	if userID != "" {
		query += ` AND (requester_user_id=$2 OR target_user_id=$2)`
		args = append(args, userID)
	}
	_, err := store.database.Pool.Exec(ctx, query, args...)
	return err
}

const selectColumns = `
	id, organization_id,
	requester_user_id, requester_agent_id, requester_agent_name,
	target_user_id, target_agent_id, target_agent_name, target_user_email, target_user_display_name,
	purpose, task, context, requested_tools, requested_permissions,
	status, decision, result, revision, expires_at, created_at, updated_at,
	teamspace_id, approval`

type scanner interface{ Scan(...any) error }

func scanInteraction(row scanner, target *agents.Interaction) error {
	var contextJSON, toolsJSON, permissionsJSON []byte
	var decisionJSON, resultJSON []byte
	err := row.Scan(
		&target.ID, &target.OrganizationID,
		&target.Requester.UserID, &target.Requester.AgentID, &target.Requester.AgentName,
		&target.Target.UserID, &target.Target.AgentID, &target.Target.AgentName, &target.Target.UserEmail, &target.Target.UserDisplayName,
		&target.Purpose, &target.Task, &contextJSON, &toolsJSON, &permissionsJSON,
		&target.Status, &decisionJSON, &resultJSON, &target.Revision, &target.ExpiresAt, &target.CreatedAt, &target.UpdatedAt,
		&target.TeamspaceID, &target.Approval,
	)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(contextJSON, &target.Context); err != nil {
		return err
	}
	if err := json.Unmarshal(toolsJSON, &target.RequestedTools); err != nil {
		return err
	}
	if err := json.Unmarshal(permissionsJSON, &target.RequestedPermissions); err != nil {
		return err
	}
	if len(decisionJSON) > 0 {
		target.Decision = &agents.InteractionDecision{}
		if err := json.Unmarshal(decisionJSON, target.Decision); err != nil {
			return err
		}
	}
	if len(resultJSON) > 0 {
		target.Result = &agents.InteractionResult{}
		if err := json.Unmarshal(resultJSON, target.Result); err != nil {
			return err
		}
	}
	return nil
}

func getForUpdate(ctx context.Context, tx pgx.Tx, id string) (agents.Interaction, error) {
	var request agents.Interaction
	err := scanInteraction(tx.QueryRow(ctx, `SELECT `+selectColumns+` FROM agents_primary.agent_interactions WHERE id=$1 FOR UPDATE`, id), &request)
	if errors.Is(err, pgx.ErrNoRows) {
		return agents.Interaction{}, ErrNotFound
	}
	return request, err
}

func recordEvent(ctx context.Context, tx pgx.Tx, detailType string, request agents.Interaction) error {
	at := request.UpdatedAt
	event, err := events.NewEvent(detailType, agentsv1.InteractionChanged{
		RequestID: request.ID, OrganizationID: request.OrganizationID,
		RequesterUserID: request.Requester.UserID, RequesterAgentID: request.Requester.AgentID,
		TargetUserID: request.Target.UserID, TargetAgentID: request.Target.AgentID,
		Status: request.Status, Revision: request.Revision, ChangedAt: at,
	}, at)
	if err != nil {
		return err
	}
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	_, err = tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace)
	return err
}

var _ Repository = (*Store)(nil)
