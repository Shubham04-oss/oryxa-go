package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// AcquireLease takes the refresh lease on a connection whose version is
// still expectedVersion, so one refresh runs at a time per connection. It
// does not bump version: a lease guards a credential in flight, not the
// credential itself.
func (store *Store) AcquireLease(ctx context.Context, organizationID, connectionID string, expectedVersion int64, token string, now, until time.Time) (bool, error) {
	tag, err := store.pool.Exec(ctx, `
		UPDATE connections
		SET refresh_lease_token = $4, refresh_lease_until = $5
		WHERE organization_id = $1 AND id = $2 AND version = $3 AND credentials_deleted_at IS NULL
		  AND (refresh_lease_until IS NULL OR refresh_lease_until <= $6)`,
		organizationID, connectionID, expectedVersion, token, until, now)
	if err != nil {
		return false, fmt.Errorf("acquire lease: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SaveRenewedCredentials saves a refreshed or newly minted credential under
// the lease that authorized it, and releases the lease. The version must still
// be the one the lease was taken at: a lease does not bump the version, so a
// reconnect, key replacement, disconnect or status change made while the
// provider was being called moves it, and the stale renewal is refused with
// ErrConflict instead of overwriting that change. A nil scopes leaves the
// connection's granted scopes as they are; a non-nil one, even empty,
// replaces them.
func (store *Store) SaveRenewedCredentials(ctx context.Context, organizationID, connectionID, token string, expectedVersion int64, credentials *domain.SealedData, scopes []string, at time.Time) (int64, error) {
	sealed, err := sealedValue(credentials)
	if err != nil {
		return 0, err
	}
	var version int64
	err = store.pool.QueryRow(ctx, `
		UPDATE connections
		SET encrypted_credentials = $4, scopes = COALESCE($5, scopes), updated_at = $6, version = version + 1,
		    refresh_lease_token = NULL, refresh_lease_until = NULL
		WHERE organization_id = $1 AND id = $2 AND refresh_lease_token = $3 AND version = $7 AND credentials_deleted_at IS NULL
		RETURNING version`,
		organizationID, connectionID, token, sealed, scopes, at, expectedVersion,
	).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrConflict
	}
	if err != nil {
		return 0, mapWriteError("save renewed credentials", err)
	}
	return version, nil
}

// ReleaseLease clears the refresh lease if token still holds it. Releasing a
// lease nothing holds, or one another caller has since taken, changes
// nothing.
func (store *Store) ReleaseLease(ctx context.Context, organizationID, connectionID, token string) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE connections SET refresh_lease_token = NULL, refresh_lease_until = NULL
		WHERE organization_id = $1 AND id = $2 AND refresh_lease_token = $3`,
		organizationID, connectionID, token); err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	return nil
}
