package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const defaultMaxMessageBytes = 256 * 1024

type SQSAPI interface {
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

type WebhookQueue struct {
	Client          SQSAPI
	QueueURL        string
	MaxMessageBytes int
}

func (q WebhookQueue) EnqueueWebhook(ctx context.Context, envelope webhooks.Envelope) error {
	if q.Client == nil || q.QueueURL == "" {
		return errors.New("webhook SQS queue is not configured")
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode webhook envelope: %w", err)
	}
	maxBytes := q.MaxMessageBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxMessageBytes
	}
	if len(body) > maxBytes {
		return fmt.Errorf("webhook envelope is %d bytes; limit is %d", len(body), maxBytes)
	}
	_, err = q.Client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(q.QueueURL),
		MessageBody: aws.String(string(body)),
	})
	if err != nil {
		return fmt.Errorf("send webhook envelope: %w", err)
	}
	return nil
}
