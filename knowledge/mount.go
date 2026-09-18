package knowledge

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/application"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/events"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

// Definition is how a host builds knowledge. Nothing calls knowledge over
// HTTP, so it names no URL variable of its own for other services to reach it
// by.
var Definition = host.Definition{Name: Name, EventsQueueVariable: "KNOWLEDGE_EVENTS_QUEUE_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs knowledge's schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles knowledge on what the host shares. The files source waits
// on a Grants implementation — see TODO.md — so it is never mounted here
// either; GitHub, Notion and Slack are mounted only once COMPOSIO_API_KEY
// says app sources are on.
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
		logger.Warn("KNOWLEDGE_ALLOWED_ORIGINS is not set: members can manage sources only with bearer tokens")
	}

	var apps application.Apps
	var extractors sources.Registry
	if config.ComposioAPIKey != "" {
		composioClient, err := composio.New(composio.Config{APIKey: config.ComposioAPIKey, BaseURL: config.ComposioBaseURL})
		if err != nil {
			return host.Service{}, err
		}
		apps = appsclient.New(config.AppsURL)
		extractors = Builtin(composioClient)
	} else {
		logger.Warn("COMPOSIO_API_KEY is not set: app sources (GitHub, Notion, Slack) are off")
	}

	eventClient := eventbridge.NewFromConfig(platform.AWS, func(options *eventbridge.Options) {
		if config.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(config.EventBridgeEndpoint)
		}
	})
	service, err := New(Dependencies{
		Store:      postgres.New(platform.Database),
		Directory:  authclient.New(config.AuthURL),
		Apps:       apps,
		Grants:     nil,
		Sink:       events.NewPublisher(eventClient, platform.EventBusName),
		Extractors: extractors,
		Logger:     logger,
	}, Config{AllowedOrigins: config.AllowedOrigins, FullSweepInterval: config.FullSweepInterval})
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
		logger.Warn("KNOWLEDGE_EVENTS_QUEUE_URL is not set: knowledge will not pause or retire sources when their connection, member or organization goes away")
	}

	return host.Service{
		Handler:  service.Handler(),
		Routes:   service.Routes(),
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 30 * time.Second},
		Workers:  workers,
	}, nil
}
