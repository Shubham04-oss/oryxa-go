package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type ConsumerSQSAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

type WebhookProcessor interface {
	ProcessWebhook(context.Context, webhooks.Envelope) error
}

type Consumer struct {
	Client    ConsumerSQSAPI
	QueueURL  string
	Processor WebhookProcessor
}

func (c Consumer) HandleMessage(ctx context.Context, body string) error {
	if c.Processor == nil {
		return errors.New("webhook processor is not configured")
	}
	var envelope webhooks.Envelope
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return fmt.Errorf("decode webhook queue message: %w", err)
	}
	if err := c.Processor.ProcessWebhook(ctx, envelope); err != nil {
		return fmt.Errorf("process webhook queue message: %w", err)
	}
	return nil
}

func (c Consumer) Run(ctx context.Context) {
	if c.Client == nil || c.QueueURL == "" || c.Processor == nil {
		log.Printf("webhook consumer is not configured")
		return
	}
	for ctx.Err() == nil {
		output, err := c.Client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(c.QueueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 20, VisibilityTimeout: 60})
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("receive webhook messages: %v", err)
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		for _, message := range output.Messages {
			if err := c.HandleMessage(ctx, aws.ToString(message.Body)); err != nil {
				log.Printf("webhook message failed: %v", err)
				continue
			}
			if _, err := c.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(c.QueueURL), ReceiptHandle: message.ReceiptHandle}); err != nil {
				log.Printf("delete webhook message: %v", err)
			}
		}
	}
}
