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

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/application"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/connectionsclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/events"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/mcpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	telemetryShutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "oryxa-integrations"})
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
		slog.Error("integrations stopped", "error", err)
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
	dispatcher := events.NewDispatcher(store, events.NewPublisher(eventClient, config.EventBusName), logger)
	go dispatcher.Run(ctx)
	// Renewal and cleanup claim under leases, so every replica may run them.
	go service.RunWorkers(ctx)
	if config.EventsQueueURL != "" {
		go events.NewConsumer(service, logger).Poll(ctx, queueClient, config.EventsQueueURL)
	} else {
		logger.Warn("INTEGRATIONS_EVENTS_QUEUE_URL is not set: integrations will not stop subscriptions when connections, members or organizations go away")
	}

	server := &http.Server{
		Addr: config.Address,
		Handler: observability.HTTPHandler("oryxa-integrations", httpserver.New(service, httpserver.Config{
			AllowedOrigins: config.AllowedOrigins, MCP: mcpserver.New(service, logger),
		}, logger)),
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

	logger.Info("integrations listening", "address", config.Address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
