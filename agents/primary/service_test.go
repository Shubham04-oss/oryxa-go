package primary

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

// platform builds a fake host.Platform. Mount touches neither the database
// nor AWS today, so the fake carries only what it actually reads.
func platform(values map[string]string) host.Platform {
	return host.Platform{
		Logger: slog.New(slog.DiscardHandler),
		Getenv: func(name string) string { return values[name] },
	}
}

func TestMountServesHealthzAndNoWorkers(t *testing.T) {
	service, err := Mount(context.Background(), platform(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler == nil || !slices.Contains(service.Routes, "GET /healthz") {
		t.Fatalf("routes = %v", service.Routes)
	}
	if len(service.Workers) != 0 {
		t.Fatalf("workers = %d, want none: there is no run API yet for a worker to carry", len(service.Workers))
	}
	if service.Timeouts.Read != 15*time.Second || service.Timeouts.Write != 0 {
		t.Fatalf("timeouts = %+v", service.Timeouts)
	}
}

func TestMountRefusesInvalidConfiguration(t *testing.T) {
	if _, err := Mount(context.Background(), platform(map[string]string{
		"AGENTS_PRIMARY_AUTO_MIGRATE": "sometimes",
	})); err == nil {
		t.Fatal("Mount() accepted an invalid AGENTS_PRIMARY_AUTO_MIGRATE")
	}
}
