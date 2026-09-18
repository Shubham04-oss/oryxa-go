package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

const connectionColumns = `id, organization_id, member_id, toolkit, auth_config_id, composio_user_id,
	composio_account_id, status, status_reason, label, return_to, auto_subscribe, auto_subscribed_at,
	created_at, updated_at, connected_at, removed_at`

// CreateConnection records a pending connection, with the hash of the state
// its browser round trip carries so PendingConnection can find it again. The
// hash lives apart from the domain value: nothing but the store ever looks a
// connection up by it.
func (store *Store) CreateConnection(ctx context.Context, connection domain.Connection, stateHash string, event domain.Event) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO apps_connections (
				id, organization_id, member_id, toolkit, auth_config_id, composio_user_id, composio_account_id,
				status, status_reason, label, state_hash, return_to, auto_subscribe, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14)`,
			connection.ID, connection.OrganizationID, connection.MemberID, connection.Toolkit, connection.AuthConfigID,
			connection.ComposioUserID, nullable(connection.ComposioAccountID), string(connection.Status),
			connection.StatusReason, connection.Label, nullable(stateHash), connection.ReturnTo,
			connection.AutoSubscribe, connection.CreatedAt,
		); err != nil {
			return mapWriteError("create connection", err)
		}
		return insertEvents(ctx, tx, event)
	})
}

// PendingConnection finds the connection a state hash names while it is
// still pending: the state a member's browser carries back from Composio.
func (store *Store) PendingConnection(ctx context.Context, stateHash string) (domain.Connection, error) {
	connections, err := queryConnections(ctx, store.pool, `
		SELECT `+connectionColumns+`
		FROM apps_connections
		WHERE state_hash = $1 AND status = 'pending'`, stateHash)
	if err != nil {
		return domain.Connection{}, err
	}
	if len(connections) == 0 {
		return domain.Connection{}, domain.ErrNotFound
	}
	return connections[0], nil
}

// ActivateConnection makes a pending connection active, clears its state
// hash so PendingConnection can no longer find it, and writes event. It
// answers false, changing nothing, when the connection is no longer pending
// - already active, failed or gone - so an event is never written for a
// change that did not happen.
func (store *Store) ActivateConnection(ctx context.Context, id, label string, now time.Time, event domain.Event) (domain.Connection, bool, error) {
	var activated domain.Connection
	changed := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		connections, err := queryConnections(ctx, tx, `
			UPDATE apps_connections
			SET status = 'active', label = $2, state_hash = NULL, connected_at = $3, updated_at = $3
			WHERE id = $1 AND status = 'pending'
			RETURNING `+connectionColumns, id, label, now)
		if err != nil {
			return err
		}
		if len(connections) == 0 {
			return nil
		}
		activated = connections[0]
		changed = true
		return insertEvents(ctx, tx, event)
	})
	return activated, changed, err
}

// FailConnection marks a pending connection failed, clears its state hash
// and writes event. A connection that never finished is an ending like any
// other, and only this says it happened — the member's browser is gone by
// the time anything else could.
func (store *Store) FailConnection(ctx context.Context, id, reason string, now time.Time, event domain.Event) (bool, error) {
	failed := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE apps_connections
			SET status = 'failed', status_reason = $2, state_hash = NULL, updated_at = $3
			WHERE id = $1 AND status = 'pending'`, id, reason, now)
		if err != nil {
			return mapWriteError("fail connection", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		failed = true
		return insertEvents(ctx, tx, event)
	})
	return failed, err
}

