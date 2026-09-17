package email

import (
	"context"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

// platform carries no Database: email keeps nothing in PostgreSQL, and Mount
// must not need one.
func platform(values map[string]string) host.Platform {
	return host.Platform{
		Logger: slog.New(slog.DiscardHandler),
		AWS:    aws.Config{Region: "ap-south-1"},
		Getenv: func(name string) string { return values[name] },
	}
}

func TestMountRunsThePollerAloneWithNoDatabase(t *testing.T) {
	service, err := Mount(context.Background(), platform(map[string]string{
		"EMAIL_FROM_ADDRESS": "no-reply@example.com",
		"EMAIL_QUEUE_URL":    "http://localhost:4566/000000000000/email-events",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler != nil || len(service.Routes) != 0 || service.Timeouts != (host.Timeouts{}) {
		t.Fatalf("service = %+v, want no HTTP surface", service)
	}
	if len(service.Workers) != 1 {
		t.Fatalf("workers = %d, want the poller alone", len(service.Workers))
	}
}

func TestMountRefusesMissingConfiguration(t *testing.T) {
	tests := map[string]map[string]string{
		"missing from address": {"EMAIL_QUEUE_URL": "http://localhost:4566/000000000000/email-events"},
		"missing queue url":    {"EMAIL_FROM_ADDRESS": "no-reply@example.com"},
	}
	for name, values := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Mount(context.Background(), platform(values)); err == nil {
				t.Fatalf("Mount() accepted %v", values)
			}
		})
	}
}
