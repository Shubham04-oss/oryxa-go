package postgres

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

func (store *Store) RecordEvents(ctx context.Context, events []domain.Event) error {
	if len(events) == 0 {
		return nil
	}
	return store.inTx(ctx, func(tx *sqlx.Tx) error {
		return insertEvents(ctx, tx, events)
	})
}
