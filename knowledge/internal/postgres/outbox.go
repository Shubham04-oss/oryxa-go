package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

func (store *Store) ClaimOutbox(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	rows, err := store.pool.Query(ctx, `
		WITH picked AS (
			SELECT id
			FROM knowledge_outbox
			WHERE published_at IS NULL
			  AND available_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY occurred_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE knowledge_outbox AS event
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
		UPDATE knowledge_outbox
		SET published_at = now(), locked_until = NULL, last_error = NULL
		WHERE id = $1`, eventID); err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	return nil
}

func (store *Store) MarkOutboxFailed(ctx context.Context, eventID string, retryAfter time.Duration, message string) error {
	if _, err := store.pool.Exec(ctx, `
		UPDATE knowledge_outbox
		SET attempts = attempts + 1,
		    available_at = now() + ($2 * interval '1 millisecond'),
		    locked_until = NULL,
		    last_error = left($3, 1000)
		WHERE id = $1`, eventID, retryAfter.Milliseconds(), message); err != nil {
		return fmt.Errorf("mark outbox failed: %w", err)
	}
	return nil
}

// EventHandled reports whether an event has already been recorded as
// handled, without recording anything itself.
func (store *Store) EventHandled(ctx context.Context, eventID string) (bool, error) {
	var handled bool
	if err := store.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM knowledge_handled_events WHERE event_id = $1)`, eventID,
	).Scan(&handled); err != nil {
		return false, fmt.Errorf("check event handled: %w", err)
	}
	return handled, nil
}

// MarkEventHandled remembers an event whose handler has already succeeded.
// The bus delivers at least once, so two redeliveries can race here; the
// later insert is silently ignored rather than treated as an error, because
// the handler it followed is idempotent under that race.
func (store *Store) MarkEventHandled(ctx context.Context, eventID, detailType string, now time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO knowledge_handled_events (event_id, detail_type, handled_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO NOTHING`, eventID, detailType, now); err != nil {
		return fmt.Errorf("mark event handled: %w", err)
	}
	return nil
}

// DeleteHandledEventsBefore forgets events old enough that no redelivery can
// still be in flight.
func (store *Store) DeleteHandledEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := store.pool.Exec(ctx, `DELETE FROM knowledge_handled_events WHERE handled_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("forget handled events: %w", err)
	}
	return tag.RowsAffected(), nil
}
