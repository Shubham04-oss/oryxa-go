package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// connectionColumns are read from apps_connections, unaliased, in a SELECT or
// an UPDATE's RETURNING. credential_kind is an Oryxa connection's credential's
// kind, read beside it: it lives in apps_credentials, which signin keeps.
const connectionColumns = `id, organization_id, member_id, toolkit, broker,
	COALESCE((SELECT credential.kind FROM apps_credentials credential
		WHERE credential.connection_id = apps_connections.id), '') AS credential_kind,
	auth_config_id, composio_user_id,
	composio_account_id, status, status_reason, label, return_to, auto_subscribe, auto_subscribed_at,
	auto_subscribe_attempts, created_at, updated_at, connected_at, removed_at, pipedream_account_id, native_connection_id`

// brokerOf is the broker a connection is stored under: Composio's unless it
// says otherwise, as every connection made before Oryxa's sign-in is.
func brokerOf(connection domain.Connection) string {
	if connection.Broker == "" {
		return domain.BrokerComposio
	}
	return connection.Broker
}

// CreateConnection records a pending connection, with the hash of the state
// its browser round trip carries so PendingConnection can find it again. The
// hash lives apart from the domain value: nothing but the store ever looks a
// connection up by it.
func (store *Store) CreateConnection(ctx context.Context, connection domain.Connection, stateHash string, event domain.Event) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO apps_connections (
				id, organization_id, member_id, toolkit, auth_config_id, composio_user_id, composio_account_id,
				status, status_reason, label, state_hash, return_to, auto_subscribe, created_at, updated_at, broker
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $14, $15)`,
			connection.ID, connection.OrganizationID, connection.MemberID, connection.Toolkit, connection.AuthConfigID,
			connection.ComposioUserID, nullable(connection.ComposioAccountID), string(connection.Status),
			connection.StatusReason, connection.Label, nullable(stateHash), connection.ReturnTo,
			connection.AutoSubscribe, connection.CreatedAt, brokerOf(connection),
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

// ActivatePipedreamConnection is ActivateConnection for a Pipedream
// connection, recording the account it was read back as in the same update.
// An account already recorded on another connection is ErrConflict, which the
// unique index decides, so two callbacks can never claim one account.
func (store *Store) ActivatePipedreamConnection(ctx context.Context, id, accountID, label string, now time.Time, event domain.Event) (domain.Connection, bool, error) {
	var activated domain.Connection
	changed := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		connections, err := queryConnections(ctx, tx, `
			UPDATE apps_connections
			SET status = 'active', label = $3, pipedream_account_id = $2, state_hash = NULL, connected_at = $4, updated_at = $4
			WHERE id = $1 AND status = 'pending' AND broker = 'pipedream'
			RETURNING `+connectionColumns, id, accountID, label, now)
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

// PipedreamConnections pages through every active Pipedream connection with
// an account, in id order after afterID, for the health sweep. It is the one
// read not scoped to an organization: the sweep checks them all.
func (store *Store) PipedreamConnections(ctx context.Context, afterID string, limit int) ([]domain.Connection, error) {
	return queryConnections(ctx, store.pool, `
		SELECT `+connectionColumns+`
		FROM apps_connections
		WHERE broker = 'pipedream' AND status = 'active' AND pipedream_account_id IS NOT NULL AND id > $1
		ORDER BY id
		LIMIT $2`, afterID, limit)
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
//
// Only Composio's connections are claimed: subscribing is a Composio call,
// and an Oryxa connection's triggers are its own integration's.
func (store *Store) ConnectionsAwaitingSubscribe(ctx context.Context, maxAttempts, limit int, now, leaseUntil time.Time) ([]domain.Connection, error) {
	return queryConnections(ctx, store.pool, `
		UPDATE apps_connections SET auto_subscribe_attempts = auto_subscribe_attempts + 1,
			auto_subscribe_claimed_until = $4, updated_at = $3
		WHERE id IN (
			SELECT id FROM apps_connections
			WHERE status = 'active' AND auto_subscribe AND auto_subscribed_at IS NULL
				AND broker = 'composio'
				AND auto_subscribe_attempts < $1
				AND (auto_subscribe_claimed_until IS NULL OR auto_subscribe_claimed_until <= $3)
			ORDER BY connected_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+connectionColumns, maxAttempts, limit, now, leaseUntil)
}

