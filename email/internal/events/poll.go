package events

import (
	"context"
	"strconv"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

type queueAPI interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(context.Context, *sqs.DeleteMessageBatchInput, ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
}

// Poll does outside Lambda what Lambda's SQS event source mapping does inside
// it: each batch goes through HandleSQS, handled messages are deleted, and
// failed ones stay on the queue until the redrive policy moves them aside.
// It returns when ctx is cancelled.
func (c *Consumer) Poll(ctx context.Context, client queueAPI, queueURL string) {
	// Poll runs as this consumer's only goroutine for its whole life, calling
	// HandleSQS sequentially, so setting this once up front and never
	// touching it again needs no lock.
	c.queueName = observability.QueueName(queueURL)
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
			c.logger.ErrorContext(ctx, "receive from queue failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if len(output.Messages) == 0 {
			continue
		}

		var batch lambdaevents.SQSEvent
		for _, message := range output.Messages {
			batch.Records = append(batch.Records, lambdaevents.SQSMessage{
				MessageId:     aws.ToString(message.MessageId),
				ReceiptHandle: aws.ToString(message.ReceiptHandle),
				Body:          aws.ToString(message.Body),
			})
		}
		response, err := c.HandleSQS(ctx, batch)
		if err != nil {
			c.logger.ErrorContext(ctx, "handle batch failed", "error", err)
			continue
		}
		c.deleteHandled(ctx, client, queueURL, batch, response)
	}
}

func (c *Consumer) deleteHandled(ctx context.Context, client queueAPI, queueURL string, batch lambdaevents.SQSEvent, response lambdaevents.SQSEventResponse) {
	failed := make(map[string]bool, len(response.BatchItemFailures))
	for _, failure := range response.BatchItemFailures {
		failed[failure.ItemIdentifier] = true
	}

	var entries []types.DeleteMessageBatchRequestEntry
	for index, record := range batch.Records {
		if !failed[record.MessageId] {
			entries = append(entries, types.DeleteMessageBatchRequestEntry{
				Id:            aws.String(strconv.Itoa(index)),
				ReceiptHandle: aws.String(record.ReceiptHandle),
			})
		}
	}
	if len(entries) == 0 {
		return
	}

	output, err := client.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{
		QueueUrl: aws.String(queueURL),
		Entries:  entries,
	})
	if err != nil {
		c.logger.ErrorContext(ctx, "delete handled messages failed", "error", err)
		return
	}
	for _, failure := range output.Failed {
		c.logger.ErrorContext(ctx, "delete handled message failed",
			"code", aws.ToString(failure.Code),
			"message", aws.ToString(failure.Message),
		)
	}
}
