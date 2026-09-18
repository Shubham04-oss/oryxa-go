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

// RecordTriggerDelivery is RecordWebhook for a delivery a trigger made, and
// counts it against that trigger in the same transaction. So a trigger's
// tally and the events published for it can never disagree, and a
// redelivery - which writes nothing at all - cannot count twice.
//
// The trigger is counted only while it is live: one removed between the
// delivery arriving and this running is no longer anyone's, and the event
// for it has already been decided against what it was.
func (store *Store) RecordTriggerDelivery(ctx context.Context, eventID, eventType, triggerID string, now time.Time, event domain.Event) (bool, error) {
	recorded := false
	err := store.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO apps_handled_events (event_id, detail_type, handled_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (event_id) DO NOTHING`, eventID, eventType, now)
		if err != nil {
			return fmt.Errorf("record trigger delivery: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return nil
		}
		recorded = true
		if _, err := tx.Exec(ctx, `
			UPDATE apps_triggers
			SET delivery_count = delivery_count + 1, last_delivery_at = $2
			WHERE id = $1 AND status <> 'removed'`, triggerID, now); err != nil {
			return fmt.Errorf("count a trigger's delivery: %w", err)
		}
		return insertEvents(ctx, tx, event)
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
