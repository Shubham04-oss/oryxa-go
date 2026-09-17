// Package connections assembles the connections service for a host: its
// API, the apps and connections it manages, the outbox it dispatches, the
// heartbeat and cleanup workers, the service identity it verifies, and the
// auth and teams events it hears.
package connections

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/application"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/events"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/teamsclient"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// Name is how ORYXA_SERVICES selects connections.
const Name = "connections"

// serviceAudience is the audience other services name in tokens for
// connections.
const serviceAudience = "connections"

// Definition is how a host builds connections.
var Definition = host.Definition{Name: Name, URLVariable: "CONNECTIONS_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs the connections schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles connections on what the host shares.
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

	var teams application.Teams
	if config.TeamsURL != "" {
		teams = teamsclient.New(config.TeamsURL)
	} else {
		logger.Warn("TEAMS_URL is not set: team connections cannot be made or seen")
	}
	if len(config.AllowedOrigins) == 0 {
		logger.Warn("CONNECTIONS_ALLOWED_ORIGINS is not set: connect flows have nowhere to return to")
	}
	service := application.New(application.Dependencies{
		Store:          store,
		Directory:      authclient.New(config.AuthURL),
		Teams:          teams,
		Sealer:         secrets.Envelope{Keys: secrets.KMSDataKeys{Client: kmsClient, KeyID: config.KMSKeyID}, KeyID: config.KMSKeyID},
		ProviderClient: providers.Client{HTTP: providers.NewHTTPClient()},
		Providers:      providers.Builtin(),
		Logger:         logger,
	}, application.Config{PublicBaseURL: config.PublicBaseURL, AllowedOrigins: config.AllowedOrigins})

	publisher := events.NewPublisher(eventClient, platform.EventBusName)
	table, err := outbox.NewTable(store.Pool(), "connections_outbox")
	if err != nil {
		return host.Service{}, err
	}
	dispatcher := outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
		return publisher.PublishEvent(ctx, domain.Event{ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data})
	}), logger)
	// Both workers claim with SKIP LOCKED, so every replica may run them.
	workers := []host.Worker{dispatcher.Run, service.RunHeartbeat, service.RunCleanup}
	if config.EventsQueueURL != "" {
		consumer := events.NewConsumer(service, logger)
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queueClient, config.EventsQueueURL) })
	} else {
		logger.Warn("CONNECTIONS_EVENTS_QUEUE_URL is not set: connections will not hear about deleted organizations, removed members, or deleted teams")
	}

	var services httpserver.ServiceVerifier
	if len(config.ServiceCallers) > 0 {
		verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Keys: kmsClient, Audience: serviceAudience, Callers: config.ServiceCallers})
		if err != nil {
			return host.Service{}, err
		}
		services = verifier
	} else {
		logger.Warn("CONNECTIONS_SERVICE_CALLERS is not set: the internal API refuses every call, so no service can get tokens")
	}

	server := httpserver.New(service, httpserver.Config{AllowedOrigins: config.AllowedOrigins, Services: services}, logger)
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: host.Timeouts{Read: 30 * time.Second, Write: 60 * time.Second},
		Workers:  workers,
	}, nil
}
