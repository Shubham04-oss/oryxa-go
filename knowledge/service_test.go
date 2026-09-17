package knowledge

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

func platform(values map[string]string) host.Platform {
	return host.Platform{
		Logger: slog.New(slog.DiscardHandler),
		// Mounting builds the store but never queries it: the pool just has to
		// be non-nil, for building the outbox table, which pgxpool.New leaves
		// true without dialing anything (it only dials lazily, on first use).
		Database:     &postgreskit.Database{Pool: unconnectedPool()},
		AWS:          aws.Config{Region: "ap-south-1"},
		EventBusName: "default",
		Getenv:       func(name string) string { return values[name] },
	}
}

func unconnectedPool() *pgxpool.Pool {
	pool, err := pgxpool.New(context.Background(), "postgres://unused@127.0.0.1:1/unused")
	if err != nil {
		panic(err)
	}
	return pool
}

func required() map[string]string {
	return map[string]string{
		"AUTH_URL":                 "http://127.0.0.1:8080",
		"CONNECTIONS_URL":          "http://127.0.0.1:8081",
		"KNOWLEDGE_SERVICE_KEY_ID": "alias/oryxa-knowledge-identity",
	}
}

func TestMountServesKnowledgeRoutesAndRunsItsWorkers(t *testing.T) {
	values := required()
	values["KNOWLEDGE_EVENTS_QUEUE_URL"] = "http://localhost:4566/000000000000/knowledge-events"
	service, err := Mount(context.Background(), platform(values))
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler == nil || !slices.Contains(service.Routes, "GET /v1/organizations/{organizationID}/sources") ||
		!slices.Contains(service.Routes, "GET /v1/sources/providers") {
		t.Fatalf("routes = %v", service.Routes)
	}
	if len(service.Workers) != 3 {
		t.Fatalf("workers = %d, want the sync loop, the dispatcher and the consumer", len(service.Workers))
	}
	if service.Timeouts.Read == 0 || service.Timeouts.Write == 0 {
		t.Fatalf("timeouts = %+v", service.Timeouts)
	}

	withoutQueue, err := Mount(context.Background(), platform(required()))
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutQueue.Workers) != 2 {
		t.Fatalf("workers without a queue = %d, want the sync loop and the dispatcher", len(withoutQueue.Workers))
	}
}

func TestMountRefusesWhatItCannotRunWith(t *testing.T) {
	for missing := range required() {
		values := required()
		delete(values, missing)
		if _, err := Mount(context.Background(), platform(values)); err == nil {
			t.Fatalf("Mount() accepted a missing %s", missing)
		}
	}

	noDatabase := platform(required())
	noDatabase.Database = nil
	if _, err := Mount(context.Background(), noDatabase); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Mount() without a database = %v", err)
	}
	if err := Migrate(context.Background(), noDatabase); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Migrate() without a database = %v", err)
	}
}
