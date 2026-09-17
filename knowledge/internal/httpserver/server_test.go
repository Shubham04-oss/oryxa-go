package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/application"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

// fakeService answers whatever a test needs, recording the credentials it was
// called with.
type fakeService struct {
	source domain.Source
	err    error
	seen   authclient.Credentials
}

func (service *fakeService) RegisterSource(_ context.Context, credentials authclient.Credentials, _ string, _ application.RegisterInput) (domain.Source, error) {
	service.seen = credentials
	return service.source, service.err
}

func (service *fakeService) Sources(_ context.Context, credentials authclient.Credentials, _ string, _ domain.SourceQuery) ([]domain.Source, error) {
	service.seen = credentials
	if service.err != nil {
		return nil, service.err
	}
	return []domain.Source{service.source}, nil
}

func (service *fakeService) Source(_ context.Context, credentials authclient.Credentials, _, _ string) (domain.Source, error) {
	service.seen = credentials
	return service.source, service.err
}

func (service *fakeService) RemoveSource(_ context.Context, credentials authclient.Credentials, _, _ string) error {
	service.seen = credentials
	return service.err
}

func (service *fakeService) Runs(_ context.Context, credentials authclient.Credentials, _, _ string, _ int) ([]domain.SyncRun, error) {
	service.seen = credentials
	return []domain.SyncRun{}, service.err
}

func (service *fakeService) Document(_ context.Context, credentials authclient.Credentials, _, _ string) (domain.Document, error) {
	service.seen = credentials
	return domain.Document{ID: "doc_1", Body: "the body"}, service.err
}

func (service *fakeService) Extractors() []string { return []string{"files"} }

func registered() domain.Source {
	return domain.Source{
		ID: "src_1", OrganizationID: "org_1",
		Provider: "files", Target: map[string]string{"team_id": "team_1"},
		Scope: domain.ScopeOrganization, CreatedBy: "user-1", Status: domain.StatusActive,
		Interval: time.Hour, CreatedAt: testClock, UpdatedAt: testClock,
		LeaseToken: "lease-a", Version: 3,
	}
}

func testServer(t *testing.T, service *fakeService) *Server {
	t.Helper()
	return New(service, Config{AllowedOrigins: []string{"https://app.example.test"}}, nil)
}

func send(server *Server, method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func bearer() map[string]string {
	return map[string]string{"Authorization": "Bearer member-token", "Content-Type": "application/json"}
}

func TestPatternsNameEveryRoute(t *testing.T) {
	server := New(&fakeService{}, Config{}, nil)
	patterns := server.Patterns()
	if len(patterns) < 8 {
		t.Fatalf("patterns = %v", patterns)
	}
	wildcards := regexp.MustCompile(`\{[^}]+\}`)
	for _, pattern := range patterns {
		method, path, _ := strings.Cut(pattern, " ")
		request := httptest.NewRequest(method, wildcards.ReplaceAllString(path, "x"), nil)
		if _, matched := server.mux.Handler(request); matched != pattern {
			t.Errorf("%s routes to %q", pattern, matched)
		}
	}
}

func TestHealthzAnswersWithoutACaller(t *testing.T) {
	recorder := send(testServer(t, &fakeService{}), http.MethodGet, "/healthz", "", nil)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("GET /healthz = %d, want 204", recorder.Code)
	}
}

func TestProvidersAreReadableBeforeACallerHasAnything(t *testing.T) {
	// A caller needs to know what can be indexed before it registers
	// anything, so this route holds nothing of any organization.
	recorder := send(testServer(t, &fakeService{}), http.MethodGet, "/v1/sources/providers", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /v1/sources/providers = %d, want 200", recorder.Code)
	}
	var answer struct {
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Providers) != 1 || answer.Providers[0] != "files" {
		t.Fatalf("providers = %v, want what the service reads", answer.Providers)
	}
}

func TestEveryOrganizationRouteNeedsACaller(t *testing.T) {
	server := testServer(t, &fakeService{source: registered()})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/organizations/org_1/sources"},
		{http.MethodPost, "/v1/organizations/org_1/sources"},
		{http.MethodGet, "/v1/organizations/org_1/sources/src_1"},
		{http.MethodDelete, "/v1/organizations/org_1/sources/src_1"},
		{http.MethodGet, "/v1/organizations/org_1/sources/src_1/runs"},
		{http.MethodGet, "/v1/organizations/org_1/documents/doc_1"},
	} {
		recorder := send(server, route.method, route.path, "{}", nil)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d, want 401", route.method, route.path, recorder.Code)
		}
	}
}