// RefundAutoSubscribeAttempt retains the lease as policy backoff. Matching both
// the lease and attempt makes retries idempotent and prevents an old worker from
// refunding a different worker's provider attempt.
func (store *Store) RefundAutoSubscribeAttempt(ctx context.Context, id string, attempt int, leaseUntil time.Time) error {
	_, err := store.pool.Exec(ctx, `UPDATE apps_connections
 SET auto_subscribe_attempts = auto_subscribe_attempts - 1
 WHERE id = $1 AND auto_subscribe_attempts = $2 AND auto_subscribe_attempts > 0
 AND auto_subscribe_claimed_until = $3 AND auto_subscribed_at IS NULL`, id, attempt, leaseUntil)
	if err != nil {
		return mapWriteError("refund automatic subscription attempt", err)
	}
	return nil
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

// FailPendingOryxaConnectionsBefore fails every pending Oryxa connection
// created before cutoff: its sign-in lasted ten minutes, so a member who never
// came back is not coming back, and the one-current-connection slot is theirs
// again. Like FailPendingConnectionsBefore, the events are built from exactly
// the rows this sweep changed and written in the same transaction.
func (store *Store) FailPendingOryxaConnectionsBefore(ctx context.Context, cutoff, now time.Time, events func([]domain.Connection) ([]domain.Event, error)) (int64, error) {
	var count int64
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		failed, err := queryConnections(ctx, tx, `
			UPDATE apps_connections
			SET status = 'failed', status_reason = 'link expired', state_hash = NULL, updated_at = $2
			WHERE status = 'pending' AND broker = 'oryxa' AND created_at < $1
			RETURNING `+connectionColumns, cutoff, now)
		if err != nil {
			return fmt.Errorf("fail abandoned Oryxa sign-ins: %w", err)
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

// RelabelConnection records that an active connection's credential was
// replaced: its label becomes the account the new credential names, and
// event is written in the same transaction. It answers false, writing
// nothing, when the connection is no longer active.
func (store *Store) RelabelConnection(ctx context.Context, id, label string, now time.Time, event domain.Event) (domain.Connection, bool, error) {
	var relabelled domain.Connection
	changed := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		connections, err := queryConnections(ctx, tx, `
			UPDATE apps_connections SET label = $2, updated_at = $3
			WHERE id = $1 AND status = 'active'
			RETURNING `+connectionColumns, id, label, now)
		if err != nil {
			return err
		}
		if len(connections) == 0 {
			return nil
		}
		relabelled, changed = connections[0], true
		return insertEvents(ctx, tx, event)
	})
	return relabelled, changed, err
}

// RecordEvents writes events that announce a change kept elsewhere, such as
// an organization's OAuth client, which signin keeps.
func (store *Store) RecordEvents(ctx context.Context, events ...domain.Event) error {
	if len(events) == 0 {
		return nil
	}
	return store.inTx(ctx, func(tx pgx.Tx) error { return insertEvents(ctx, tx, events...) })
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
	// An UPDATE ... RETURNING reports a broken constraint here, not from
	// Query, so it is mapped the same way.
	if err := rows.Err(); err != nil {
		return nil, mapWriteError("query connections", err)
	}
	return connections, nil
}

func scanConnection(rows pgx.Rows) (domain.Connection, error) {
	var connection domain.Connection
	var status string
	var composioAccountID, pipedreamAccountID, nativeConnectionID *string
	if err := rows.Scan(&connection.ID, &connection.OrganizationID, &connection.MemberID, &connection.Toolkit,
		&connection.Broker, &connection.CredentialKind, &connection.AuthConfigID, &connection.ComposioUserID, &composioAccountID, &status, &connection.StatusReason,
		&connection.Label, &connection.ReturnTo, &connection.AutoSubscribe, &connection.AutoSubscribedAt,
		&connection.AutoSubscribeAttempts, &connection.CreatedAt, &connection.UpdatedAt,
		&connection.ConnectedAt, &connection.RemovedAt, &pipedreamAccountID, &nativeConnectionID); err != nil {
		return domain.Connection{}, fmt.Errorf("scan connection: %w", err)
	}
	connection.ComposioAccountID = text(composioAccountID)
	connection.PipedreamAccountID = text(pipedreamAccountID)
	connection.NativeConnectionID = text(nativeConnectionID)
	connection.Status = domain.ConnectionStatus(status)
	connection.CreatedAt = connection.CreatedAt.UTC()
	connection.UpdatedAt = connection.UpdatedAt.UTC()
	return connection, nil
}

// NativeOrganizations lists, in order, every organization that has a native
// connection row, whatever its status, for the reconcile pass. It is the one
// other read not scoped to an organization, as the pass visits them all.
func (store *Store) NativeOrganizations(ctx context.Context) ([]string, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT DISTINCT organization_id FROM apps_connections
		WHERE broker = 'native'
		ORDER BY organization_id`)
	if err != nil {
		return nil, fmt.Errorf("list organizations with native connections: %w", err)
	}
	organizations, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list organizations with native connections: %w", err)
	}
	return organizations, nil
}

// ApplyNativeConnection records what the integrations service reported of a
// connection: a new row, or a change to the one it already named. The row's
// status and at (the event's UpdatedAt) are what is applied, and only when
// at is newer than the newest already applied, so an old event arriving late
// or twice changes nothing. A removed status is final: it applies whatever
// its time, and nothing changes a removed row afterwards. A removed event for
// a connection never seen leaves a removed row behind, so a created event
// that was overtaken cannot bring it back.
func (store *Store) ApplyNativeConnection(ctx context.Context, connection domain.Connection, at time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO apps_connections (
			id, organization_id, member_id, toolkit, auth_config_id, composio_user_id, status, status_reason,
			auto_subscribe, created_at, updated_at, connected_at, removed_at, broker, native_connection_id, native_updated_at
		) VALUES ($1, $2, $3, $4, '', $5, $6, $7, false, $8, $8,
			CASE WHEN $6 = 'active' THEN $8::timestamptz END, CASE WHEN $6 = 'removed' THEN $8::timestamptz END,
			'native', $9, $8)
		ON CONFLICT (native_connection_id) DO UPDATE SET
			status = EXCLUDED.status, status_reason = EXCLUDED.status_reason, updated_at = EXCLUDED.updated_at,
			native_updated_at = EXCLUDED.native_updated_at,
			connected_at = COALESCE(apps_connections.connected_at, EXCLUDED.connected_at),
			removed_at = EXCLUDED.removed_at
		WHERE apps_connections.status <> 'removed'
		  AND (EXCLUDED.status = 'removed' OR apps_connections.native_updated_at < EXCLUDED.native_updated_at)`,
		connection.ID, connection.OrganizationID, connection.MemberID, connection.Toolkit,
		domain.ComposioUserID(connection.OrganizationID, connection.MemberID),
		string(connection.Status), connection.StatusReason, at, connection.NativeConnectionID); err != nil {
		return mapWriteError("apply native connection", err)
	}
	return nil
}
