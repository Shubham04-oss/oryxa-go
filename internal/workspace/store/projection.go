package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/projection"
	"github.com/jackc/pgx/v5"
)

func (store *Store) Available(ctx context.Context) (bool, error) {
	var available bool
	err := store.database.Pool.QueryRow(ctx, `SELECT to_regclass('workspace.configs') IS NOT NULL`).Scan(&available)
	if err != nil {
		return false, fmt.Errorf("check workspace schema: %w", err)
	}
	return available, nil
}

// Policy reads only a small projection in steady state. Legacy versions are
// decoded tolerantly once; the version guard prevents a racing apply from
// receiving old settings during backfill.
func (store *Store) Policy(ctx context.Context, organizationID string) (int64, []byte, error) {
	var version int64
	var settings, legacy []byte
	err := store.database.Pool.QueryRow(ctx, `SELECT version, policy_settings,
		CASE WHEN policy_settings IS NULL THEN document END
		FROM workspace.configs WHERE organization_id=$1`, organizationID).Scan(&version, &settings, &legacy)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrVersionNotFound
	}
	if err != nil {
		return 0, nil, fmt.Errorf("read workspace policy projection: %w", err)
	}
	if settings != nil {
		return version, settings, nil
	}
	value, err := projection.FromYAML(legacy)
	if err != nil {
		return 0, nil, err
	}
	settings, err = json.Marshal(value)
	if err != nil {
		return 0, nil, err
	}
	_, err = store.database.Pool.Exec(ctx, `UPDATE workspace.configs SET policy_settings=$3
		WHERE organization_id=$1 AND version=$2 AND policy_settings IS NULL`, organizationID, version, settings)
	if err != nil {
		return 0, nil, fmt.Errorf("backfill workspace policy projection: %w", err)
	}
	return version, settings, nil
}
