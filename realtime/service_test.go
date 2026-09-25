package realtime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/events"
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
		Definition.URLVariable != "" || Definition.Migrate != nil || Definition.Mount == nil || Definition.Enabled == nil {
		t.Fatalf("Definition = %+v", Definition)
	}
	selected, err := host.Select([]host.Definition{Definition}, []string{"realtime"})
	if err != nil || len(selected) != 1 {
		t.Fatalf("Select(realtime) = %v, %v", host.Names(selected), err)
	}
	// Off, realtime keeps no production start from running, so a release can
	// carry it before its queue exists.
	if err := host.CheckEventQueues(selected, func(string) string { return "" }); err != nil {
		t.Fatalf("CheckEventQueues() with realtime off = %v", err)
	}
	// Being switched on, a production start names its queue among those it
	// must have.
	for _, setting := range []string{"REALTIME_WORKER_URL", "REALTIME_PURGE_SECRET"} {
		values := map[string]string{setting: configured()[setting]}
		if err := host.CheckEventQueues(selected, func(name string) string { return values[name] }); err == nil ||
			!strings.Contains(err.Error(), "REALTIME_EVENTS_QUEUE_URL (realtime)") {
			t.Fatalf("%s set: CheckEventQueues() = %v", setting, err)
		}
	}
}

func TestRealtimeIsOnOnceEitherOfItsOwnSettingsIsSet(t *testing.T) {
	for name, test := range map[string]struct {
		values map[string]string
		on     bool
	}{
		"nothing set": {map[string]string{}, false},
		// What production's task holds while realtime_purge is off.
		"only its queue":   {map[string]string{"REALTIME_EVENTS_QUEUE_URL": configured()["REALTIME_EVENTS_QUEUE_URL"]}, false},
		"blank settings":   {map[string]string{"REALTIME_WORKER_URL": " ", "REALTIME_PURGE_SECRET": "\n"}, false},
		"the Worker's URL": {map[string]string{"REALTIME_WORKER_URL": configured()["REALTIME_WORKER_URL"]}, true},
		"the secret":       {map[string]string{"REALTIME_PURGE_SECRET": configured()["REALTIME_PURGE_SECRET"]}, true},
		"everything":       {configured(), true},
	} {
		if got := Definition.Enabled(func(name string) string { return test.values[name] }); got != test.on {
			t.Errorf("%s: Enabled() = %v, want %v", name, got, test.on)
		}
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
		unset  string
	}{
		"nothing set":   {map[string]string{}, "REALTIME_WORKER_URL, REALTIME_PURGE_SECRET, REALTIME_EVENTS_QUEUE_URL"},
		"no Worker URL": {without(configured(), "REALTIME_WORKER_URL"), "REALTIME_WORKER_URL"},
		"no secret":     {without(configured(), "REALTIME_PURGE_SECRET"), "REALTIME_PURGE_SECRET"},
		"no queue":      {without(configured(), "REALTIME_EVENTS_QUEUE_URL"), "REALTIME_EVENTS_QUEUE_URL"},
		// Running every service, which is what ORYXA_SERVICES=all means.
		"everything selected": {map[string]string{"ORYXA_SERVICES": "all"}, "REALTIME_WORKER_URL, REALTIME_PURGE_SECRET, REALTIME_EVENTS_QUEUE_URL"},
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
			says := "these are not set: " + test.unset
			if got := strings.Count(logs.String(), "level=WARN"); got != 1 || !strings.Contains(logs.String(), "realtime is off") ||
				!strings.Contains(logs.String(), says) {
				t.Fatalf("logs = %s; want one warning that realtime is off, saying %q", logs.String(), says)
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
		if err == nil || !strings.Contains(err.Error(), "ORYXA_SERVICES names realtime, but these are not set: REALTIME_PURGE_SECRET;") {
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

// Enforcing signatures, a purge that waited on realtime's queue, or came back
// from the bus's archive, still verifies for as long as either keeps it: well
// past the day other consumers allow.
func TestRealtimeVerifiesAPurgeForAsLongAsItCanWait(t *testing.T) {
	signedAt := time.Now()
	clock := signedAt
	registry := eventauth.NewRegistry(nil, func() time.Time { return clock })
	signer, err := registry.Signer("auth")
	if err != nil {
		t.Fatal(err)
	}
	const source, detailType, occurredAt = "oryxa.auth", "auth.organization.deleted.v1", "2026-09-24T12:00:00Z"
	data := []byte(`{"organization_id":"org_1","deleted_at":"2026-09-24T12:00:00Z"}`)
	sig, err := signer.Sign(source, detailType, "evt_1", 1, occurredAt, data)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(verifier *eventauth.Verifier) error {
		return verifier.Verify(source, detailType, "evt_1", 1, occurredAt, data, sig)
	}

	clock = signedAt.Add(20 * 24 * time.Hour)
	if err := verify(verifier(registry)); err != nil {
		t.Fatalf("a purge 20 days old was refused: %v", err)
	}
	if err := verify(registry.Verifier()); !errors.Is(err, eventauth.ErrUnverified) {
		t.Fatalf("another consumer's verifier accepted an event 20 days old: %v", err)
	}
	clock = signedAt.Add(events.SignatureMaxAge + time.Minute)
	if err := verify(verifier(registry)); !errors.Is(err, eventauth.ErrUnverified) {
		t.Fatalf("a purge older than its queue and the archive keep one verified: %v", err)
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
