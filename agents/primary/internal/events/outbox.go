package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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

type outboxRow struct {
	ID         string          `db:"id"`
	DetailType string          `db:"detail_type"`
	Detail     json.RawMessage `db:"detail"`
	OccurredAt time.Time       `db:"occurred_at"`
	Attempts   int             `db:"attempts"`
}

// ClaimOutbox claims up to limit available events, locking them for 30
// seconds so replicas do not publish the same row at once. The dispatcher
// built in cmd/devui/main.go no longer calls this: it claims and marks
// through internal/outbox instead, with a claim token this does not carry.
// This stays because adksession's own integration test reads a session's
// event back through it; deleting it needs that test changed too, outside
// this change's scope.
func (outbox *Outbox) ClaimOutbox(ctx context.Context, limit int) ([]OutboxEvent, error) {
	var rows []outboxRow
	err := outbox.database.SQLX.SelectContext(ctx, &rows, `
		WITH picked AS (
			SELECT id
			FROM `+schema+`.agents_outbox
			WHERE published_at IS NULL
			  AND available_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY occurred_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE `+schema+`.agents_outbox AS event
		SET locked_until = now() + interval '30 seconds'
		FROM picked
		WHERE event.id = picked.id
		RETURNING event.id, event.detail_type, event.detail, event.occurred_at, event.attempts`, limit)
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	events := make([]OutboxEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, OutboxEvent{
			Event: Event{
				ID:         row.ID,
				DetailType: row.DetailType,
				OccurredAt: row.OccurredAt,
				Data:       row.Detail,
			},
			Attempts: row.Attempts,
		})
	}
	return events, nil
}
