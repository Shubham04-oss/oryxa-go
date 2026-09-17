package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// outboxClaimTTL is how long one claim on an outbox event lasts. It is far
// longer than a batch should take, because a claim that expires while its
// dispatcher is still publishing lets another replica publish the same event
// again; the claim token is what keeps the stale dispatcher from then marking
// a row it no longer owns.
const outboxClaimTTL = 2 * time.Minute

// ClaimOutbox hands out up to limit published-pending events to one dispatcher,
// oldest first, stamped with token. Events another dispatcher is working
// under a live claim are skipped.
func (store *Store) ClaimOutbox(ctx context.Context, limit int, token string) ([]domain.OutboxEvent, error) {
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
		SET locked_until = now() + ($2 * interval '1 millisecond'), claim_token = $3
		FROM picked
		WHERE event.id = picked.id
		RETURNING event.id, event.detail_type, event.detail, event.occurred_at, event.attempts, event.claim_token`,
		limit, outboxClaimTTL.Milliseconds(), token)
	if err != nil {
		return nil, fmt.Errorf("claim outbox: %w", err)
	}
	defer rows.Close()
	events := []domain.OutboxEvent{}
	for rows.Next() {
		var event domain.OutboxEvent
		var detail []byte
		if err := rows.Scan(&event.ID, &event.DetailType, &detail, &event.OccurredAt, &event.Attempts, &event.ClaimToken); err != nil {
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

// MarkOutboxPublished records that an event reached the bus. A token that no
// longer owns the row, because the claim expired and another dispatcher took
// it, changes nothing and reports false.
func (store *Store) MarkOutboxPublished(ctx context.Context, eventID, token string) (bool, error) {
	tag, err := store.pool.Exec(ctx, `
		UPDATE connections_outbox
		SET published_at = now(), locked_until = NULL, claim_token = NULL, last_error = NULL
		WHERE id = $1 AND claim_token = $2`, eventID, token)
	if err != nil {
		return false, fmt.Errorf("mark outbox published: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkOutboxFailed sends an event back for retry. A token that no longer owns
// the row changes nothing and reports false.
func (store *Store) MarkOutboxFailed(ctx context.Context, eventID, token string, retryAfter time.Duration, message string) (bool, error) {
	tag, err := store.pool.Exec(ctx, `
		UPDATE connections_outbox
		SET attempts = attempts + 1,
		    available_at = now() + ($3 * interval '1 millisecond'),
		    locked_until = NULL,
		    claim_token = NULL,
		    last_error = left($4, 1000)
		WHERE id = $1 AND claim_token = $2`, eventID, token, retryAfter.Milliseconds(), message)
	if err != nil {
		return false, fmt.Errorf("mark outbox failed: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// DeletePublishedOutbox removes up to limit events that reached the bus before
// the given time. Nothing reads a published row again, so they are kept only
// briefly to explain recent deliveries; without this the table grows once per
// connection change, for ever.
func (store *Store) DeletePublishedOutbox(ctx context.Context, before time.Time, limit int) (int, error) {
	tag, err := store.pool.Exec(ctx, `
		DELETE FROM connections_outbox
		WHERE id IN (
			SELECT id FROM connections_outbox WHERE published_at IS NOT NULL AND published_at < $1 LIMIT $2
		)`, before, limit)
	if err != nil {
		return 0, fmt.Errorf("delete published outbox events: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
