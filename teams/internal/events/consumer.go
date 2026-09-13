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
)

// AuthSubscriptions are the auth events teams consumes. The rule that
// delivers them to teams' queue is built from this list.
func AuthSubscriptions() []string {
	return []string{
		autheventsv1.OrganizationCreatedDetailType,
		autheventsv1.OrganizationUpdatedDetailType,
		autheventsv1.OrganizationDeletedDetailType,
		autheventsv1.MemberRemovedDetailType,
	}
}

type application interface {
	OrganizationCreated(context.Context, autheventsv1.OrganizationCreated) error
	OrganizationUpdated(context.Context, autheventsv1.OrganizationUpdated) error
	OrganizationDeleted(context.Context, autheventsv1.OrganizationDeleted) error
	MemberRemoved(context.Context, autheventsv1.MemberRemoved) error
}

type handler func(context.Context, json.RawMessage) error

type Consumer struct {
	handlers map[string]handler
	logger   *slog.Logger
}

func NewConsumer(app application, logger *slog.Logger) *Consumer {
	return &Consumer{
		handlers: map[string]handler{
			autheventsv1.OrganizationCreatedDetailType: decode(app.OrganizationCreated),
			autheventsv1.OrganizationUpdatedDetailType: decode(app.OrganizationUpdated),
			autheventsv1.OrganizationDeletedDetailType: decode(app.OrganizationDeleted),
			autheventsv1.MemberRemovedDetailType:       decode(app.MemberRemoved),
		},
		logger: logger,
	}
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

// Handle processes one SQS message body: an EventBridge event from auth.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
	var event eventBridgeEvent
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return fmt.Errorf("decode EventBridge event: %w", err)
	}
	handle, ok := consumer.handlers[event.DetailType]
	if !ok || event.Source != autheventsv1.Source {
		return fmt.Errorf("event %s: no handler for %s from %s", event.ID, event.DetailType, event.Source)
	}
	var detail receivedEnvelope
	if err := json.Unmarshal(event.Detail, &detail); err != nil {
		return fmt.Errorf("event %s: decode detail: %w", event.ID, err)
	}
	if detail.Version != 1 || detail.EventID == "" {
		return fmt.Errorf("event %s: unsupported envelope (version %d, event_id %q)", event.ID, detail.Version, detail.EventID)
	}
	if err := handle(ctx, detail.Data); err != nil {
		return fmt.Errorf("event %s: %s: %w", detail.EventID, event.DetailType, err)
	}
	consumer.logger.InfoContext(ctx, "auth event handled", "event_id", detail.EventID, "detail_type", event.DetailType)
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

// Poll consumes the queue until ctx is cancelled. Handled messages are
// deleted; failed ones return after their visibility timeout, and the redrive
// policy moves a message that keeps failing aside.
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
			consumer.logger.ErrorContext(ctx, "receive from teams queue failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		var handled []sqstypes.DeleteMessageBatchRequestEntry
		for index, message := range output.Messages {
			if err := consumer.Handle(ctx, aws.ToString(message.Body)); err != nil {
				consumer.logger.ErrorContext(ctx, "auth event failed", "sqs_message_id", aws.ToString(message.MessageId), "error", err)
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
