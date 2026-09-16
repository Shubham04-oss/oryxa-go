package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

const actionColumns = `id, organization_id, connection_id, app_id, provider, action, idempotency_key, scope,
	team_id, member_id, provider_account_id, provider_account_name, requested_by, status, error, resource,
	result, started_at, ended_at`

// StartAction records that an action is about to run. The unique index on
// (connection_id, action, idempotency_key) is what makes a repeat refuse the
// insert instead of running the action twice: ON CONFLICT DO NOTHING leaves
// the existing row alone, RETURNING then answers no row, and the caller reads
// that first outcome back and gets false. Postgres only resolves the conflict
// once the first insert has committed, so the follow-up read always finds it,
// even racing the same key from two callers at once.
func (store *Store) StartAction(ctx context.Context, action domain.Action) (domain.Action, bool, error) {
	started, err := scanAction(store.pool.QueryRow(ctx, `
		INSERT INTO integrations_actions (
			id, organization_id, connection_id, app_id, provider, action, idempotency_key, scope,
			team_id, member_id, provider_account_id, provider_account_name, requested_by, status, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'running', $14)
		ON CONFLICT (connection_id, action, idempotency_key) DO NOTHING
		RETURNING `+actionColumns,
		action.ID, action.OrganizationID, action.ConnectionID, action.AppID, action.Provider, action.Name,
		action.IdempotencyKey, action.Scope, nullable(action.TeamID), nullable(action.MemberID),
		action.ProviderAccount.ID, action.ProviderAccount.Name, action.RequestedBy, action.StartedAt))
	if err == nil {
		return started, true, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Action{}, false, err
	}
	// No row came back: another call already holds this idempotency key. This
	// is the repeat, not an error, so read back what it started and answer
	// false instead of running the action again.
	existing, err := store.actionByIdempotencyKey(ctx, action.ConnectionID, action.Name, action.IdempotencyKey)
	if err != nil {
		return domain.Action{}, false, err
	}
	return existing, false, nil
}

// actionByIdempotencyKey reads the action already stored under a repeated
// (connection, action, idempotency key), for StartAction to answer with.
func (store *Store) actionByIdempotencyKey(ctx context.Context, connectionID, name, idempotencyKey string) (domain.Action, error) {
	return scanAction(store.pool.QueryRow(ctx, `
		SELECT `+actionColumns+` FROM integrations_actions
		WHERE connection_id = $1 AND action = $2 AND idempotency_key = $3`, connectionID, name, idempotencyKey))
}

// FinishAction records how an action ended and writes its event in the same
// transaction. An action that is not running any more — already finished by
// an earlier call — leaves the row untouched and answers domain.ErrConflict,
// so an action is never finished, and never publishes its event, twice.
func (store *Store) FinishAction(ctx context.Context, action domain.Action, event domain.Event) error {
	resource, err := actionResourceValue(action.Resource)
	if err != nil {
		return err
	}
	return store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE integrations_actions
			SET status = $2, error = $3, resource = $4, result = $5, ended_at = $6
			WHERE id = $1 AND status = 'running'`,
			action.ID, action.Status, action.Error, resource, jsonValue(action.Result), action.EndedAt)
		if err != nil {
			return mapWriteError("finish action", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("finish action: the action is not running: %w", domain.ErrConflict)
		}
		return insertEvents(ctx, tx, event)
	})
}

// DeleteActionsBefore removes actions that ended before a time. ended_at is
// NULL for a running action, and NULL is never less than before, so running
// actions are always kept.
func (store *Store) DeleteActionsBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := store.pool.Exec(ctx, `
		DELETE FROM integrations_actions WHERE ended_at IS NOT NULL AND ended_at < $1`, before)
	if err != nil {
		return 0, fmt.Errorf("delete old actions: %w", err)
	}
	return tag.RowsAffected(), nil
}

func scanAction(row pgx.Row) (domain.Action, error) {
	var action domain.Action
	var teamID, memberID *string
	var resourceRaw, resultRaw []byte
	err := row.Scan(&action.ID, &action.OrganizationID, &action.ConnectionID, &action.AppID, &action.Provider,
		&action.Name, &action.IdempotencyKey, &action.Scope, &teamID, &memberID, &action.ProviderAccount.ID,
		&action.ProviderAccount.Name, &action.RequestedBy, &action.Status, &action.Error, &resourceRaw, &resultRaw,
		&action.StartedAt, &action.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Action{}, domain.ErrNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			return domain.Action{}, mapWriteError("read action", err)
		}
		return domain.Action{}, fmt.Errorf("scan action: %w", err)
	}
	if teamID != nil {
		action.TeamID = *teamID
	}
	if memberID != nil {
		action.MemberID = *memberID
	}
	if action.Resource, err = actionResourceFrom(resourceRaw); err != nil {
		return domain.Action{}, err
	}
	action.Result = json.RawMessage(resultRaw)
	return action, nil
}

func actionResourceValue(resource *domain.ActionResource) ([]byte, error) {
	if resource == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(resource)
	if err != nil {
		return nil, fmt.Errorf("encode action resource: %w", err)
	}
	return encoded, nil
}

func actionResourceFrom(raw []byte) (*domain.ActionResource, error) {
	if raw == nil {
		return nil, nil
	}
	var resource domain.ActionResource
	if err := json.Unmarshal(raw, &resource); err != nil {
		return nil, fmt.Errorf("decode action resource: %w", err)
	}
	return &resource, nil
}

// jsonValue is a json.RawMessage ready to bind to a jsonb column, or nil when
// there is nothing to store.
func jsonValue(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}
