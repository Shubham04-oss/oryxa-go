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

	"github.com/Shubham04-oss/oryxa-go/billing/internal/application"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/catalog"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/dodo"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/events"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

const (
	housekeepingInterval = 15 * time.Minute
	// webhookRetention outlasts the days Dodo keeps retrying a delivery.
	webhookRetention = 30 * 24 * time.Hour
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	telemetryShutdown, err := observability.Setup(ctx, observability.Config{ServiceName: "oryxa-billing"})
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
		slog.Error("billing stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	logger := slog.Default()
	config, err := serviceconfig.FromEnv()
	if err != nil {
		return err
	}
	plans, err := catalog.Load(config.PlansFile)
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

	var payments application.Payments
	var webhooks *dodo.Client
	if config.Dodo.APIKey != "" {
		if webhooks, err = dodo.New(config.Dodo); err != nil {
			return err
		}
		payments = webhooks
	} else {
		logger.Warn("DODO_PAYMENTS_API_KEY is not set: checkout, the customer portal and webhooks are unavailable")
	}

	service := application.New(store, payments, authclient.New(config.AuthURL), plans, application.Config{
		BillingURL:            config.BillingURL,
		RenewalReminderWindow: time.Duration(config.RenewalReminderDays) * 24 * time.Hour,
	}, logger)

	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		return err
	}
	otelaws.AppendMiddlewares(&awsConfig.APIOptions)
	dispatcher := events.NewDispatcher(store, events.NewPublisher(eventbridge.NewFromConfig(awsConfig), config.EventBusName), logger)
	go dispatcher.Run(ctx)
	if config.EventsQueueURL != "" {
		go events.NewConsumer(service, logger).Poll(ctx, sqs.NewFromConfig(awsConfig), config.EventsQueueURL)
	} else {
		logger.Warn("BILLING_EVENTS_QUEUE_URL is not set: billing will not hear about new, renamed or deleted organizations")
	}

	if changed, err := service.ReconcileEntitlements(ctx); err != nil {
		logger.Error("reconcile entitlements with the plan catalog failed", "error", err)
	} else {
		logger.Info("entitlements reconciled with the plan catalog", "plans", len(plans.Plans()), "changed", changed)
	}
	go housekeeping(ctx, service, store, logger)

	server := &http.Server{
		Addr:              config.Address,
		Handler:           observability.HTTPHandler("oryxa-billing", httpserver.New(service, webhooks, httpserver.Config{AllowedOrigins: config.AllowedOrigins}, logger)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
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

	logger.Info("billing listening", "address", config.Address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// housekeeping sends renewal reminders and forgets old webhook deliveries.
func housekeeping(ctx context.Context, service *application.Service, store *postgres.Store, logger *slog.Logger) {
	ticker := time.NewTicker(housekeepingInterval)
	defer ticker.Stop()
	for {
		for {
			reminded, err := service.SendRenewalReminders(ctx)
			if err != nil {
				if ctx.Err() == nil {
					logger.Error("send renewal reminders failed", "error", err)
				}
				break
			}
			if reminded == 0 {
				break
			}
			logger.Info("renewal reminders recorded", "count", reminded)
		}
		if err := store.PruneWebhookDeliveries(ctx, time.Now().Add(-webhookRetention)); err != nil && ctx.Err() == nil {
			logger.Error("prune webhook deliveries failed", "error", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
