package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Store keeps hooks' endpoints and records their deliveries.
var _ hooks.Store = (*Store)(nil)

// Marks in apps_handled_events. detail_type says what a mark is, and the key
// which delivery or event.
const (
	hookDeliveryMark = "apps.hook.delivery"
	hookEventMark    = "apps.hook.event"
)

const hookEndpointColumns = `id, toolkit, scope, organization_id, connection_id, scheme, sealed_secret, secret_version,
	previous_sealed_secret, previous_secret_until, secret_revealed_at, remote_id, expires_at, status, replaced_by,
	validated_at, created_at, updated_at`

// hookExecer is what both a pool and a transaction answer, so an endpoint is
// inserted the same way inside one or outside.
type hookExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

// CreateHookEndpoint stores a new endpoint. A second active deployment
// endpoint for a toolkit, or organization endpoint for an organization and
// toolkit, is domain.ErrConflict; a connection that does not exist is
// domain.ErrNotFound; an owner that does not fit the scope, or a connection
// in another organization than the one named, is domain.ErrInvalid.
func (store *Store) CreateHookEndpoint(ctx context.Context, endpoint hooks.Endpoint) error {
	return insertHookEndpoint(ctx, store.pool, endpoint)
}

func insertHookEndpoint(ctx context.Context, db hookExecer, endpoint hooks.Endpoint) error {
	// Deliveries to a connection endpoint reach only its own connection in
	// its own organization, so the pair must be the connection's own. A
	// connection never changes organization, so reading it first is enough.
	if endpoint.ConnectionID != "" {
		var organizationID string
		err := db.QueryRow(ctx, `SELECT organization_id FROM apps_connections WHERE id = $1`, endpoint.ConnectionID).Scan(&organizationID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("the hook endpoint's connection: %w", domain.ErrNotFound)
		case err != nil:
			return fmt.Errorf("read the hook endpoint's connection: %w", err)
		case organizationID != endpoint.OrganizationID:
			return fmt.Errorf("the hook endpoint's connection is in another organization: %w", domain.ErrInvalid)
		}
	}
	scheme, err := json.Marshal(endpoint.Scheme)
	if err != nil {
		return fmt.Errorf("encode a hook endpoint's scheme: %w", err)
	}
	current, err := hookSealedJSON(endpoint.Secret.Current)
	if err != nil {
		return err
	}
	previous, err := hookSealedJSON(endpoint.Secret.Previous)
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO apps_hook_endpoints (`+hookEndpointColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		endpoint.ID, endpoint.Toolkit, endpoint.Scope, endpoint.OrganizationID, nullable(endpoint.ConnectionID),
		scheme, current, endpoint.Secret.Version, previous, endpoint.Secret.PreviousUntil, endpoint.Secret.RevealedAt,
		endpoint.RemoteID, endpoint.ExpiresAt, string(endpoint.Status), nullable(endpoint.ReplacedBy),
		endpoint.ValidatedAt, endpoint.CreatedAt, endpoint.UpdatedAt,
	); err != nil {
		return mapWriteError("create hook endpoint", err)
	}
	return nil
}

// HookEndpoint answers one endpoint.
func (store *Store) HookEndpoint(ctx context.Context, id string) (hooks.Endpoint, error) {
	endpoint, err := scanHookEndpoint(store.pool.QueryRow(ctx,
		`SELECT `+hookEndpointColumns+` FROM apps_hook_endpoints WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return hooks.Endpoint{}, fmt.Errorf("hook endpoint: %w", domain.ErrNotFound)
	}
	return endpoint, err
}

// ActiveHookEndpoint answers the active organization or deployment endpoint
// for a toolkit.
func (store *Store) ActiveHookEndpoint(ctx context.Context, scope, organizationID, toolkit string) (hooks.Endpoint, error) {
	endpoint, err := scanHookEndpoint(store.pool.QueryRow(ctx, `
		SELECT `+hookEndpointColumns+`
		FROM apps_hook_endpoints
		WHERE scope = $1 AND organization_id = $2 AND toolkit = $3 AND status = 'active'
		ORDER BY created_at DESC, id
		LIMIT 1`, scope, organizationID, toolkit))
	if errors.Is(err, pgx.ErrNoRows) {
		return hooks.Endpoint{}, fmt.Errorf("hook endpoint: %w", domain.ErrNotFound)
	}
	return endpoint, err
}

