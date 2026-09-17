// Package postgres is knowledge's store: the sources an organization
// registered, the documents extracted from them, where each run stopped, and
// the outbox that carries what changed onto the bus.
package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Replicas that start together must not install the schema at the same time;
// IF NOT EXISTS alone does not serialize catalog changes.
const migrationsAdvisoryLock int64 = 7263510500

type Store struct {
	pool     *pgxpool.Pool
	database *postgreskit.Database
}

func Open(ctx context.Context, databaseURL string, requiredExtensions ...string) (*Store, error) {
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-knowledge", RequiredExtensions: requiredExtensions,
	})
	if err != nil {
		return nil, fmt.Errorf("open knowledge postgres: %w", err)
	}
	return &Store{pool: database.Pool, database: database}, nil
}

func (store *Store) Close() { store.database.Close() }

func (store *Store) ExtensionVersions() map[string]string { return store.database.ExtensionVersions() }

func (store *Store) Migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list knowledge migrations: %w", err)
	}
	slices.Sort(names)
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin knowledge migrations: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationsAdvisoryLock); err != nil {
		return fmt.Errorf("lock knowledge migrations: %w", err)
	}
	for _, name := range names {
		contents, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read knowledge migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply knowledge migration %s: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit knowledge migrations: %w", err)
	}
	return nil
}
