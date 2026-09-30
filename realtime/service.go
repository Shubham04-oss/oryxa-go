// Package realtime assembles the realtime service for a host. The web app
// keeps direct messages and teamspace side conversations only in its Worker's
// Durable Objects, and realtime is the platform's way to them in both
// directions. When a member leaves an organization, or the organization is
// deleted, it hears so from auth on its own queue, and when a member leaves a
// teamspace, or the teamspace is deleted, from agents, and has the Worker
// purge what they kept, recording each purge done as
// realtime.purge.completed.v1.
// It hears from agents, too, that a request between two members was made,
// decided or answered, and has the Worker note it in their direct message and
// inboxes (POST {worker}/internal/realtime/requests, ids and a state only).
// And the Worker tells it what happens in those conversations, on
// POST /v1/realtime/events, which it publishes as oryxa.realtime, so the rest
// of the platform can react.
package realtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/events"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/requests"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/serviceconfig"
)

// Name is how ORYXA_SERVICES selects realtime.
const Name = "realtime"

// Definition is how a host builds realtime. Nothing calls it by a URL
// variable: the Worker reaches its route through the API's public origin.
var Definition = host.Definition{
	Name: Name, EventsQueueVariable: "REALTIME_EVENTS_QUEUE_URL", Enabled: enabled, Migrate: Migrate, Mount: Mount,
}

// enabled reports whether realtime is being switched on: whether the Worker's
// URL or the secret it shares is set. Until one is, realtime ships off and
// hears nothing, so a production start does not wait for its queue; once one
// is, the queue is required like any consumer's, and Mount says what else is
// missing.
func enabled(getenv func(string) string) bool {
	return strings.TrimSpace(getenv("REALTIME_WORKER_URL")) != "" || strings.TrimSpace(getenv("REALTIME_PURGE_SECRET")) != ""
}

// Migrate installs realtime's schema: its outbox, and the Worker's event ids
// it has accepted. Like every service's, it needs only the database, so it
// runs whether realtime is on or not, and turning realtime on needs no
// migration.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles realtime on what the host shares. It is off until the
// Worker's URL, the secret the Worker shares and the queue are all set: a
// process running every service says once that it is off and carries on
// without it, serving no route of realtime's, and one asked for realtime by
// name refuses to start. Once on, it needs the database, as every service
// that keeps anything does.
func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	if missing := config.Missing(); len(missing) > 0 {
		unset := strings.Join(missing, ", ")
		if namedIn(platform.Getenv("ORYXA_SERVICES")) {
			return host.Service{}, fmt.Errorf("ORYXA_SERVICES names realtime, but these are not set: %s; set them, or leave realtime out of ORYXA_SERVICES to run without it", unset)
		}
		logger.Warn("realtime is off, so a member's direct messages and side conversations in the web app outlive " +
			"their place in an organization or a teamspace, a deleted organization's or teamspace's outlive it, " +
			"and nothing said in them reaches the bus; these are not set: " + unset)
		return host.Service{}, nil
	}
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}

	client, err := purge.New(purge.Config{WorkerURL: config.WorkerURL, Secret: config.PurgeSecret})
	if err != nil {
		return host.Service{}, err
	}
	// The same Worker, and the same secret, at a route of its own.
	noter, err := requests.New(requests.Config{WorkerURL: config.WorkerURL, Secret: config.PurgeSecret})
	if err != nil {
		return host.Service{}, err
	}
	store := postgres.New(platform.Database)
	signer, err := platform.EventAuth.Signer(Name)
	if err != nil {
		return host.Service{}, err
	}
	publisher, err := events.NewPublisher(eventbridge.NewFromConfig(platform.AWS), platform.EventBusName, signer)
	if err != nil {
		return host.Service{}, err
	}
	table, err := outbox.NewTable(store.Pool(), postgres.OutboxTable)
	if err != nil {
		return host.Service{}, err
	}
	dispatcher := outbox.NewDispatcher(table, publisher, logger)
	consumer, err := events.NewConsumer(client, noter, store, verifier(platform.EventAuth), logger)
	if err != nil {
		return host.Service{}, err
	}
	server, err := httpserver.New(httpserver.Config{Secret: config.PurgeSecret}, store, logger)
	if err != nil {
		return host.Service{}, err
	}
	queue := sqs.NewFromConfig(platform.AWS)
	logger.Info("realtime mounted: a member leaving an organization or a teamspace, or either deleted, purges their "+
		"conversations through the Worker, a request between two members that moves is noted through it, and the "+
		"Worker's events are published", "worker_url", config.WorkerURL, "route", httpserver.Path)
	return host.Service{
		Handler:  server,
		Routes:   server.Patterns(),
		Timeouts: host.Timeouts{Read: 15 * time.Second, Write: 30 * time.Second},
		Workers: []host.Worker{
			func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) },
			dispatcher.Run,
			forgetAccepted(store, logger),
		},
	}, nil
}

// verifier checks the signatures of the events realtime purges for, auth's
// and agents', each against its own service's key, for as long as a purge can
// wait to be handled (events.SignatureMaxAge), rather than a consumer's usual
// day.
func verifier(registry *eventauth.Registry) *eventauth.Verifier {
	return registry.Verifier(eventauth.MaxAge(events.SignatureMaxAge))
}

// namedIn reports whether services, ORYXA_SERVICES's value, names realtime
// itself rather than leaving the process to run every service.
func namedIn(services string) bool {
	for _, name := range strings.Split(services, ",") {
		if strings.TrimSpace(name) == Name {
			return true
		}
	}
	return false
}

// forgetter deletes the Worker's ids accepted long enough ago;
// *postgres.Store is one.
type forgetter interface {
	ForgetAccepted(ctx context.Context, before time.Time, limit int) (int, error)
}

const (
	// forgetEvery is how often the Worker's old ids are deleted.
	forgetEvery = time.Hour
	// forgetBatch is how many are deleted at once.
	forgetBatch = 1000
)

// forgetAccepted is the worker that deletes, every hour until it is stopped,
// the Worker's ids accepted more than postgres.AcceptedRetention ago, so the
// table holds about a week of them.
func forgetAccepted(store forgetter, logger *slog.Logger) host.Worker {
	return func(ctx context.Context) {
		ticker := time.NewTicker(forgetEvery)
		defer ticker.Stop()
		for {
			forget(ctx, store, time.Now().Add(-postgres.AcceptedRetention), logger)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}
}

// forget deletes the ids accepted before before, a batch at a time, until a
// batch comes back short.
func forget(ctx context.Context, store forgetter, before time.Time, logger *slog.Logger) {
	for ctx.Err() == nil {
		deleted, err := store.ForgetAccepted(ctx, before, forgetBatch)
		if err != nil {
			if ctx.Err() == nil {
				logger.WarnContext(ctx, "forget the Worker's accepted event ids failed; the next hour tries again", "error", err)
			}
			return
		}
		if deleted < forgetBatch {
			return
		}
	}
}
