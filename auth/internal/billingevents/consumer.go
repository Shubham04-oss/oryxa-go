// Package billingevents consumes the billing events auth acts on: the
// entitlements that set each organization's member limit.
package billingevents

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

	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// component identifies this consumer in its metrics and logs, low-cardinality
// and fixed regardless of which detail type it is handling.
const component = "auth"

// Subscriptions are the billing events auth consumes. The rule delivering them
// to auth's queue is built from this list.
func Subscriptions() []string {
	return []string{billingeventsv1.EntitlementsChangedDetailType}
}

type application interface {
	ApplyBillingEntitlements(context.Context, billingeventsv1.EntitlementsChanged) error
}

type Consumer struct {
	app     application
	logger  *slog.Logger
	metrics observability.ConsumerMetrics
	// verifier proves who published each event. Nil is a build error caught
	// at mount: a consumer that cannot check signatures would run any
	// message a queue handed it.
	verifier *eventauth.Verifier
	// queueName is set once, at the top of Poll, before Poll's single
	// goroutine starts calling handle; it is not touched concurrently.
	queueName string
}

func NewConsumer(app application, verifier *eventauth.Verifier, logger *slog.Logger) (*Consumer, error) {
	if verifier == nil {
		return nil, errors.New("auth cannot consume unsigned events: a signature verifier is required")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	metrics, err := observability.NewConsumerMetrics()
	if err != nil {
		logger.Warn("consumer metrics unavailable; handling continues without them", "error", err)
	}
	return &Consumer{app: app, logger: logger, metrics: metrics, verifier: verifier}, nil
}

type eventBridgeEvent struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	DetailType string          `json:"detail-type"`
	Detail     json.RawMessage `json:"detail"`
}

type envelope struct {
	EventID    string          `json:"event_id"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
	// Signature is the publisher's proof over the envelope's content. It is
	// checked before any handler runs; a missing or wrong signature means
	// the message never came from the service it claims.
	Signature string `json:"sig"`
	// TraceCarrier decodes the envelope's optional traceparent/tracestate
	// fields; a message published before this rollout, or one that never
	// carried one, decodes to the zero TraceCarrier, and handling continues
	// under a fresh span rather than failing.
	observability.TraceCarrier
}

// Handle processes one SQS message body: an EventBridge event from billing.
// It is exported on its own, receivedAt defaulting to now, for tests and any
// other caller that does not go through Poll.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
	return consumer.handle(ctx, body, time.Now())
}

func (consumer *Consumer) handle(ctx context.Context, body string, receivedAt time.Time) error {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return fmt.Errorf("decode EventBridge event: %w", err)
	}
	if event.Source != billingeventsv1.Source || event.DetailType != billingeventsv1.EntitlementsChangedDetailType {
		return fmt.Errorf("event %s: no handler for %s from %s", event.ID, event.DetailType, event.Source)
	}
	var detail envelope
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		return fmt.Errorf("event %s: decode detail: %w", event.ID, err)
	}
	if detail.Version != 1 || detail.EventID == "" {
		return fmt.Errorf("event %s: unsupported envelope (version %d, event_id %q)", event.ID, detail.Version, detail.EventID)
	}
	// The signature is checked before anything in the event is used, so a
	// message that reached the queue without the named service having sent
	// it is refused whatever its payload says.
	if err := consumer.verifier.Verify(event.Source, event.DetailType, detail.EventID, detail.Version, detail.OccurredAt, detail.Data, detail.Signature); err != nil {
		return fmt.Errorf("event %s: %w", detail.EventID, err)
	}
	var entitlements billingeventsv1.EntitlementsChanged
	if err := json.Unmarshal(detail.Data, &entitlements); err != nil {
		return fmt.Errorf("event %s: decode entitlements: %w", detail.EventID, err)
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, detail.OccurredAt)

	ctx, span := observability.StartConsumerSpan(ctx, detail.TraceCarrier, consumer.queueName, event.DetailType, detail.EventID)
	started := time.Now()
	err := consumer.app.ApplyBillingEntitlements(ctx, entitlements)
	observability.EndSpan(span, err)
	consumer.metrics.Record(ctx, component, event.DetailType, occurredAt, receivedAt, started, err)
	if err != nil {
		return fmt.Errorf("event %s: apply entitlements: %w", detail.EventID, err)
	}
	consumer.logger.InfoContext(ctx, "billing event handled", "event_id", detail.EventID, "detail_type", event.DetailType)
	return nil
}

type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

// Poll consumes the queue until ctx is cancelled. Handled messages are
// deleted; failed ones return after their visibility timeout, and the redrive
// policy moves a message that keeps failing aside.
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
			consumer.logger.ErrorContext(ctx, "receive billing events failed", "error", err)
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
				consumer.logger.ErrorContext(ctx, "billing event failed", "sqs_message_id", aws.ToString(message.MessageId), "error", err)
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
				consumer.logger.ErrorContext(ctx, "delete handled billing events failed", "error", err)
			}
			continue
		}
		for _, failure := range deleted.Failed {
			consumer.logger.ErrorContext(ctx, "delete handled billing event failed", "code", aws.ToString(failure.Code), "message", aws.ToString(failure.Message))
		}
	}
}
