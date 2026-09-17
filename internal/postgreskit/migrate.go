package postgreskit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"slices"

	"github.com/jackc/pgx/v5"
)

// migrationsLock serializes creating the table that records migrations, which
// two processes starting together would otherwise race to create.
const migrationsLock int64 = 7263510401

// Migrate applies a service's migration files, the ones pattern matches in
// files, in name order, each at most once while it stays unchanged.
//
// It records every file it applies in oryxa_migrations, by service and name,
// with a checksum of its contents. A file recorded with the same checksum is
// skipped, so a restart no longer takes the locks and scans that repeating a
// migration costs. A new file is applied. A file whose contents changed since
// it was applied is applied again, with a warning: every migration here is
// written to be safe to repeat, and editing one in place is how some schemas
// have changed.
//
// Each file runs in a transaction of its own, together with its record and
// under the service's advisory lock, so replicas starting together take turns
// and a failure leaves every earlier file applied and recorded.
func (database *Database) Migrate(ctx context.Context, service string, files fs.FS, pattern string, lock int64) error {
	names, err := fs.Glob(files, pattern)
	if err != nil {
		return fmt.Errorf("list %s migrations: %w", service, err)
	}
	slices.Sort(names)
	if err := database.ensureMigrationsTable(ctx); err != nil {
		return err
	}
	// Reading every record at once makes the usual start, where nothing has
	// changed, one query; a file that looks due is checked again under the
	// lock before it is applied.
	recorded, err := database.recordedMigrations(ctx, service)
	if err != nil {
		return err
	}
	for _, name := range names {
		contents, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("read %s migration %s: %w", service, name, err)
		}
		if recorded[path.Base(name)] == checksumOf(contents) {
			continue
		}
		if err := database.applyMigration(ctx, service, path.Base(name), contents, lock); err != nil {
			return err
		}
	}
	return nil
}

func (database *Database) recordedMigrations(ctx context.Context, service string) (map[string]string, error) {
	rows, err := database.Pool.Query(ctx, `SELECT name, checksum FROM oryxa_migrations WHERE service = $1`, service)
	if err != nil {
		return nil, fmt.Errorf("read %s migration records: %w", service, err)
	}
	defer rows.Close()
	recorded := map[string]string{}
	for rows.Next() {
		var name, checksum string
		if err := rows.Scan(&name, &checksum); err != nil {
			return nil, fmt.Errorf("read %s migration records: %w", service, err)
		}
		recorded[name] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s migration records: %w", service, err)
	}
	return recorded, nil
}

func checksumOf(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func (database *Database) ensureMigrationsTable(ctx context.Context) error {
	return pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationsLock); err != nil {
			return fmt.Errorf("lock migration records: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			CREATE TABLE IF NOT EXISTS oryxa_migrations (
				service text NOT NULL,
				name text NOT NULL,
				checksum text NOT NULL,
				applied_at timestamptz NOT NULL DEFAULT now(),
				PRIMARY KEY (service, name)
			)`); err != nil {
			return fmt.Errorf("create migration records: %w", err)
		}
		return nil
	})
}

func (database *Database) applyMigration(ctx context.Context, service, name string, contents []byte, lock int64) error {
	checksum := checksumOf(contents)
	return pgx.BeginFunc(ctx, database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lock); err != nil {
			return fmt.Errorf("lock %s migrations: %w", service, err)
		}
		var recorded string
		err := tx.QueryRow(ctx, `SELECT checksum FROM oryxa_migrations WHERE service = $1 AND name = $2`, service, name).Scan(&recorded)
		switch {
		case err == nil && recorded == checksum:
			return nil
		case err == nil:
			slog.WarnContext(ctx, "migration changed since it was applied; applying it again", "service", service, "migration", name)
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read %s migration record %s: %w", service, name, err)
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply %s migration %s: %w", service, name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO oryxa_migrations (service, name, checksum) VALUES ($1, $2, $3)
			ON CONFLICT (service, name) DO UPDATE SET checksum = EXCLUDED.checksum, applied_at = now()`,
			service, name, checksum); err != nil {
			return fmt.Errorf("record %s migration %s: %w", service, name, err)
		}
		return nil
	})
}
