package postgres

import (
	"context"
	"fmt"
	"time"
)

// DeletePublishedOutbox removes up to limit events that reached the bus before
// the given time. internal/outbox's dispatcher now sweeps connections_outbox
// itself, so nothing in this package calls this any more; it remains only
// because connections/internal/application.Store still declares it, outside
// this change's scope. Deleting it here needs that interface trimmed too.
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
