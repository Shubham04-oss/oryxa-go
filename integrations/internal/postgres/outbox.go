package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func (s *Store) ClaimOutbox(ctx context.Context, limit int) ([]domain.OutboxEvent, error) {
	rows, err := s.pool.Query(ctx, `
		WITH picked AS (
			SELECT id FROM integrations_outbox
			WHERE published_at IS NULL AND available_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY occurred_at, id FOR UPDATE SKIP LOCKED LIMIT $1
		)
		UPDATE integrations_outbox AS event
		SET locked_until = now() + interval '30 seconds'
		FROM picked WHERE event.id = picked.id
		RETURNING event.id, event.source, event.detail_type, event.version,
		          event.organization_id, event.detail, event.occurred_at, event.attempts`, limit)
	if err != nil {
		return nil, fmt.Errorf("claim integrations outbox: %w", err)
	}
	defer rows.Close()
	result := make([]domain.OutboxEvent, 0)
	for rows.Next() {
		var event domain.OutboxEvent
		var detail json.RawMessage
		if err := rows.Scan(&event.EventID, &event.Source, &event.DetailType, &event.Version,
			&event.OrganizationID, &detail, &event.OccurredAt, &event.Attempts); err != nil {
			return nil, fmt.Errorf("scan integrations outbox: %w", err)
		}
		event.Data = detail
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate integrations outbox: %w", err)
	}
	return result, nil
}

func (s *Store) MarkOutboxPublished(ctx context.Context, eventID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE integrations_outbox
		SET published_at = now(), locked_until = NULL, last_error = NULL WHERE id = $1`, eventID)
	if err != nil {
		return fmt.Errorf("mark integrations outbox published: %w", err)
	}
	return nil
}

func (s *Store) MarkOutboxFailed(ctx context.Context, eventID string, retryAfter time.Duration, message string) error {
	_, err := s.pool.Exec(ctx, `UPDATE integrations_outbox
		SET attempts = attempts + 1,
		    available_at = now() + ($2 * interval '1 millisecond'),
		    locked_until = NULL, last_error = left($3, 1000)
		WHERE id = $1`, eventID, retryAfter.Milliseconds(), message)
	if err != nil {
		return fmt.Errorf("mark integrations outbox failed: %w", err)
	}
	return nil
}
