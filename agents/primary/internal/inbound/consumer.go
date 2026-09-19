// Package inbound handles what other services publish that agents must act
// on: a batch of what a member's apps reported, ready to wake Krude for
// them, and a member leaving an organization or the organization going
// away, which takes their conversations with it.
//
// Batching decides when a member's app triggers make a batch — it sees every
// trigger, and holds them across replicas and restarts — so agents consumes
// its batches rather than the triggers themselves, and wakes Krude for each
// one as soon as it arrives.
package inbound

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

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	batchingv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/batching/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/wakeups"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// component identifies this consumer in its metrics and logs, low-cardinality
// and fixed regardless of which queue or detail type it is handling.
const component = "agents"

// Subscriptions are the detail types agents consumes, by source, for a host
// provisioning the rule that feeds its queue.
func Subscriptions() map[string][]string {
	return map[string][]string{
		batchingv1.Source: {batchingv1.TriggerBatchReadyDetailType},
		authv1.Source:     {authv1.MemberRemovedDetailType, authv1.OrganizationDeletedDetailType},
	}
}

// Recorder keeps a batch's trigger events until they wake Krude, all of them
// or none, so a wake-up never hands over part of a batch. It answers how many
// it had not seen before. wakeups.Store implements it.
type Recorder interface {
	RecordBatch(ctx context.Context, events []wakeups.Event) (int, error)
}

// Forgetter removes what a service keeps for an organization — only one
// member's, when memberID is named. threads.Service and wakeups.Store each
// forget their own part; the threads service reports how many it removed.
type Forgetter func(ctx context.Context, organizationID, memberID string) error

// Consumer dispatches agents' queue.
type Consumer struct {
	recorder Recorder
	forget   []Forgetter
	logger   *slog.Logger
	metrics  observability.ConsumerMetrics
	// queueName is set once, at the top of Poll, before Poll's single
	// goroutine starts calling handle; it is not touched concurrently.
	queueName string
}

// NewConsumer returns a consumer that records triggers with recorder and,
// when a member or an organization is gone, runs every forgetter.
func NewConsumer(recorder Recorder, forget []Forgetter, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	metrics, err := observability.NewConsumerMetrics()
	if err != nil {
		logger.Warn("consumer metrics unavailable; handling continues without them", "error", err)
	}
	return &Consumer{recorder: recorder, forget: forget, logger: logger, metrics: metrics}
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

// Handle decodes one SQS message body as an EventBridge event and acts on
// it. The bus delivers at least once, and every handler is safe to repeat: a
// batch's triggers are recorded by their own events' ids, and forgetting what
// is already gone removes nothing more.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
	return consumer.handle(ctx, body, time.Now())
}

func (consumer *Consumer) handle(ctx context.Context, body string, receivedAt time.Time) error {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return fmt.Errorf("decode EventBridge event: %w", err)
	}
	var envelope receivedEnvelope
	if err := json.Unmarshal(event.Detail, &envelope); err != nil {
		return fmt.Errorf("event %s: decode detail: %w", event.ID, err)
	}
	if envelope.Version != 1 || envelope.EventID == "" {
		return fmt.Errorf("event %s: unsupported envelope (version %d, event_id %q)", event.ID, envelope.Version, envelope.EventID)
	}
	occurredAt, _ := time.Parse(time.RFC3339Nano, envelope.OccurredAt)

	ctx, span := observability.StartConsumerSpan(ctx, envelope.TraceCarrier, consumer.queueName, event.DetailType, envelope.EventID)
	started := time.Now()
	err := consumer.dispatch(ctx, event, envelope)
	observability.EndSpan(span, err)
	consumer.metrics.Record(ctx, component, event.DetailType, occurredAt, receivedAt, started, err)
	return err
}

