// Package realtime assembles the realtime service for a host. The web app
// keeps direct messages and teamspace side conversations only in its Worker's
// Durable Objects, so when a member leaves an organization, or the
// organization is deleted, nothing but the Worker can purge them. Realtime
// hears both from auth on its own queue and turns each into one signed call to
// the Worker. It has no API and keeps nothing.
package realtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/events"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/purge"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/serviceconfig"
)

// Name is how ORYXA_SERVICES selects realtime.
const Name = "realtime"

// Definition is how a host builds realtime. Nothing calls it over HTTP and it
// keeps nothing in PostgreSQL, so it names no URL variable and has no
// Migrate.
var Definition = host.Definition{Name: Name, EventsQueueVariable: "REALTIME_EVENTS_QUEUE_URL", Mount: Mount}

// Mount assembles realtime on what the host shares. It is off until the
// Worker's URL, the secret the Worker shares and the queue are all set: a
// process running every service says once that it is off and carries on
// without it, and one asked for realtime by name refuses to start.
func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	config, err := serviceconfig.FromEnv(platform.Getenv)
	if err != nil {
		return host.Service{}, err
	}
	logger := platform.Logger
	if missing := config.Missing(); len(missing) > 0 {
		unset := notSet(missing)
		if namedIn(platform.Getenv("ORYXA_SERVICES")) {
			return host.Service{}, fmt.Errorf("ORYXA_SERVICES names realtime, but %s; set them, or leave realtime out of ORYXA_SERVICES to run without it", unset)
		}
		logger.Warn(unset + ": realtime is off, so a member's direct messages and side conversations in the web app outlive " +
			"their place in an organization, and a deleted organization's outlive it")
		return host.Service{}, nil
	}

	client, err := purge.New(purge.Config{WorkerURL: config.WorkerURL, Secret: config.PurgeSecret})
	if err != nil {
		return host.Service{}, err
	}
	consumer, err := events.NewConsumer(client, platform.EventAuth.Verifier(), logger)
	if err != nil {
		return host.Service{}, err
	}
	queue := sqs.NewFromConfig(platform.AWS)
	logger.Info("realtime mounted: a member leaving or an organization deleted purges their conversations through the Worker",
		"worker_url", config.WorkerURL)
	return host.Service{
		Workers: []host.Worker{func(ctx context.Context) { consumer.Poll(ctx, queue, config.EventsQueueURL) }},
	}, nil
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

// notSet says which settings are unset, as a clause.
func notSet(names []string) string {
	if len(names) == 1 {
		return names[0] + " is not set"
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " are not set"
}
