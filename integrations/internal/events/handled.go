package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// handledRetention is how long a handled event is remembered: as long as a
// signature is accepted, so any delivery the consumer would act on is still
// known when it comes again.
const handledRetention = SignatureMaxAge

// forgetEvery is how often RunForget sweeps.
const forgetEvery = time.Hour

// Handled records the bus events already acted on (migration 007).
type Handled struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	now    func() time.Time
}

// NewHandled returns the record of handled events kept in pool.
func NewHandled(pool *pgxpool.Pool, logger *slog.Logger) *Handled {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Handled{pool: pool, logger: logger, now: time.Now}
}

// AlreadyHandled says whether an event has been acted on. It records nothing.
func (h *Handled) AlreadyHandled(ctx context.Context, eventID string) (bool, error) {
	var handled bool
	err := h.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM integrations.handled_events WHERE event_id = $1)`, eventID).Scan(&handled)
	return handled, err
}

// MarkHandled records that an event's handler succeeded. Recording it again
// changes nothing.
func (h *Handled) MarkHandled(ctx context.Context, eventID, detailType string) error {
	_, err := h.pool.Exec(ctx, `INSERT INTO integrations.handled_events (event_id, detail_type, handled_at)
		VALUES ($1, $2, $3) ON CONFLICT (event_id) DO NOTHING`, eventID, detailType, h.now().UTC())
	return err
}

// Forget drops the events handled before cutoff, answering how many.
func (h *Handled) Forget(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := h.pool.Exec(ctx, `DELETE FROM integrations.handled_events WHERE handled_at < $1`, cutoff)
	return tag.RowsAffected(), err
}

// RunForget forgets events old enough never to be delivered again, hourly,
// until ctx is cancelled.
func (h *Handled) RunForget(ctx context.Context) {
	for {
		if forgotten, err := h.Forget(ctx, h.now().Add(-handledRetention)); err != nil {
			if ctx.Err() == nil {
				h.logger.ErrorContext(ctx, "forget handled events", "error", err)
			}
		} else if forgotten > 0 {
			h.logger.InfoContext(ctx, "handled events forgotten", "count", forgotten)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(forgetEvery):
		}
	}
}
