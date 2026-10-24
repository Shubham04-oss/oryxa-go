// Package knowledge assembles the service: the pipes between what an
// organization holds and the retrieval that reads it.
//
// This package exists so that mounting knowledge is three lines rather than a
// transcription of its wiring. Everything a host has to do — install the
// schema, serve the API, run the background work — is named here, and nothing
// in it assumes knowledge owns the process. A host that runs one service and
// a host that runs all of them use the same shape.
//
// What is deliberately not here is where the dependencies come from. Building
// a Postgres pool, an AWS client or a token sink is the host's
// job, because a host running several services shares those rather than
// having each service build its own.
package knowledge

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	workspacepolicy "github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/application"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/events"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

// Builtin is the extractors knowledge ships with, sharing one HTTP client so
// that pacing and rate-limit backoff are per toolkit across all of them, and
// reading every one of them through Composio's proxy: proxy is nil only when
// COMPOSIO_API_KEY is not configured, in which case Mount does not call this
// at all.
//
// Files is not here: it reads an internal service rather than a toolkit, so a
// host supplies it along with the client it should read through.
func Builtin(proxy sources.Proxier) sources.Registry {
	client := sources.NewClient(proxy)
	registry := sources.Registry{}
	for _, extractor := range []sources.Extractor{
		sources.NewGitHub(client),
		sources.NewNotion(client),
		sources.NewSlack(client),
		sources.NewGoogleDrive(client),
		sources.NewGmail(client),
	} {
		registry[extractor.Provider()] = extractor
	}
	return registry
}

// Name is what this service calls itself: to the bus when it publishes, and
// to a host mounting it.
const Name = "knowledge"

// Worker is background work a host runs until its context is cancelled. Each
// returns when the context is done, so a host can wait for all of them.
type Worker func(context.Context)

// Dependencies are what a host builds and knowledge borrows.
type Dependencies struct {
	// Store is knowledge's own schema. Required.
	Store *postgres.Store
	// Directory is auth; Apps is nil unless COMPOSIO_API_KEY is configured,
	// in which case no provider source can be registered; Grants mints a
	// token that acts as a member for an internal source.
	Directory application.Directory
	Apps      application.Apps
	Policy    interface {
		Get(context.Context, string) (workspacepolicy.Settings, error)
	}
	Grants application.Grants
	// Sink is where published events go. A host on the bus passes an
	// EventBridge publisher; a host that also runs the retrieval pipeline can
	// pass that instead, and nothing inside changes.
	Sink events.Sink
	// Verifier proves who published each event the consumer handles: it
	// resolves the signing key from the source an event names, so one
	// verifier covers every source knowledge hears from. Nil is refused,
	// because a consumer that cannot check signatures would run any message
	// a queue handed it.
	Verifier *eventauth.Verifier
	// Extractors is what this build can read. None is built in: a host
	// supplies each one with the client it reads through.
	Extractors sources.Registry
	// Originals is where a document's own bytes are kept when nothing here
	// reads them as text — a PDF, an Office file. Nil unless
	// KNOWLEDGE_ORIGINALS_BUCKET is configured, in which case such a document
	// is skipped instead, as it always has been.
	Originals application.Originals
	// Judge decides whether a connected app's document is worth indexing.
	// Nil unless KNOWLEDGE_JUDGE_MODEL is configured, in which case only the
	// rules keep noise out.
	Judge application.Judge
	// Recorder keeps who reached a member's sources and documents, and why,
	// for their Activity page. A host passes access.NewLog on the shared
	// database; nil records nothing.
	Recorder application.Recorder
	// Shadow compares access decisions with SpiceDB; nil compares nothing.
	Shadow access.Comparer
	// Engine and Relationships switch source access to SpiceDB (see
	// application.Dependencies); nil keeps the code deciding.
	Engine        access.Decider
	Relationships application.Relationships
	Logger        *slog.Logger
}

// Config is what a host tells knowledge about itself.
type Config struct {
	// AllowedOrigins may send cookie-authenticated mutations.
	AllowedOrigins []string
	// FullSweepInterval is how long a full sweep's word about what a source
	// holds is trusted before a pass reads it from the beginning again. Zero
	// defaults inside application to domain.DefaultFullSweepInterval.
	FullSweepInterval time.Duration
	// Services identifies a service reading a document's body through the
	// internal API. Nil means that route refuses every call.
	Services httpserver.ServiceVerifier
	// OriginalsBucket names the bucket Dependencies.Originals writes to, so a
	// published document's Original names where a consumer reads it from.
	// Empty unless Dependencies.Originals is set.
	OriginalsBucket string
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
// Store's pool is nil, knowledge_outbox is not a name internal/outbox
// accepts, or the consumer has no signature verifier — none of which a real
// host can trigger.
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
		Store:         dependencies.Store,
		Directory:     dependencies.Directory,
		Apps:          dependencies.Apps,
		Policy:        dependencies.Policy,
		Grants:        dependencies.Grants,
		Extractors:    extractors,
		Originals:     dependencies.Originals,
		Judge:         dependencies.Judge,
		Recorder:      dependencies.Recorder,
		Shadow:        dependencies.Shadow,
		Engine:        dependencies.Engine,
		Relationships: dependencies.Relationships,
		Logger:        logger,
	}, application.Config{
		ServiceName: Name, FullSweepInterval: config.FullSweepInterval, OriginalsBucket: config.OriginalsBucket,
	})

	consumer, err := events.NewConsumer(app, dependencies.Verifier, logger)
	if err != nil {
		return nil, err
	}
	service := &Service{
		app:   app,
		store: dependencies.Store,
		handler: httpserver.New(app, httpserver.Config{
			AllowedOrigins: config.AllowedOrigins, Services: config.Services,
		}, logger),
		consumer: consumer,
	}
	if dependencies.Sink != nil {
		table, err := outbox.NewTable(dependencies.Store.Pool(), "knowledge_outbox")
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
