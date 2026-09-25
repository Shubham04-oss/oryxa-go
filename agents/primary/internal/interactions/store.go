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
	// A request that names no approval is decided by its agent, as every
	// request made before teamspaces was — but only outside a teamspace. One
	// made in a teamspace must say who approves it, and never gets the
	// permissive default by leaving it out. The column keeps its 'agent'
	// default for an older build's inserts, which never name a teamspace.
	if request.Approval == "" {
		if request.TeamspaceID != "" {
			return agents.Interaction{}, fmt.Errorf("%w: a request made in a teamspace must say who approves it", ErrInvalid)
		}
		request.Approval = ApprovalAgent
	}
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
			// The key is taken: return what it names, which the service
			// compares with what it was asked to create.
			return scanInteraction(tx.QueryRow(ctx, byKeyStatement,
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

const byKeyStatement = `SELECT ` + selectColumns + ` FROM agents_primary.agent_interactions
	WHERE organization_id=$1 AND requester_agent_id=$2 AND idempotency_key=$3`

// FindByKey is the request one organization's requesting agent stored under
// an idempotency key, found through the key's own unique index.
func (store *Store) FindByKey(ctx context.Context, organizationID, requesterAgentID, key string) (agents.Interaction, error) {
	var request agents.Interaction
	err := scanInteraction(store.database.Pool.QueryRow(ctx, byKeyStatement, organizationID, requesterAgentID, key), &request)
	if errors.Is(err, pgx.ErrNoRows) {
		return agents.Interaction{}, ErrNotFound
	}
	if err != nil {
		return agents.Interaction{}, fmt.Errorf("find agent request by idempotency key: %w", err)
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
	statement, args, err := listStatement(organizationID, userID, query)
	if err != nil {
		return nil, err
	}
	rows, err := store.database.Pool.Query(ctx, statement, args...)
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

// listStatement is the query List runs and its arguments. Only placeholder
// numbers are formatted into it; every value is an argument.
//
// Each side of a request — the member as its target, or as its requester —
// is read on its own, in order, from an index that leads with that side's
// column, and stops at the limit: the inbox from agent_interactions_inbox_idx,
// the sent requests from agent_interactions_sent_idx, and, inside a teamspace
// with the other member named (a side conversation's poll), either direction
// from agent_interactions_teamspace_pair_idx, with nothing left to filter or
// sort. Both sides together are the two reads merged, still in order, to the
// limit: an OR across the two columns could use neither index for its order,
// and read and sorted every request in the organization (or the teamspace)
// instead. A request is never on both sides of itself (its requester and
// target are different members), so UNION ALL repeats none.
func listStatement(organizationID, userID string, query Query) (string, []any, error) {
	args := []any{organizationID, userID}
	with := ""
	if query.WithUserID != "" {
		args = append(args, query.WithUserID)
		with = fmt.Sprintf("$%d", len(args))
	}
	teamspace := ""
	if query.TeamspaceID != "" {
		// Restating the partial indexes' own predicate is what lets a generic
		// plan — the one a cached statement settles on after a few runs, as a
		// side conversation's polling does — use them: the planner cannot
		// prove teamspace_id=$n excludes '' for every $n.
		args = append(args, query.TeamspaceID)
		teamspace = fmt.Sprintf(` AND teamspace_id <> '' AND teamspace_id=$%d`, len(args))
	}
	args = append(args, clamp(query.Limit))
	limit := fmt.Sprintf("$%d", len(args))
	side := func(column, otherColumn string) string {
		where := `organization_id=$1 AND ` + column + `=$2`
		if with != "" {
			where += ` AND ` + otherColumn + `=` + with
		}
		return `SELECT ` + selectColumns + ` FROM agents_primary.agent_interactions
			WHERE ` + where + teamspace + ` ORDER BY updated_at DESC, id DESC LIMIT ` + limit
	}
	asTarget, asRequester := side("target_user_id", "requester_user_id"), side("requester_user_id", "target_user_id")
	switch query.Box {
	case BoxInbox:
		return asTarget, args, nil
	case BoxSent:
		return asRequester, args, nil
	case BoxAll:
		return `SELECT ` + selectColumns + ` FROM ((` + asTarget + `) UNION ALL (` + asRequester + `)) AS listed
			ORDER BY updated_at DESC, id DESC LIMIT ` + limit, args, nil
	default:
		return "", nil, fmt.Errorf("%w: unknown listing %q", ErrInvalid, query.Box)
	}
}

func (store *Store) Decide(ctx context.Context, id string, input agents.InteractionDecisionInput, decider agents.InteractionDecider, at time.Time) (agents.Interaction, error) {
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
			SET status=$2, decision=$3, revision=revision+1, updated_at=$4,
				decided_by_kind=$5, decided_by_user_id=$6, decided_by_agent_id=$7
			WHERE id=$1 RETURNING `+selectColumns,
			id, status, decisionJSON, at, decider.Kind, decider.UserID, decider.AgentID), &result)
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

// Hold keeps a request for its owner's own decision. A pending request is
// marked held, which nobody is waiting on, so no event is recorded. An
// approved one has its approval, and who made it, withdrawn and is pending
// again, held: that is recorded as the request's new requested event, which
// the waker completes without waking anyone, as it does any held request.
func (store *Store) Hold(ctx context.Context, id string, at time.Time) (agents.Interaction, error) {
	var result agents.Interaction
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if !current.ExpiresAt.After(at) {
			return ErrExpired
		}
		switch current.Status {
		case StatusPending:
			if current.Approval == ApprovalOwner {
				result = current
				return nil
			}
			return scanInteraction(tx.QueryRow(ctx, `UPDATE agents_primary.agent_interactions
				SET approval='owner', revision=revision+1, updated_at=$2 WHERE id=$1 RETURNING `+selectColumns,
				id, at), &result)
		case StatusApproved:
			err := scanInteraction(tx.QueryRow(ctx, `UPDATE agents_primary.agent_interactions
				SET status='pending', approval='owner', decision=NULL,
					decided_by_kind='', decided_by_user_id='', decided_by_agent_id='',
					revision=revision+1, updated_at=$2
				WHERE id=$1 RETURNING `+selectColumns, id, at), &result)
			if err != nil {
				return err
			}
			return recordEvent(ctx, tx, agentsv1.InteractionRequestedDetailType, result)
		default:
			return ErrConflict
		}
	})
	if err != nil {
		return agents.Interaction{}, err
	}
	return result, nil
}

// Close ends, as Oryxa and saying why, a request made in a teamspace that no
// longer stands there. A pending request is rejected, with the reason as its
// decision's message and Oryxa as its decider; an approved one fails with
// the reason as its result, before its work begins. Either is recorded as
// the event that tells the member who asked, exactly as a rejection or a
// failed result from the target would.
func (store *Store) Close(ctx context.Context, id, reason string, at time.Time) (agents.Interaction, error) {
	var result agents.Interaction
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		current, err := getForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}
		if !current.ExpiresAt.After(at) {
			return ErrExpired
		}
		switch current.Status {
		case StatusPending:
			decisionJSON, err := json.Marshal(agents.InteractionDecision{
				Decision: DecisionRejected, Message: reason, AllowedTools: []string{}, GrantedPermissions: []string{}, DecidedAt: at,
			})
			if err != nil {
				return err
			}
			err = scanInteraction(tx.QueryRow(ctx, `UPDATE agents_primary.agent_interactions
				SET status='rejected', decision=$2, decided_by_kind='system', decided_by_user_id='', decided_by_agent_id='',
					revision=revision+1, updated_at=$3
				WHERE id=$1 RETURNING `+selectColumns, id, decisionJSON, at), &result)
			if err != nil {
				return err
			}
			return recordEvent(ctx, tx, agentsv1.InteractionDecidedDetailType, result)
		case StatusApproved:
			resultJSON, err := json.Marshal(agents.InteractionResult{
				Status: StatusFailed, Summary: reason, ToolsUsed: []string{}, Artifacts: []string{}, CompletedAt: at,
			})
			if err != nil {
				return err
			}
			err = scanInteraction(tx.QueryRow(ctx, `UPDATE agents_primary.agent_interactions
				SET status='failed', result=$2, revision=revision+1, updated_at=$3
				WHERE id=$1 RETURNING `+selectColumns, id, resultJSON, at), &result)
			if err != nil {
				return err
			}
			return recordEvent(ctx, tx, agentsv1.InteractionCompletedDetailType, result)
		default:
			return ErrConflict
		}
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
	teamspace_id, approval, decided_by_kind, decided_by_user_id, decided_by_agent_id`

type scanner interface{ Scan(...any) error }

func scanInteraction(row scanner, target *agents.Interaction) error {
	var contextJSON, toolsJSON, permissionsJSON []byte
	var decisionJSON, resultJSON []byte
	var decider agents.InteractionDecider
	err := row.Scan(
		&target.ID, &target.OrganizationID,
		&target.Requester.UserID, &target.Requester.AgentID, &target.Requester.AgentName,
		&target.Target.UserID, &target.Target.AgentID, &target.Target.AgentName, &target.Target.UserEmail, &target.Target.UserDisplayName,
		&target.Purpose, &target.Task, &contextJSON, &toolsJSON, &permissionsJSON,
		&target.Status, &decisionJSON, &resultJSON, &target.Revision, &target.ExpiresAt, &target.CreatedAt, &target.UpdatedAt,
		&target.TeamspaceID, &target.Approval, &decider.Kind, &decider.UserID, &decider.AgentID,
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
		// Who decided is kept in columns of its own, which the database
		// checks, never in the decision's JSON.
		target.Decision.DecidedBy = nil
		if decider.Kind != "" {
			target.Decision.DecidedBy = &decider
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
