package application

import (
	"context"
	"time"
)

const (
	// workerInterval is how often a replica looks for sources that are due.
	workerInterval = 30 * time.Second
	// workerBatch is how many a single tick takes on. It is small because a
	// pass over one source can be long, and a replica holding ten of them
	// starves the rest.
	workerBatch = 5
	// LeaseWindow is how long a claim holds before another replica may take
	// the source back. It is short on purpose: a run renews it between pages,
	// so a worker that died is noticed in a window rather than an hour.
	LeaseWindow = 2 * time.Minute

	// handledEventMemory outlasts any redelivery still in flight.
	handledEventMemory = 7 * 24 * time.Hour
	// runMemory is how long a source's history explains what it holds.
	runMemory = 30 * 24 * time.Hour
)

// RunWorkers reads the sources that are due, and forgets what is too old to
// matter, until ctx is cancelled. Work is claimed under leases, so every
// replica may run it.
func (s *Service) RunWorkers(ctx context.Context) {
	ticker := time.NewTicker(workerInterval)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		s.SyncDue(ctx)
		if s.now().Sub(lastPrune) >= time.Hour {
			s.prune(ctx)
			lastPrune = s.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SyncDue claims what is due and reads it. Each source is closed off by Sync
// itself, whether it finished or failed, so a failure here is only worth
// logging.
func (s *Service) SyncDue(ctx context.Context) {
	lease, err := newID("lease")
	if err != nil {
		s.logger.ErrorContext(ctx, "generate a source lease", "error", err)
		return
	}
	now := s.now()
	due, err := s.store.ClaimDueSources(ctx, now, workerBatch, lease, now.Add(LeaseWindow))
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "claim due sources", "error", err)
		}
		return
	}
	for _, source := range due {
		if err := s.Sync(ctx, source, lease); err != nil && ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "read source", "source_id", source.ID, "error", err)
		}
	}
}

func (s *Service) prune(ctx context.Context) {
	now := s.now()
	if _, err := s.store.DeleteHandledEventsBefore(ctx, now.Add(-handledEventMemory)); err != nil && ctx.Err() == nil {
		s.logger.ErrorContext(ctx, "forget handled events", "error", err)
	}
	if _, err := s.store.DeleteRunsBefore(ctx, now.Add(-runMemory)); err != nil && ctx.Err() == nil {
		s.logger.ErrorContext(ctx, "forget old runs", "error", err)
	}
}
