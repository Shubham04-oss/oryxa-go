package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// querier is what both a pool and a transaction answer, so a statement reads
// the same inside one as outside.
type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (store *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func insertEvents(ctx context.Context, tx pgx.Tx, events ...domain.Event) error {
	// The writing transaction's trace, if it has one, travels with the event
	// from here on: the dispatcher's publisher links its producer span back
	// to it, and a consumer becomes that producer span's child.
	traceContext := observability.CaptureTraceCarrier(ctx).Encode()
	for _, event := range events {
		if event.ID == "" || event.DetailType == "" || event.OccurredAt.IsZero() {
			return errors.New("event id, detail type and occurrence time are required")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO apps_outbox (id, detail_type, detail, occurred_at, trace_context)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''))`, event.ID, event.DetailType, []byte(event.Data), event.OccurredAt, traceContext); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
	}
	return nil
}

func mapWriteError(operation string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%s: %w", operation, domain.ErrConflict)
		case "23503":
			return fmt.Errorf("%s: %w", operation, domain.ErrNotFound)
		case "23514":
			return fmt.Errorf("%s breaks a rule: %w", operation, domain.ErrInvalid)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// nullable turns an empty string into SQL NULL, for an optional text column.
func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// text reads an optional text column back as "" rather than a nil pointer.
func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
