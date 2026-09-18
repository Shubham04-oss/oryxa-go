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
)

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
}

func NewConsumer(app application, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Consumer{
		handlers: map[string]subscription{
			appsv1.TriggerReceivedDetailType:     {source: appsv1.Source, handle: decodeWithID(app.TriggerReceived)},
			appsv1.ConnectionRemovedDetailType:   {source: appsv1.Source, handle: decode(app.ConnectionRemoved)},
			appsv1.TriggerUnsubscribedDetailType: {source: appsv1.Source, handle: decode(app.TriggerUnsubscribed)},
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

// receiveSize is how many messages one receive asks for, the most SQS
// gives. They are handled side by side, so at most that many at once, and
// one waiting on the classifier holds up none of the others, a removal
// included.
const receiveSize = 10

func (consumer *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
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
		handled := consumer.handleAll(ctx, output.Messages)
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
func (consumer *Consumer) handleAll(ctx context.Context, messages []sqstypes.Message) []sqstypes.DeleteMessageBatchRequestEntry {
	handled := make([]bool, len(messages))
	var group sync.WaitGroup
	for index, message := range messages {
		group.Go(func() {
			if err := consumer.Handle(ctx, aws.ToString(message.Body)); err != nil {
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
