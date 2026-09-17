package connections

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

func platform(values map[string]string) host.Platform {
	return host.Platform{
		Logger: slog.New(slog.DiscardHandler),
		// Mounting builds the store but never queries it.
		Database:     &postgreskit.Database{},
		AWS:          aws.Config{Region: "ap-south-1"},
		EventBusName: "default",
		Getenv:       func(name string) string { return values[name] },
	}
}

// requiredValues is the smallest environment Mount accepts.
func requiredValues() map[string]string {
	return map[string]string{
		"AUTH_URL":                    "http://127.0.0.1:8080",
		"CONNECTIONS_PUBLIC_BASE_URL": "https://connect.example.test",
		"CONNECTIONS_KMS_KEY_ID":      "alias/oryxa-connections",
	}
}

func TestMountServesConnectionsRoutesAndRunsItsWorkers(t *testing.T) {
	values := requiredValues()
	values["CONNECTIONS_EVENTS_QUEUE_URL"] = "http://localhost:4566/000000000000/connections-events"
	service, err := Mount(context.Background(), platform(values))
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler == nil || !slices.Contains(service.Routes, "GET /v1/providers") {
		t.Fatalf("routes = %v", service.Routes)
	}
	if len(service.Workers) != 4 {
		t.Fatalf("workers = %d, want the dispatcher, heartbeat, cleanup and the consumer", len(service.Workers))
	}
	if service.Timeouts.Read == 0 || service.Timeouts.Write == 0 {
		t.Fatalf("timeouts = %+v", service.Timeouts)
	}

	withoutQueue, err := Mount(context.Background(), platform(requiredValues()))
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutQueue.Workers) != 3 {
		t.Fatalf("workers without a queue = %d, want the dispatcher, heartbeat and cleanup", len(withoutQueue.Workers))
	}
}

func TestMountRefusesWhatItCannotRunWith(t *testing.T) {
	if _, err := Mount(context.Background(), platform(map[string]string{})); err == nil {
		t.Fatal("Mount() accepted missing configuration")
	}
	noDatabase := platform(requiredValues())
	noDatabase.Database = nil
	if _, err := Mount(context.Background(), noDatabase); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Mount() without a database = %v", err)
	}
	if err := Migrate(context.Background(), noDatabase); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Migrate() without a database = %v", err)
	}
}
