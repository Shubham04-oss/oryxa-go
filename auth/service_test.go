package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// cognitoDiscovery answers the OIDC discovery request Mount makes against
// COGNITO_ISSUER, the way Cognito's issuer would, so Mount can be exercised
// without reaching AWS.
func cognitoDiscovery(t *testing.T) string {
	t.Helper()
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/oauth2/authorize",
			"token_endpoint":         issuer + "/oauth2/token",
			"jwks_uri":               issuer + "/.well-known/jwks.json",
		})
	}))
	t.Cleanup(server.Close)
	issuer = server.URL
	return issuer
}

// requiredEnv is the minimum set of variables Mount needs, so each test
// starts from a valid configuration and overrides only what it means to
// exercise. It spins up a fake Cognito issuer, so it costs a real (loopback)
// server per call.
func requiredEnv(t *testing.T) map[string]string {
	return map[string]string{
		"COGNITO_ISSUER":         cognitoDiscovery(t),
		"COGNITO_USER_POOL_ID":   "pool",
		"COGNITO_CLIENT_ID":      "client-id",
		"COGNITO_DOMAIN":         "https://identity.example.com",
		"COGNITO_CALLBACK_URL":   "https://auth.example.com/v1/auth/callback",
		"INVITATION_ACCEPT_URL":  "https://app.example.com/invitations/accept",
		"EMAIL_VERIFICATION_URL": "https://app.example.com/auth/verify-email",
		"PASSWORD_RESET_URL":     "https://app.example.com/auth/reset-password",
		"AUTH_RETURN_URLS":       "https://app.example.com",
	}
}

func TestMountServesAuthRoutesAndRunsItsWorkers(t *testing.T) {
	values := requiredEnv(t)
	values["AUTH_EVENTS_QUEUE_URL"] = "http://localhost:4566/000000000000/auth-events"
	service, err := Mount(context.Background(), platform(values))
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler == nil || !slices.Contains(service.Routes, "GET /v1/auth/me") {
		t.Fatalf("routes = %v", service.Routes)
	}
	if len(service.Workers) != 2 {
		t.Fatalf("workers = %d, want the dispatcher and the billing entitlements consumer", len(service.Workers))
	}
	if service.Timeouts.Read == 0 || service.Timeouts.Write == 0 {
		t.Fatalf("timeouts = %+v", service.Timeouts)
	}

	withoutQueue, err := Mount(context.Background(), platform(requiredEnv(t)))
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutQueue.Workers) != 1 {
		t.Fatalf("workers without a queue = %d, want the dispatcher alone", len(withoutQueue.Workers))
	}
}

func TestMountRefusesWhatItCannotRunWith(t *testing.T) {
	if _, err := Mount(context.Background(), platform(map[string]string{})); err == nil {
		t.Fatal("Mount() accepted a configuration missing every required variable")
	}

	noDatabase := platform(map[string]string{})
	noDatabase.Database = nil
	if _, err := Mount(context.Background(), noDatabase); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Mount() without a database = %v", err)
	}
	if err := Migrate(context.Background(), noDatabase); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Migrate() without a database = %v", err)
	}
}
