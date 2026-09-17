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

	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	connectionsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"
)

// Subscriptions are the detail types knowledge consumes, by source.
//
// What knowledge holds outlives the connections and people it was read
// through, so it has to hear when one of those goes away — otherwise a
// retrieval pipeline keeps answering out of a repository nobody can reach any
// more.
func Subscriptions() map[string][]string {
	return map[string][]string{
		authv1.Source: {
			authv1.OrganizationDeletedDetailType,
			authv1.MemberRemovedDetailType,
		},
		connectionsv1.Source: {
			connectionsv1.ConnectionDisconnectedDetailType,
			connectionsv1.ConnectionNeedsReconnectDetailType,
			connectionsv1.AppDeletedDetailType,
		},
	}
}

// application is what the consumer dispatches decoded events to. It is
// satisfied by *knowledge/internal/application.Service.
type application interface {
	OrganizationDeleted(context.Context, authv1.OrganizationDeleted) error
	MemberRemoved(context.Context, authv1.MemberRemoved) error
	ConnectionDisconnected(context.Context, connectionsv1.ConnectionDisconnected) error
	ConnectionNeedsReconnect(context.Context, connectionsv1.ConnectionNeedsReconnect) error
	AppDeleted(context.Context, connectionsv1.AppDeleted) error
	Handled(ctx context.Context, eventID, detailType string) (bool, error)
}

type handler func(context.Context, json.RawMessage) error

type subscription struct {
	source string
	handle handler
}

// Consumer pauses or retires what an organization can no longer reach.
type Consumer struct {
	handlers map[string]subscription
	app      application
	logger   *slog.Logger
}

func NewConsumer(app application, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Consumer{
		handlers: map[string]subscription{
			authv1.OrganizationDeletedDetailType:             {source: authv1.Source, handle: decode(app.OrganizationDeleted)},
			authv1.MemberRemovedDetailType:                   {source: authv1.Source, handle: decode(app.MemberRemoved)},
			connectionsv1.ConnectionDisconnectedDetailType:   {source: connectionsv1.Source, handle: decode(app.ConnectionDisconnected)},
			connectionsv1.ConnectionNeedsReconnectDetailType: {source: connectionsv1.Source, handle: decode(app.ConnectionNeedsReconnect)},
			connectionsv1.AppDeletedDetailType:               {source: connectionsv1.Source, handle: decode(app.AppDeleted)},
		},
		app:    app,
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

// Handle decodes one SQS message body as an EventBridge event and dispatches
// it to the subscribed handler.
//
// Unlike the lifecycle handlers elsewhere in this repo, these are not
// naturally idempotent: pausing a source and retracting its documents both
// write events a retrieval pipeline acts on, so a redelivery would tell it to
// forget the same documents twice. The envelope's id is claimed before
// anything is done, and a second delivery of it does nothing.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
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

	first, err := consumer.app.Handled(ctx, detail.EventID, event.DetailType)
	if err != nil {
		return fmt.Errorf("event %s: %w", detail.EventID, err)
	}
	if !first {
		consumer.logger.InfoContext(ctx, "event already handled", "event_id", detail.EventID, "detail_type", event.DetailType)
		return nil
	}

	if err := subscribed.handle(ctx, detail.Data); err != nil {
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

// receiveBackoff is how long Poll waits after a failed receive before trying
// again. A var so tests can shorten it instead of waiting for real time.
var receiveBackoff = 5 * time.Second

// Poll long-polls the knowledge-events queue until ctx is cancelled, deleting
// only the messages it handled successfully.
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
			consumer.logger.ErrorContext(ctx, "receive from knowledge queue failed", "error", err)
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
				consumer.logger.ErrorContext(ctx, "event failed",
					"sqs_message_id", aws.ToString(message.MessageId), "error", err)
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
