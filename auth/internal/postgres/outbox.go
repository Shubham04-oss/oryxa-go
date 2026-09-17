package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func (store *Store) EnqueueEvent(ctx context.Context, event domain.Event) error {
	_, err := store.pool.Exec(ctx, `
		INSERT INTO auth_outbox (id, detail_type, detail, occurred_at)
		VALUES ($1, $2, $3, $4)`, event.ID, event.DetailType, event.Data, event.OccurredAt)
	if err != nil {
		return fmt.Errorf("enqueue event: %w", err)
	}
	return nil
}

// ClaimOutbox, MarkOutboxPublished and MarkOutboxFailed below are no longer
// what publishes auth's outbox: auth/service.go now dispatches through
// internal/outbox, which claims and marks with a claim token these do not
// carry. They remain only because auth/internal/application.Store (outside
// this change's scope) still declares them; deleting them here needs that
// interface trimmed too.
func (store *Store) ClaimOutbox(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	rows, err := store.pool.Query(ctx, `
		WITH picked AS (
			SELECT id
			FROM auth_outbox
			WHERE published_at IS NULL
			  AND available_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY occurred_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE auth_outbox AS event
		SET locked_until = now() + interval '30 seconds'
		FROM picked
		WHERE event.id = picked.id
		RETURNING event.id, event.detail_type, event.detail, event.occurred_at, event.attempts`, limit)
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	defer rows.Close()

	events := make([]domain.OutboxEvent, 0)
	for rows.Next() {
		var event domain.OutboxEvent
		if err := rows.Scan(&event.ID, &event.DetailType, &event.Data, &event.OccurredAt, &event.Attempts); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox events: %w", err)
	}
	return events, nil
}

func (store *Store) MarkOutboxPublished(ctx context.Context, eventID string) error {
	_, err := store.pool.Exec(ctx, `
		UPDATE auth_outbox
		SET published_at = now(), locked_until = NULL, last_error = NULL,
		    detail = CASE
		        WHEN detail_type IN (
		            'auth.user.email-verification.delivery-requested.v1',
		            'auth.user.password-reset.delivery-requested.v1',
		            'auth.member.invited.v1'
		        ) THEN '{}'::jsonb
		        ELSE detail
		    END
		WHERE id = $1`, eventID)
	if err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	return nil
}

func (store *Store) MarkOutboxFailed(ctx context.Context, eventID string, retryAfter time.Duration, message string) error {
	_, err := store.pool.Exec(ctx, `
		UPDATE auth_outbox
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
