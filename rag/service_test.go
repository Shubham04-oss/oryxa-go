package rag

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

func platform(values map[string]string) host.Platform {
	return host.Platform{
		Logger: slog.New(slog.DiscardHandler),
		// Mount never queries the pool itself: it only reads the extensions
		// snapshot the host already took, which Detached can report without a
		// connection.
		Database:     postgreskit.Detached(map[string]string{"vector": "0.8.6"}),
		AWS:          aws.Config{Region: "ap-south-1"},
		EventBusName: "default",
		Getenv:       func(name string) string { return values[name] },
	}
}

func TestMountServesRagRoutesAndRunsItsWorkers(t *testing.T) {
	service, err := Mount(context.Background(), platform(map[string]string{
		"AUTH_URL":             "http://127.0.0.1:8080",
		"RAG_EVENTS_QUEUE_URL": "http://localhost:4566/000000000000/rag-events",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler == nil || !slices.Contains(service.Routes, "GET /v1/organizations/{organizationID}/corpora") {
		t.Fatalf("routes = %v", service.Routes)
	}
	if len(service.Workers) != 3 {
		t.Fatalf("workers = %d, want the outbox dispatcher, the ingest pipeline and the events consumer", len(service.Workers))
	}
	if service.Timeouts.Read == 0 || service.Timeouts.Write == 0 {
		t.Fatalf("timeouts = %+v", service.Timeouts)
	}

	withoutQueue, err := Mount(context.Background(), platform(map[string]string{"AUTH_URL": "http://127.0.0.1:8080"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutQueue.Workers) != 2 {
		t.Fatalf("workers without a queue = %d, want the dispatcher and the ingest pipeline alone", len(withoutQueue.Workers))
	}
}

func TestMountRefusesWhatItCannotRunWith(t *testing.T) {
	if _, err := Mount(context.Background(), platform(map[string]string{})); err == nil {
		t.Fatal("Mount() accepted a missing AUTH_URL")
	}

	noDatabase := platform(map[string]string{"AUTH_URL": "http://127.0.0.1:8080"})
	noDatabase.Database = nil
	if _, err := Mount(context.Background(), noDatabase); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Mount() without a database = %v", err)
	}
	if err := Migrate(context.Background(), noDatabase); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Migrate() without a database = %v", err)
	}

	noVector := platform(map[string]string{"AUTH_URL": "http://127.0.0.1:8080"})
	noVector.Database = postgreskit.Detached(nil)
	if _, err := Mount(context.Background(), noVector); err == nil || !strings.Contains(err.Error(), "vector") {
		t.Fatalf("Mount() without the pgvector extension = %v", err)
	}
}
