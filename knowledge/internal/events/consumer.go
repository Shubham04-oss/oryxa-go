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
	filesv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
)

// Subscriptions are the detail types knowledge consumes, by source.
//
// What knowledge holds outlives the connections and people it was read
// through, so it has to hear when one of those goes away — otherwise a
// retrieval pipeline keeps answering out of a repository nobody can reach
// any more.
func Subscriptions() map[string][]string {
	return map[string][]string{
		authv1.Source: {
			authv1.OrganizationDeletedDetailType,
			authv1.MemberRemovedDetailType,
		},
		appsv1.Source: {
			// A connection made is what makes an app's contents searchable;
			// one lost is what stops them being answered out of.
			appsv1.ConnectionConnectedDetailType,
			appsv1.ConnectionExpiredDetailType,
			appsv1.ConnectionRemovedDetailType,
			// What an app reports is what it has just changed, so its
			// sources are swept now rather than at their next hour.
			appsv1.TriggerReceivedDetailType,
		},
		filesv1.Source: {
			// The organization's own files have no connection to announce
			// them, so the first shared one is what starts indexing them.
			filesv1.ObjectCreatedDetailType,
		},
	}
}

// application is what the consumer dispatches decoded events to. It is
// satisfied by *knowledge/internal/application.Service.
type application interface {
	OrganizationDeleted(context.Context, authv1.OrganizationDeleted) error
	MemberRemoved(context.Context, authv1.MemberRemoved) error
	ConnectionConnected(context.Context, appsv1.ConnectionConnected) error
	TriggerReceived(context.Context, appsv1.TriggerReceived) error
	FileStored(context.Context, filesv1.ObjectCreated) error
	ConnectionExpired(context.Context, appsv1.ConnectionExpired) error
	ConnectionRemoved(context.Context, appsv1.ConnectionRemoved) error
	// AlreadyHandled checks without recording anything; MarkHandled records,
	// and must only be called once the handler it followed has succeeded.
	AlreadyHandled(ctx context.Context, eventID string) (bool, error)
	MarkHandled(ctx context.Context, eventID, detailType string) error
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
			authv1.OrganizationDeletedDetailType: {source: authv1.Source, handle: decode(app.OrganizationDeleted)},
			authv1.MemberRemovedDetailType:       {source: authv1.Source, handle: decode(app.MemberRemoved)},
			appsv1.ConnectionConnectedDetailType: {source: appsv1.Source, handle: decode(app.ConnectionConnected)},
			appsv1.TriggerReceivedDetailType:     {source: appsv1.Source, handle: decode(app.TriggerReceived)},
			filesv1.ObjectCreatedDetailType:      {source: filesv1.Source, handle: decode(app.FileStored)},
			appsv1.ConnectionExpiredDetailType:   {source: appsv1.Source, handle: decode(app.ConnectionExpired)},
			appsv1.ConnectionRemovedDetailType:   {source: appsv1.Source, handle: decode(app.ConnectionRemoved)},
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
// The bus delivers at least once. The envelope's id is checked before the
// handler runs and recorded only once it has succeeded: recording it first
// would turn a handler that then fails into an event nobody acts on, because
// the redelivery would find it recorded and Poll would delete the message.
// A handler that does run twice — it succeeded but the record failed, or two
// deliveries raced — writes nothing the second time, because pausing and
// retiring change only sources that are still active or not yet removed.
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

	already, err := consumer.app.AlreadyHandled(ctx, detail.EventID)
	if err != nil {
		return fmt.Errorf("event %s: %w", detail.EventID, err)
	}
	if already {
		consumer.logger.InfoContext(ctx, "event already handled", "event_id", detail.EventID, "detail_type", event.DetailType)
		return nil
	}

	if err := subscribed.handle(ctx, detail.Data); err != nil {
		return fmt.Errorf("event %s: %s: %w", detail.EventID, event.DetailType, err)
	}
	// A concurrent redelivery may be recording the same event; the second
	// record is ignored.
	if err := consumer.app.MarkHandled(ctx, detail.EventID, event.DetailType); err != nil {
		return fmt.Errorf("event %s: mark handled: %w", detail.EventID, err)
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