// HookEndpointsForConnection answers a connection's endpoints, oldest first.
func (store *Store) HookEndpointsForConnection(ctx context.Context, connectionID string) ([]hooks.Endpoint, error) {
	return queryHookEndpoints(ctx, store.pool, `
		SELECT `+hookEndpointColumns+`
		FROM apps_hook_endpoints
		WHERE connection_id = $1
		ORDER BY created_at, id`, connectionID)
}

// SetHookEndpointRemote records the app's id for an endpoint's subscription,
// and when it lapses.
func (store *Store) SetHookEndpointRemote(ctx context.Context, id, remoteID string, expiresAt *time.Time, now time.Time) error {
	tag, err := store.pool.Exec(ctx, `
		UPDATE apps_hook_endpoints
		SET remote_id = $2, expires_at = $3, updated_at = $4
		WHERE id = $1`, id, remoteID, expiresAt, now)
	if err != nil {
		return mapWriteError("record a hook endpoint's subscription", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("hook endpoint: %w", domain.ErrNotFound)
	}
	return nil
}

// RotateHookSecret swaps in a new sealed secret, keeping the current one as
// the previous, only while the endpoint's secret is still at version: a
// rotation never overwrites a secret it did not read.
func (store *Store) RotateHookSecret(ctx context.Context, id string, version int, sealed secrets.SealedData, previousUntil, now time.Time) (bool, error) {
	encoded, err := hookSealedJSON(&sealed)
	if err != nil {
		return false, err
	}
	tag, err := store.pool.Exec(ctx, `
		UPDATE apps_hook_endpoints
		SET previous_sealed_secret = sealed_secret, previous_secret_until = $4,
		    sealed_secret = $3, secret_version = secret_version + 1, updated_at = $5
		WHERE id = $1 AND secret_version = $2 AND sealed_secret IS NOT NULL`, id, version, encoded, previousUntil, now)
	if err != nil {
		return false, mapWriteError("rotate a hook endpoint's secret", err)
	}
	return tag.RowsAffected() == 1, nil
}

// CaptureHookSecret keeps the secret an app sent, only while the endpoint
// has none, so the first one to arrive is the only one ever kept.
func (store *Store) CaptureHookSecret(ctx context.Context, id string, sealed secrets.SealedData, now time.Time) (bool, error) {
	encoded, err := hookSealedJSON(&sealed)
	if err != nil {
		return false, err
	}
	tag, err := store.pool.Exec(ctx, `
		UPDATE apps_hook_endpoints
		SET sealed_secret = $2, secret_version = secret_version + 1, updated_at = $3
		WHERE id = $1 AND sealed_secret IS NULL`, id, encoded, now)
	if err != nil {
		return false, mapWriteError("capture a hook endpoint's secret", err)
	}
	return tag.RowsAffected() == 1, nil
}

// MarkHookSecretRevealed records that a captured secret was shown. Only the
// first call does, so only one caller ever shows it.
func (store *Store) MarkHookSecretRevealed(ctx context.Context, id string, now time.Time) (bool, error) {
	tag, err := store.pool.Exec(ctx, `
		UPDATE apps_hook_endpoints
		SET secret_revealed_at = $2, updated_at = $2
		WHERE id = $1 AND sealed_secret IS NOT NULL AND secret_revealed_at IS NULL`, id, now)
	if err != nil {
		return false, mapWriteError("record a hook secret shown", err)
	}
	return tag.RowsAffected() == 1, nil
}

// MarkHookValidated records an endpoint's first handshake, for an endpoint
// made after createdAfter that has never validated.
func (store *Store) MarkHookValidated(ctx context.Context, id string, createdAfter, now time.Time) (bool, error) {
	tag, err := store.pool.Exec(ctx, `
		UPDATE apps_hook_endpoints
		SET validated_at = $3, updated_at = $3
		WHERE id = $1 AND validated_at IS NULL AND created_at > $2`, id, createdAfter, now)
	if err != nil {
		return false, mapWriteError("record a hook endpoint's handshake", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReplaceHookEndpoint stores a renewal's endpoint and marks the one it takes
// over from replaced, together. If oldID is no longer active, nothing is
// stored.
func (store *Store) ReplaceHookEndpoint(ctx context.Context, oldID string, replacement hooks.Endpoint) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		if err := insertHookEndpoint(ctx, tx, replacement); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE apps_hook_endpoints
			SET status = 'replaced', replaced_by = $2, updated_at = $3
			WHERE id = $1 AND status = 'active'`, oldID, replacement.ID, replacement.CreatedAt)
		if err != nil {
			return mapWriteError("replace a hook endpoint", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("the hook endpoint is gone or already replaced: %w", domain.ErrConflict)
		}
		return nil
	})
}

// DeleteHookEndpoint deletes an endpoint, and makes the one it had replaced
// active again: a replacement deleted before it took over leaves the old
// endpoint in charge, and due for renewal once more.
func (store *Store) DeleteHookEndpoint(ctx context.Context, id string) (bool, error) {
	deleted := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE apps_hook_endpoints
			SET status = 'active', replaced_by = NULL
			WHERE replaced_by = $1`, id); err != nil {
			return mapWriteError("restore a replaced hook endpoint", err)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM apps_hook_endpoints WHERE id = $1`, id)
		if err != nil {
			return mapWriteError("delete a hook endpoint", err)
		}
		deleted = tag.RowsAffected() == 1
		return nil
	})
	return deleted, err
}

// DeleteHookEndpointsForConnection deletes a connection's endpoints and
// answers them, for their subscriptions to be removed at the app.
func (store *Store) DeleteHookEndpointsForConnection(ctx context.Context, connectionID string) ([]hooks.Endpoint, error) {
	return queryHookEndpoints(ctx, store.pool, `
		DELETE FROM apps_hook_endpoints
		WHERE connection_id = $1
		RETURNING `+hookEndpointColumns, connectionID)
}

// HookEndpointsExpiring answers active endpoints that lapse by before,
// soonest first.
func (store *Store) HookEndpointsExpiring(ctx context.Context, before time.Time, limit int) ([]hooks.Endpoint, error) {
	return queryHookEndpoints(ctx, store.pool, `
		SELECT `+hookEndpointColumns+`
		FROM apps_hook_endpoints
		WHERE status = 'active' AND expires_at IS NOT NULL AND expires_at <= $1
		ORDER BY expires_at, id
		LIMIT $2`, before, limit)
}

// DeleteExpiredHookEndpoints deletes endpoints whose subscriptions have
// lapsed, and forgets previous secrets whose overlap has ended.
func (store *Store) DeleteExpiredHookEndpoints(ctx context.Context, now time.Time) (int64, error) {
	var deleted int64
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM apps_hook_endpoints
			WHERE expires_at IS NOT NULL AND expires_at <= $1`, now)
		if err != nil {
			return mapWriteError("delete lapsed hook endpoints", err)
		}
		deleted = tag.RowsAffected()
		if _, err := tx.Exec(ctx, `
			UPDATE apps_hook_endpoints
			SET previous_sealed_secret = NULL, previous_secret_until = NULL
			WHERE previous_secret_until IS NOT NULL AND previous_secret_until <= $1`, now); err != nil {
			return mapWriteError("forget rotated-out hook secrets", err)
		}
		return nil
	})
	return deleted, err
}

