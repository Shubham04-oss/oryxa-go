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
const migrationsAdvisoryLock int64 = 7263510499

type Store struct {
	pool     *pgxpool.Pool
	database *postgreskit.Database
	// borrowed is a pool someone else opened and will close.
	borrowed bool
}

// Open opens a pool of integrations' own, which Close closes.
func Open(ctx context.Context, databaseURL string, requiredExtensions ...string) (*Store, error) {
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-integrations", RequiredExtensions: requiredExtensions,
	})
	if err != nil {
		return nil, fmt.Errorf("open integrations postgres: %w", err)
	}
	return &Store{pool: database.Pool, database: database}, nil
}

// New uses a pool the caller owns, such as the one a host shares between
// services. Close leaves it open.
func New(database *postgreskit.Database) *Store {
	return &Store{pool: database.Pool, database: database, borrowed: true}
}

func (store *Store) Close() {
	if !store.borrowed {
		store.database.Close()
	}
}

func (store *Store) ExtensionVersions() map[string]string { return store.database.ExtensionVersions() }

func (store *Store) Migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list integrations migrations: %w", err)
	}
	slices.Sort(names)
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin integrations migrations: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationsAdvisoryLock); err != nil {
		return fmt.Errorf("lock integrations migrations: %w", err)
	}
	for _, name := range names {
		contents, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read integrations migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply integrations migration %s: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit integrations migrations: %w", err)
	}
	return nil
}
