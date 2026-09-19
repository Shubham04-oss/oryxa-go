package outbox

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// Store is what a Dispatcher needs from an outbox table. *Table is one.
type Store interface {
	Name() string
	Claim(ctx context.Context, limit int, ttl time.Duration, token string) ([]Event, error)
	MarkPublished(ctx context.Context, eventID, token string) (bool, error)
	MarkFailed(ctx context.Context, eventID, token string, retryAfter time.Duration, message string) (bool, error)
	Release(ctx context.Context, eventIDs []string, token string) error
	DeletePublished(ctx context.Context, before time.Time, limit int) (int, error)
}

// Publisher sends one event on to wherever its service publishes.
type Publisher interface {
	Publish(context.Context, Event) error
}

// PublisherFunc is a function that is a Publisher.
type PublisherFunc func(context.Context, Event) error

// Publish calls publish.
func (publish PublisherFunc) Publish(ctx context.Context, event Event) error {
	return publish(ctx, event)
}

// Dispatcher publishes an outbox table's events and forgets them once they
// are old enough.
type Dispatcher struct {
	store     Store
	publisher Publisher
	logger    *slog.Logger
	metrics   observability.OutboxMetrics
	now       func() time.Time

	interval time.Duration
	batch    int
	// workers bounds how many events of one batch publish at once, which keeps
	// a claim short.
	workers int
	// claimTTL is far longer than a batch should take: a claim that expires
	// while its dispatcher is still publishing lets another replica publish
	// the same event again, and the bus is only at-least-once.
	claimTTL time.Duration
	// grace is how long work already under way may continue once the
	// dispatcher is told to stop, so an event it was publishing is marked
	// rather than left claimed until the claim expires.
	grace time.Duration

	retention  time.Duration
	sweepEvery time.Duration
	sweepBatch int
}

// NewDispatcher publishes store's events through publisher.
func NewDispatcher(store Store, publisher Publisher, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger = logger.With("table", store.Name())
	metrics, err := observability.NewOutboxMetrics()
	if err != nil {
		// The zero OutboxMetrics is a safe, silent no-op (see its Record
		// doc): publishing must not fail just because its own metrics could
		// not be created, which in practice only a broken instrument name
		// could cause.
		logger.Warn("outbox metrics unavailable; publishing continues without them", "error", err)
	}
	return &Dispatcher{
		store:     store,
		publisher: publisher,
		logger:    logger,
		metrics:   metrics,
		now:       time.Now,
		interval:  time.Second,
		batch:     20,
		workers:   8,
		claimTTL:  2 * time.Minute,
		grace:     5 * time.Second,
		// Nothing reads a published event again; it is kept only to explain
		// recent deliveries.
		retention:  7 * 24 * time.Hour,
		sweepEvery: time.Hour,
		sweepBatch: 500,
	}
}

// Run publishes due events every second, and forgets old published ones every
// hour, until ctx is cancelled.
func (dispatcher *Dispatcher) Run(ctx context.Context) {
	dispatcher.flush(ctx)
	dispatcher.sweep(ctx)
	lastSweep := dispatcher.now()
	ticker := time.NewTicker(dispatcher.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatcher.flush(ctx)
			if dispatcher.now().Sub(lastSweep) >= dispatcher.sweepEvery {
				dispatcher.sweep(ctx)
				lastSweep = dispatcher.now()
			}
		}
	}
}

