// Package apps assembles the service: a member's third-party apps, reached
// through Composio, and the safety layer around them.
//
// Composio holds every credential and answers to one project API key that
// reaches every user in the project, so the key never leaves this process and
// nothing outside apps chooses whose account a call uses. What a host has to
// do — install the schema, serve the API and the MCP endpoint, run the
// background work — is named here, and nothing in it assumes apps owns the
// process.
package apps

import (
	"context"
	"log/slog"
	"net/http"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/events"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/mcpserver"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
)

// Name is what this service calls itself: to the bus when it publishes, and
// to a host mounting it.
const Name = "apps"

// Worker is background work a host runs until its context is cancelled.
type Worker func(context.Context)

// Dependencies are what a host builds and apps borrows.
type Dependencies struct {
	// Store is apps' own schema. Required.
	Store *postgres.Store
	// Directory is auth, which decides who a caller is. Required.
	Directory application.Directory
	// Composio is the API every app is reached through. Required.
	Composio application.Composio
	// Sink is where published events go. Without one, apps still works and
	// its outbox simply fills up, which a host should not choose to do.
	Sink events.Sink
	// Verifier proves who published each event apps consumes. Required:
	// a consumer that cannot check signatures would run any message a queue
	// handed it, so New refuses to build without one.
	Verifier *eventauth.Verifier
	Logger   *slog.Logger
}

// Config is what a host tells apps about itself.
type Config struct {
	// PublicBaseURL is where a member's browser and Composio reach this
	// process. The connect callback and the webhook URL are built on it.
	PublicBaseURL string
	// AllowedOrigins may send cookie-authenticated mutations, and are where a
	// member's browser may be sent back to after connecting an app.
	AllowedOrigins []string
	// WebhookSecret verifies Composio's deliveries. Without it, every
	// delivery is refused.
	WebhookSecret string
	// NoAutoSubscribe stops a newly connected app from being subscribed to
	// everything it can deliver.
	NoAutoSubscribe bool
}

// Service is apps, ready to mount.
type Service struct {
	app        *application.Service
	store      *postgres.Store
	handler    *httpserver.Server
	mcp        *mcpserver.Handler
	dispatcher *outbox.Dispatcher
	consumer   *events.Consumer
}

// New assembles the service. It does not touch the database, start anything,
// or listen: a host decides when each of those happens.
func New(dependencies Dependencies, config Config) (*Service, error) {
	logger := dependencies.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	app, err := application.New(application.Dependencies{
		Store:     dependencies.Store,
		Directory: dependencies.Directory,
		Composio:  dependencies.Composio,
		Logger:    logger,
	}, application.Config{
		PublicBaseURL:   config.PublicBaseURL,
		ReturnOrigins:   config.AllowedOrigins,
		WebhookSecret:   config.WebhookSecret,
		NoAutoSubscribe: config.NoAutoSubscribe,
	})
	if err != nil {
		return nil, err
	}

	mcp := mcpserver.New(app, logger)
	consumer, err := events.NewConsumer(app, dependencies.Verifier, logger)
	if err != nil {
		return nil, err
	}
	service := &Service{
		app:      app,
		store:    dependencies.Store,
		mcp:      mcp,
		handler:  httpserver.New(app, httpserver.Config{AllowedOrigins: config.AllowedOrigins, MCP: mcp}, logger),
		consumer: consumer,
	}
	if dependencies.Sink != nil {
		// A trigger's payload is the app's own data — an email's body, an
		// issue's text. It travels to whoever the event reaches and is of no
		// use once delivered, so the outbox forgets it as soon as it is
		// published rather than keeping it for the retention window.
		table, err := outbox.NewTable(dependencies.Store.Pool(), "apps_outbox",
			outbox.RedactOnPublish(appsv1.TriggerReceivedDetailType))
		if err != nil {
			return nil, err
		}
		sink := dependencies.Sink
		service.dispatcher = outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
			return sink.PublishEvent(ctx, domain.Event{
				ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data,
				TraceContext: event.TraceContext,
			})
		}), logger)
	}
	return service, nil
}

// Name is what this service calls itself.
func (service *Service) Name() string { return Name }

// Migrate installs apps' schema. A host runs it before serving, and running
// it twice is harmless.
func (service *Service) Migrate(ctx context.Context) error { return service.store.Migrate(ctx) }

// Handler is apps' HTTP API, including the MCP endpoint, ready to mount
// wherever a host serves.
func (service *Service) Handler() http.Handler { return service.handler }

// Routes lists every path Handler serves, for a host that mounts several
// services on one listener.
func (service *Service) Routes() []string { return service.handler.Patterns() }

// WebhookPath is where Composio delivers, under the public base URL. A host
// prints it when provisioning the subscription.
const WebhookPath = "/v1/apps/webhooks/composio"

// Workers is the background work apps needs running. A host starts each one
// and waits for it on the way down.
func (service *Service) Workers() []Worker {
	workers := []Worker{service.app.RunMaintenance, service.app.RunAutoSubscribe}
	if service.dispatcher != nil {
		workers = append(workers, service.dispatcher.Run)
	}
	return workers
}

// Consumer handles what other services publish that apps must act on: a
// member leaving or an organization going away takes their connections with
// it. A host polls it from whichever queue it provisioned.
func (service *Service) Consumer() *events.Consumer { return service.consumer }

// Subscriptions are the detail types apps consumes, by source, for a host
// provisioning the rule that feeds its queue.
func Subscriptions() map[string][]string { return events.Subscriptions() }
