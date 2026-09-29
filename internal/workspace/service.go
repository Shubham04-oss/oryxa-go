// Package workspace assembles the workspace configuration API in the one
// Oryxa binary. Its applier reaches the existing service APIs as the admin.
package workspace

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/events"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/httpserver"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

const Name = "workspace"

var Definition = host.Definition{Name: Name, URLVariable: "WORKSPACE_URL", EventsQueueVariable: "WORKSPACE_EVENTS_QUEUE_URL", Migrate: Migrate, Mount: Mount}

func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return store.New(platform.Database).Migrate(ctx)
}

func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	apiURL := platform.Getenv("WORKSPACE_API_URL")
	if apiURL == "" {
		apiURL = platform.Getenv("AUTH_URL")
	}
	if apiURL == "" {
		return host.Service{}, fmt.Errorf("WORKSPACE_API_URL or AUTH_URL is required")
	}
	storage := store.New(platform.Database)
	server, err := httpserver.New(storage, apiURL)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	if logger == nil {
		logger = slog.Default()
	}
	workers := []host.Worker{func(ctx context.Context) { purgeExpiredPlans(ctx, storage, logger) }}
	if queueURL := platform.Getenv("WORKSPACE_EVENTS_QUEUE_URL"); queueURL != "" {
		consumer, err := events.NewConsumer(storage, platform.EventAuth.Verifier(), logger)
		if err != nil {
			return host.Service{}, err
		}
		queue := sqs.NewFromConfig(platform.AWS)
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queue, queueURL) })
	} else {
		logger.Warn("WORKSPACE_EVENTS_QUEUE_URL is not set: workspace will not forget departed members or deleted teams")
	}
	return host.Service{
		Workers: workers,
		Handler: server, Routes: server.Patterns(),
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 2 * time.Minute},
	}, nil
}

// Approval payloads expire even when the lifecycle queue is idle or omitted in
// local development. Run once at startup, then bound retention to fifteen minutes.
func purgeExpiredPlans(ctx context.Context, storage *store.Store, logger *slog.Logger) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		purgeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := storage.PurgeExpiredPlans(purgeCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "purge expired workspace plans failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
