// Package store keeps applied workspace files and their history in PostgreSQL.
package store

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationLock int64 = 7263510713
const applyLockNamespace int32 = 72635107

var ErrApplyBusy = errors.New("another workspace apply is in progress")
var ErrVersionNotFound = errors.New("workspace version not found")

type Store struct{ database *postgreskit.Database }

type Version struct {
	Number    int64     `json:"version"`
	AppliedBy string    `json:"applied_by"`
	AppliedAt time.Time `json:"applied_at"`
	Document  []byte    `json:"-"`
}

func New(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "workspace", migrations, "migrations/*.sql", migrationLock)
}

// WithApplyLock holds a session advisory lock through provider API calls. A
// second process cannot apply to the same organization at the same time.
func (store *Store) WithApplyLock(ctx context.Context, organizationID string, operation func() error) error {
	connection, err := store.database.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire workspace apply connection: %w", err)
	}
	defer connection.Release()
	var locked bool
	if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, hashtext($2))`, applyLockNamespace, organizationID).Scan(&locked); err != nil {
		return fmt.Errorf("take workspace apply lock: %w", err)
	}
	if !locked {
		return ErrApplyBusy
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = connection.Exec(unlockCtx, `SELECT pg_advisory_unlock($1, hashtext($2))`, applyLockNamespace, organizationID)
	}()
	return operation()
}

func (store *Store) Latest(ctx context.Context, organizationID string) (Version, error) {
	var version Version
	err := store.database.Pool.QueryRow(ctx, `SELECT version, document, applied_by, applied_at
		FROM workspace.configs WHERE organization_id=$1`, organizationID).
		Scan(&version.Number, &version.Document, &version.AppliedBy, &version.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrVersionNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("read workspace version: %w", err)
	}
	return version, nil
}

func (store *Store) AtVersion(ctx context.Context, organizationID string, number int64) (Version, error) {
	var version Version
	err := store.database.Pool.QueryRow(ctx, `SELECT version, document, applied_by, applied_at
		FROM workspace.versions WHERE organization_id=$1 AND version=$2`, organizationID, number).
		Scan(&version.Number, &version.Document, &version.AppliedBy, &version.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrVersionNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("read workspace version: %w", err)
	}
	return version, nil
}

func (store *Store) Versions(ctx context.Context, organizationID string) ([]Version, error) {
	rows, err := store.database.Pool.Query(ctx, `SELECT version, applied_by, applied_at
		FROM workspace.versions WHERE organization_id=$1 ORDER BY version DESC LIMIT 100`, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list workspace versions: %w", err)
	}
	defer rows.Close()
	versions := []Version{}
	for rows.Next() {
		var version Version
		if err := rows.Scan(&version.Number, &version.AppliedBy, &version.AppliedAt); err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

// Save is idempotent for identical files. The row lock serializes version
// numbers even if a caller bypasses WithApplyLock.
func (store *Store) Save(ctx context.Context, organizationID, actorID string, document []byte) (Version, error) {
	var result Version
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		var latest Version
		err := tx.QueryRow(ctx, `SELECT version, document, applied_by, applied_at
			FROM workspace.configs WHERE organization_id=$1 FOR UPDATE`, organizationID).
			Scan(&latest.Number, &latest.Document, &latest.AppliedBy, &latest.AppliedAt)
		if err == nil && bytes.Equal(latest.Document, document) {
			result = latest
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result = Version{Number: latest.Number + 1, AppliedBy: actorID, AppliedAt: time.Now().UTC(), Document: document}
		if _, err := tx.Exec(ctx, `INSERT INTO workspace.configs
			(organization_id, version, document, applied_by, applied_at)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (organization_id) DO UPDATE SET
			version=EXCLUDED.version, document=EXCLUDED.document,
			applied_by=EXCLUDED.applied_by, applied_at=EXCLUDED.applied_at`,
			organizationID, result.Number, result.Document, result.AppliedBy, result.AppliedAt); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO workspace.versions
			(organization_id, version, document, applied_by, applied_at)
			VALUES ($1,$2,$3,$4,$5)`,
			organizationID, result.Number, result.Document, result.AppliedBy, result.AppliedAt)
		return err
	})
	if err != nil {
		return Version{}, fmt.Errorf("save workspace version: %w", err)
	}
	return result, nil
}
