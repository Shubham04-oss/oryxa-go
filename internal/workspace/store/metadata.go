package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// VersionNumber avoids loading a full manifest to validate a cache or plan.
func (store *Store) VersionNumber(ctx context.Context, organizationID string) (int64, error) {
	var version int64
	err := store.database.Pool.QueryRow(ctx, `SELECT version FROM workspace.configs WHERE organization_id=$1`, organizationID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrVersionNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read workspace version number: %w", err)
	}
	return version, nil
}
