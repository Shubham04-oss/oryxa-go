package events

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
)

func TestPublisherWithEventBridgeOnMini(t *testing.T) {
	endpoint := os.Getenv("INTEGRATIONS_TEST_AWS_ENDPOINT")
	if endpoint == "" {
		t.Skip("INTEGRATIONS_TEST_AWS_ENDPOINT is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("ap-south-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	client := eventbridge.NewFromConfig(config, func(options *eventbridge.Options) { options.BaseEndpoint = aws.String(endpoint) })
	bus := "oryxa-integrations-test-" + time.Now().UTC().Format("150405000000000")
	if _, err := client.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{Name: aws.String(bus)}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = client.DeleteEventBus(context.Background(), &eventbridge.DeleteEventBusInput{Name: aws.String(bus)})
	}()
	err = (Publisher{Client: client, BusName: bus}).PublishEvent(ctx, domain.Event{EventID: "evt-mini", Source: "oryxa.integrations", DetailType: "integrations.test.v1", Version: 1, OrganizationID: "org-mini", OccurredAt: time.Now().UTC(), Data: map[string]any{"safe": true}})
	if err != nil {
		t.Fatal(err)
	}
}
