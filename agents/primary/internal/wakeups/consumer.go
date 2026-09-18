package wakeups

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
)

// recorder keeps what the consumer reads. Store implements it.
type recorder interface {
	Record(ctx context.Context, event Event) (bool, error)
}

// Consumer records the trigger events agents' queue delivers, for the waker
// to hand to Krude.
type Consumer struct {
	store  recorder
	logger *slog.Logger
}

// NewConsumer returns a consumer recording into store.
func NewConsumer(store recorder, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Consumer{store: store, logger: logger}
}

type eventBridgeEvent struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	DetailType string          `json:"detail-type"`
	Detail     json.RawMessage `json:"detail"`
}

type receivedEnvelope struct {
	EventID string          `json:"event_id"`
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data"`
}

// Handle decodes one SQS message body as an EventBridge event and records
// the trigger it carries. Recording is keyed by the envelope's own id, so a
// delivery the bus repeats is recorded once and handing it on twice is not
// possible.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return fmt.Errorf("decode EventBridge event: %w", err)
	}
	if event.Source != appsv1.Source || event.DetailType != appsv1.TriggerReceivedDetailType {
		return fmt.Errorf("event %s: no handler for %s from %s", event.ID, event.DetailType, event.Source)
	}
	var envelope receivedEnvelope
	if err := json.Unmarshal(event.Detail, &envelope); err != nil {
		return fmt.Errorf("event %s: decode detail: %w", event.ID, err)
	}
	if envelope.Version != 1 || envelope.EventID == "" {
		return fmt.Errorf("event %s: unsupported envelope (version %d, event_id %q)", event.ID, envelope.Version, envelope.EventID)
	}
	var trigger appsv1.TriggerReceived
	if err := json.Unmarshal(envelope.Data, &trigger); err != nil {
		return fmt.Errorf("event %s: decode trigger: %w", envelope.EventID, err)
	}
	if trigger.OrganizationID == "" || trigger.MemberID == "" {
		// Nobody to wake: dropping it is the only thing to do, and retrying
		// would not change that.
		consumer.logger.WarnContext(ctx, "trigger event names no member; dropped", "event_id", envelope.EventID)
		return nil
	}
	recorded, err := consumer.store.Record(ctx, Event{
		ID:             envelope.EventID,
		OrganizationID: trigger.OrganizationID,
		MemberID:       trigger.MemberID,
		Toolkit:        trigger.Toolkit,
		Trigger:        trigger.Trigger,
		Data:           trigger.Data,
		DataOmitted:    trigger.DataOmitted,
		ReceivedAt:     trigger.ReceivedAt,
	})
	if err != nil {
		return fmt.Errorf("event %s: %w", envelope.EventID, err)
	}
	if !recorded {
		consumer.logger.InfoContext(ctx, "trigger event already recorded", "event_id", envelope.EventID)
	}
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
// only the messages it recorded.
func (consumer *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
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

		var handled []sqstypes.DeleteMessageBatchRequestEntry
		for index, message := range output.Messages {
			if err := consumer.Handle(ctx, aws.ToString(message.Body)); err != nil {
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
