package realtime

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

func platform(values map[string]string, logs *bytes.Buffer) host.Platform {
	return host.Platform{
		EventAuth:    host.TestRegistry(),
		Logger:       slog.New(slog.NewTextHandler(logs, nil)),
		AWS:          aws.Config{Region: "ap-south-1", Credentials: aws.AnonymousCredentials{}},
		EventBusName: "default",
		Getenv:       func(name string) string { return values[name] },
	}
}

// configured is everything realtime needs to run.
func configured() map[string]string {
	return map[string]string{
		"REALTIME_WORKER_URL":       "http://127.0.0.1:3001",
		"REALTIME_PURGE_SECRET":     "5f2b8e1c9a7d4036b1e8c2f94a6d0e3b7c1f5a9d2e8b4c6f0a3d7e1b9c5f2a84",
		"REALTIME_EVENTS_QUEUE_URL": "http://localhost:4566/000000000000/realtime-events",
	}
}

func without(values map[string]string, names ...string) map[string]string {
	kept := maps.Clone(values)
	for _, name := range names {
		delete(kept, name)
	}
	return kept
}

func TestDefinitionRegistersAConsumerWithItsOwnQueue(t *testing.T) {
	if Definition.Name != "realtime" || Definition.EventsQueueVariable != "REALTIME_EVENTS_QUEUE_URL" ||
		Definition.URLVariable != "" || Definition.Migrate != nil || Definition.Mount == nil {
		t.Fatalf("Definition = %+v", Definition)
	}
	selected, err := host.Select([]host.Definition{Definition}, []string{"realtime"})
	if err != nil || len(selected) != 1 {
		t.Fatalf("Select(realtime) = %v, %v", host.Names(selected), err)
	}
	// A production start names realtime's queue among those it must have.
	if err := host.CheckEventQueues(selected, func(string) string { return "" }); err == nil ||
		!strings.Contains(err.Error(), "REALTIME_EVENTS_QUEUE_URL (realtime)") {
		t.Fatalf("CheckEventQueues() = %v", err)
	}
}

func TestMountRunsTheConsumerOnceConfigured(t *testing.T) {
	var logs bytes.Buffer
	service, err := Mount(context.Background(), platform(configured(), &logs))
	if err != nil {
		t.Fatal(err)
	}
	if len(service.Workers) != 1 || service.Handler != nil || len(service.Routes) != 0 {
		t.Fatalf("service = %d workers, handler %v, routes %v; want the consumer alone", len(service.Workers), service.Handler, service.Routes)
	}
	if !strings.Contains(logs.String(), "realtime mounted") || !strings.Contains(logs.String(), "worker_url=http://127.0.0.1:3001") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestMountIsOffUntilEverythingItNeedsIsSet(t *testing.T) {
	tests := map[string]struct {
		values map[string]string
		says   string
	}{
		"nothing set": {map[string]string{},
			"REALTIME_WORKER_URL, REALTIME_PURGE_SECRET and REALTIME_EVENTS_QUEUE_URL are not set: realtime is off"},
		"no Worker URL": {without(configured(), "REALTIME_WORKER_URL"), "REALTIME_WORKER_URL is not set: realtime is off"},
		"no secret":     {without(configured(), "REALTIME_PURGE_SECRET"), "REALTIME_PURGE_SECRET is not set: realtime is off"},
		"no queue":      {without(configured(), "REALTIME_EVENTS_QUEUE_URL"), "REALTIME_EVENTS_QUEUE_URL is not set: realtime is off"},
		"neither Worker URL nor secret": {without(configured(), "REALTIME_WORKER_URL", "REALTIME_PURGE_SECRET"),
			"REALTIME_WORKER_URL and REALTIME_PURGE_SECRET are not set: realtime is off"},
		// Running every service, which is what ORYXA_SERVICES=all means.
		"everything selected": {map[string]string{"ORYXA_SERVICES": "all"}, "are not set: realtime is off"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			service, err := Mount(context.Background(), platform(test.values, &logs))
			if err != nil {
				t.Fatalf("Mount() = %v, want realtime off rather than the process refusing to start", err)
			}
			if len(service.Workers) != 0 || service.Handler != nil {
				t.Fatalf("service = %+v, want nothing running", service)
			}
			if got := strings.Count(logs.String(), "level=WARN"); got != 1 || !strings.Contains(logs.String(), test.says) {
				t.Fatalf("logs = %s; want one warning saying %q", logs.String(), test.says)
			}
			if strings.Contains(logs.String(), configured()["REALTIME_PURGE_SECRET"]) {
				t.Fatal("the secret was logged")
			}
		})
	}
}

func TestMountRefusesToStartWhenAskedForByNameWithoutWhatItNeeds(t *testing.T) {
	for _, services := range []string{"realtime", " auth, realtime ,teams"} {
		values := without(configured(), "REALTIME_PURGE_SECRET")
		values["ORYXA_SERVICES"] = services
		var logs bytes.Buffer
		_, err := Mount(context.Background(), platform(values, &logs))
		if err == nil || !strings.Contains(err.Error(), "ORYXA_SERVICES names realtime, but REALTIME_PURGE_SECRET is not set") {
			t.Fatalf("ORYXA_SERVICES=%q: Mount() = %v", services, err)
		}
	}
	values := configured()
	values["ORYXA_SERVICES"] = "realtime"
	if service, err := Mount(context.Background(), platform(values, &bytes.Buffer{})); err != nil || len(service.Workers) != 1 {
		t.Fatalf("ORYXA_SERVICES=realtime, configured: Mount() = %d workers, %v", len(service.Workers), err)
	}
	for services, want := range map[string]bool{"": false, "all": false, "auth,teams": false, "realtimes": false, "realtime": true, "teams, realtime": true} {
		if got := namedIn(services); got != want {
			t.Errorf("namedIn(%q) = %v, want %v", services, got, want)
		}
	}
}

func TestMountRefusesMalformedSettingsEvenWhenOptional(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"a relative Worker URL": {"REALTIME_WORKER_URL": "app.oryxa.in"},
		"a short secret":        {"REALTIME_PURGE_SECRET": "short"},
	} {
		if _, err := Mount(context.Background(), platform(values, &bytes.Buffer{})); err == nil {
			t.Errorf("%s: Mount() = nil error", name)
		}
	}
}

// Realtime serves no route, so it shares the process's listener with any
// service; the host answers for it on /healthz.
func TestTheHostAssemblesRealtime(t *testing.T) {
	for name, values := range map[string]map[string]string{"off": {}, "on": configured()} {
		handler, closeAll, err := host.Assemble(context.Background(), platform(values, &bytes.Buffer{}), []host.Definition{Definition})
		if err != nil {
			t.Fatalf("%s: Assemble() = %v", name, err)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		closeAll()
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("%s: /healthz = %d", name, recorder.Code)
		}
	}
}
