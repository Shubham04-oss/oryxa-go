package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// AddHold keeps a disconnected connection's credentials alive for a holder
// that is still cleaning up remote resources. An existing hold for the same
// holder is left as it is. The connection must exist in the organization
// with credentials not yet deleted; it may be live or already disconnected.
func (store *Store) AddHold(ctx context.Context, organizationID, connectionID, holder string, at time.Time) error {
	return store.inTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM connections
				WHERE organization_id = $1 AND id = $2 AND credentials_deleted_at IS NULL)`,
			organizationID, connectionID).Scan(&exists); err != nil {
			return fmt.Errorf("check connection for hold: %w", err)
		}
		if !exists {
			return domain.ErrNotFound
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO connections_holds (organization_id, connection_id, holder, created_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (organization_id, connection_id, holder) DO NOTHING`,
			organizationID, connectionID, holder, at); err != nil {
			return mapWriteError("add hold", err)
		}
		return nil
	})
}

// ReleaseHold removes a hold. Releasing one that is not there changes
// nothing.
func (store *Store) ReleaseHold(ctx context.Context, organizationID, connectionID, holder string) error {
	if _, err := store.pool.Exec(ctx, `
		DELETE FROM connections_holds WHERE organization_id = $1 AND connection_id = $2 AND holder = $3`,
		organizationID, connectionID, holder); err != nil {
		return fmt.Errorf("release hold: %w", err)
	}
	return nil
}

// CountHolds counts the holds open on a connection.
func (store *Store) CountHolds(ctx context.Context, organizationID, connectionID string) (int, error) {
	var count int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM connections_holds WHERE organization_id = $1 AND connection_id = $2`,
		organizationID, connectionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count holds: %w", err)
	}
	return count, nil
}
