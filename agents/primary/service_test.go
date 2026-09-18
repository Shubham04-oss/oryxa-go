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

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// platform builds a fake host.Platform carrying only what Mount reads before
// it would need Google's credentials.
func platform(values map[string]string, database *postgreskit.Database) host.Platform {
	return host.Platform{
		Logger:   slog.New(slog.DiscardHandler),
		Database: database,
		Getenv:   func(name string) string { return values[name] },
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
	if service.Timeouts.Read != 15*time.Second || service.Timeouts.Write != 0 {
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

func TestMigrateNeedsTheDatabase(t *testing.T) {
	if err := Migrate(context.Background(), platform(nil, nil)); !errors.Is(err, host.ErrDatabaseRequired) {
		t.Fatalf("Migrate() = %v, want ErrDatabaseRequired", err)
	}
}
