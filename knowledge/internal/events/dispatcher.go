package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

type outboxStore interface {
	ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error)
	MarkOutboxPublished(context.Context, string) error
	MarkOutboxFailed(context.Context, string, time.Duration, string) error
}

// Sink is where a dispatched event goes.
//
// It is an interface rather than the EventBridge publisher itself because
// that choice is not the outbox's business. Today it is the bus; in a build
// where the retrieval pipeline shares this process, it is that pipeline, and
// the durability, ordering and retry above it are identical either way.
type Sink interface {
	PublishEvent(context.Context, domain.Event) error
}

// Dispatcher publishes the outbox: events written in the same transaction as
// the change they describe.
type Dispatcher struct {
	store     outboxStore
	sink      Sink
	logger    *slog.Logger
	interval  time.Duration
	batchSize int
}

func NewDispatcher(store outboxStore, sink Sink, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Dispatcher{store: store, sink: sink, logger: logger, interval: time.Second, batchSize: 20}
}

func (dispatcher *Dispatcher) Run(ctx context.Context) {
	dispatcher.flush(ctx)
	ticker := time.NewTicker(dispatcher.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatcher.flush(ctx)
		}
	}
}

func (dispatcher *Dispatcher) flush(ctx context.Context) {
	events, err := dispatcher.store.ClaimOutbox(ctx, dispatcher.batchSize)
	if err != nil {
		if ctx.Err() == nil {
			dispatcher.logger.ErrorContext(ctx, "claim knowledge outbox failed", "error", err)
		}
		return
	}
	for _, event := range events {
		if err := dispatcher.sink.PublishEvent(ctx, event.Event); err != nil {
			dispatcher.logger.ErrorContext(ctx, "publish knowledge event failed",
				"event_id", event.ID, "detail_type", event.DetailType, "error", err)
			if markErr := dispatcher.store.MarkOutboxFailed(ctx, event.ID, retryDelay(event.Attempts), err.Error()); markErr != nil {
				dispatcher.logger.ErrorContext(ctx, "mark knowledge event failed", "event_id", event.ID, "error", markErr)
			}
			continue
		}
		if err := dispatcher.store.MarkOutboxPublished(ctx, event.ID); err != nil {
			dispatcher.logger.ErrorContext(ctx, "mark knowledge event published failed", "event_id", event.ID, "error", err)
		}
	}
}

func retryDelay(attempts int) time.Duration {
	return time.Second * time.Duration(1<<min(max(attempts, 0), 6))
}
