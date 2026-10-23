// Package files assembles the files service for a host: its API, the object
// store it fronts, and the organization, member and team events it hears.
package files

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/files/internal/application"
	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/blob"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/files/internal/events"
	"github.com/Shubham04-oss/oryxa-go/files/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/files/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/files/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/files/internal/teamsclient"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
)

// Name is how ORYXA_SERVICES selects files.
const Name = "files"

// Definition is how a host builds files.
var Definition = host.Definition{Name: Name, URLVariable: "FILES_URL", EventsQueueVariable: "FILES_EVENTS_QUEUE_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs the files schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	if err := postgres.New(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	return access.Migrate(ctx, platform.Database)
}

// Mount assembles files on what the host shares.
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
	blobs := blob.NewS3FromConfig(platform.AWS, config.S3Endpoint, config.BucketName)
	var teams application.Teams
	if config.TeamsURL != "" {
		teams = teamsclient.New(config.TeamsURL)
	} else {
		logger.Warn("TEAMS_URL is not set: team-scoped files cannot be created")
	}
	service := application.New(store, blobs, authclient.New(config.AuthURL), teams, access.NewLog(platform.Database.Pool, logger), logger)

	signer, err := platform.EventAuth.Signer(Name)
	if err != nil {
		return host.Service{}, err
	}
	publisher, err := events.NewPublisher(eventbridge.NewFromConfig(platform.AWS), platform.EventBusName, signer)
	if err != nil {
		return host.Service{}, err
	}
	table, err := outbox.NewTable(store.Pool(), "files_outbox")
	if err != nil {
		return host.Service{}, err
	}
	dispatcher := outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
		return publisher.PublishEvent(ctx, domain.Event{
			ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data,
			TraceContext: event.TraceContext,
		})
	}), logger)
	workers := []host.Worker{dispatcher.Run}
	if config.EventsQueueURL != "" {
		consumer, err := events.NewConsumer(service, platform.EventAuth.Verifier(), logger)
		if err != nil {
			return host.Service{}, err
		}
		queue := sqs.NewFromConfig(platform.AWS)
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) })
	} else {
		logger.Warn("FILES_EVENTS_QUEUE_URL is not set: files will not hear about organizations, members leaving them, or deleted teams")
	}

	server := httpserver.New(service, httpserver.Config{AllowedOrigins: config.AllowedOrigins}, logger)
	logger.Info("files mounted", "bucket", config.BucketName)
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: host.Timeouts{Read: 30 * time.Second, Write: 60 * time.Second},
		Workers:  workers,
	}, nil
}
