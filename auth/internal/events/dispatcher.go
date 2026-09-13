package events

import (
	"context"
	"log"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
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
	interval  time.Duration
	batchSize int
}

func NewDispatcher(store outboxStore, publisher eventPublisher) *Dispatcher {
	return &Dispatcher{
		store:     store,
		publisher: publisher,
		interval:  time.Second,
		batchSize: 20,
	}
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
		log.Printf("claim auth outbox: %v", err)
		return
	}
	for _, event := range events {
		if err := dispatcher.publisher.PublishEvent(ctx, event.Event); err != nil {
			delay := retryDelay(event.Attempts)
			if markErr := dispatcher.store.MarkOutboxFailed(ctx, event.ID, delay, err.Error()); markErr != nil {
				log.Printf("mark auth event %s failed: %v", event.ID, markErr)
			}
			continue
		}
		if err := dispatcher.store.MarkOutboxPublished(ctx, event.ID); err != nil {
			log.Printf("mark auth event %s published: %v", event.ID, err)
		}
	}
}

func retryDelay(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 6 {
		attempts = 6
	}
	return time.Second * time.Duration(1<<attempts)
}