// flush claims a batch under one token and publishes it. Every mark carries
// that token, so if the claim expired mid-batch and another replica took an
// event over, these marks change nothing and the other replica's result
// stands.
func (dispatcher *Dispatcher) flush(ctx context.Context) {
	token, err := claimToken()
	if err != nil {
		dispatcher.logger.ErrorContext(ctx, "make outbox claim token failed", "error", err)
		return
	}
	events, err := dispatcher.store.Claim(ctx, dispatcher.batch, dispatcher.claimTTL, token)
	if err != nil {
		if ctx.Err() == nil {
			dispatcher.logger.ErrorContext(ctx, "claim outbox events failed", "error", err)
		}
		return
	}
	if len(events) == 0 {
		return
	}
	// The claim hands events back in no particular order; with one worker
	// they still go out oldest first.
	slices.SortFunc(events, func(a, b Event) int {
		return cmp.Or(a.OccurredAt.Compare(b.OccurredAt), cmp.Compare(a.ID, b.ID))
	})

	work, finish := outlive(ctx, dispatcher.grace)
	defer finish()
	queue := make(chan Event)
	var group sync.WaitGroup
	for range max(1, min(dispatcher.workers, len(events))) {
		group.Go(func() {
			for event := range queue {
				dispatcher.publish(work, event, token)
			}
		})
	}
	for index, event := range events {
		select {
		case queue <- event:
		case <-ctx.Done():
			close(queue)
			group.Wait()
			dispatcher.release(work, events[index:], token)
			return
		}
	}
	close(queue)
	group.Wait()
}

func (dispatcher *Dispatcher) publish(ctx context.Context, event Event, token string) {
	started := dispatcher.now()
	// The trace context travelling with the event, if there is one, is
	// carried in event.TraceContext for the publisher itself to decode and
	// link its producer span to: internal/outbox stays agnostic of what
	// "publish" means for any given service (EventBridge here, potentially
	// something else for a service that has not adopted it yet), so it is
	// not this package's place to start that span.
	err := dispatcher.publisher.Publish(ctx, event)
	dispatcher.metrics.Record(ctx, dispatcher.store.Name(), event.DetailType, event.OccurredAt, started, err)
	if err != nil {
		dispatcher.logger.ErrorContext(ctx, "publish outbox event failed", "event_id", event.ID, "detail_type", event.DetailType, "error", err)
		marked, markErr := dispatcher.store.MarkFailed(ctx, event.ID, token, retryDelay(event.Attempts), err.Error())
		dispatcher.reportMark(ctx, event, marked, markErr, "mark outbox event for retry failed")
		return
	}
	marked, err := dispatcher.store.MarkPublished(ctx, event.ID, token)
	dispatcher.reportMark(ctx, event, marked, err, "mark outbox event published failed")
}

func (dispatcher *Dispatcher) reportMark(ctx context.Context, event Event, marked bool, err error, failure string) {
	switch {
	case err != nil:
		dispatcher.logger.ErrorContext(ctx, failure, "event_id", event.ID, "error", err)
	case !marked:
		dispatcher.logger.WarnContext(ctx, "outbox event was claimed again before it could be marked", "event_id", event.ID)
	}
}

// release hands back what a stopping dispatcher never started, so the next
// dispatcher need not wait out this one's claim.
func (dispatcher *Dispatcher) release(ctx context.Context, events []Event, token string) {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	if err := dispatcher.store.Release(ctx, ids, token); err != nil {
		dispatcher.logger.WarnContext(ctx, "release unpublished outbox events failed", "count", len(ids), "error", err)
	}
}

// sweep deletes published events older than the retention, a batch at a
// time, until a batch comes back short.
func (dispatcher *Dispatcher) sweep(ctx context.Context) {
	before := dispatcher.now().Add(-dispatcher.retention)
	for ctx.Err() == nil {
		deleted, err := dispatcher.store.DeletePublished(ctx, before, dispatcher.sweepBatch)
		if err != nil {
			if ctx.Err() == nil {
				dispatcher.logger.WarnContext(ctx, "delete published outbox events failed", "error", err)
			}
			return
		}
		if deleted < dispatcher.sweepBatch {
			return
		}
	}
}

// outlive returns a context that stays alive for grace after ctx is
// cancelled, for work that should finish briefly rather than stop mid-write.
func outlive(ctx context.Context, grace time.Duration) (context.Context, context.CancelFunc) {
	work, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() { time.AfterFunc(grace, cancel) })
	return work, func() {
		stop()
		cancel()
	}
}

// claimToken names one claim on a batch. It is only compared for equality,
// so any unguessable value does.
func claimToken() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// retryDelay doubles from a second up to about a minute.
func retryDelay(attempts int) time.Duration {
	return time.Second * time.Duration(1<<min(max(attempts, 0), 6))
}
