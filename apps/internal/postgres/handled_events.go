package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// RecordWebhook records a webhook delivery as handled together with the
// events it caused, in one transaction, so a redelivery of the same webhook -
// the bus and Composio's own webhooks both promise at least one - can never
// cause its events to be inserted twice.
func (store *Store) RecordWebhook(ctx context.Context, eventID, eventType string, now time.Time, events ...domain.Event) (bool, error) {
	recorded := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO apps_handled_events (event_id, detail_type, handled_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (event_id) DO NOTHING`, eventID, eventType, now)
		if err != nil {
			return fmt.Errorf("record webhook: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		recorded = true
		return insertEvents(ctx, tx, events...)
	})
	return recorded, err
}

// EventHandled reports whether a bus event has already been recorded as
// handled, without recording anything itself.
func (store *Store) EventHandled(ctx context.Context, eventID string) (bool, error) {
	var handled bool
	if err := store.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM apps_handled_events WHERE event_id = $1)`, eventID,
	).Scan(&handled); err != nil {
		return false, fmt.Errorf("check event handled: %w", err)
	}
	return handled, nil
}

// MarkEventHandled remembers a bus event whose handler has already
// succeeded. The bus delivers at least once, so two redeliveries can race
// here; the later insert is silently ignored rather than treated as an
// error, because the handler it followed is idempotent under that race.
func (store *Store) MarkEventHandled(ctx context.Context, eventID, detailType string, now time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO apps_handled_events (event_id, detail_type, handled_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO NOTHING`, eventID, detailType, now); err != nil {
		return fmt.Errorf("mark event handled: %w", err)
	}
	return nil
}

// DeleteHandledEventsBefore forgets deliveries and events old enough that no
// redelivery can still be in flight.
func (store *Store) DeleteHandledEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := store.pool.Exec(ctx, `DELETE FROM apps_handled_events WHERE handled_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("forget handled events: %w", err)
	}
	return tag.RowsAffected(), nil
}
