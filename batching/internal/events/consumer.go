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

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
)

// Subscriptions are the raw app triggers batching groups and the auth events
// that remove work nobody may receive any more.
func Subscriptions() map[string][]string {
	return map[string][]string{
		appsv1.Source: {appsv1.TriggerReceivedDetailType},
		authv1.Source: {authv1.MemberRemovedDetailType, authv1.OrganizationDeletedDetailType},
	}
}

type application interface {
	TriggerReceived(context.Context, string, appsv1.TriggerReceived) error
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
}

func NewConsumer(app application, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Consumer{
		handlers: map[string]subscription{
			appsv1.TriggerReceivedDetailType:     {source: appsv1.Source, handle: decodeWithID(app.TriggerReceived)},
			authv1.MemberRemovedDetailType:       {source: authv1.Source, handle: decode(app.MemberRemoved)},
			authv1.OrganizationDeletedDetailType: {source: authv1.Source, handle: decode(app.OrganizationDeleted)},
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

type envelope struct {
	EventID string          `json:"event_id"`
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data"`
}

func (consumer *Consumer) Handle(ctx context.Context, body string) error {
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
	if err := subscribed.handle(ctx, detail.EventID, detail.Data); err != nil {
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

func (consumer *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
	for {
		output, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 20,
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
		var handled []sqstypes.DeleteMessageBatchRequestEntry
		for index, message := range output.Messages {
			if err := consumer.Handle(ctx, aws.ToString(message.Body)); err != nil {
				consumer.logger.ErrorContext(ctx, "event failed", "sqs_message_id", aws.ToString(message.MessageId), "error", err)
				continue
			}
			handled = append(handled, sqstypes.DeleteMessageBatchRequestEntry{
				Id: aws.String(strconv.Itoa(index)), ReceiptHandle: message.ReceiptHandle,
			})
		}
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
