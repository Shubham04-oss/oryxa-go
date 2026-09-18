// Package batching assembles the service that turns raw app triggers into
// durable, per-member batches ready for an agent turn.
package batching

import (
	"context"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/application"
	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/batching/internal/events"
	"github.com/Shubham04-oss/oryxa-go/batching/internal/jev"
	"github.com/Shubham04-oss/oryxa-go/batching/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/batching/internal/serviceconfig"
	batchingv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/batching/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
)

const Name = "batching"

var Definition = host.Definition{Name: Name, EventsQueueVariable: "BATCHING_EVENTS_QUEUE_URL", Migrate: Migrate, Mount: Mount}

func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	var classifier application.Classifier = application.DigestClassifier{}
	if config.TypeSafeAPIKey != "" {
		jevClassifier, err := jev.New(&http.Client{Timeout: config.JevTimeout}, jev.Config{
			APIKey: config.TypeSafeAPIKey, BaseURL: config.TypeSafeBaseURL, Model: config.JevModel,
			ImmediateProbability: config.JevImmediate, MinimumConfidence: config.JevMinConfidence,
		})
		if err != nil {
			return host.Service{}, err
		}
		classifier = application.NewResilientClassifier(jevClassifier, logger)
	} else {
		logger.Warn("TYPESAFE_API_KEY is not set: batching will use deterministic digest classification")
	}
	rule := domain.Rule{Size: config.BatchSize, MaxWait: config.MaxWait, Largest: config.LargestBatch}
	scheduler, err := domain.NewAdaptiveScheduler(rule)
	if err != nil {
		return host.Service{}, err
	}
	store := postgres.New(platform.Database, scheduler)
	service := application.New(store, classifier, application.Config{
		Rule:     rule,
		Interval: config.SweepInterval, ClaimLease: config.ClaimLease, GroupsPerSweep: config.GroupsPerSweep,
	}, logger)

	eventClient := eventbridge.NewFromConfig(platform.AWS, func(options *eventbridge.Options) {
		if config.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(config.EventBridgeEndpoint)
		}
	})
	publisher := events.NewPublisher(eventClient, platform.EventBusName)
	// A published batch carries every trigger's app payload — mail bodies,
	// messages — so once it is on the bus its copy here is blanked rather
	// than kept for the outbox's retention, as apps does with its own.
	table, err := outbox.NewTable(store.Pool(), "batching_outbox",
		outbox.RedactOnPublish(batchingv1.TriggerBatchReadyDetailType))
	if err != nil {
		return host.Service{}, err
	}
	dispatcher := outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
		return publisher.PublishEvent(ctx, domain.Event{
			ID: event.ID, DetailType: event.DetailType, Data: event.Data, OccurredAt: event.OccurredAt,
		})
	}), logger)
	workers := []host.Worker{dispatcher.Run, service.Run}
	if config.EventsQueueURL != "" {
		queue := sqs.NewFromConfig(platform.AWS, func(options *sqs.Options) {
			if config.SQSEndpoint != "" {
				options.BaseEndpoint = aws.String(config.SQSEndpoint)
			}
		})
		consumer := events.NewConsumer(service, logger)
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) })
	} else {
		logger.Warn("BATCHING_EVENTS_QUEUE_URL is not set: batching will publish no app-trigger batches")
	}
	logger.Info("batching mounted", "cold_target", config.BatchSize, "hard_cadence", config.MaxWait.String(),
		"largest_batch", config.LargestBatch, "policy", domain.AdaptivePolicyVersion,
		"jev_enabled", config.TypeSafeAPIKey != "", "jev_model", config.JevModel)
	return host.Service{Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 15 * time.Second}, Workers: workers}, nil
}
