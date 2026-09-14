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

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"

	"github.com/Shubham04-oss/oryxa-go/files/internal/application"
	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/blob"
	"github.com/Shubham04-oss/oryxa-go/files/internal/events"
	"github.com/Shubham04-oss/oryxa-go/files/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/files/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/files/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/files/internal/teamsclient"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	telemetryShutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "oryxa-files"})
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
		slog.Error("files stopped", "error", err)
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
	slog.Info("postgres connected", "extensions", store.ExtensionVersions())
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
	blobs := blob.NewS3FromConfig(awsConfig, config.S3Endpoint, config.BucketName)
	var teams application.Teams
	if config.TeamsURL != "" {
		teams = teamsclient.New(config.TeamsURL)
	} else {
		logger.Warn("TEAMS_URL is not set: team-scoped files cannot be created")
	}
	service := application.New(store, blobs, authclient.New(config.AuthURL), teams, logger)
	dispatcher := events.NewDispatcher(store, events.NewPublisher(eventbridge.NewFromConfig(awsConfig), config.EventBusName), logger)
	go dispatcher.Run(ctx)
	if config.EventsQueueURL != "" {
		go events.NewConsumer(service, logger).Poll(ctx, sqs.NewFromConfig(awsConfig), config.EventsQueueURL)
	} else {
		logger.Warn("FILES_EVENTS_QUEUE_URL is not set: files will not hear about organizations, members leaving them, or deleted teams")
	}

	server := &http.Server{
		Addr:              config.Address,
		Handler:           observability.HTTPHandler("oryxa-files", httpserver.New(service, httpserver.Config{AllowedOrigins: config.AllowedOrigins}, logger)),
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

	logger.Info("files listening", "address", config.Address, "bucket", config.BucketName)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
