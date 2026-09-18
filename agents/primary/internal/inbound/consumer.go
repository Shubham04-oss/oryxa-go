// Package inbound handles what other services publish that agents must act
// on: a member's app reporting something, which waits to wake Krude for
// them, and a member leaving an organization or the organization going
// away, which takes their conversations with it.
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

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/wakeups"
)

// Subscriptions are the detail types agents consumes, by source, for a host
// provisioning the rule that feeds its queue.
func Subscriptions() map[string][]string {
	return map[string][]string{
		appsv1.Source: {appsv1.TriggerReceivedDetailType},
		authv1.Source: {authv1.MemberRemovedDetailType, authv1.OrganizationDeletedDetailType},
	}
}

// Recorder keeps a trigger event until it wakes Krude. wakeups.Store
// implements it.
type Recorder interface {
	Record(ctx context.Context, event wakeups.Event) (bool, error)
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
}

// NewConsumer returns a consumer that records triggers with recorder and,
// when a member or an organization is gone, runs every forgetter.
func NewConsumer(recorder Recorder, forget []Forgetter, logger *slog.Logger) *Consumer {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Consumer{recorder: recorder, forget: forget, logger: logger}
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

// Handle decodes one SQS message body as an EventBridge event and acts on
// it. The bus delivers at least once, and every handler is safe to repeat: a
// trigger is recorded by its event's own id, and forgetting what is already
// gone removes nothing more.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
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
	switch {
	case event.Source == appsv1.Source && event.DetailType == appsv1.TriggerReceivedDetailType:
		return consumer.triggerReceived(ctx, envelope)
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

func (consumer *Consumer) triggerReceived(ctx context.Context, envelope receivedEnvelope) error {
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
	recorded, err := consumer.recorder.Record(ctx, wakeups.Event{
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
