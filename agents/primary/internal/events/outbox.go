package events

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// schema is the PostgreSQL schema agents primary owns; ADK sessions and the
// events outbox share it.
const schema = "agents_primary"

// Outbox stores events until the dispatcher publishes them. Rows are written
// with the state change they describe, so a crash between the two loses
// nothing.
type Outbox struct {
	database *postgreskit.Database
}

func NewOutbox(database *postgreskit.Database) *Outbox {
	return &Outbox{database: database}
}

// Ensure creates the outbox table and its pending index when they are missing.
func (outbox *Outbox) Ensure(ctx context.Context) error {
	statements := []string{
		`CREATE SCHEMA IF NOT EXISTS ` + schema,
		`CREATE TABLE IF NOT EXISTS ` + schema + `.agents_outbox (
			id text PRIMARY KEY,
			detail_type text NOT NULL,
			detail jsonb NOT NULL,
			occurred_at timestamptz NOT NULL,
			attempts integer NOT NULL DEFAULT 0,
			available_at timestamptz NOT NULL DEFAULT now(),
			locked_until timestamptz,
			published_at timestamptz,
			last_error text
		)`,
		`CREATE INDEX IF NOT EXISTS agents_outbox_pending_idx
			ON ` + schema + `.agents_outbox (available_at, occurred_at, id)
			WHERE published_at IS NULL`,
		// A claim is now owned, by internal/outbox: it stamps the rows it hands
		// out with a token, and marks published or failed only touch a row that
		// still carries the same token. Without it a dispatcher whose claim
		// expired mid-batch could mark a row another replica had since
		// re-claimed, clearing that claim's lock while it was still publishing.
		`ALTER TABLE ` + schema + `.agents_outbox ADD COLUMN IF NOT EXISTS claim_token text`,
		// The event pipeline now carries a W3C trace context end to end: an
		// outbox row records the trace of the transaction that wrote it, so the
		// dispatcher's publisher can link its producer span back to that write,
		// and a consumer can become its child. NULL means the write carried no
		// span, which is ordinary for anything that runs unsampled or outside a
		// traced request.
		`ALTER TABLE ` + schema + `.agents_outbox ADD COLUMN IF NOT EXISTS trace_context text`,
	}
	exec := func(ctx context.Context, statement string) error {
		_, err := outbox.database.SQLX.ExecContext(ctx, statement)
		return err
	}
	for _, statement := range statements {
		if err := ensureStatement(ctx, exec, statement); err != nil {
			return fmt.Errorf("ensure agents outbox: %w", err)
		}
	}
	return nil
}

// ensureStatement runs one of Ensure's statements, each made idempotent by IF
// NOT EXISTS. PostgreSQL does not make IF NOT EXISTS safe against itself: two
// sessions that both find an object missing both create it, and the one that
// commits second is refused by the catalog's own unique index, as a duplicate
// schema, table, index, type or column, or as the unique violation beneath
// them. Another replica starting, or another test package on the same
// database, ensuring the outbox, or migrating the run store that also creates
// it, at the same moment is such a session. The object then exists, made by
// the other one, so the statement is run once more, and this time IF NOT
// EXISTS finds it. Any other failure, or a second one, is returned.
func ensureStatement(ctx context.Context, exec func(context.Context, string) error, statement string) error {
	err := exec(ctx, statement)
	if createdConcurrently(err) {
		err = exec(ctx, statement)
	}
	return err
}

// createdConcurrently is whether err is PostgreSQL refusing to create an object
// that another session created at the same moment.
func createdConcurrently(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "42P06", // duplicate_schema
		"42P07", // duplicate_table, which an index is as well
		"42710", // duplicate_object, as the table's row type
		"42701", // duplicate_column
		"23505": // unique_violation, on the catalog's unique index
		return true
	}
	return false
}

// RecordEvents writes events in one transaction.
func (outbox *Outbox) RecordEvents(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := outbox.database.SQLX.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin outbox transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()
	// The writing transaction's trace, if it has one, travels with the event
	// from here on: the dispatcher's publisher links its producer span back
	// to it, and a consumer becomes that producer span's child.
	traceContext := observability.CaptureTraceCarrier(ctx).Encode()
	for _, event := range events {
		if event.ID == "" || event.DetailType == "" || event.OccurredAt.IsZero() {
			return fmt.Errorf("outbox event %q needs an id, detail type and occurrence time", event.DetailType)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+schema+`.agents_outbox (id, detail_type, detail, occurred_at, trace_context)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''))`, event.ID, event.DetailType, event.Data, event.OccurredAt, traceContext); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit outbox transaction: %w", err)
	}
	return nil
}
