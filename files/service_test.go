package files

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

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

func TestMountServesFilesRoutesAndRunsItsWorkers(t *testing.T) {
	service, err := Mount(context.Background(), platform(map[string]string{
		"AUTH_URL":               "http://127.0.0.1:8080",
		"FILES_BUCKET_NAME":      "oryxa-files",
		"FILES_EVENTS_QUEUE_URL": "http://localhost:4566/000000000000/files-events",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler == nil || !slices.Contains(service.Routes, "GET /v1/organizations/{organizationID}/files") {
		t.Fatalf("routes = %v", service.Routes)
	}
	if len(service.Workers) != 2 {
		t.Fatalf("workers = %d, want the dispatcher and the consumer", len(service.Workers))
	}
	if service.Timeouts.Read != 30*time.Second || service.Timeouts.Write != 60*time.Second {
		t.Fatalf("timeouts = %+v", service.Timeouts)
	}

	withoutQueue, err := Mount(context.Background(), platform(map[string]string{
		"AUTH_URL":          "http://127.0.0.1:8080",
		"FILES_BUCKET_NAME": "oryxa-files",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutQueue.Workers) != 1 {
		t.Fatalf("workers without a queue = %d, want the dispatcher alone", len(withoutQueue.Workers))
	}
}

func TestMountRefusesWhatItCannotRunWith(t *testing.T) {
	tests := map[string]map[string]string{
		"missing auth url": {"FILES_BUCKET_NAME": "oryxa-files"},
		"missing bucket":   {"AUTH_URL": "http://127.0.0.1:8080"},
	}
	for name, values := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Mount(context.Background(), platform(values)); err == nil {
				t.Fatalf("Mount() accepted %v", values)
			}
		})
	}

	noDatabase := platform(map[string]string{"AUTH_URL": "http://127.0.0.1:8080", "FILES_BUCKET_NAME": "oryxa-files"})
	noDatabase.Database = nil
	if _, err := Mount(context.Background(), noDatabase); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Mount() without a database = %v", err)
	}
	if err := Migrate(context.Background(), noDatabase); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Migrate() without a database = %v", err)
	}
}
