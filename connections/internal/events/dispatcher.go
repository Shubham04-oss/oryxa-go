package events

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

type outboxStore interface {
	ClaimOutbox(context.Context, int, string) ([]domain.OutboxEvent, error)
	MarkOutboxPublished(context.Context, string, string) (bool, error)
	MarkOutboxFailed(context.Context, string, string, time.Duration, string) (bool, error)
}

type eventPublisher interface {
	PublishEvent(context.Context, domain.Event) error
}

// Dispatcher publishes the outbox: events written in the same transaction as
// the change they describe.
type Dispatcher struct {
	store     outboxStore
	publisher eventPublisher
	logger    *slog.Logger
	interval  time.Duration
	batchSize int
	// workers bounds how many events of one batch publish at once. Publishing
	// a batch in parallel keeps a claim short: a claim that expires while its
	// dispatcher still works lets another replica publish the same event
	// again, and the bus is only at-least-once.
	workers int
}

func NewDispatcher(store outboxStore, publisher eventPublisher, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{store: store, publisher: publisher, logger: logger, interval: time.Second, batchSize: 20, workers: 8}
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

// flush claims a batch under one token and publishes it. Each event is marked
// with that token, so if the claim expired while the batch was in flight and
// another replica took the event over, these marks change nothing and the
// other replica's result stands.
func (dispatcher *Dispatcher) flush(ctx context.Context) {
	token, err := claimToken()
	if err != nil {
		dispatcher.logger.ErrorContext(ctx, "make connections outbox claim token failed", "error", err)
		return
	}
	events, err := dispatcher.store.ClaimOutbox(ctx, dispatcher.batchSize, token)
	if err != nil {
		if ctx.Err() == nil {
			dispatcher.logger.ErrorContext(ctx, "claim connections outbox failed", "error", err)
		}
		return
	}
	publish := func(event domain.OutboxEvent) {
		err := dispatcher.publisher.PublishEvent(ctx, event.Event)
		if err == nil {
			dispatcher.markPublished(ctx, event, token)
			return
		}
		dispatcher.logger.ErrorContext(ctx, "publish connections event failed", "event_id", event.ID, "detail_type", event.DetailType, "error", err)
		if marked, markErr := dispatcher.store.MarkOutboxFailed(ctx, event.ID, token, retryDelay(event.Attempts), err.Error()); markErr != nil {
			dispatcher.logger.ErrorContext(ctx, "mark connections event failed", "event_id", event.ID, "error", markErr)
		} else if !marked {
			dispatcher.logger.WarnContext(ctx, "connections event was re-claimed before it could be marked", "event_id", event.ID)
		}
	}

	workers := min(max(dispatcher.workers, 1), len(events))
	if workers <= 0 {
		return
	}
	queue := make(chan domain.OutboxEvent)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for event := range queue {
				publish(event)
			}
		}()
	}
	for _, event := range events {
		select {
		case queue <- event:
		case <-ctx.Done():
			close(queue)
			group.Wait()
			return
		}
	}
	close(queue)
	group.Wait()
}

func (dispatcher *Dispatcher) markPublished(ctx context.Context, event domain.OutboxEvent, token string) {
	marked, err := dispatcher.store.MarkOutboxPublished(ctx, event.ID, token)
	switch {
	case err != nil:
		dispatcher.logger.ErrorContext(ctx, "mark connections event published failed", "event_id", event.ID, "error", err)
	case !marked:
		dispatcher.logger.WarnContext(ctx, "connections event was re-claimed before it could be marked", "event_id", event.ID)
	}
}

// claimToken names one claim on a batch. It is opaque to the store and only
// compared for equality, so any unguessable value does.
func claimToken() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func retryDelay(attempts int) time.Duration {
	return time.Second * time.Duration(1<<min(max(attempts, 0), 6))
}
