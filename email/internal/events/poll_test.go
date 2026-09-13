package events

import (
	"context"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
)

type fakeQueue struct {
	batches [][]types.Message
	deleted []types.DeleteMessageBatchRequestEntry
	cancel  context.CancelFunc
}

func (f *fakeQueue) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	if len(f.batches) == 0 {
		f.cancel()
		return nil, ctx.Err()
	}
	batch := f.batches[0]
	f.batches = f.batches[1:]
	return &sqs.ReceiveMessageOutput{Messages: batch}, nil
}

func (f *fakeQueue) DeleteMessageBatch(_ context.Context, input *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	f.deleted = append(f.deleted, input.Entries...)
	return &sqs.DeleteMessageBatchOutput{}, nil
}

func TestPollDeletesOnlyHandledMessages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := &fakeQueue{cancel: cancel, batches: [][]types.Message{{
		{
			MessageId:     aws.String("handled"),
			ReceiptHandle: aws.String("receipt-handled"),
			Body:          aws.String(domainBody(autheventsv1.PasswordResetDeliveryRequestedDetailType, 1, resetData)),
		},
		{
			MessageId:     aws.String("failed"),
			ReceiptHandle: aws.String("receipt-failed"),
			Body:          aws.String("{"),
		},
	}}}
	consumer := NewConsumer(&fakeApplication{}, slog.New(slog.DiscardHandler))

	consumer.Poll(ctx, queue, "http://localhost:4566/000000000000/email-events")

	if len(queue.deleted) != 1 || aws.ToString(queue.deleted[0].ReceiptHandle) != "receipt-handled" {
		t.Fatalf("deleted = %+v, want only receipt-handled", queue.deleted)
	}
}
