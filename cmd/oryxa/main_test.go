package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestEveryServiceSharesOneListener mounts every service this binary carries,
// as a process running all of them does, and proves they coexist: no two
// claim one path, and each path reaches the service that serves it.
func TestEveryServiceSharesOneListener(t *testing.T) {
	// Auth discovers its Cognito issuer while it mounts.
	var issuer *httptest.Server
	issuer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(writer, request)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"issuer":                                issuer.URL,
			"authorization_endpoint":                issuer.URL + "/oauth2/authorize",
			"token_endpoint":                        issuer.URL + "/oauth2/token",
			"jwks_uri":                              issuer.URL + "/.well-known/jwks.json",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}))
	defer issuer.Close()

	// The smallest environment every service accepts. Nothing here is
	// contacted: mounting builds clients but calls no AWS service.
	environment := map[string]string{
		"COGNITO_ISSUER":         issuer.URL,
		"COGNITO_USER_POOL_ID":   "ap-south-1_test",
		"COGNITO_CLIENT_ID":      "oryxa-web",
		"COGNITO_DOMAIN":         "https://oryxa.auth.ap-south-1.amazoncognito.com",
		"COGNITO_CALLBACK_URL":   "http://localhost:8080/v1/auth/callback",
		"AUTH_RETURN_URLS":       "http://localhost:3000",
		"INVITATION_ACCEPT_URL":  "http://localhost:3000/invitations/accept",
		"EMAIL_VERIFICATION_URL": "http://localhost:3000/auth/verify-email",
		"PASSWORD_RESET_URL":     "http://localhost:3000/auth/reset-password",
		"BILLING_URL":            "http://localhost:3000/settings/billing",
		"BILLING_PLANS_FILE":     "../../billing/plans.example.json",
		"FILES_BUCKET_NAME":      "oryxa-files",
		"EMAIL_FROM_ADDRESS":     "no-reply@example.com",
		"EMAIL_QUEUE_URL":        "http://localhost:4566/000000000000/email-events",
		"COMPOSIO_API_KEY":       "ak_test",
		"APPS_PUBLIC_BASE_URL":   "http://localhost:8080",
		// Realtime ships off; on, it serves the Worker's route.
		"REALTIME_WORKER_URL":       "http://localhost:3001",
		"REALTIME_PURGE_SECRET":     "5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84",
		"REALTIME_EVENTS_QUEUE_URL": "http://localhost:4566/000000000000/realtime-events",
	}
	// Mounting never queries the database; rag only asks it for pgvector.
	database := postgreskit.Detached(map[string]string{"vector": "0.8.6", "ltree": "1.3", "pg_trgm": "1.6"})
	platform := host.Platform{
		Logger:       slog.New(slog.DiscardHandler),
		Database:     database,
		AWS:          aws.Config{Region: "ap-south-1", Credentials: aws.AnonymousCredentials{}},
		EventBusName: "default",
		EventAuth:    host.TestRegistry(),
		Getenv:       host.Environment(func(name string) string { return environment[name] }, definitions, "http://127.0.0.1:1"),
	}
	handler, closeAll, err := host.Assemble(context.Background(), platform, definitions)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll()

	probes := []struct {
		service, path string
		status        int
		// mark is something only that service's answer contains.
		mark string
	}{
		{"the host", "/healthz", http.StatusNoContent, ""},
		{"auth", "/v1/auth/config", http.StatusOK, `"password_policy"`},
		{"billing", "/v1/billing/plans", http.StatusOK, `"unpaid"`},
		{"knowledge", "/v1/sources/providers", http.StatusOK, `"providers"`},
		{"rag", "/v1/capabilities", http.StatusOK, `"embedding_providers"`},
		// Every organization route wants a member before it does anything.
		{"teams", "/v1/organizations/org_1/teams", http.StatusUnauthorized, ""},
		{"files", "/v1/organizations/org_1/files", http.StatusUnauthorized, ""},
		{"knowledge", "/v1/organizations/org_1/sources", http.StatusUnauthorized, ""},
		{"rag", "/v1/organizations/org_1/corpora", http.StatusUnauthorized, ""},
		{"apps", "/v1/organizations/org_1/apps/connections", http.StatusUnauthorized, ""},
		// Without a Google Cloud project there is no model to run Krude on,
		// and agents says so rather than keeping the process from starting.
		{"agents", "/v1/organizations/org_1/threads", http.StatusServiceUnavailable, `"agents_unavailable"`},
		// The Worker's route takes a POST alone.
		{"realtime", "/v1/realtime/events", http.StatusMethodNotAllowed, ""},
		{"nobody", "/v1/unknown", http.StatusNotFound, ""},
	}
	for _, probe := range probes {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, probe.path, nil))
		if recorder.Code != probe.status || !strings.Contains(recorder.Body.String(), probe.mark) {
			t.Errorf("GET %s (%s) = %d %s, want %d containing %s", probe.path, probe.service,
				recorder.Code, strings.TrimSpace(recorder.Body.String()), probe.status, probe.mark)
		}
	}
}

