package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// component identifies this consumer in its metrics and logs, low-cardinality
// and fixed regardless of which queue or detail type it is handling.
const component = "files"

func AuthSubscriptions() []string {
	return []string{
		autheventsv1.OrganizationCreatedDetailType,
		autheventsv1.OrganizationUpdatedDetailType,
		autheventsv1.OrganizationDeletedDetailType,
		autheventsv1.MemberRemovedDetailType,
	}
}

func TeamSubscriptions() []string {
	return []string{
		teamseventsv1.TeamDeletedDetailType,
	}
}

type application interface {
	OrganizationCreated(context.Context, autheventsv1.OrganizationCreated) error
	OrganizationUpdated(context.Context, autheventsv1.OrganizationUpdated) error
	OrganizationDeleted(context.Context, autheventsv1.OrganizationDeleted) error
	MemberRemoved(context.Context, autheventsv1.MemberRemoved) error
	TeamDeleted(context.Context, teamseventsv1.TeamDeleted) error
}

type handler func(context.Context, json.RawMessage) error

type subscription struct {
	source string
	handle handler
}

type Consumer struct {
	handlers map[string]subscription
	logger   *slog.Logger
	metrics  observability.ConsumerMetrics
	// queueName is set once, at the top of Poll, before Poll's single
	// goroutine starts calling handle; it is not touched concurrently.
	queueName string
}

func NewConsumer(app application, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	metrics, err := observability.NewConsumerMetrics()
	if err != nil {
		logger.Warn("consumer metrics unavailable; handling continues without them", "error", err)
	}
	return &Consumer{
		handlers: map[string]subscription{
			autheventsv1.OrganizationCreatedDetailType: {source: autheventsv1.Source, handle: decode(app.OrganizationCreated)},
			autheventsv1.OrganizationUpdatedDetailType: {source: autheventsv1.Source, handle: decode(app.OrganizationUpdated)},
			autheventsv1.OrganizationDeletedDetailType: {source: autheventsv1.Source, handle: decode(app.OrganizationDeleted)},
			autheventsv1.MemberRemovedDetailType:       {source: autheventsv1.Source, handle: decode(app.MemberRemoved)},
			teamseventsv1.TeamDeletedDetailType:        {source: teamseventsv1.Source, handle: decode(app.TeamDeleted)},
		},
		logger:  logger,
		metrics: metrics,
	}
}

type eventBridgeEvent struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	DetailType string          `json:"detail-type"`
	Detail     json.RawMessage `json:"detail"`
}

type receivedEnvelope struct {
	EventID    string          `json:"event_id"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
	// TraceCarrier decodes the envelope's optional traceparent/tracestate
	// fields; a message published before this rollout, or one that never
	// carried one, decodes to the zero TraceCarrier, and handling continues
	// under a fresh span rather than failing.
	observability.TraceCarrier
}

func (consumer *Consumer) Handle(ctx context.Context, body string) error {
	return consumer.handle(ctx, body, time.Now())
}

func (consumer *Consumer) handle(ctx context.Context, body string, receivedAt time.Time) error {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return fmt.Errorf("decode EventBridge event: %w", err)
	}
	subscribed, ok := consumer.handlers[event.DetailType]
	if !ok || event.Source != subscribed.source {
		return fmt.Errorf("event %s: no handler for %s from %s", event.ID, event.DetailType, event.Source)
	}
	var detail receivedEnvelope
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		return fmt.Errorf("event %s: decode detail: %w", event.ID, err)
	}
	if detail.Version != 1 || detail.EventID == "" {
		return fmt.Errorf("event %s: unsupported envelope (version %d, event_id %q)", event.ID, detail.Version, detail.EventID)
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, detail.OccurredAt)

	ctx, span := observability.StartConsumerSpan(ctx, detail.TraceCarrier, consumer.queueName, event.DetailType, detail.EventID)
	started := time.Now()
	err := subscribed.handle(ctx, detail.Data)
	observability.EndSpan(span, err)
	consumer.metrics.Record(ctx, component, event.DetailType, occurredAt, receivedAt, started, err)
	if err != nil {
		return fmt.Errorf("event %s: %s: %w", detail.EventID, event.DetailType, err)
	}
	consumer.logger.InfoContext(ctx, "event handled", "event_id", detail.EventID, "detail_type", event.DetailType)
	return nil
}

func decode[T any](handle func(context.Context, T) error) handler {
	return func(ctx context.Context, data json.RawMessage) error {
		var event T
		if err := json.Unmarshal(data, &event); err != nil {
			return fmt.Errorf("decode event data: %w", err)
		}
		return handle(ctx, event)
	}
}

type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

func (consumer *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
	// Poll runs as this consumer's only goroutine for its whole life, calling
	// handle sequentially, so setting this once up front and never touching
	// it again needs no lock.
	consumer.queueName = observability.QueueName(queueURL)
	for {
		output, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     20,
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			consumer.logger.ErrorContext(ctx, "receive from files queue failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		// One timestamp for the whole batch: ReceiveMessage already returned,
		// so every message in it was equally "received" as of now.
		receivedAt := time.Now()

		var handled []sqstypes.DeleteMessageBatchRequestEntry
		for index, message := range output.Messages {
			if err := consumer.handle(ctx, aws.ToString(message.Body), receivedAt); err != nil {
				consumer.logger.ErrorContext(ctx, "event failed", "sqs_message_id", aws.ToString(message.MessageId), "error", err)
				continue
			}
			handled = append(handled, sqstypes.DeleteMessageBatchRequestEntry{
				Id:            aws.String(strconv.Itoa(index)),
				ReceiptHandle: message.ReceiptHandle,
			})
		}
		if len(handled) == 0 {
			continue
		}
		deleted, err := client.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(queueURL), Entries: handled})
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				consumer.logger.ErrorContext(ctx, "delete handled messages failed", "error", err)
			}
			continue
		}
		for _, failure := range deleted.Failed {
			consumer.logger.ErrorContext(ctx, "delete handled message failed", "code", aws.ToString(failure.Code), "message", aws.ToString(failure.Message))
		}
	}
}
