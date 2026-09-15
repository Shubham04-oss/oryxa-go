package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func (store *Store) ClaimOutbox(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	rows, err := store.pool.Query(ctx, `
		WITH picked AS (
			SELECT id
			FROM connections_outbox
			WHERE published_at IS NULL
			  AND available_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY occurred_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE connections_outbox AS event
		SET locked_until = now() + interval '30 seconds'
		FROM picked
		WHERE event.id = picked.id
		RETURNING event.id, event.detail_type, event.detail, event.occurred_at, event.attempts`, limit)
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	defer rows.Close()
	events := []domain.OutboxEvent{}
	for rows.Next() {
		var event domain.OutboxEvent
		var detail []byte
		if err := rows.Scan(&event.ID, &event.DetailType, &detail, &event.OccurredAt, &event.Attempts); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		event.Data = json.RawMessage(detail)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	return events, nil
}

func (store *Store) MarkOutboxPublished(ctx context.Context, eventID string) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE connections_outbox
		SET published_at = now(), locked_until = NULL, last_error = NULL
		WHERE id = $1`, eventID); err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	return nil
}

func (store *Store) MarkOutboxFailed(ctx context.Context, eventID string, retryAfter time.Duration, message string) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE connections_outbox
		SET attempts = attempts + 1,
		    available_at = now() + ($2 * interval '1 millisecond'),
		    locked_until = NULL,
		    last_error = left($3, 1000)
		WHERE id = $1`, eventID, retryAfter.Milliseconds(), message); err != nil {
		return fmt.Errorf("mark outbox failed: %w", err)
	}
	return nil
}
