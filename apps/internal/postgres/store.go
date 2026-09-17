// Package postgres is apps' store: the connections a member made to
// third-party apps, the triggers subscribed on them, the tools run through
// them, and the outbox that carries what changed onto the bus.
package postgres

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Replicas that start together must not install the schema at the same time;
// IF NOT EXISTS alone does not serialize catalog changes.
const migrationsAdvisoryLock int64 = 7263510600

type Store struct {
	pool     *pgxpool.Pool
	database *postgreskit.Database
	// borrowed is a pool someone else opened and will close.
	borrowed bool
}

// Store is apps' application.Store.
var _ application.Store = (*Store)(nil)

// Open opens a pool of apps' own, which Close closes.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-apps",
	})
	if err != nil {
		return nil, fmt.Errorf("open apps postgres: %w", err)
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

// Pool is apps' pool, for internal/outbox's table to claim, mark and sweep
// apps_outbox directly rather than through a copy of that logic here.
func (store *Store) Pool() *pgxpool.Pool { return store.pool }

// Migrate applies each migration in name order, once while it stays unchanged,
// recording what it applied; see postgreskit's Migrate.
func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "apps", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}
