// Package integrations assembles the integrations service for a host: the
// webhook ingress and subscriptions API, actions and MCP tools run with a
// connection, the workers that renew and remove provider registrations, and
// the events it publishes and hears.
package integrations

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/application"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/connectionsclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/events"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/mcpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// Name is how ORYXA_SERVICES selects integrations.
const Name = "integrations"

// Definition is how a host builds integrations.
var Definition = host.Definition{Name: Name, URLVariable: "INTEGRATIONS_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs the integrations schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles integrations on what the host shares.
func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	store := postgres.New(platform.Database)

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
	queueClient := sqs.NewFromConfig(platform.AWS, func(options *sqs.Options) {
		if config.SQSEndpoint != "" {
			options.BaseEndpoint = aws.String(config.SQSEndpoint)
		}
	})

	if len(config.AllowedOrigins) == 0 {
		logger.Warn("INTEGRATIONS_ALLOWED_ORIGINS is not set: members can manage subscriptions only with bearer tokens")
	}
	signer := serviceauth.Signer{Client: kmsClient, KeyID: config.ServiceKeyID, Service: config.ServiceName}
	service := application.New(application.Dependencies{
		Store:       store,
		Directory:   authclient.New(config.AuthURL),
		Connections: connectionsclient.New(config.ConnectionsURL, signer),
		Sealer:      secrets.Envelope{Keys: secrets.KMSDataKeys{Client: kmsClient, KeyID: config.KMSKeyID}, KeyID: config.KMSKeyID},
		Providers:   providers.Builtin(),
		HTTP:        providers.NewHTTPClient(),
		Logger:      logger,
	}, application.Config{PublicBaseURL: config.PublicBaseURL, ServiceName: config.ServiceName})

	publisher := events.NewPublisher(eventClient, platform.EventBusName)
	table, err := outbox.NewTable(store.Pool(), "integrations_outbox")
	if err != nil {
		return host.Service{}, err
	}
	dispatcher := outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
		return publisher.PublishEvent(ctx, domain.Event{ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data})
	}), logger)
	// Renewal and cleanup claim under leases, so every replica may run them.
	workers := []host.Worker{dispatcher.Run, service.RunWorkers}
	if config.EventsQueueURL != "" {
		consumer := events.NewConsumer(service, logger)
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queueClient, config.EventsQueueURL) })
	} else {
		logger.Warn("INTEGRATIONS_EVENTS_QUEUE_URL is not set: integrations will not stop subscriptions when connections, members or organizations go away")
	}

	server := httpserver.New(service, httpserver.Config{
		AllowedOrigins: config.AllowedOrigins, MCP: mcpserver.New(service, logger),
	}, logger)
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: host.Timeouts{Read: 30 * time.Second, Write: 60 * time.Second},
		Workers:  workers,
	}, nil
}
