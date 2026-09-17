// Package teams assembles the teams service for a host: its API, the events it
// publishes, and the organization and member events it hears.
package teams

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/application"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/events"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/serviceconfig"
)

// Name is how ORYXA_SERVICES selects teams.
const Name = "teams"

// Definition is how a host builds teams.
var Definition = host.Definition{Name: Name, URLVariable: "TEAMS_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs the teams schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles teams on what the host shares.
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
	service := application.New(store, authclient.New(config.AuthURL), logger)

	publisher := events.NewPublisher(eventbridge.NewFromConfig(platform.AWS), platform.EventBusName)
	table, err := outbox.NewTable(store.Pool(), "teams_outbox")
	if err != nil {
		return host.Service{}, err
	}
	dispatcher := outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
		return publisher.PublishEvent(ctx, domain.Event{ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data})
	}), logger)
	workers := []host.Worker{dispatcher.Run}
	if config.EventsQueueURL != "" {
		consumer := events.NewConsumer(service, logger)
		queue := sqs.NewFromConfig(platform.AWS)
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) })
	} else {
		logger.Warn("TEAMS_EVENTS_QUEUE_URL is not set: teams will not hear about new, renamed or deleted organizations, or members leaving them")
	}

	server := httpserver.New(service, httpserver.Config{AllowedOrigins: config.AllowedOrigins}, logger)
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 30 * time.Second},
		Workers:  workers,
	}, nil
}