// TestEveryEventConsumerDeclaresItsQueue pins which services consume events.
// A production start refuses one whose queue is unset (host.CheckEventQueues);
// a consumer that stopped declaring its queue would start without it and
// never hear what others deleted.
func TestEveryEventConsumerDeclaresItsQueue(t *testing.T) {
	want := map[string]string{
		"agents": "AGENTS_EVENTS_QUEUE_URL", "apps": "APPS_EVENTS_QUEUE_URL", "auth": "AUTH_EVENTS_QUEUE_URL",
		"batching": "BATCHING_EVENTS_QUEUE_URL", "billing": "BILLING_EVENTS_QUEUE_URL", "email": "EMAIL_QUEUE_URL",
		"files": "FILES_EVENTS_QUEUE_URL", "knowledge": "KNOWLEDGE_EVENTS_QUEUE_URL", "rag": "RAG_EVENTS_QUEUE_URL",
		"realtime": "REALTIME_EVENTS_QUEUE_URL", "teams": "TEAMS_EVENTS_QUEUE_URL",
	}
	// consumesNothing names the services that read no events at all. A new
	// service is in neither list until someone decides which it is: a
	// consumer that forgot to declare its queue would otherwise pass here
	// and start in production without it, never hearing what others deleted.
	consumesNothing := map[string]bool{"workspace": true}
	got := map[string]string{}
	for _, definition := range definitions {
		switch {
		case definition.EventsQueueVariable != "":
			got[definition.Name] = definition.EventsQueueVariable
		case !consumesNothing[definition.Name]:
			t.Errorf("%s declares no events queue: declare EventsQueueVariable, or list it in consumesNothing if it reads no events", definition.Name)
		}
	}
	if !maps.Equal(got, want) {
		t.Fatalf("event queues = %v, want %v", got, want)
	}
}

func TestTeamsMigratesBeforeRag(t *testing.T) {
	names := host.Names(definitions)
	teams, rag := slices.Index(names, "teams"), slices.Index(names, "rag")
	if teams < 0 || rag < 0 || teams > rag {
		t.Fatalf("services = %v: teams installs the vector extension rag's tables need, so it must come first", names)
	}
}

func TestCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"services"}, &stdout, &stderr); code != 0 ||
		!slices.Equal(strings.Fields(stdout.String()), host.Names(definitions)) {
		t.Fatalf("services = %d %q", code, stdout.String())
	}
	stdout.Reset()
	if code := run(context.Background(), []string{"unheard-of"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "Usage") {
		t.Fatalf("unknown command = %d %q", code, stderr.String())
	}

	up := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(writer, request)
	}))
	defer up.Close()
	address := strings.TrimPrefix(up.URL, "http://")
	if code := healthcheck(context.Background(), address, &stderr); code != 0 {
		t.Fatalf("healthcheck(%s) = %d: %s", address, code, stderr.String())
	}
	up.Close()
	if code := healthcheck(context.Background(), address, &stderr); code != 1 {
		t.Fatalf("healthcheck of a stopped process = %d, want 1", code)
	}
}