// HookDeliveryHandled says whether a delivery's dedupe key is recorded.
func (store *Store) HookDeliveryHandled(ctx context.Context, key string) (bool, error) {
	var handled bool
	if err := store.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM apps_handled_events WHERE event_id = $1)`, key,
	).Scan(&handled); err != nil {
		return false, fmt.Errorf("check a hook delivery handled: %w", err)
	}
	return handled, nil
}

// RecordHookDelivery records a delivery and the events it became in one
// transaction, so a redelivery, however it races the first, publishes
// nothing twice. An event already recorded for its trigger by an earlier
// delivery is skipped, and each recorded event is counted against its
// trigger, as a Composio delivery is.
func (store *Store) RecordHookDelivery(ctx context.Context, delivery hooks.Delivery) (bool, error) {
	recorded := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO apps_handled_events (event_id, detail_type, handled_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (event_id) DO NOTHING`, delivery.Key, hookDeliveryMark, delivery.ReceivedAt)
		if err != nil {
			return fmt.Errorf("record a hook delivery: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		recorded = true
		for _, event := range delivery.Events {
			if event.Key != "" {
				tag, err := tx.Exec(ctx, `
					INSERT INTO apps_handled_events (event_id, detail_type, handled_at)
					VALUES ($1, $2, $3)
					ON CONFLICT (event_id) DO NOTHING`, event.Key, hookEventMark, delivery.ReceivedAt)
				if err != nil {
					return fmt.Errorf("record a hook event: %w", err)
				}
				if tag.RowsAffected() != 1 {
					continue
				}
			}
			if _, err := tx.Exec(ctx, `
				UPDATE apps_triggers
				SET delivery_count = delivery_count + 1, last_delivery_at = $2
				WHERE id = $1 AND status <> 'removed'`, event.TriggerID, delivery.ReceivedAt); err != nil {
				return fmt.Errorf("count a trigger's delivery: %w", err)
			}
			if err := insertEvents(ctx, tx, event.Event); err != nil {
				return err
			}
		}
		return nil
	})
	return recorded, err
}

