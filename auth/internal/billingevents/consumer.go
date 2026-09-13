// Package billingevents consumes the billing events auth acts on: the
// entitlements that set each organization's member limit.
package billingevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"
)

// Subscriptions are the billing events auth consumes. The rule delivering them
// to auth's queue is built from this list.
func Subscriptions() []string {
	return []string{billingeventsv1.EntitlementsChangedDetailType}
}

type application interface {
	ApplyBillingEntitlements(context.Context, billingeventsv1.EntitlementsChanged) error
}

type Consumer struct {
	app application
}

func NewConsumer(app application) *Consumer {
	return &Consumer{app: app}
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

// Handle processes one SQS message body: an EventBridge event from billing.
func (consumer *Consumer) Handle(ctx context.Context, body string) error {
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
	var entitlements billingeventsv1.EntitlementsChanged
	if err := json.Unmarshal(detail.Data, &entitlements); err != nil {
		return fmt.Errorf("event %s: decode entitlements: %w", detail.EventID, err)
	}
	if err := consumer.app.ApplyBillingEntitlements(ctx, entitlements); err != nil {
		return fmt.Errorf("event %s: apply entitlements: %w", detail.EventID, err)
	}
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
			log.Printf("receive billing events: %v", err)
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
				log.Printf("billing event %s failed: %v", aws.ToString(message.MessageId), err)
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
				log.Printf("delete handled billing events: %v", err)
			}
			continue
		}
		for _, failure := range deleted.Failed {
			log.Printf("delete handled billing event: %s: %s", aws.ToString(failure.Code), aws.ToString(failure.Message))
		}
	}
}
