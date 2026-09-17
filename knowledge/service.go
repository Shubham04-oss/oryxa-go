// Package knowledge assembles the service: the pipes between an
// organization's connected apps and the retrieval that reads them.
//
// This package exists so that mounting knowledge is three lines rather than a
// transcription of its wiring. Everything a host has to do — install the
// schema, serve the API, run the background work — is named here, and nothing
// in it assumes knowledge owns the process. A host that runs one service and
// a host that runs all of them use the same shape.
//
// What is deliberately not here is where the dependencies come from. Building
// a Postgres pool, an AWS client, a KMS signer or a token sink is the host's
// job, because a host running several services shares those rather than
// having each service build its own.
package knowledge

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/application"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/events"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

// Name is what this service calls itself: to connections when it signs a
// call, to the bus when it publishes, and to a host mounting it.
const Name = "knowledge"

// Worker is background work a host runs until its context is cancelled. Each
// returns when the context is done, so a host can wait for all of them.
type Worker func(context.Context)

// Dependencies are what a host builds and knowledge borrows.
type Dependencies struct {
	// Store is knowledge's own schema. Required.
	Store *postgres.Store
	// Directory is auth, Connections is connections, Grants mints a token
	// that acts as a member for an internal source.
	Directory   application.Directory
	Connections application.Connections
	Grants      application.Grants
	// Sink is where published events go. A host on the bus passes an
	// EventBridge publisher; a host that also runs the retrieval pipeline can
	// pass that instead, and nothing inside changes.
	Sink events.Sink
	// Extractors is what this build can read. Builtin covers the providers
	// knowledge ships with.
	Extractors sources.Registry
	Logger     *slog.Logger
}

// Config is what a host tells knowledge about itself.
type Config struct {
	// AllowedOrigins may send cookie-authenticated mutations.
	AllowedOrigins []string
	// FullSweepInterval is how long a full sweep's word about what a source
	// holds is trusted before a pass reads it from the beginning again. Zero
	// defaults inside application to domain.DefaultFullSweepInterval.
	FullSweepInterval time.Duration
}

// Service is knowledge, ready to mount.
type Service struct {
	app        *application.Service
	store      *postgres.Store
	handler    *httpserver.Server
	dispatcher *outbox.Dispatcher
	consumer   *events.Consumer
}

// New assembles the service. It does not touch the database, start anything,
// or listen: a host decides when each of those happens. It errors only if
// Store's pool is nil or knowledge_outbox is not a name internal/outbox
// accepts, neither of which a real host can trigger.
func New(dependencies Dependencies, config Config) (*Service, error) {
	logger := dependencies.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	extractors := dependencies.Extractors
	if extractors == nil {
		extractors = sources.Registry{}
	}

	app := application.New(application.Dependencies{
		Store:       dependencies.Store,
		Directory:   dependencies.Directory,
		Connections: dependencies.Connections,
		Grants:      dependencies.Grants,
		Extractors:  extractors,
		Logger:      logger,
	}, application.Config{ServiceName: Name, FullSweepInterval: config.FullSweepInterval})

	service := &Service{
		app:      app,
		store:    dependencies.Store,
		handler:  httpserver.New(app, httpserver.Config{AllowedOrigins: config.AllowedOrigins}, logger),
		consumer: events.NewConsumer(app, logger),
	}
	if dependencies.Sink != nil {
		table, err := outbox.NewTable(dependencies.Store.Pool(), "knowledge_outbox")
		if err != nil {
			return nil, err
		}
		sink := dependencies.Sink
		service.dispatcher = outbox.NewDispatcher(table, outbox.PublisherFunc(func(ctx context.Context, event outbox.Event) error {
			return sink.PublishEvent(ctx, domain.Event{ID: event.ID, DetailType: event.DetailType, OccurredAt: event.OccurredAt, Data: event.Data})
		}), logger)
	}
	return service, nil
}

// Name is what this service calls itself.
func (service *Service) Name() string { return Name }

// Migrate installs knowledge's schema. A host runs it before serving, and
// running it twice is harmless.
func (service *Service) Migrate(ctx context.Context) error { return service.store.Migrate(ctx) }

// Handler is knowledge's HTTP API, ready to mount wherever a host serves.
func (service *Service) Handler() http.Handler { return service.handler }

// Routes lists every path Handler serves, such as
// "GET /v1/organizations/{organizationID}/sources", for a host that mounts
// several services on one listener and has to know which paths are this
// one's.
func (service *Service) Routes() []string { return service.handler.Patterns() }

// Workers is the background work knowledge needs running to do anything at
// all. A host starts each one and waits for it on the way down: a half-read
// source is worth finishing.
func (service *Service) Workers() []Worker {
	workers := []Worker{service.app.RunWorkers}
	if service.dispatcher != nil {
		workers = append(workers, service.dispatcher.Run)
	}
	return workers
}

// Consumer handles what other services publish that knowledge must act on. A
// host polls it from whichever queue it provisioned; Subscriptions says which
// events that queue must carry.
func (service *Service) Consumer() *events.Consumer { return service.consumer }

// Subscriptions are the detail types knowledge consumes, by source, for a
// host provisioning the rule that feeds its queue.
func Subscriptions() map[string][]string { return events.Subscriptions() }

// Builtin is the extractors knowledge ships with, sharing one HTTP client so
// that pacing and rate-limit backoff are per provider across all of them.
//
// Files is not here: it reads an internal service rather than a provider, so
// a host supplies it along with the client it should read through.
func Builtin() sources.Registry {
	client := sources.NewClient()
	registry := sources.Registry{}
	for _, extractor := range []sources.Extractor{
		sources.NewGitHub(client),
		sources.NewNotion(client),
		sources.NewSlack(client),
	} {
		registry[extractor.Provider()] = extractor
	}
	return registry
}
