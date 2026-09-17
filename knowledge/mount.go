package knowledge

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/connectionsclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/events"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/serviceconfig"
)

// Definition is how a host builds knowledge. Nothing calls knowledge over
// HTTP, so it names no URL variable of its own for other services to reach it
// by.
var Definition = host.Definition{Name: Name, Migrate: Migrate, Mount: Mount}

// Migrate installs knowledge's schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles knowledge on what the host shares. Grants has no
// implementation yet, so an internal files source stays unavailable until one
// is supplied — see TODO.md.
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

	kmsClient := kms.NewFromConfig(platform.AWS, func(options *kms.Options) {
		if config.KMSEndpoint != "" {
			options.BaseEndpoint = aws.String(config.KMSEndpoint)
		}
	})
	eventClient := eventbridge.NewFromConfig(platform.AWS, func(options *eventbridge.Options) {
		if config.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(config.EventBridgeEndpoint)
		}
	})
	signer := serviceauth.Signer{Client: kmsClient, KeyID: config.ServiceKeyID, Service: config.ServiceName}

	service, err := New(Dependencies{
		Store:       postgres.New(platform.Database),
		Directory:   authclient.New(config.AuthURL),
		Connections: connectionsclient.New(config.ConnectionsURL, signer),
		Grants:      nil,
		Sink:        events.NewPublisher(eventClient, platform.EventBusName),
		Extractors:  Builtin(),
		Logger:      logger,
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
		logger.Warn("KNOWLEDGE_EVENTS_QUEUE_URL is not set: knowledge will not pause or retire sources when their connection, app, member or organization goes away")
	}

	return host.Service{
		Handler:  service.Handler(),
		Routes:   service.Routes(),
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 30 * time.Second},
		Workers:  workers,
	}, nil
}