// dispatch runs the handler for one decoded message. It is Handle's own
// switch, unchanged, pulled out so handle can wrap it in a span and record
// its outcome without the span covering decoding failures that never got far
// enough to know a detail type worth attributing them to.
func (consumer *Consumer) dispatch(ctx context.Context, event eventBridgeEvent, envelope receivedEnvelope) error {
	switch {
	case event.Source == batchingv1.Source && event.DetailType == batchingv1.TriggerBatchReadyDetailType:
		return consumer.batchReady(ctx, envelope)
	case event.Source == appsv1.Source && event.DetailType == appsv1.TriggerReceivedDetailType:
		// Delivered straight from apps, as agents' rule did before batching
		// took over; one can still be on the queue from then. Batching has
		// its own copy and hands it over in a batch, so it is left to that
		// rather than waking Krude for it alone.
		consumer.logger.InfoContext(ctx, "an app trigger arrived directly; batching delivers it", "event_id", envelope.EventID)
		return nil
	case event.Source == authv1.Source && event.DetailType == authv1.MemberRemovedDetailType:
		var removed authv1.MemberRemoved
		if err := json.Unmarshal(envelope.Data, &removed); err != nil {
			return fmt.Errorf("event %s: decode member removed: %w", envelope.EventID, err)
		}
		if removed.OrganizationID == "" || removed.UserID == "" {
			return fmt.Errorf("event %s: a removed member needs an organization and a user", envelope.EventID)
		}
		return consumer.forgetAll(ctx, envelope.EventID, removed.OrganizationID, removed.UserID)
	case event.Source == authv1.Source && event.DetailType == authv1.OrganizationDeletedDetailType:
		var deleted authv1.OrganizationDeleted
		if err := json.Unmarshal(envelope.Data, &deleted); err != nil {
			return fmt.Errorf("event %s: decode organization deleted: %w", envelope.EventID, err)
		}
		if deleted.OrganizationID == "" {
			return fmt.Errorf("event %s: a deleted organization needs its id", envelope.EventID)
		}
		return consumer.forgetAll(ctx, envelope.EventID, deleted.OrganizationID, "")
	default:
		return fmt.Errorf("event %s: no handler for %s from %s", event.ID, event.DetailType, event.Source)
	}
}

func (consumer *Consumer) batchReady(ctx context.Context, envelope receivedEnvelope) error {
	var batch batchingv1.TriggerBatchReady
	if err := json.Unmarshal(envelope.Data, &batch); err != nil {
		return fmt.Errorf("event %s: decode trigger batch: %w", envelope.EventID, err)
	}
	if batch.OrganizationID == "" || batch.MemberID == "" {
		// Nobody to wake: dropping it is the only thing to do, and retrying
		// would not change that.
		consumer.logger.WarnContext(ctx, "trigger batch names no member; dropped",
			"event_id", envelope.EventID, "batch_id", batch.BatchID)
		return nil
	}
	events := make([]wakeups.Event, 0, len(batch.Triggers))
	for _, trigger := range batch.Triggers {
		if trigger.EventID == "" {
			return fmt.Errorf("event %s: batch %s carries a trigger with no event id", envelope.EventID, batch.BatchID)
		}
		// Each trigger keeps its own app event's id, the one it was recorded
		// by when agents read triggers straight from apps, so a trigger seen
		// then is recognised now rather than told twice.
		events = append(events, wakeups.Event{
			ID:             trigger.EventID,
			OrganizationID: batch.OrganizationID,
			MemberID:       batch.MemberID,
			Toolkit:        trigger.Toolkit,
			Trigger:        trigger.Trigger,
			Data:           trigger.Data,
			DataOmitted:    trigger.DataOmitted,
			ReceivedAt:     trigger.ReceivedAt,
		})
	}
	if len(events) == 0 {
		consumer.logger.WarnContext(ctx, "trigger batch carries no triggers; dropped",
			"event_id", envelope.EventID, "batch_id", batch.BatchID)
		return nil
	}
	recorded, err := consumer.recorder.RecordBatch(ctx, events)
	if err != nil {
		return fmt.Errorf("event %s: %w", envelope.EventID, err)
	}
	consumer.logger.InfoContext(ctx, "trigger batch recorded to wake Krude",
		"event_id", envelope.EventID, "batch_id", batch.BatchID, "triggers", len(events), "new", recorded,
		"lane", batch.Lane, "flush_reason", batch.FlushReason)
	return nil
}

func (consumer *Consumer) forgetAll(ctx context.Context, eventID, organizationID, memberID string) error {
	var failures []error
	for _, forget := range consumer.forget {
		if err := forget(ctx, organizationID, memberID); err != nil {
			failures = append(failures, err)
		}
	}
	if err := errors.Join(failures...); err != nil {
		return fmt.Errorf("event %s: %w", eventID, err)
	}
	consumer.logger.InfoContext(ctx, "forgot what agents kept", "event_id", eventID, "organization_id", organizationID, "member", memberID != "")
	return nil
}

type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

// receiveBackoff is how long Poll waits after a failed receive before trying
// again. A var so tests can shorten it.
var receiveBackoff = 5 * time.Second

// Poll long-polls the agents-events queue until ctx is cancelled, deleting
// only the messages it handled.
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
			consumer.logger.ErrorContext(ctx, "receive from agents queue failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(receiveBackoff):
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
			consumer.logger.ErrorContext(ctx, "delete handled message failed",
				"code", aws.ToString(failure.Code), "message", aws.ToString(failure.Message))
		}
	}
}
