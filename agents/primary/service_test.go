package primary

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/assembly"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// platform builds a fake host.Platform carrying only what Mount reads before
// it would need Google's credentials.
func platform(values map[string]string, database *postgreskit.Database) host.Platform {
	return host.Platform{
		EventAuth: host.TestRegistry(),
		Logger:    slog.New(slog.DiscardHandler),
		Database:  database,
		Getenv:    func(name string) string { return values[name] },
	}
}

func TestMountWithoutAModelAnswersEveryConversationWithTheReason(t *testing.T) {
	service, err := Mount(context.Background(), platform(map[string]string{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if service.Handler == nil || !slices.Contains(service.Routes, "POST /v1/organizations/{organizationID}/threads/{threadID}/runs") {
		t.Fatalf("routes = %v", service.Routes)
	}
	if len(service.Workers) != 0 || service.Close != nil {
		t.Fatalf("an unconfigured service started work: %d workers, close %t", len(service.Workers), service.Close != nil)
	}
	if service.Timeouts.Read != 15*time.Second || service.Timeouts.Write != 2*time.Minute {
		t.Fatalf("timeouts = %+v", service.Timeouts)
	}
	recorder := httptest.NewRecorder()
	service.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/organizations/org_1/threads", nil))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "agents_unavailable") {
		t.Fatalf("threads = %d %s, want 503 agents_unavailable", recorder.Code, recorder.Body.String())
	}
}

func TestMountWithAModelNeedsTheDatabaseAndAuth(t *testing.T) {
	configured := map[string]string{"GOOGLE_CLOUD_PROJECT": "oryxa-test"}
	if _, err := Mount(context.Background(), platform(configured, nil)); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Mount() without a database = %v, want ErrDatabaseRequired", err)
	}
	database := postgreskit.Detached(nil)
	defer database.Close()
	if _, err := Mount(context.Background(), platform(configured, database)); err == nil || !strings.Contains(err.Error(), "AUTH_URL") {
		t.Fatalf("Mount() without AUTH_URL = %v, want it named", err)
	}
}

func TestMountRefusesInvalidConfiguration(t *testing.T) {
	if _, err := Mount(context.Background(), platform(map[string]string{
		"AGENTS_PRIMARY_AUTO_MIGRATE": "sometimes",
	}, nil)); err == nil {
		t.Fatal("Mount() accepted an invalid AGENTS_PRIMARY_AUTO_MIGRATE")
	}
}

// TestMountRefusesADeliveryThinkingLevelItCannotUse: the service does not
// start on a thinking level that is none, or that the model a delivery runs
// on would refuse, rather than fail every delivery once it has started.
func TestMountRefusesADeliveryThinkingLevelItCannotUse(t *testing.T) {
	for _, values := range []map[string]string{
		{"AGENTS_PRIMARY_DELIVERY_THINKING": "a little"},
		{"AGENTS_PRIMARY_DELIVERY_THINKING": "minimal", "AGENTS_PRIMARY_MODEL": "gemini-3.8-flash"},
	} {
		values["GOOGLE_CLOUD_PROJECT"] = "oryxa-test"
		if _, err := Mount(context.Background(), platform(values, nil)); err == nil || !strings.Contains(err.Error(), "AGENTS_PRIMARY_DELIVERY_THINKING") {
			t.Errorf("Mount() with %v = %v, want it refused, naming the setting", values, err)
		}
	}
}

// TestMountRefusesHalfAnAnswerGuard: a guardrail id without its version, or
// a version without its id, would check no answer while looking set up, so
// the service does not start, with a model or without one.
func TestMountRefusesHalfAnAnswerGuard(t *testing.T) {
	for _, values := range []map[string]string{
		{"AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID": "gr-answers"},
		{"AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION": "1"},
		{"AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID": "gr-answers", "GOOGLE_CLOUD_PROJECT": "oryxa-test"},
	} {
		if _, err := Mount(context.Background(), platform(values, nil)); err == nil || !strings.Contains(err.Error(), "AGENTS_PRIMARY_ANSWER_GUARDRAIL") {
			t.Errorf("Mount() with %v = %v, want it refused, naming the settings", values, err)
		}
	}
}

// TestMountRefusesAnAnswerGuardCeilingItDoesNotKnow: the relevance check's
// ceiling is hold, shadow or off, and a list of exempt organizations is
// separated by commas, or the service does not start: a typo would otherwise
// let a check hold answers a deployment meant it not to, or exempt nobody.
func TestMountRefusesAnAnswerGuardCeilingItDoesNotKnow(t *testing.T) {
	for variable, value := range map[string]string{
		"AGENTS_PRIMARY_GUARD_RELEVANCE_CAP": "strict",
		"AGENTS_PRIMARY_GUARD_HOLD_ORGS":     "org_1 org_2",
	} {
		if _, err := Mount(context.Background(), platform(map[string]string{variable: value}, nil)); err == nil || !strings.Contains(err.Error(), variable) {
			t.Errorf("Mount() with %s=%q = %v, want it refused, naming the setting", variable, value, err)
		}
	}
}

// TestTheHostServesTheAnswerGuardSettingsRoutes: the host sends a service the
// paths it lists, so the settings routes have to be among them, whether or not
// the service can run agents.
func TestTheHostServesTheAnswerGuardSettingsRoutes(t *testing.T) {
	service, err := Mount(context.Background(), platform(map[string]string{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		"GET /v1/organizations/{organizationID}/answer-guard/preferences",
		"PUT /v1/organizations/{organizationID}/answer-guard/preferences",
	} {
		if !slices.Contains(service.Routes, route) {
			t.Errorf("routes = %v, want %q among them", service.Routes, route)
		}
	}
	// A server that cannot run agents answers for them all the same way.
	recorder := httptest.NewRecorder()
	service.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/organizations/org_1/answer-guard/preferences", nil))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "agents_unavailable") {
		t.Fatalf("answer guard preferences = %d %s, want 503 agents_unavailable", recorder.Code, recorder.Body.String())
	}
}

// TestADeliveryReachesAsFarAsItsProfile: approved work reads what it was
// allowed of the member's apps and knowledge, a new request only their
// knowledge, and a report, or a delivering profile this does not know, the
// request alone.
func TestADeliveryReachesAsFarAsItsProfile(t *testing.T) {
	for profile, want := range map[managedagents.UnattendedProfile]assembly.DeliveryReach{
		managedagents.DeliveryReads:  assembly.DeliveryReads,
		managedagents.DeliveryApps:   assembly.DeliveryApps,
		managedagents.DeliveryQuiet:  assembly.DeliveryQuiet,
		managedagents.DeliveryReport: assembly.DeliveryReport,
		"delivery_everything":        assembly.DeliveryReport,
	} {
		if got := deliveryReach(profile); got != want {
			t.Errorf("deliveryReach(%q) = %d, want %d", profile, got, want)
		}
	}
}

func TestMigrateNeedsTheDatabase(t *testing.T) {
	if err := Migrate(context.Background(), platform(nil, nil)); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Migrate() = %v, want ErrDatabaseRequired", err)
	}
}