// ConnectionsAwaitingSubscribe answers the active connections that asked to
// be subscribed to their app's events and have not been, oldest first,
// counts an attempt against each and leases it until leaseUntil - so a
// toolkit that keeps failing is given up on after maxAttempts rather than
// retried for ever, and a second pass cannot claim the same connection while
// this one still owns it.
//
// FOR UPDATE SKIP LOCKED alone protects only this statement: once it commits,
// a second replica's next tick would see the same rows as unclaimed again and
// take them too, doubling the Composio calls. The lease is what closes that
// window between passes, not just within one.
func (store *Store) ConnectionsAwaitingSubscribe(ctx context.Context, maxAttempts, limit int, now, leaseUntil time.Time) ([]domain.Connection, error) {
	return queryConnections(ctx, store.pool, `
		UPDATE apps_connections SET auto_subscribe_attempts = auto_subscribe_attempts + 1,
			auto_subscribe_claimed_until = $4, updated_at = $3
		WHERE id IN (
			SELECT id FROM apps_connections
			WHERE status = 'active' AND auto_subscribe AND auto_subscribed_at IS NULL
				AND auto_subscribe_attempts < $1
				AND (auto_subscribe_claimed_until IS NULL OR auto_subscribe_claimed_until <= $3)
			ORDER BY connected_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+connectionColumns, maxAttempts, limit, now, leaseUntil)
}

// MarkSubscribed records that a connection has been subscribed to what its
// app delivers, so it is not claimed again.
func (store *Store) MarkSubscribed(ctx context.Context, id string, now time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE apps_connections SET auto_subscribed_at = $2, updated_at = $2
		WHERE id = $1 AND auto_subscribed_at IS NULL`, id, now); err != nil {
		return mapWriteError("mark a connection subscribed", err)
	}
	return nil
}

// Connection reads one connection of an organization, whatever its status.
// Requiring both the organization and the id in the same WHERE clause is
// what keeps another organization's connection from ever matching: it reads
// as not found rather than as a different organization's row.
func (store *Store) Connection(ctx context.Context, organizationID, id string) (domain.Connection, error) {
	connections, err := queryConnections(ctx, store.pool, `
		SELECT `+connectionColumns+`
		FROM apps_connections
		WHERE organization_id = $1 AND id = $2`, organizationID, id)
	if err != nil {
		return domain.Connection{}, err
	}
	if len(connections) == 0 {
		return domain.Connection{}, domain.ErrNotFound
	}
	return connections[0], nil
}

// ConnectionByAccount reads the connection a Composio account backs, in
// whatever status it is in. A webhook or a cleanup pass names the account,
// not the organization, so there is nothing else to scope this read by.
func (store *Store) ConnectionByAccount(ctx context.Context, composioAccountID string) (domain.Connection, error) {
	connections, err := queryConnections(ctx, store.pool, `
		SELECT `+connectionColumns+`
		FROM apps_connections
		WHERE composio_account_id = $1`, composioAccountID)
	if err != nil {
		return domain.Connection{}, err
	}
	if len(connections) == 0 {
		return domain.Connection{}, domain.ErrNotFound
	}
	return connections[0], nil
}

// Connections lists an organization's connections, newest first. An empty
// Statuses keeps everything but removed connections, which nobody needs
// listed back to them; naming removed explicitly still finds it.
func (store *Store) Connections(ctx context.Context, organizationID string, query domain.ConnectionQuery) ([]domain.Connection, error) {
	sql := `SELECT ` + connectionColumns + ` FROM apps_connections WHERE organization_id = $1`
	args := []any{organizationID}
	bind := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if len(query.Statuses) == 0 {
		sql += ` AND status <> 'removed'`
	} else {
		statuses := make([]string, len(query.Statuses))
		for index, status := range query.Statuses {
			statuses[index] = string(status)
		}
		sql += ` AND status = ANY(` + bind(statuses) + `)`
	}
	if query.MemberID != "" {
		sql += ` AND member_id = ` + bind(query.MemberID)
	}
	if query.Toolkit != "" {
		sql += ` AND toolkit = ` + bind(query.Toolkit)
	}
	sql += ` ORDER BY created_at DESC, id`
	return queryConnections(ctx, store.pool, sql, args...)
}

