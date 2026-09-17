package postgres

import (
	"context"
	"fmt"
	"time"
)

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
