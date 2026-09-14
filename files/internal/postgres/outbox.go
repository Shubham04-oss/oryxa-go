package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

type outboxRow struct {
	ID         string          `db:"id"`
	DetailType string          `db:"detail_type"`
	Detail     json.RawMessage `db:"detail"`
	OccurredAt time.Time       `db:"occurred_at"`
	Attempts   int             `db:"attempts"`
}

func (store *Store) RecordEvents(ctx context.Context, events []domain.Event) error {
	if len(events) == 0 {
		return nil
	}
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		return insertEvents(ctx, tx, events)
	})
}

func (store *Store) ClaimOutbox(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	var rows []outboxRow
	err := store.db.SelectContext(ctx, &rows, `
		WITH picked AS (
			SELECT id
			FROM files_outbox
			WHERE published_at IS NULL
			  AND available_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY occurred_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE files_outbox AS event
		SET locked_until = now() + interval '30 seconds'
		FROM picked
		WHERE event.id = picked.id
		RETURNING event.id, event.detail_type, event.detail, event.occurred_at, event.attempts`, limit)
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	events := make([]domain.OutboxEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, domain.OutboxEvent{
			Event: domain.Event{
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

func (store *Store) MarkOutboxPublished(ctx context.Context, eventID string) error {
	_, err := store.db.ExecContext(ctx, `
		UPDATE files_outbox
		SET published_at = now(), locked_until = NULL, last_error = NULL
		WHERE id = $1`, eventID)
	if err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	return nil
}

func (store *Store) MarkOutboxFailed(ctx context.Context, eventID string, retryAfter time.Duration, message string) error {
	_, err := store.db.ExecContext(ctx, `
		UPDATE files_outbox
		SET attempts = attempts + 1,
		    available_at = now() + ($2 * interval '1 millisecond'),
		    locked_until = NULL,
		    last_error = left($3, 1000)
		WHERE id = $1`, eventID, retryAfter.Milliseconds(), message)
	if err != nil {
		return fmt.Errorf("mark outbox failed: %w", err)
	}
	return nil
}
