package secretshares

import (
	"context"
	"log/slog"
	"time"
)

const (
	// sweepInterval is how often Run deletes the secrets whose time is up. No
	// call depends on it: a view or a revoke refuses an expired secret itself.
	// It is what keeps even the ciphertext from outliving its hour by more than
	// about this long.
	sweepInterval = time.Minute
	// sweepBatch is how many secrets one delete removes at most.
	sweepBatch = 200
)

// Run deletes the expired secrets at once and then every minute, until ctx
// ends. It has the shape of host.Worker, and is one: every replica runs it,
// and two sweeps at the same moment take different rows.
func (store *Store) Run(ctx context.Context) {
	sweepLoop(ctx, sweepInterval, sweepBatch, store.Sweep, store.logger)
}

// sweepLoop calls sweep at once and then every interval until ctx ends. A sweep
// that removed a whole batch is followed by another right away, since there may
// be more; a failure is logged and tried again on the next tick.
func sweepLoop(ctx context.Context, interval time.Duration, batch int, sweep func(context.Context, int) (int, error), logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		removed := 0
		for ctx.Err() == nil {
			count, err := sweep(ctx, batch)
			if err != nil {
				if ctx.Err() == nil {
					logger.ErrorContext(ctx, "delete expired one-time secrets", "error", err)
				}
				break
			}
			removed += count
			if count < batch {
				break
			}
		}
		if removed > 0 {
			logger.InfoContext(ctx, "deleted expired one-time secrets", "deleted", removed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