// ExpireConnection marks an active connection expired and writes event. Its
// state hash was already cleared when it activated, so there is nothing left
// to clear here.
func (store *Store) ExpireConnection(ctx context.Context, id, reason string, now time.Time, event domain.Event) (bool, error) {
	expired := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE apps_connections
			SET status = 'expired', status_reason = $2, updated_at = $3
			WHERE id = $1 AND status = 'active'`, id, reason, now)
		if err != nil {
			return mapWriteError("expire connection", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		expired = true
		return insertEvents(ctx, tx, event)
	})
	return expired, err
}

// RemoveConnection marks a connection and every live trigger on it removed,
// in one transaction, and inserts whatever events the events callback builds
// from the two as they now stand. The UPDATE that removes the connection is
// itself the lock: it is the same statement that decides whether the
// connection was already removed, so two callers racing to remove it can
// never both see it as live.
func (store *Store) RemoveConnection(ctx context.Context, id, reason string, now time.Time, events func(domain.Connection, []domain.Trigger) ([]domain.Event, error)) (bool, error) {
	removed := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		connections, err := queryConnections(ctx, tx, `
			UPDATE apps_connections
			SET status = 'removed', status_reason = $2, state_hash = NULL, removed_at = $3, updated_at = $3
			WHERE id = $1 AND status <> 'removed'
			RETURNING `+connectionColumns, id, reason, now)
		if err != nil {
			return err
		}
		if len(connections) == 0 {
			return nil
		}
		connection := connections[0]

		triggers, err := queryTriggers(ctx, tx, `
			UPDATE apps_triggers
			SET status = 'removed', status_reason = $2, updated_at = $3
			WHERE connection_id = $1 AND status <> 'removed'
			RETURNING `+triggerColumns, id, reason, now)
		if err != nil {
			return err
		}

		removed = true
		if events == nil {
			return nil
		}
		recorded, err := events(connection, triggers)
		if err != nil {
			return err
		}
		return insertEvents(ctx, tx, recorded...)
	})
	return removed, err
}

// FailPendingConnectionsBefore fails every pending connection whose browser
// round trip began before cutoff and so is never coming back, freeing its
// account id and state hash for a fresh attempt.
//
// The connections it failed are returned from the update itself, so the
// events are built from exactly the rows this sweep changed and written in
// the same transaction — two replicas sweeping at once cannot both announce
// the same abandoned connection.
func (store *Store) FailPendingConnectionsBefore(ctx context.Context, cutoff, now time.Time, events func([]domain.Connection) ([]domain.Event, error)) (int64, error) {
	var count int64
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		failed, err := queryConnections(ctx, tx, `
			UPDATE apps_connections
			SET status = 'failed', status_reason = 'link expired', state_hash = NULL, updated_at = $2
			WHERE status = 'pending' AND created_at < $1
			RETURNING `+connectionColumns, cutoff, now)
		if err != nil {
			return fmt.Errorf("fail expired pending connections: %w", err)
		}
		count = int64(len(failed))
		if count == 0 {
			return nil
		}
		built, err := events(failed)
		if err != nil {
			return err
		}
		return insertEvents(ctx, tx, built...)
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

func queryConnections(ctx context.Context, db querier, sql string, args ...any) ([]domain.Connection, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapWriteError("query connections", err)
	}
	defer rows.Close()
	connections := []domain.Connection{}
	for rows.Next() {
		connection, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query connections: %w", err)
	}
	return connections, nil
}

func scanConnection(rows pgx.Rows) (domain.Connection, error) {
	var connection domain.Connection
	var status string
	var composioAccountID *string
	if err := rows.Scan(&connection.ID, &connection.OrganizationID, &connection.MemberID, &connection.Toolkit,
		&connection.AuthConfigID, &connection.ComposioUserID, &composioAccountID, &status, &connection.StatusReason,
		&connection.Label, &connection.ReturnTo, &connection.AutoSubscribe, &connection.AutoSubscribedAt,
		&connection.CreatedAt, &connection.UpdatedAt,
		&connection.ConnectedAt, &connection.RemovedAt); err != nil {
		return domain.Connection{}, fmt.Errorf("scan connection: %w", err)
	}
	connection.ComposioAccountID = text(composioAccountID)
	connection.Status = domain.ConnectionStatus(status)
	connection.CreatedAt = connection.CreatedAt.UTC()
	connection.UpdatedAt = connection.UpdatedAt.UTC()
	return connection, nil
}
