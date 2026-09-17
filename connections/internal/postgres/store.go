package postgres

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Replicas that start together must not install the schema at the same time;
// IF NOT EXISTS alone does not serialize catalog changes.
const migrationsAdvisoryLock int64 = 7263510498

type Store struct {
	pool     *pgxpool.Pool
	database *postgreskit.Database
	// borrowed is a pool someone else opened and will close.
	borrowed bool
}

// Open opens a pool of connections' own, which Close closes.
func Open(ctx context.Context, databaseURL string, requiredExtensions ...string) (*Store, error) {
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-connections", RequiredExtensions: requiredExtensions,
	})
	if err != nil {
		return nil, fmt.Errorf("open connections postgres: %w", err)
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

// Migrate applies each migration in name order, once while it stays unchanged,
// recording what it applied; see postgreskit's Migrate.
func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "connections", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}
