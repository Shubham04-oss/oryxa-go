package integrations

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// platform is what a host hands the service. Its database never connects, so
// a test that reaches it fails: that is how "touches nothing" is proved.
func platform(values map[string]string, logs *bytes.Buffer) host.Platform {
	return host.Platform{
		Logger:   slog.New(slog.NewTextHandler(logs, nil)),
		Database: postgreskit.Detached(nil),
		AWS:      aws.Config{Region: "ap-south-1", Credentials: aws.AnonymousCredentials{}},
		Getenv:   func(name string) string { return values[name] },
	}
}

func TestDefinitionShipsOff(t *testing.T) {
	if Definition.Name != "integrations" || Definition.Enabled == nil || Definition.Migrate == nil || Definition.Mount == nil {
		t.Fatalf("definition = %+v", Definition)
	}
	if Definition.Enabled(func(string) string { return "" }) {
		t.Fatal("enabled with nothing set")
	}
}

func TestOffItNeitherMigratesNorMountsNorNeedsConfiguration(t *testing.T) {
	var logs bytes.Buffer
	// ORYXA_SERVICES unset, as in production, which runs every service.
	off := platform(map[string]string{}, &logs)
	if err := Migrate(context.Background(), off); err != nil {
		t.Fatalf("off, Migrate touched the database: %v", err)
	}
	service, err := Mount(context.Background(), off)
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler != nil || len(service.Routes) != 0 || len(service.Workers) != 0 || service.Close != nil {
		t.Fatalf("off, the service mounted %+v", service)
	}
	if !strings.Contains(logs.String(), "integrations is off") {
		t.Fatalf("it did not say it is off:\n%s", logs.String())
	}
}

func TestNamedButOffRefusesToStart(t *testing.T) {
	var logs bytes.Buffer
	_, err := Mount(context.Background(), platform(map[string]string{"ORYXA_SERVICES": "auth, integrations"}, &logs))
	if err == nil || !strings.Contains(err.Error(), "INTEGRATIONS_ENABLED") {
		t.Fatalf("err = %v", err)
	}
}

func TestOnItMigratesAndMountsRoutes(t *testing.T) {
	var logs bytes.Buffer
	on := platform(map[string]string{"INTEGRATIONS_ENABLED": "true"}, &logs)
	if err := Migrate(context.Background(), on); err == nil {
		t.Fatal("on, Migrate did not reach the database")
	}
	service, err := Mount(context.Background(), on)
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler == nil || len(service.Routes) != 1 || service.Routes[0] != "GET /healthz" {
		t.Fatalf("service = %+v", service)
	}
	if !strings.Contains(logs.String(), "INTEGRATIONS_SERVICE_CALLERS is not set") {
		t.Fatalf("no warning about callers:\n%s", logs.String())
	}
}