func TestACookieAuthenticatedChangeNeedsAKnownOrigin(t *testing.T) {
	server := testServer(t, &fakeService{source: registered()})
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/org_1/sources",
		strings.NewReader(`{"provider":"files"}`))
	request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: "session"})
	request.Header.Set("Origin", "https://evil.example.test")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST from an unknown origin = %d, want 403", recorder.Code)
	}
}

func TestRegisterSourceAnswersWhatWasCreated(t *testing.T) {
	service := &fakeService{source: registered()}
	recorder := send(testServer(t, service), http.MethodPost, "/v1/organizations/org_1/sources",
		`{"provider":"files","target":{"team_id":"team_1"},"interval_seconds":3600}`, bearer())

	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST /v1/organizations/org_1/sources = %d, want 201: %s", recorder.Code, recorder.Body)
	}
	if service.seen.Authorization != "Bearer member-token" {
		t.Fatalf("the caller's credentials were not forwarded: %+v", service.seen)
	}
	var answer sourceView
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.ID != "src_1" || answer.IntervalSecs != 3600 {
		t.Fatalf("answer = %+v", answer)
	}
}

func TestRegisterSourceRefusesABodyItDoesNotUnderstand(t *testing.T) {
	server := testServer(t, &fakeService{source: registered()})
	recorder := send(server, http.MethodPost, "/v1/organizations/org_1/sources",
		`{"provider":"files","surprise":true}`, bearer())
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST with an unknown field = %d, want 400", recorder.Code)
	}
}

func TestASourceNeverShowsHowRepicasShareIt(t *testing.T) {
	// The lease and version are how workers divide the work between them, and
	// are nobody else's business.
	service := &fakeService{source: registered()}
	recorder := send(testServer(t, service), http.MethodGet, "/v1/organizations/org_1/sources/src_1", "", bearer())
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET source = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, leaked := range []string{"lease", "version", "attempts"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("a source answered with %q in it: %s", leaked, body)
		}
	}
}

func TestFailuresBecomeTheDocumentedCodes(t *testing.T) {
	for _, failure := range []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrInvalid, http.StatusBadRequest, "invalid_request"},
		{domain.ErrUnauthorized, http.StatusUnauthorized, "unauthorized"},
		{domain.ErrForbidden, http.StatusForbidden, "forbidden"},
		{domain.ErrNotFound, http.StatusNotFound, "not_found"},
		{domain.ErrConflict, http.StatusConflict, "conflict"},
		{domain.ErrCredentialRejected, http.StatusUnprocessableEntity, "credential_rejected"},
		{domain.ErrRateLimited, http.StatusTooManyRequests, "rate_limited"},
		{domain.ErrProviderUnavailable, http.StatusServiceUnavailable, "provider_unavailable"},
	} {
		server := testServer(t, &fakeService{source: registered(), err: failure.err})
		recorder := send(server, http.MethodGet, "/v1/organizations/org_1/sources/src_1", "", bearer())
		if recorder.Code != failure.status {
			t.Fatalf("%v answered %d, want %d", failure.err, recorder.Code, failure.status)
		}
		var answer struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatal(err)
		}
		if answer.Error.Code != failure.code {
			t.Fatalf("%v answered code %q, want %q", failure.err, answer.Error.Code, failure.code)
		}
	}
}

func TestAnAnswerIsNeverCached(t *testing.T) {
	// A source's status changes behind the caller, and a cached answer would
	// say it is still running long after it paused.
	recorder := send(testServer(t, &fakeService{source: registered()}), http.MethodGet,
		"/v1/organizations/org_1/sources/src_1", "", bearer())
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestRemovingASourceAnswersWithNothing(t *testing.T) {
	recorder := send(testServer(t, &fakeService{source: registered()}), http.MethodDelete,
		"/v1/organizations/org_1/sources/src_1", "", bearer())
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("DELETE source = %d, want 204", recorder.Code)
	}
}
