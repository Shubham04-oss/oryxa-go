package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

type outboxStore interface {
	ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error)
	MarkOutboxPublished(context.Context, string) error
	MarkOutboxFailed(context.Context, string, time.Duration, string) error
}

type eventPublisher interface {
	PublishEvent(context.Context, domain.Event) error
}

type Dispatcher struct {
	store     outboxStore
	publisher eventPublisher
	logger    *slog.Logger
	interval  time.Duration
	batchSize int
}

func NewDispatcher(store outboxStore, publisher eventPublisher, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{store: store, publisher: publisher, logger: logger, interval: time.Second, batchSize: 20}
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
			dispatcher.logger.ErrorContext(ctx, "claim rag outbox failed", "error", err)
		}
		return
	}
	for _, event := range events {
		if err := dispatcher.publisher.PublishEvent(ctx, event.Event); err != nil {
			dispatcher.logger.ErrorContext(ctx, "publish rag event failed", "event_id", event.ID, "detail_type", event.DetailType, "error", err)
			if markErr := dispatcher.store.MarkOutboxFailed(ctx, event.ID, retryDelay(event.Attempts), err.Error()); markErr != nil {
				dispatcher.logger.ErrorContext(ctx, "mark rag event failed", "event_id", event.ID, "error", markErr)
			}
			continue
		}
		if err := dispatcher.store.MarkOutboxPublished(ctx, event.ID); err != nil {
			dispatcher.logger.ErrorContext(ctx, "mark rag event published failed", "event_id", event.ID, "error", err)
		}
	}
}

func retryDelay(attempts int) time.Duration {
	return time.Second * time.Duration(1<<min(max(attempts, 0), 6))
}
