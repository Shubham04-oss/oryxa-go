// Package primary assembles the agents service for a host: Krude, and the
// run API a member's conversations with it go through. The agents
// themselves, and everything they run with, live under internal/.
package primary

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/assembly"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
)

// Name is how ORYXA_SERVICES selects agents primary.
const Name = "agents"

// schema is where agents keeps everything: ADK's sessions, the runs, and the
// outbox.
const schema = "agents_primary"

// closeTimeout bounds stopping what Krude opened, such as AgentCore sessions,
// on the way down.
const closeTimeout = 10 * time.Second

// Definition is how a host builds agents primary.
var Definition = host.Definition{Name: Name, Migrate: Migrate, Mount: Mount}

// Migrate installs agents' tables: the runs and the outbox, named with their
// schema, on the host's pool; then ADK's, which ADK names without one, over
// a pool of their own whose search_path leads with agents_primary.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	if err := runstore.New(platform.Database).Migrate(ctx); err != nil {
		return err
	}
	scoped, err := platform.Database.WithSchema(ctx, schema, "oryxa-migrate-agents", 2)
	if err != nil {
		return err
	}
	defer scoped.Close()
	return adksession.Migrate(ctx, scoped)
}

// Mount assembles agents on what the host shares. Without a Google Cloud
// project there is no model to run Krude on, so the service mounts its
// routes answering 503 with the reason, rather than stopping every other
// service in the process from starting.
func Mount(ctx context.Context, platform host.Platform) (host.Service, error) {
	settings, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	timeouts := host.Timeouts{
		Read: 15 * time.Second,
		// No write deadline: a run streams its answer for as long as it
		// takes, so the run route sets its own through
		// http.ResponseController.
		Write: 0,
	}
	if settings.GoogleCloudProject == "" {
		logger.Warn("GOOGLE_CLOUD_PROJECT is not set: agents mounts, and answers every conversation with 503, until there is a model to run Krude on")
		server := httpserver.New(nil, nil, httpserver.Config{Unavailable: "Krude is not set up on this server."}, logger)
		return host.Service{Handler: server, Routes: server.Patterns(), Timeouts: timeouts}, nil
	}
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	if settings.AuthURL == "" {
		return host.Service{}, errors.New("AUTH_URL is required: agents asks auth who is calling")
	}
	if len(settings.AllowedOrigins) == 0 {
		logger.Warn("AGENTS_ALLOWED_ORIGINS is not set: members can reach Krude only with bearer tokens, not from the web app")
	}

	prices, err := pricing.ParseOverrides(settings.ModelPrices)
	if err != nil {
		return host.Service{}, err
	}
	priceTable, err := pricing.NewTable(prices)
	if err != nil {
		return host.Service{}, err
	}

	// ADK names its tables without a schema, so its sessions live on a pool
	// whose search_path leads with agents_primary. It connects on first use.
	pool, err := platform.Database.WithSchema(ctx, schema, "oryxa-agents", int32(settings.DatabaseMaxConns))
	if err != nil {
		return host.Service{}, err
	}
	sessionStore, err := adksession.NewStore(pool)
	if err != nil {
		pool.Close()
		return host.Service{}, err
	}
	sessions := sessionStore.Sessions(events.NewSessionRecorder(events.NewOutbox(pool)))

	krude, err := assembly.Build(ctx, assembly.Options{Settings: settings, AWS: platform.AWS, Logger: logger})
	if err != nil {
		sessionStore.Close()
		return host.Service{}, fmt.Errorf("assemble Krude: %w", err)
	}
	closeAll := func() {
		closeContext, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		if err := krude.Close(closeContext); err != nil {
			logger.Warn("stop what Krude opened", "error", err)
		}
		sessionStore.Close()
	}

	service, err := threads.New(threads.Config{
		AppName:    agents.RootName,
		Agent:      krude.Root,
		Sessions:   sessions,
		Artifacts:  krude.Artifacts,
		Runs:       runstore.New(platform.Database),
		Prices:     priceTable,
		Logger:     logger,
		RunTimeout: settings.RunTimeout,
	})
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	server := httpserver.New(authclient.New(settings.AuthURL), httpserver.ForThreads(service), httpserver.Config{
		AllowedOrigins: settings.AllowedOrigins,
		RunTimeout:     settings.RunTimeout,
	}, logger)

	table, err := outbox.NewTable(platform.Database.Pool, schema+".agents_outbox")
	if err != nil {
		closeAll()
		return host.Service{}, err
	}
	eventClient := eventbridge.NewFromConfig(platform.AWS, func(options *eventbridge.Options) {
		if settings.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(settings.EventBridgeEndpoint)
		}
	})
	publisher := events.NewPublisher(eventClient, platform.EventBusName)
	dispatcher := outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
		return publisher.PublishEvent(ctx, events.Event{ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data})
	}), logger)

	logger.Info("agents mounted", "model", settings.Model, "model_lite", settings.ModelLite, "run_timeout", settings.RunTimeout.String())
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: timeouts,
		Workers:  []host.Worker{dispatcher.Run},
		Close:    closeAll,
	}, nil
}
