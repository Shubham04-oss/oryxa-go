// Package postgres is what rag keeps in the shared PostgreSQL: the corpora,
// the documents and the text they were chunked from, the chunks themselves,
// and the outbox it publishes from.
//
// The chunk text lives here whichever backend holds the vectors. That is what
// keeps one answer to what a chunk says, and it is what lets a citation point
// at a byte range of a document that is still on disk rather than at something
// only an index remembers.
package postgres

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	pool     *pgxpool.Pool
	db       *sqlx.DB
	database *postgreskit.Database
}

// Open connects and checks the capabilities this service cannot work without.
// pgvector is required whatever backend a corpus names, because the chunk
// tables and the vector tables are created together.
func Open(ctx context.Context, databaseURL string, requiredExtensions ...string) (*Store, error) {
	if !slices.Contains(requiredExtensions, "vector") && !slices.Contains(requiredExtensions, "pgvector") {
		requiredExtensions = append(requiredExtensions, "vector")
	}
	database, err := postgreskit.Open(ctx, postgreskit.Config{
		URL: databaseURL, ApplicationName: "oryxa-rag", RequiredExtensions: requiredExtensions,
	})
	if err != nil {
		return nil, fmt.Errorf("open rag postgres: %w", err)
	}
	return &Store{pool: database.Pool, db: database.SQLX, database: database}, nil
}

func (store *Store) Close() {
	store.database.Close()
}

func (store *Store) ExtensionVersions() map[string]string { return store.database.ExtensionVersions() }

// PgvectorVersion is what the vector store needs to decide whether the
// features it wants are there.
func (store *Store) PgvectorVersion() string { return store.database.ExtensionVersions()["vector"] }

// Pool exposes the native pool so the pgvector store can use the binary
// protocol for vectors rather than sending them as very long strings.
func (store *Store) Pool() *pgxpool.Pool { return store.pool }

func (store *Store) Migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list rag migrations: %w", err)
	}
	slices.Sort(names)
	for _, name := range names {
		contents, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read rag migration %s: %w", name, err)
		}
		if _, err := store.db.ExecContext(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply rag migration %s: %w", name, err)
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
			INSERT INTO rag_outbox (id, detail_type, detail, occurred_at)
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

// encodeMetadata renders metadata for a jsonb column. Nil becomes an empty
// object rather than SQL NULL, so a filter never has to allow for both.
func encodeMetadata(metadata domain.Metadata) ([]byte, error) {
	if metadata == nil {
		metadata = domain.Metadata{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode metadata: %w", err)
	}
	return encoded, nil
}

func decodeMetadata(raw []byte) (domain.Metadata, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var metadata domain.Metadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	if len(metadata) == 0 {
		return nil, nil
	}
	return metadata, nil
}
