package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestConfigFromEnvDefaults(t *testing.T) {
	config, err := ConfigFromEnv(env(map[string]string{"DATABASE_URL": " postgres://oryxa@localhost/oryxa "}))
	if err != nil {
		t.Fatal(err)
	}
	if config.Address != ":8080" || config.Services != nil || config.DatabaseURL != "postgres://oryxa@localhost/oryxa" ||
		!config.AutoMigrate || config.DatabaseMaxConns != 32 || config.AWSRegion != "ap-south-1" ||
		config.EventBusName != "default" || config.ShutdownTimeout != 30*time.Second {
		t.Fatalf("config = %+v", config)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	config, err := ConfigFromEnv(env(map[string]string{
		"ORYXA_ADDRESS":            "127.0.0.1:9090",
		"ORYXA_SERVICES":           " teams, files ,email",
		"ORYXA_AUTO_MIGRATE":       "false",
		"ORYXA_DATABASE_MAX_CONNS": "64",
		"ORYXA_SHUTDOWN_TIMEOUT":   "5s",
		"AWS_REGION":               "us-east-1",
		"EVENT_BUS_NAME":           "oryxa",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.Address != "127.0.0.1:9090" || !slices.Equal(config.Services, []string{"teams", "files", "email"}) ||
		config.AutoMigrate || config.DatabaseMaxConns != 64 || config.ShutdownTimeout != 5*time.Second ||
		config.AWSRegion != "us-east-1" || config.EventBusName != "oryxa" {
		t.Fatalf("config = %+v", config)
	}
	if all, _ := ConfigFromEnv(env(map[string]string{"ORYXA_SERVICES": "all"})); all.Services != nil {
		t.Fatalf("ORYXA_SERVICES=all selected %v", all.Services)
	}
}

func TestConfigFromEnvRefusesMalformedValues(t *testing.T) {
	for name, value := range map[string]string{
		"ORYXA_AUTO_MIGRATE":       "sometimes",
		"ORYXA_DATABASE_MAX_CONNS": "0",
		"ORYXA_SHUTDOWN_TIMEOUT":   "soon",
	} {
		if _, err := ConfigFromEnv(env(map[string]string{name: value})); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%q: error = %v", name, value, err)
		}
	}
}

func definition(name, urlVariable string) Definition {
	return Definition{Name: name, URLVariable: urlVariable, Mount: func(context.Context, Platform) (Service, error) {
		return Service{}, nil
	}}
}

func TestSelectKeepsDefinitionOrderAndRefusesUnknownNames(t *testing.T) {
	definitions := []Definition{definition("auth", "AUTH_URL"), definition("teams", "TEAMS_URL"), definition("email", "")}
	all, err := Select(definitions, nil)
	if err != nil || !slices.Equal(Names(all), []string{"auth", "teams", "email"}) {
		t.Fatalf("Select(all) = %v, %v", Names(all), err)
	}
	some, err := Select(definitions, []string{"email", "auth"})
	if err != nil || !slices.Equal(Names(some), []string{"auth", "email"}) {
		t.Fatalf("Select(email, auth) = %v, %v", Names(some), err)
	}
	if _, err := Select(definitions, []string{"billing"}); err == nil || !strings.Contains(err.Error(), "auth, teams, email") {
		t.Fatalf("Select(billing) error = %v", err)
	}
	if _, err := Select(append(definitions, definition("auth", "")), nil); err == nil {
		t.Fatal("Select accepted a service defined twice")
	}
}

func TestEnvironmentPointsRunningServicesAtThisProcess(t *testing.T) {
	getenv := Environment(env(map[string]string{"TEAMS_URL": "https://teams.example.test", "OTHER": "kept"}),
		[]Definition{definition("auth", "AUTH_URL"), definition("teams", "TEAMS_URL"), definition("email", "")},
		"http://127.0.0.1:8080")
	if got := getenv("AUTH_URL"); got != "http://127.0.0.1:8080" {
		t.Errorf("AUTH_URL = %q, want this process", got)
	}
	if got := getenv("TEAMS_URL"); got != "https://teams.example.test" {
		t.Errorf("TEAMS_URL = %q, want the configured one", got)
	}
	if got := getenv("FILES_URL"); got != "" {
		t.Errorf("FILES_URL = %q, want nothing for a service that is not running", got)
	}
	if got := getenv("OTHER"); got != "kept" {
		t.Errorf("OTHER = %q", got)
	}
}

// fakeService is a service with its own mux, as every real one has.
func fakeService(name string, patterns ...string) mounted {
	mux := http.NewServeMux()
	for _, pattern := range patterns {
		mux.HandleFunc(pattern, func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("X-Served-By", name)
			_, _ = io.WriteString(writer, request.Pattern)
		})
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodOptions {
			writer.Header().Set("X-Served-By", name)
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		mux.ServeHTTP(writer, request)
	})
	return mounted{name: name, service: Service{Handler: handler, Routes: patterns}}
}

func TestRoutesSendEachPathToTheServiceThatServesIt(t *testing.T) {
	handler, err := routes([]mounted{
		fakeService("auth", "GET /healthz", "GET /v1/organizations/{organizationID}", "DELETE /v1/organizations/{organizationID}/members/{userID}"),
		fakeService("teams", "GET /healthz", "GET /v1/organizations/{organizationID}/teams", "POST /v1/organizations/{organizationID}/teams",
			"GET /v1/organizations/{organizationID}/members/search"),
		{name: "email"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path string
		status       int
		servedBy     string
	}{
		{http.MethodGet, "/healthz", http.StatusNoContent, ""},
		{http.MethodGet, "/v1/organizations/org-1", http.StatusOK, "auth"},
		{http.MethodPost, "/v1/organizations/org-1/teams", http.StatusOK, "teams"},
		{http.MethodGet, "/v1/organizations/org-1/members/search", http.StatusOK, "teams"},
		{http.MethodDelete, "/v1/organizations/org-1/members/usr-1", http.StatusOK, "auth"},
		// A method the owner does not serve is still the owner's to refuse,
		// and a preflight is still the owner's to answer.
		{http.MethodPut, "/v1/organizations/org-1/teams", http.StatusMethodNotAllowed, ""},
		{http.MethodOptions, "/v1/organizations/org-1", http.StatusNoContent, "auth"},
		{http.MethodGet, "/v1/unknown", http.StatusNotFound, ""},
	}
	for _, test := range cases {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		if recorder.Code != test.status || recorder.Header().Get("X-Served-By") != test.servedBy {
			t.Errorf("%s %s = %d served by %q, want %d by %q", test.method, test.path, recorder.Code,
				recorder.Header().Get("X-Served-By"), test.status, test.servedBy)
		}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/v1/organizations/org-1/teams", nil))
	if allow := recorder.Header().Get("Allow"); !strings.Contains(allow, "GET") || !strings.Contains(allow, "POST") {
		t.Errorf("Allow = %q, want the service's own methods", allow)
	}
}

func TestRoutesRefuseTwoServicesClaimingOnePath(t *testing.T) {
	_, err := routes([]mounted{
		fakeService("connections", "GET /v1/providers"),
		fakeService("knowledge", "GET /v1/providers"),
	})
	if err == nil || !strings.Contains(err.Error(), "knowledge cannot serve /v1/providers") || !strings.Contains(err.Error(), "connections") {
		t.Fatalf("routes() error = %v", err)
	}
	_, err = routes([]mounted{
		fakeService("files", "GET /v1/organizations/{organizationID}/files/{fileID}"),
		fakeService("other", "GET /v1/organizations/{org}/files/{id}"),
	})
	if err == nil || !strings.Contains(err.Error(), "files (as /v1/organizations/{organizationID}/files/{fileID})") {
		t.Fatalf("routes() error = %v", err)
	}
	if _, err := routes([]mounted{fakeService("host-bound", "GET example.com/v1/items")}); err == nil {
		t.Fatal("routes() accepted a host-bound pattern")
	}
	if _, err := routes([]mounted{{name: "handlerless", service: Service{Routes: []string{"GET /v1/items"}}}}); err == nil {
		t.Fatal("routes() accepted routes without a handler")
	}
}

func TestRoutesHoldEachServiceToItsOwnDeadlines(t *testing.T) {
	slow := func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(writer, "late")
	}
	strict := http.NewServeMux()
	strict.HandleFunc("GET /v1/strict", slow)
	streaming := http.NewServeMux()
	streaming.HandleFunc("GET /v1/streaming", slow)
	handler, err := routes([]mounted{
		{name: "strict", service: Service{Handler: strict, Routes: []string{"GET /v1/strict"}, Timeouts: Timeouts{Write: 50 * time.Millisecond}}},
		{name: "streaming", service: Service{Handler: streaming, Routes: []string{"GET /v1/streaming"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := server.Client()

	if response, err := client.Get(server.URL + "/v1/strict"); err == nil {
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr == nil && string(body) == "late" {
			t.Fatal("a response written after the service's write deadline arrived")
		}
	}
	// The same connection pool, so a keep-alive connection that carried the
	// strict request must not carry its deadline into this one.
	response, err := client.Get(server.URL + "/v1/streaming")
	if err != nil {
		t.Fatalf("streaming request: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "late" {
		t.Fatalf("streaming body = %q", body)
	}
}

func TestServeDrainsRequestsBeforeStoppingWorkers(t *testing.T) {
	var (
		mu     sync.Mutex
		events []string
	)
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	release := make(chan struct{})
	arrived := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/slow", func(writer http.ResponseWriter, _ *http.Request) {
		close(arrived)
		<-release
		record("request finished")
		_, _ = io.WriteString(writer, "done")
	})
	service := Service{
		Handler: mux,
		Routes:  []string{"GET /v1/slow"},
		Workers: []Worker{func(ctx context.Context) {
			<-ctx.Done()
			record("worker stopped")
		}},
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var logs bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, []mounted{{name: "slow", service: service}}, 5*time.Second, slog.New(slog.NewJSONHandler(&logs, nil)))
	}()

	responded := make(chan string, 1)
	go func() {
		response, err := http.Get(selfURL(listener.Addr()) + "/v1/slow")
		if err != nil {
			responded <- err.Error()
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		responded <- string(body)
	}()
	<-arrived
	cancel()
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	early := slices.Clone(events)
	mu.Unlock()
	if len(early) != 0 {
		t.Fatalf("events before the request finished = %v", early)
	}
	close(release)

	if body := <-responded; body != "done" {
		t.Fatalf("in-flight request = %q, want it to finish", body)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve() = %v", err)
	}
	if !slices.Equal(events, []string{"request finished", "worker stopped"}) {
		t.Fatalf("events = %v", events)
	}
	if strings.Contains(logs.String(), "worker stopped before shutdown") {
		t.Fatalf("a worker that stopped on shutdown was reported as stopping early: %s", logs.String())
	}
}

func TestServeReportsAWorkerThatStopsEarly(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var logs safeBuffer
	stopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, listener, []mounted{{name: "quitter", service: Service{Workers: []Worker{func(context.Context) {
			close(stopped)
		}}}}}, time.Second, slog.New(slog.NewJSONHandler(&logs, nil)))
	}()
	<-stopped
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "worker stopped before shutdown") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"component":"quitter"`) {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestMountClosesWhatItMountedWhenALaterServiceFails(t *testing.T) {
	var closed []string
	closing := func(name string) Definition {
		return Definition{Name: name, Mount: func(context.Context, Platform) (Service, error) {
			return Service{Close: func() { closed = append(closed, name) }}, nil
		}}
	}
	failing := Definition{Name: "broken", Mount: func(context.Context, Platform) (Service, error) {
		return Service{}, errors.New("no configuration")
	}}
	platform := Platform{Logger: slog.New(slog.DiscardHandler)}
	_, err := mount(context.Background(), platform, []Definition{closing("first"), closing("second"), failing})
	if err == nil || !strings.Contains(err.Error(), "mount broken: no configuration") {
		t.Fatalf("mount() error = %v", err)
	}
	if !slices.Equal(closed, []string{"second", "first"}) {
		t.Fatalf("closed = %v, want reverse mount order", closed)
	}
}

func TestSelfURL(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:8080":   "http://127.0.0.1:8080",
		"[::]:8080":      "http://127.0.0.1:8080",
		"127.0.0.1:9000": "http://127.0.0.1:9000",
		"[::1]:9000":     "http://[::1]:9000",
	}
	for address, want := range cases {
		resolved, err := net.ResolveTCPAddr("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		if got := selfURL(resolved); got != want {
			t.Errorf("selfURL(%s) = %q, want %q", address, got, want)
		}
	}
}

type safeBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *safeBuffer) Write(p []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(p)
}

func (buffer *safeBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}
