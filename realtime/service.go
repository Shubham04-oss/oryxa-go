// Package realtime assembles the realtime service for a host. The web app
// keeps direct messages and teamspace side conversations only in its Worker's
// Durable Objects, so when a member leaves an organization, or the
// organization is deleted, nothing but the Worker can purge them. Realtime
// hears both from auth on its own queue, turns each into one signed call to
// the Worker, and records each purge done as realtime.purge.completed.v1,
// which it publishes as oryxa.realtime.
package realtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/outbox"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/events"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/serviceconfig"
)

// Name is how ORYXA_SERVICES selects realtime.
const Name = "realtime"

// Definition is how a host builds realtime. Nothing calls it over HTTP, so it
// names no URL variable.
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

// Migrate installs realtime's schema, its outbox among it. Like every
// service's, it needs only the database, so it runs whether realtime is on or
// not, and turning realtime on needs no migration.
func Migrate(ctx context.Context, platform host.Platform) error {
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return postgres.New(platform.Database).Migrate(ctx)
}

// Mount assembles realtime on what the host shares. It is off until the
// Worker's URL, the secret the Worker shares and the queue are all set: a
// process running every service says once that it is off and carries on
// without it, and one asked for realtime by name refuses to start. Once on,
// it needs the database, as every service that keeps anything does.
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
			"their place in an organization, and a deleted organization's outlive it; these are not set: " + unset)
		return host.Service{}, nil
	}
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}

	client, err := purge.New(purge.Config{WorkerURL: config.WorkerURL, Secret: config.PurgeSecret})
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
	consumer, err := events.NewConsumer(client, store, verifier(platform.EventAuth), logger)
	if err != nil {
		return host.Service{}, err
	}
	queue := sqs.NewFromConfig(platform.AWS)
	logger.Info("realtime mounted: a member leaving or an organization deleted purges their conversations through the Worker",
		"worker_url", config.WorkerURL)
	return host.Service{
		Workers: []host.Worker{
			func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) },
			dispatcher.Run,
		},
	}, nil
}

// verifier checks auth's signatures for as long as a purge can wait to be
// handled (events.SignatureMaxAge), rather than a consumer's usual day.
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
