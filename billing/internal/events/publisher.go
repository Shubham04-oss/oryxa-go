package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

type putEventsAPI interface {
	PutEvents(context.Context, *eventbridge.PutEventsInput, ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

// Publisher puts billing's events on EventBridge in the envelope every Oryxa
// publisher uses.
type Publisher struct {
	client  putEventsAPI
	busName string
}

type envelope struct {
	EventID    string          `json:"event_id"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

func NewPublisher(client putEventsAPI, busName string) *Publisher {
	return &Publisher{client: client, busName: busName}
}

func (publisher *Publisher) PublishEvent(ctx context.Context, event domain.Event) error {
	if event.ID == "" || event.DetailType == "" || event.OccurredAt.IsZero() {
		return errors.New("event id, detail type, and occurrence time are required")
	}
	occurredAt := event.OccurredAt.UTC()
	detail, err := json.Marshal(envelope{
		EventID:    event.ID,
		Version:    1,
		OccurredAt: occurredAt.Format(time.RFC3339Nano),
		Data:       event.Data,
	})
	if err != nil {
		return fmt.Errorf("encode event detail: %w", err)
	}

	output, err := publisher.client.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []types.PutEventsRequestEntry{{
			Detail:       aws.String(string(detail)),
			DetailType:   aws.String(event.DetailType),
			EventBusName: aws.String(publisher.busName),
			Source:       aws.String(billingeventsv1.Source),
			Time:         aws.Time(occurredAt),
		}},
	})
	if err != nil {
		return fmt.Errorf("put event: %w", err)
	}
	if output == nil {
		return errors.New("put event returned no result")
	}
	if output.FailedEntryCount > 0 {
		if len(output.Entries) == 0 {
			return errors.New("put event failed without an entry result")
		}
		entry := output.Entries[0]
		return fmt.Errorf("put event failed: %s: %s", aws.ToString(entry.ErrorCode), aws.ToString(entry.ErrorMessage))
	}
	return nil
}

type outboxStore interface {
	ClaimOutbox(context.Context, int) ([]domain.OutboxEvent, error)
	MarkOutboxPublished(context.Context, string) error
	MarkOutboxFailed(context.Context, string, time.Duration, string) error
}

type eventPublisher interface {
	PublishEvent(context.Context, domain.Event) error
}

// Dispatcher publishes the outbox: every event is committed with the state it
// describes, then published at least once.
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
			dispatcher.logger.ErrorContext(ctx, "claim billing outbox failed", "error", err)
		}
		return
	}
	for _, event := range events {
		if err := dispatcher.publisher.PublishEvent(ctx, event.Event); err != nil {
			dispatcher.logger.ErrorContext(ctx, "publish billing event failed", "event_id", event.ID, "detail_type", event.DetailType, "error", err)
			if markErr := dispatcher.store.MarkOutboxFailed(ctx, event.ID, retryDelay(event.Attempts), err.Error()); markErr != nil {
				dispatcher.logger.ErrorContext(ctx, "mark billing event failed", "event_id", event.ID, "error", markErr)
			}
			continue
		}
		if err := dispatcher.store.MarkOutboxPublished(ctx, event.ID); err != nil {
			dispatcher.logger.ErrorContext(ctx, "mark billing event published failed", "event_id", event.ID, "error", err)
		}
	}
}

func retryDelay(attempts int) time.Duration {
	return time.Second * time.Duration(1<<min(max(attempts, 0), 6))
}