func queryHookEndpoints(ctx context.Context, db querier, sql string, arguments ...any) ([]hooks.Endpoint, error) {
	rows, err := db.Query(ctx, sql, arguments...)
	if err != nil {
		return nil, fmt.Errorf("read hook endpoints: %w", err)
	}
	defer rows.Close()
	var endpoints []hooks.Endpoint
	for rows.Next() {
		endpoint, err := scanHookEndpoint(rows)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, endpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read hook endpoints: %w", err)
	}
	return endpoints, nil
}

func scanHookEndpoint(row pgx.Row) (hooks.Endpoint, error) {
	var (
		endpoint                 hooks.Endpoint
		connectionID, replacedBy *string
		scheme, current, prior   []byte
		status                   string
	)
	if err := row.Scan(&endpoint.ID, &endpoint.Toolkit, &endpoint.Scope, &endpoint.OrganizationID, &connectionID,
		&scheme, &current, &endpoint.Secret.Version, &prior, &endpoint.Secret.PreviousUntil,
		&endpoint.Secret.RevealedAt, &endpoint.RemoteID, &endpoint.ExpiresAt, &status, &replacedBy,
		&endpoint.ValidatedAt, &endpoint.CreatedAt, &endpoint.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return hooks.Endpoint{}, err
		}
		return hooks.Endpoint{}, fmt.Errorf("read a hook endpoint: %w", err)
	}
	endpoint.ConnectionID, endpoint.ReplacedBy, endpoint.Status = text(connectionID), text(replacedBy), hooks.Status(status)
	if err := json.Unmarshal(scheme, &endpoint.Scheme); err != nil {
		return hooks.Endpoint{}, fmt.Errorf("read a hook endpoint's scheme: %w", err)
	}
	var err error
	if endpoint.Secret.Current, err = hookSealedFrom(current); err != nil {
		return hooks.Endpoint{}, err
	}
	if endpoint.Secret.Previous, err = hookSealedFrom(prior); err != nil {
		return hooks.Endpoint{}, err
	}
	return endpoint, nil
}

// hookSealedJSON is a sealed secret as its jsonb column holds it, or NULL.
func hookSealedJSON(sealed *secrets.SealedData) (any, error) {
	if sealed == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(sealed)
	if err != nil {
		return nil, fmt.Errorf("encode a sealed hook secret: %w", err)
	}
	return encoded, nil
}

func hookSealedFrom(encoded []byte) (*secrets.SealedData, error) {
	if encoded == nil {
		return nil, nil
	}
	var sealed secrets.SealedData
	if err := json.Unmarshal(encoded, &sealed); err != nil {
		return nil, fmt.Errorf("read a sealed hook secret: %w", err)
	}
	return &sealed, nil
}
