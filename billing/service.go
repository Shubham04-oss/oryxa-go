// Package billing assembles the billing service for a host: subscriptions and
// entitlements through Dodo Payments, the organization events it hears, and
// the housekeeping that reminds customers of renewals and forgets old
// webhook deliveries.
package billing

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/application"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/catalog"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/dodo"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/events"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
)

// Name is how ORYXA_SERVICES selects billing.
const Name = "billing"

// Definition is how a host builds billing. Nothing calls billing over HTTP,
// so it names no URL variable.
var Definition = host.Definition{Name: Name, EventsQueueVariable: "BILLING_EVENTS_QUEUE_URL", Migrate: Migrate, Mount: Mount}

// Migrate installs the billing schema.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles billing on what the host shares.
func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	plans, err := catalog.Load(config.PlansFile)
	if err != nil {
		return host.Service{}, err
	}
	store := postgres.New(platform.Database)

	var payments application.Payments
	var webhooks *dodo.Client
	if config.Dodo.APIKey != "" {
		if webhooks, err = dodo.New(config.Dodo); err != nil {
			return host.Service{}, err
		}
		payments = webhooks
	} else {
		logger.Warn("DODO_PAYMENTS_API_KEY is not set: checkout, the customer portal and webhooks are unavailable")
	}

	service := application.New(store, payments, authclient.New(config.AuthURL), plans, application.Config{
		BillingURL:            config.BillingURL,
		RenewalReminderWindow: time.Duration(config.RenewalReminderDays) * 24 * time.Hour,
	}, logger)

	publisher := events.NewPublisher(eventbridge.NewFromConfig(platform.AWS), platform.EventBusName)
	table, err := outbox.NewTable(store.Pool(), "billing_outbox")
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
		logger.Warn("BILLING_EVENTS_QUEUE_URL is not set: billing will not hear about new, renamed or deleted organizations")
	}
	workers = append(workers, func(ctx context.Context) { housekeeping(ctx, service, store, plans, logger) })

	server := httpserver.New(service, webhooks, httpserver.Config{AllowedOrigins: config.AllowedOrigins}, logger)
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 30 * time.Second},
		Workers:  workers,
	}, nil
}

const (
	housekeepingInterval = 15 * time.Minute
	// webhookRetention outlasts the days Dodo keeps retrying a delivery.
	webhookRetention = 30 * 24 * time.Hour
)

// housekeeping reconciles entitlements with the plan catalog once, so a
// catalog edited since billing last mounted reaches every organization, then
// sends renewal reminders and forgets old webhook deliveries until ctx is
// cancelled.
func housekeeping(ctx context.Context, service *application.Service, store *postgres.Store, plans *catalog.Catalog, logger *slog.Logger) {
	if changed, err := service.ReconcileEntitlements(ctx); err != nil {
		logger.Error("reconcile entitlements with the plan catalog failed", "error", err)
	} else {
		logger.Info("entitlements reconciled with the plan catalog", "plans", len(plans.Plans()), "changed", changed)
	}

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
