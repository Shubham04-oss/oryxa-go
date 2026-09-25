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
