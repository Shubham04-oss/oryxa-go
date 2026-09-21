package apps

import (
	"context"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/events"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

// URLVariable is how another service reaches apps, such as agents finding the
// MCP endpoint.
const URLVariable = "APPS_URL"

// Definition is how a host builds apps.
var Definition = host.Definition{Name: Name, URLVariable: URLVariable, EventsQueueVariable: "APPS_EVENTS_QUEUE_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs apps' schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles apps on what the host shares.
func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	if len(config.AllowedOrigins) == 0 {
		logger.Warn("APPS_ALLOWED_ORIGINS is not set: members can manage apps only with bearer tokens, and no page can be returned to after connecting one")
	}
	if config.ComposioWebhookSecret == "" {
		logger.Warn("COMPOSIO_WEBHOOK_SECRET is not set: every webhook delivery will be refused, so no trigger will fire")
	}
	if config.NoAutoSubscribe {
		logger.Info("apps auto-subscribe is off (APPS_AUTO_SUBSCRIBE=false): a newly connected app is subscribed to nothing until a member asks, whatever the connection itself requested")
	} else {
		logger.Info("apps auto-subscribe is on: a newly connected app is subscribed to everything it can deliver unless the connection says otherwise")
	}

	client, err := composio.New(composio.Config{
		APIKey:  config.ComposioAPIKey,
		BaseURL: config.ComposioBaseURL,
		HTTP:    &http.Client{Timeout: 60 * time.Second, Transport: observability.Transport(nil)},
	})
	if err != nil {
		return host.Service{}, err
	}
	eventClient := eventbridge.NewFromConfig(platform.AWS, func(options *eventbridge.Options) {
		if config.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(config.EventBridgeEndpoint)
		}
	})
	signer, err := platform.EventAuth.Signer(Name)
	if err != nil {
		return host.Service{}, err
	}
	publisher, err := events.NewPublisher(eventClient, platform.EventBusName, signer)
	if err != nil {
		return host.Service{}, err
	}

	service, err := New(Dependencies{
		Store:     postgres.New(platform.Database),
		Directory: authclient.New(config.AuthURL),
		Composio:  client,
		Sink:      publisher,
		Verifier:  platform.EventAuth.Verifier(),
		Logger:    logger,
	}, Config{
		PublicBaseURL:   config.PublicBaseURL,
		AllowedOrigins:  config.AllowedOrigins,
		WebhookSecret:   config.ComposioWebhookSecret,
		NoAutoSubscribe: config.NoAutoSubscribe,
	})
	if err != nil {
		return host.Service{}, err
	}

	workers := make([]host.Worker, 0, len(service.Workers())+1)
	for _, worker := range service.Workers() {
		workers = append(workers, host.Worker(worker))
	}
	if config.EventsQueueURL != "" {
		queue := sqs.NewFromConfig(platform.AWS, func(options *sqs.Options) {
			if config.SQSEndpoint != "" {
				options.BaseEndpoint = aws.String(config.SQSEndpoint)
			}
		})
		consumer := service.Consumer()
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) })
	} else {
		logger.Warn("APPS_EVENTS_QUEUE_URL is not set: apps will not disconnect a member's apps when they leave an organization")
	}

	logger.Info("apps mounted", "webhook_url", config.PublicBaseURL+WebhookPath)
	return host.Service{
		Handler: service.Handler(),
		Routes:  service.Routes(),
		// A tool run waits on a third-party app, which is slower than
		// anything else Oryxa serves; a write must not be cut off halfway.
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 120 * time.Second},
		Workers:  workers,
	}, nil
}
