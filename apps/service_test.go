package apps

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

func platform(values map[string]string) host.Platform {
	return host.Platform{
		EventAuth: host.TestRegistry(),
		Logger:    slog.New(slog.DiscardHandler),
		// Mounting builds the store but never queries it: the pool just has to
		// be non-nil, for building the outbox table, which pgxpool.New leaves
		// true without dialing anything.
		Database:     postgreskit.Detached(nil),
		AWS:          aws.Config{Region: "ap-south-1"},
		EventBusName: "default",
		Getenv:       func(name string) string { return values[name] },
	}
}

func required() map[string]string {
	return map[string]string{
		"AUTH_URL":             "http://127.0.0.1:8080",
		"COMPOSIO_API_KEY":     "ak_test",
		"APPS_PUBLIC_BASE_URL": "https://api.oryxa.test",
	}
}

func TestMountServesAppsRoutesAndRunsItsWorkers(t *testing.T) {
	values := required()
	values["APPS_ALLOWED_ORIGINS"] = "https://app.oryxa.test"
	values["COMPOSIO_WEBHOOK_SECRET"] = "whsec_test"
	values["APPS_EVENTS_QUEUE_URL"] = "http://localhost:4566/000000000000/apps-events"

	service, err := Mount(context.Background(), platform(values))
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler == nil {
		t.Fatal("no handler")
	}
	for _, route := range []string{
		"GET /v1/apps/catalog",
		"GET /v1/apps/connect/callback",
		"POST " + WebhookPath,
		"GET /v1/organizations/{organizationID}/apps/connections",
		"POST /v1/organizations/{organizationID}/apps/tools/{tool}/run",
		"POST /v1/organizations/{organizationID}/apps/triggers",
		"POST /v1/mcp/apps",
	} {
		if !slices.Contains(service.Routes, route) {
			t.Errorf("routes do not include %q: %v", route, service.Routes)
		}
	}
	if len(service.Workers) != 4 {
		t.Fatalf("workers = %d, want maintenance, the auto-subscriber, the dispatcher and the consumer", len(service.Workers))
	}
	if service.Timeouts.Read == 0 || service.Timeouts.Write == 0 {
		t.Fatalf("timeouts = %+v", service.Timeouts)
	}

	withoutQueue, err := Mount(context.Background(), platform(required()))
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutQueue.Workers) != 3 {
		t.Fatalf("workers without a queue = %d, want maintenance, the auto-subscriber and the dispatcher", len(withoutQueue.Workers))
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

func TestMountedAppsAnswersItsOwnRoutes(t *testing.T) {
	service, err := Mount(context.Background(), platform(required()))
	if err != nil {
		t.Fatal(err)
	}
	// Every organization route wants a member before it does anything, and
	// the MCP endpoint says how to authenticate.
	for _, probe := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/v1/organizations/org_1/apps/connections", http.StatusUnauthorized},
		{http.MethodPost, "/v1/organizations/org_1/apps/triggers", http.StatusUnauthorized},
		{http.MethodPost, "/v1/mcp/apps", http.StatusUnauthorized},
		{http.MethodGet, "/v1/apps/connect/callback", http.StatusBadRequest},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(probe.method, probe.path, strings.NewReader(`{}`))
		if probe.path == "/v1/mcp/apps" {
			request.Header.Set("X-Oryxa-Organization", "org_1")
		}
		service.Handler.ServeHTTP(recorder, request)
		if recorder.Code != probe.status {
			t.Errorf("%s %s = %d, want %d: %s", probe.method, probe.path, recorder.Code, probe.status, recorder.Body)
		}
	}
	// A delivery with no signature is refused without touching the body.
	recorder := httptest.NewRecorder()
	service.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(`{"id":"wh_1","type":"composio.trigger.message"}`)))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned delivery = %d, want 401: %s", recorder.Code, recorder.Body)
	}
}

func TestSubscriptionsAreWhatAppsActsOn(t *testing.T) {
	subscriptions := Subscriptions()
	types := subscriptions[authv1.Source]
	for _, detailType := range []string{authv1.MemberRemovedDetailType, authv1.OrganizationDeletedDetailType} {
		if !slices.Contains(types, detailType) {
			t.Errorf("apps does not consume %s: %v", detailType, subscriptions)
		}
	}
	// apps consumes nothing of its own, so its queue cannot feed itself.
	if _, ours := subscriptions[appsv1.Source]; ours {
		t.Fatalf("apps subscribes to its own events: %v", subscriptions)
	}
}

func TestNewNeedsAReturnOriginItCanParse(t *testing.T) {
	_, err := New(Dependencies{Store: nil}, Config{
		PublicBaseURL: "https://api.oryxa.test", AllowedOrigins: []string{"not an origin"},
	})
	if err == nil {
		t.Fatal("New() accepted an origin that is not absolute")
	}
}

func TestDefinitionNamesAppsAndItsURL(t *testing.T) {
	if Definition.Name != Name || Definition.URLVariable != URLVariable {
		t.Fatalf("definition = %+v", Definition)
	}
	if Definition.Migrate == nil || Definition.Mount == nil {
		t.Fatalf("definition = %+v", Definition)
	}
}
