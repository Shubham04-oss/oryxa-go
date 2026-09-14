package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	pool     *pgxpool.Pool
	db       *sqlx.DB
	database *postgreskit.Database
}

func Open(ctx context.Context, databaseURL string, requiredExtensions ...string) (*Store, error) {
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-files", RequiredExtensions: requiredExtensions,
	})
	if err != nil {
		return nil, fmt.Errorf("open files postgres: %w", err)
	}
	return &Store{pool: database.Pool, db: database.SQLX, database: database}, nil
}

func (store *Store) Close() {
	store.database.Close()
}

func (store *Store) ExtensionVersions() map[string]string { return store.database.ExtensionVersions() }

func (store *Store) Migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list files migrations: %w", err)
	}
	slices.Sort(names)
	for _, name := range names {
		contents, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read files migration %s: %w", name, err)
		}
		if _, err := store.db.ExecContext(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply files migration %s: %w", name, err)
		}
	}
	return nil
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
	return tx.Commit()
}

func insertEvents(ctx context.Context, ext execer, events []domain.Event) error {
	for _, event := range events {
		if _, err := ext.ExecContext(ctx, `
			INSERT INTO files_outbox (id, detail_type, detail, occurred_at)
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
