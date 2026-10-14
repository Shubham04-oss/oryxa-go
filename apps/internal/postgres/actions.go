package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

const actionColumns = `id, organization_id, member_id, connection_id, toolkit, tool, read_only, agent,
	agent_id, agent_role, idempotency_key, status, result, error, log_id, created_at, finished_at`

// StartAction records a tool run as running. An idempotency key is unique
// per member per organization: a retry under the same key finds the run
// StartAction already began instead of starting a second one, whether that
// first run is still going or has long since finished. An empty key stores
// as NULL, which the partial unique index leaves out entirely, so ordinary
// runs without a key never collide with one another.
func (store *Store) StartAction(ctx context.Context, action domain.Action) (domain.Action, bool, error) {
	var result domain.Action
	started := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		actions, err := queryActions(ctx, tx, `
			INSERT INTO apps_actions (
				id, organization_id, member_id, connection_id, toolkit, tool, read_only, agent,
				agent_id, agent_role, idempotency_key, status, created_at, broker
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $11, $12, $9, 'running', $10,
				-- The broker the run goes through is its connection's, which
				-- is what usage is counted by.
				COALESCE((SELECT broker FROM apps_connections WHERE id = $4), 'composio'))
			ON CONFLICT (organization_id, member_id, idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
			RETURNING `+actionColumns,
			action.ID, action.OrganizationID, action.MemberID, action.ConnectionID, action.Toolkit, action.Tool,
			action.ReadOnly, action.Agent, nullable(action.IdempotencyKey), action.CreatedAt, action.AgentID, action.AgentRole)
		if err != nil {
			return mapWriteError("start action", err)
		}
		if len(actions) == 1 {
			result = actions[0]
			started = true
			return nil
		}
		// The insert found a live key already recorded; answer that action
		// instead of the one just asked for.
		existing, err := queryActions(ctx, tx, `
			SELECT `+actionColumns+`
			FROM apps_actions
			WHERE organization_id = $1 AND member_id = $2 AND idempotency_key = $3`,
			action.OrganizationID, action.MemberID, action.IdempotencyKey)
		if err != nil {
			return err
		}
		if len(existing) == 0 {
			return fmt.Errorf("start action: the conflicting run vanished mid-transaction: %w", domain.ErrConflict)
		}
		result = existing[0]
		return nil
	})
	return result, started, err
}

// FinishAction records how a running action ended, and writes event when
// there is one. An action that is no longer running - one FinishAction
// already recorded, most likely - answers ErrConflict rather than silently
// overwriting what it already said happened.
func (store *Store) FinishAction(ctx context.Context, action domain.Action, event *domain.Event) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE apps_actions
			SET status = $2, result = $3, error = $4, log_id = $5, finished_at = $6
			WHERE id = $1 AND status = 'running'`,
			action.ID, string(action.Status), action.Result, action.Error, action.LogID, action.FinishedAt)
		if err != nil {
			return mapWriteError("finish action", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("finish action: %w", domain.ErrConflict)
		}
		if event != nil {
			return insertEvents(ctx, tx, *event)
		}
		return nil
	})
}

func queryActions(ctx context.Context, db querier, sql string, args ...any) ([]domain.Action, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapWriteError("query actions", err)
	}
	defer rows.Close()
	actions := []domain.Action{}
	for rows.Next() {
		action, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query actions: %w", err)
	}
	return actions, nil
}

func scanAction(rows pgx.Rows) (domain.Action, error) {
	var action domain.Action
	var idempotencyKey *string
	var status string
	// result is nullable: a running action has none yet, and pgx decodes a
	// NULL jsonb column into a nil []byte, which is exactly the nil
	// json.RawMessage that Result carries while a run is unfinished.
	var result []byte
	if err := rows.Scan(&action.ID, &action.OrganizationID, &action.MemberID, &action.ConnectionID, &action.Toolkit,
		&action.Tool, &action.ReadOnly, &action.Agent, &action.AgentID, &action.AgentRole, &idempotencyKey, &status, &result, &action.Error,
		&action.LogID, &action.CreatedAt, &action.FinishedAt); err != nil {
		return domain.Action{}, fmt.Errorf("scan action: %w", err)
	}
	action.IdempotencyKey = text(idempotencyKey)
	action.Status = domain.ActionStatus(status)
	action.Result = json.RawMessage(result)
	action.CreatedAt = action.CreatedAt.UTC()
	return action, nil
}
