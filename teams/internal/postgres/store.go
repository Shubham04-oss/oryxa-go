package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Replicas that start together must not install the schema at the same time;
// IF NOT EXISTS alone does not serialize catalog changes.
const migrationsAdvisoryLock int64 = 7263510405

// Store is teams' PostgreSQL access through the shared postgreskit boundary:
// one instrumented pgx pool, with sqlx on that same pool.
type Store struct {
	pool     *pgxpool.Pool
	db       *sqlx.DB
	database *postgreskit.Database
	// borrowed is a pool someone else opened and will close.
	borrowed bool
}

// Open opens a pool of teams' own, which Close closes.
func Open(ctx context.Context, databaseURL string, requiredExtensions ...string) (*Store, error) {
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-teams", RequiredExtensions: requiredExtensions,
	})
	if err != nil {
		return nil, fmt.Errorf("open teams postgres: %w", err)
	}
	return &Store{pool: database.Pool, db: database.SQLX, database: database}, nil
}

// New uses a pool the caller owns, such as the one a host shares between
// services. Close leaves it open.
func New(database *postgreskit.Database) *Store {
	return &Store{pool: database.Pool, db: database.SQLX, database: database, borrowed: true}
}

func (store *Store) Close() {
	if !store.borrowed {
		store.database.Close()
	}
}

func (store *Store) ExtensionVersions() map[string]string { return store.database.ExtensionVersions() }

// Pool is teams' pool, for internal/outbox's table to claim, mark and sweep
// teams_outbox directly rather than through a copy of that logic here.
func (store *Store) Pool() *pgxpool.Pool { return store.pool }

// Migrate applies each migration in name order, once while it stays unchanged,
// recording what it applied; see postgreskit's Migrate.
func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "teams", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	GetContext(context.Context, any, string, ...any) error
	SelectContext(context.Context, any, string, ...any) error
}

func (store *Store) inTx(ctx context.Context, fn func(*sqlx.Tx) error) error {
	tx, err := store.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func insertEvents(ctx context.Context, ext execer, events []domain.Event) error {
	for _, event := range events {
		if _, err := ext.ExecContext(ctx, `
			INSERT INTO teams_outbox (id, detail_type, detail, occurred_at)
			VALUES ($1, $2, $3, $4)`, event.ID, event.DetailType, event.Data, event.OccurredAt); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
	}
	return nil
}

func mapWriteError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && (postgresError.Code == "23505" || postgresError.Code == "23503") {
		return fmt.Errorf("%s: %w", operation, domain.ErrConflict)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, pgx.ErrNoRows)
}
