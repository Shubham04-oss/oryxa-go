package queue

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

func TestWebhookQueueWithSQS(t *testing.T) {
	endpoint := os.Getenv("INTEGRATIONS_TEST_AWS_ENDPOINT")
	if endpoint == "" {
		t.Skip("INTEGRATIONS_TEST_AWS_ENDPOINT is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("ap-south-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := sqs.NewFromConfig(config, func(options *sqs.Options) { options.BaseEndpoint = aws.String(endpoint) })
	name := "oryxa-integrations-test-" + time.Now().UTC().Format("20060102150405000000000")
	created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name)})
	if err != nil {
		t.Fatalf("create queue: %v", err)
	}
	defer func() {
		_, _ = client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: created.QueueUrl})
	}()

	queue := WebhookQueue{Client: client, QueueURL: aws.ToString(created.QueueUrl)}
	want := webhooks.Envelope{
		InvocationID: "invocation-1", OrganizationID: "org-1", ProfileID: "app-1",
		Provider: "slack", PieceName: "@activepieces/piece-slack", PieceVersion: "0.17.10",
		TriggerName: "new-message", IdentifierValue: "T123", RawBody: []byte(`{"team_id":"T123"}`),
		ReceivedAt: time.Now().UTC().Truncate(time.Nanosecond),
	}
	if err := queue.EnqueueWebhook(ctx, want); err != nil {
		t.Fatal(err)
	}
	received, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: created.QueueUrl, MaxNumberOfMessages: 1, WaitTimeSeconds: 1})
	if err != nil {
		t.Fatalf("receive message: %v", err)
	}
	if len(received.Messages) != 1 {
		t.Fatalf("received %d messages, want 1", len(received.Messages))
	}
	var got webhooks.Envelope
	if err := json.Unmarshal([]byte(aws.ToString(received.Messages[0].Body)), &got); err != nil {
		t.Fatal(err)
	}
	if got.InvocationID != want.InvocationID || got.IdentifierValue != "T123" || string(got.RawBody) != string(want.RawBody) {
		t.Fatalf("unexpected queued envelope: %+v", got)
	}
}
