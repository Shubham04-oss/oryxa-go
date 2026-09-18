package knowledge

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/membertoken"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/application"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/events"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/filesclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
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

// Mount assembles knowledge on what the host shares. GitHub, Notion and
// Slack are mounted once COMPOSIO_API_KEY says app sources are on; files,
// which reads as the member who registered it, once FILES_URL and
// KNOWLEDGE_SERVICE_KEY_ID give it a way to act as one.
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
	extractors := sources.Registry{}
	if config.ComposioAPIKey != "" {
		composioClient, err := composio.New(composio.Config{APIKey: config.ComposioAPIKey, BaseURL: config.ComposioBaseURL})
		if err != nil {
			return host.Service{}, err
		}
		apps = appsclient.New(config.AppsURL)
		for provider, extractor := range Builtin(composioClient) {
			extractors[provider] = extractor
		}
	} else {
		logger.Warn("COMPOSIO_API_KEY is not set: app sources (GitHub, Notion, Slack) are off")
	}

	// Files is the one source with nobody to act as: every route there wants
	// a member's own credentials, and a scheduled pass has none. Auth's
	// standing grant is what fills that, so it is mounted only where both
	// halves of reaching auth are configured.
	var grants application.Grants
	switch {
	case config.FilesURL == "":
		logger.Warn("FILES_URL is not set: the organization's own files are not indexed")
	case config.ServiceKeyID == "":
		logger.Warn("KNOWLEDGE_SERVICE_KEY_ID is not set: the organization's own files are not indexed, " +
			"since reading them means acting as the member who registered them")
	default:
		grants = membertoken.New(membertoken.Config{
			AuthURL: config.AuthURL,
			Signer: serviceauth.Signer{
				Client: kms.NewFromConfig(platform.AWS), KeyID: config.ServiceKeyID, Service: Name,
			},
		})
		files := sources.NewFiles(filesclient.New(config.FilesURL))
		extractors[files.Provider()] = files
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
		Grants:     grants,
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
