package events

import (
	"context"
	"fmt"

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
	}
	for _, statement := range statements {
		if _, err := outbox.database.SQLX.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure agents outbox: %w", err)
		}
	}
	return nil
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
	for _, event := range events {
		if event.ID == "" || event.DetailType == "" || event.OccurredAt.IsZero() {
			return fmt.Errorf("outbox event %q needs an id, detail type and occurrence time", event.DetailType)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+schema+`.agents_outbox (id, detail_type, detail, occurred_at)
			VALUES ($1, $2, $3, $4)`, event.ID, event.DetailType, event.Data, event.OccurredAt); err != nil {
			return fmt.Errorf("insert outbox event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit outbox transaction: %w", err)
	}
	return nil
}
