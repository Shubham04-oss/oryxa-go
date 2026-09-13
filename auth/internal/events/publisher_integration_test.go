package events

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func TestPublisherWithEventBridge(t *testing.T) {
	endpoint := os.Getenv("AUTH_TEST_EVENTBRIDGE_ENDPOINT")
	if endpoint == "" {
		t.Skip("AUTH_TEST_EVENTBRIDGE_ENDPOINT is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion("ap-south-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load AWS config: %v", err)
	}
	client := eventbridge.NewFromConfig(config, func(options *eventbridge.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})
	publisher := NewPublisher(client, "default", "oryxa.auth")
	detail, _ := json.Marshal(map[string]string{"integration_test": "true"})

	err = publisher.PublishEvent(ctx, domain.Event{
		ID:         "it-eventbridge-" + time.Now().UTC().Format("20060102150405.000000000"),
		DetailType: "auth.integration-tested.v1",
		OccurredAt: time.Now().UTC(),
		Data:       detail,
	})
	if err != nil {
		t.Fatalf("PublishEvent() error = %v", err)
	}
}
