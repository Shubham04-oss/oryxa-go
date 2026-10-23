package projector

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector/tokens"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

// Name is how ORYXA_SERVICES selects the projector.
const Name = "spicedb"

// reconcileHour is when, in UTC, the nightly reconcile runs.
const reconcileHour = 3

// Definition is how a host builds the projector. It is off, and touches
// nothing, until SPICEDB_ENDPOINT is set.
var Definition = host.Definition{
	Name: Name, EventsQueueVariable: "SPICEDB_EVENTS_QUEUE_URL", Enabled: enabled, Migrate: Migrate, Mount: Mount,
}

func enabled(getenv func(string) string) bool {
	return strings.TrimSpace(getenv("SPICEDB_ENDPOINT")) != ""
}

// Migrate installs the ZedToken table, only once SpiceDB is configured.
func Migrate(ctx context.Context, platform host.Platform) error {
	if !enabled(platform.Getenv) {
		return nil
	}
	if platform.Database == nil {
		return host.ErrDatabaseRequired
	}
	return tokens.Migrate(ctx, platform.Database)
}

// Mount runs the consumer, when its queue is set, and the nightly reconcile.
func Mount(_ context.Context, platform host.Platform) (host.Service, error) {
	if !enabled(platform.Getenv) {
		return host.Service{}, nil
	}
	if platform.Database == nil {
		return host.Service{}, host.ErrDatabaseRequired
	}
	reconciler, closeClient, err := Open(platform)
	if err != nil {
		return host.Service{}, err
	}
	workers := []host.Worker{func(ctx context.Context) { reconciler.Nightly(ctx, reconcileHour) }}
	if queueURL := strings.TrimSpace(platform.Getenv("SPICEDB_EVENTS_QUEUE_URL")); queueURL != "" {
		consumer, err := NewConsumer(reconciler.writer, reconciler, platform.EventAuth.Verifier(), platform.Logger)
		if err != nil {
			closeClient()
			return host.Service{}, err
		}
		consumer.seen = tokens.NewSeen(platform.Database.Pool)
		queue := sqs.NewFromConfig(platform.AWS)
		workers = append(workers, func(ctx context.Context) { consumer.Poll(ctx, queue, queueURL) })
	} else {
		platform.Logger.Warn("SPICEDB_EVENTS_QUEUE_URL is not set: SpiceDB follows changes only at the nightly reconcile")
	}
	return host.Service{Workers: workers, Close: closeClient}, nil
}

// Open dials SpiceDB and builds a Reconciler on the platform's database; the
// CLI's reconcile command uses it too.
func Open(platform host.Platform) (*Reconciler, func(), error) {
	client, err := spicedb.Dial(spicedb.Config{
		Endpoint:     strings.TrimSpace(platform.Getenv("SPICEDB_ENDPOINT")),
		PresharedKey: platform.Getenv("SPICEDB_PRESHARED_KEY"),
		Insecure:     platform.Getenv("SPICEDB_INSECURE") == "true",
	})
	if err != nil {
		return nil, nil, err
	}
	writer := NewWriter(client, tokens.New(platform.Database.Pool))
	return NewReconciler(writer, platform.Database.Pool, platform.Logger), func() { _ = client.Close() }, nil
}
