package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/application"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/events"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/teamsclient"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// serviceAudience is the audience other services name in tokens for
// connections.
const serviceAudience = "connections"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	telemetryShutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "oryxa-connections"})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := telemetryShutdown(shutdownContext); err != nil {
			slog.Error("shutdown telemetry", "error", err)
		}
	}()
	if err := run(ctx); err != nil {
		slog.Error("connections stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	logger := slog.Default()
	config, err := serviceconfig.FromEnv()
	if err != nil {
		return err
	}
	store, err := postgres.Open(ctx, config.DatabaseURL, config.PostgresExtensions...)
	if err != nil {
		return err
	}
	defer store.Close()
	logger.Info("postgres connected", "extensions", store.ExtensionVersions())
	if config.AutoMigrate {
		if err := store.Migrate(ctx); err != nil {
			return err
		}
	}

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		return err
	}
	otelaws.AppendMiddlewares(&awsConfig.APIOptions)
	kmsClient := kms.NewFromConfig(awsConfig, func(options *kms.Options) {
		if config.KMSEndpoint != "" {
			options.BaseEndpoint = aws.String(config.KMSEndpoint)
		}
	})
	eventClient := eventbridge.NewFromConfig(awsConfig, func(options *eventbridge.Options) {
		if config.EventBridgeEndpoint != "" {
			options.BaseEndpoint = aws.String(config.EventBridgeEndpoint)
		}
	})
	queueClient := sqs.NewFromConfig(awsConfig, func(options *sqs.Options) {
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
	dispatcher := events.NewDispatcher(store, events.NewPublisher(eventClient, config.EventBusName), logger)
	go dispatcher.Run(ctx)
	// Both workers claim with SKIP LOCKED, so every replica may run them.
	go service.RunHeartbeat(ctx)
	go service.RunCleanup(ctx)
	if config.EventsQueueURL != "" {
		go events.NewConsumer(service, logger).Poll(ctx, queueClient, config.EventsQueueURL)
	} else {
		logger.Warn("CONNECTIONS_EVENTS_QUEUE_URL is not set: connections will not hear about deleted organizations, removed members, or deleted teams")
	}

	var services httpserver.ServiceVerifier
	if len(config.ServiceCallers) > 0 {
		verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{Keys: kmsClient, Audience: serviceAudience, Callers: config.ServiceCallers})
		if err != nil {
			return err
		}
		services = verifier
	} else {
		logger.Warn("CONNECTIONS_SERVICE_CALLERS is not set: the internal API refuses every call, so no service can get tokens")
	}

	server := &http.Server{
		Addr: config.Address,
		Handler: observability.HTTPHandler("oryxa-connections",
			httpserver.New(service, httpserver.Config{AllowedOrigins: config.AllowedOrigins, Services: services}, logger)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("shutdown", "error", err)
		}
	}()

	logger.Info("connections listening", "address", config.Address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
