package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// ClaimCredentialDeletions claims up to limit disconnected connections whose
// credentials are due for deletion: no hold is open (or MaxHold has passed
// since they disconnected) and no refresh is in flight, oldest first. It
// returns the full connections, including their still-encrypted
// credentials, so the caller can revoke the grant at the provider before
// deleting them.
func (store *Store) ClaimCredentialDeletions(ctx context.Context, now time.Time, limit int, token string, until time.Time) ([]domain.Connection, error) {
	cutoff := now.Add(-domain.MaxHold)
	rows, err := store.pool.Query(ctx, `
		WITH picked AS (
			SELECT c.id
			FROM connections c
			WHERE c.status = 'disconnected' AND c.credentials_deleted_at IS NULL
			  AND (c.refresh_lease_until IS NULL OR c.refresh_lease_until <= $1)
			  AND (
			      NOT EXISTS (
			          SELECT 1 FROM connections_holds hold
			          WHERE hold.organization_id = c.organization_id AND hold.connection_id = c.id)
			      OR c.status_changed_at <= $2
			  )
			ORDER BY c.status_changed_at
			FOR UPDATE OF c SKIP LOCKED
			LIMIT $3
		)
		UPDATE connections AS c
		SET refresh_lease_token = $4, refresh_lease_until = $5
		FROM picked
		WHERE c.id = picked.id
		RETURNING `+connectionColumns("c"),
		now, cutoff, limit, token, until)
	if err != nil {
		return nil, fmt.Errorf("claim credential deletions: %w", err)
	}
	defer rows.Close()
	connections := []domain.Connection{}
	for rows.Next() {
		connection, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan claimed connection: %w", err)
		}
		connections = append(connections, connection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim credential deletions: %w", err)
	}
	return connections, nil
}

// DeleteCredentials removes a connection's credentials once they are
// revoked at the provider, along with its holds and health row, under the
// lease ClaimCredentialDeletions handed out. A token that no longer matches,
// because the connection was not claimed, is live, or its credentials are
// already gone, answers ErrConflict.
func (store *Store) DeleteCredentials(ctx context.Context, organizationID, connectionID, token string, at time.Time, event domain.Event) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE connections
			SET encrypted_credentials = NULL, credentials_deleted_at = $4, updated_at = $4, version = version + 1,
			    refresh_lease_token = NULL, refresh_lease_until = NULL
			WHERE organization_id = $1 AND id = $2 AND refresh_lease_token = $3
			  AND status = 'disconnected' AND credentials_deleted_at IS NULL`,
			organizationID, connectionID, token, at)
		if err != nil {
			return mapWriteError("delete credentials", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrConflict
		}
		if _, err := tx.Exec(ctx, `DELETE FROM connections_holds WHERE organization_id = $1 AND connection_id = $2`,
			organizationID, connectionID); err != nil {
			return mapWriteError("delete connection holds", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM connections_health WHERE organization_id = $1 AND connection_id = $2`,
			organizationID, connectionID); err != nil {
			return mapWriteError("delete connection health", err)
		}
		return insertEvents(ctx, tx, event)
	})
}

// WithProviderGrantLock runs fn while holding a transaction-scoped advisory
// lock on one app and provider account. Connections of the same app and
// account share one grant at the provider, so deciding whether to revoke it
// and deleting their credentials must happen one connection at a time, even
// across replicas: otherwise two deletions can each see the other still
// holding the grant, and neither revokes it. fn runs its own store calls; the
// lock is released when fn returns.
func (store *Store) WithProviderGrantLock(ctx context.Context, organizationID, appID, providerAccountID string, fn func(context.Context) error) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin provider grant lock: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	key := strings.Join([]string{"provider-grant", organizationID, appID, providerAccountID}, "\x1f")
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return fmt.Errorf("lock provider grant: %w", err)
	}
	return fn(ctx)
}
