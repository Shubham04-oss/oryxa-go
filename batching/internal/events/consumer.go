package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// component identifies this consumer in its metrics and logs, low-cardinality
// and fixed regardless of which queue or detail type it is handling.
const component = "batching"

// Subscriptions are the raw app triggers batching groups, and the app and
// auth events that remove work nobody should receive any more: a member's
// app disconnected or trigger removed, a member gone, an organization gone.
func Subscriptions() map[string][]string {
	return map[string][]string{
		appsv1.Source: {appsv1.TriggerReceivedDetailType, appsv1.ConnectionRemovedDetailType, appsv1.TriggerUnsubscribedDetailType},
		authv1.Source: {authv1.MemberRemovedDetailType, authv1.OrganizationDeletedDetailType},
	}
}

type application interface {
	TriggerReceived(context.Context, string, appsv1.TriggerReceived) error
	ConnectionRemoved(context.Context, appsv1.ConnectionRemoved) error
	TriggerUnsubscribed(context.Context, appsv1.TriggerUnsubscribed) error
	MemberRemoved(context.Context, authv1.MemberRemoved) error
	OrganizationDeleted(context.Context, authv1.OrganizationDeleted) error
}

type handler func(context.Context, string, json.RawMessage) error

type subscription struct {
	source string
	handle handler
}

type Consumer struct {
	handlers map[string]subscription
	logger   *slog.Logger
	metrics  observability.ConsumerMetrics
	// verifier proves who published each event. Nil is a build error caught
	// at mount: a consumer that cannot check signatures would run any
	// message a queue handed it.
	verifier *eventauth.Verifier
	// queueName is set once, at the top of Poll, before Poll starts calling
	// handle from handleAll's goroutines; the Go memory model makes that
	// write visible to every goroutine handleAll spawns afterward, so reading
	// it there needs no lock.
	queueName string
}

func NewConsumer(app application, verifier *eventauth.Verifier, logger *slog.Logger) (*Consumer, error) {
	if verifier == nil {
		return nil, errors.New("batching cannot consume unsigned events: a signature verifier is required")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	metrics, err := observability.NewConsumerMetrics()
	if err != nil {
		logger.Warn("consumer metrics unavailable; handling continues without them", "error", err)
	}
	return &Consumer{
		handlers: map[string]subscription{
			appsv1.TriggerReceivedDetailType:     {source: appsv1.Source, handle: decodeWithID(app.TriggerReceived)},
			appsv1.ConnectionRemovedDetailType:   {source: appsv1.Source, handle: decode(app.ConnectionRemoved)},
			appsv1.TriggerUnsubscribedDetailType: {source: appsv1.Source, handle: decode(app.TriggerUnsubscribed)},
			authv1.MemberRemovedDetailType:       {source: authv1.Source, handle: decode(app.MemberRemoved)},
			authv1.OrganizationDeletedDetailType: {source: authv1.Source, handle: decode(app.OrganizationDeleted)},
		},
		logger:   logger,
		metrics:  metrics,
		verifier: verifier,
	}, nil
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

func (consumer *Consumer) Handle(ctx context.Context, body string) error {
	return consumer.handle(ctx, body, time.Now())
}

func (consumer *Consumer) handle(ctx context.Context, body string, receivedAt time.Time) error {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return fmt.Errorf("decode EventBridge event: %w", err)
	}
	subscribed, ok := consumer.handlers[event.DetailType]
	if !ok || subscribed.source != event.Source {
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
	// it is refused whatever its payload says. The source the signature is
	// checked against is the event's own — apps or auth — so one verifier
	// call covers every source batching subscribes to.
	if err := consumer.verifier.Verify(event.Source, event.DetailType, detail.EventID, detail.Version, detail.OccurredAt, detail.Data, detail.Signature); err != nil {
		return fmt.Errorf("event %s: %w", detail.EventID, err)
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, detail.OccurredAt)

	ctx, span := observability.StartConsumerSpan(ctx, detail.TraceCarrier, consumer.queueName, event.DetailType, detail.EventID)
	started := time.Now()
	err := subscribed.handle(ctx, detail.EventID, detail.Data)
	observability.EndSpan(span, err)
	consumer.metrics.Record(ctx, component, event.DetailType, occurredAt, receivedAt, started, err)
	if err != nil {
		return fmt.Errorf("event %s: %s: %w", detail.EventID, event.DetailType, err)
	}
	consumer.logger.InfoContext(ctx, "event handled", "event_id", detail.EventID, "detail_type", event.DetailType)
	return nil
}

func decode[T any](handle func(context.Context, T) error) handler {
	return func(ctx context.Context, _ string, data json.RawMessage) error {
		var value T
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("decode event data: %w", err)
		}
		return handle(ctx, value)
	}
}

func decodeWithID[T any](handle func(context.Context, string, T) error) handler {
	return func(ctx context.Context, eventID string, data json.RawMessage) error {
		var value T
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("decode event data: %w", err)
		}
		return handle(ctx, eventID, value)
	}
}

type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

var receiveBackoff = 5 * time.Second

// receiveSize is how many messages one receive asks for, the most SQS
// gives. They are handled side by side, so at most that many at once, and
// one waiting on the classifier holds up none of the others, a removal
// included.
const receiveSize = 10

func (consumer *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
	consumer.queueName = observability.QueueName(queueURL)
	for {
		output, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: receiveSize, WaitTimeSeconds: 20,
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			consumer.logger.ErrorContext(ctx, "receive from batching queue failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(receiveBackoff):
			}
			continue
		}
		// One timestamp for the whole batch: ReceiveMessage already returned,
		// so every message in it was equally "received" as of now.
		handled := consumer.handleAll(ctx, output.Messages, time.Now())
		if len(handled) == 0 {
			continue
		}
		deleted, err := client.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{
			QueueUrl: aws.String(queueURL), Entries: handled,
		})
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				consumer.logger.ErrorContext(ctx, "delete handled messages failed", "error", err)
			}
			continue
		}
		for _, failure := range deleted.Failed {
			consumer.logger.ErrorContext(ctx, "delete handled message failed",
				"code", aws.ToString(failure.Code), "message", aws.ToString(failure.Message))
		}
	}
}

// handleAll handles a receive's messages side by side, and answers the
// delete entries for those handled. One that failed is left on the queue,
// to be delivered again once its visibility timeout runs out.
func (consumer *Consumer) handleAll(ctx context.Context, messages []sqstypes.Message, receivedAt time.Time) []sqstypes.DeleteMessageBatchRequestEntry {
	handled := make([]bool, len(messages))
	var group sync.WaitGroup
	for index, message := range messages {
		group.Go(func() {
			if err := consumer.handle(ctx, aws.ToString(message.Body), receivedAt); err != nil {
				consumer.logger.ErrorContext(ctx, "event failed", "sqs_message_id", aws.ToString(message.MessageId), "error", err)
				return
			}
			handled[index] = true
		})
	}
	group.Wait()
	var entries []sqstypes.DeleteMessageBatchRequestEntry
	for index, message := range messages {
		if handled[index] {
			entries = append(entries, sqstypes.DeleteMessageBatchRequestEntry{
				Id: aws.String(strconv.Itoa(index)), ReceiptHandle: message.ReceiptHandle,
			})
		}
	}
	return entries
}
