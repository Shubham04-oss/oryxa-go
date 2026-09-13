package events

import (
	"context"
	"log"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

type Outbox interface {
	ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error)
	MarkOutboxPublished(context.Context, string) error
	MarkOutboxFailed(context.Context, string, time.Duration, string) error
}

type EventPublisher interface {
	PublishEvent(context.Context, domain.Event) error
}

type Dispatcher struct {
	Store     Outbox
	Publisher EventPublisher
	Interval  time.Duration
	BatchSize int
}

func (d Dispatcher) Run(ctx context.Context) {
	interval := d.Interval
	if interval == 0 {
		interval = time.Second
	}
	d.flush(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.flush(ctx)
		}
	}
}

func (d Dispatcher) flush(ctx context.Context) {
	batch := d.BatchSize
	if batch == 0 {
		batch = 20
	}
	events, err := d.Store.ClaimOutbox(ctx, batch)
	if err != nil {
		log.Printf("claim integrations outbox: %v", err)
		return
	}
	for _, event := range events {
		if err := d.Publisher.PublishEvent(ctx, event.Event); err != nil {
			delay := retryDelay(event.Attempts)
			if markErr := d.Store.MarkOutboxFailed(ctx, event.EventID, delay, err.Error()); markErr != nil {
				log.Printf("mark integrations event %s failed: %v", event.EventID, markErr)
			}
			continue
		}
		if err := d.Store.MarkOutboxPublished(ctx, event.EventID); err != nil {
			log.Printf("mark integrations event %s published: %v", event.EventID, err)
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
