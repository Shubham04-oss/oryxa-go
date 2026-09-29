package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/application"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

// fakeService answers whatever a test needs, recording the credentials and
// input it was called with.
type fakeService struct {
	source     domain.Source
	document   domain.Document
	err        error
	seen       authclient.Credentials
	registered application.RegisterInput
	query      domain.SourceQuery
	kinds      []string
	kindsSet   bool
}

func (service *fakeService) SetSourceKinds(_ context.Context, credentials authclient.Credentials, _, _ string, include []string) (domain.Source, error) {
	service.seen, service.kinds, service.kindsSet = credentials, include, true
	if service.err != nil {
		return domain.Source{}, service.err
	}
	source := service.source
	source.IncludeKinds = include
	return source, nil
}

func (service *fakeService) RegisterSource(_ context.Context, credentials authclient.Credentials, _ string, input application.RegisterInput) (domain.Source, error) {
	service.seen = credentials
	service.registered = input
	return service.source, service.err
}

func (service *fakeService) Sources(_ context.Context, credentials authclient.Credentials, _ string, query domain.SourceQuery) ([]domain.Source, error) {
	service.seen = credentials
	service.query = query
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
		{http.MethodPut, "/v1/organizations/org_1/sources/src_1/kinds"},
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

// jsonNUL is how a JSON document spells a NUL inside a string.
const jsonNUL = `\u` + `0000`

func TestRegisterSourceRefusesANulInATarget(t *testing.T) {
	service := &fakeService{source: registered()}
	recorder := send(testServer(t, service), http.MethodPost, "/v1/organizations/org_1/sources",
		`{"provider":"files","target":{"team_id":"team`+jsonNUL+`_1"}}`, bearer())
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_request") {
		t.Fatalf("status = %d, want 400 invalid_request: %s", recorder.Code, recorder.Body)
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

func TestRegisterSourceForwardsTheConnectionID(t *testing.T) {
	service := &fakeService{source: registered()}
	recorder := send(testServer(t, service), http.MethodPost, "/v1/organizations/org_1/sources",
		`{"provider":"github","connection_id":"apc_1","target":{"repository":"acme/api"}}`, bearer())

	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST /v1/organizations/org_1/sources = %d, want 201: %s", recorder.Code, recorder.Body)
	}
	if service.registered.ConnectionID != "apc_1" {
		t.Fatalf("RegisterInput.ConnectionID = %q, want apc_1", service.registered.ConnectionID)
	}
}

func TestListSourcesForwardsTheConnectionIDFilter(t *testing.T) {
	service := &fakeService{source: registered()}
	recorder := send(testServer(t, service), http.MethodGet, "/v1/organizations/org_1/sources?connection_id=apc_1", "", bearer())

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET sources = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if service.query.ConnectionID != "apc_1" {
		t.Fatalf("SourceQuery.ConnectionID = %q, want apc_1", service.query.ConnectionID)
	}
}

func TestASourceShowsItsConnection(t *testing.T) {
	withConnection := registered()
	withConnection.ConnectionID = "apc_1"
	service := &fakeService{source: withConnection}
	recorder := send(testServer(t, service), http.MethodGet, "/v1/organizations/org_1/sources/src_1", "", bearer())

	var answer sourceView
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.ConnectionID != "apc_1" {
		t.Fatalf("ConnectionID = %q, want apc_1", answer.ConnectionID)
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
		{domain.ErrNeedsReconnect, http.StatusConflict, "needs_reconnect"},
		{domain.ErrScopeRequired, http.StatusForbidden, "scope_required"},
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

// fakeVerifier stands in for the service-token check, answering whatever
// claims a test needs.
type fakeVerifier struct {
	claims serviceauth.Claims
	err    error
	tokens []string
}

func (verifier *fakeVerifier) Verify(_ context.Context, token string) (serviceauth.Claims, error) {
	verifier.tokens = append(verifier.tokens, token)
	if verifier.err != nil {
		return serviceauth.Claims{}, verifier.err
	}
	return verifier.claims, nil
}

const bodyPath = "/v1/internal/organizations/org_1/documents/doc_1/body"

// The internal route is how a retrieval pipeline reads back a body too large
// to have travelled on the bus. It identifies the caller and nothing else: a
// service has no member to be, which is why it answers one document by id
// and never a listing.

func TestTheInternalBodyRouteAnswersAnAdmittedService(t *testing.T) {
	service := &fakeService{document: domain.Document{ID: "doc_1", Body: "the handbook: a < b && c > d", ContentHash: "hash-1"}}
	verifier := &fakeVerifier{claims: serviceauth.Claims{Service: "rag", Purpose: "documents"}}
	server := New(service, Config{Services: verifier}, nil)

	recorder := send(server, http.MethodGet, bodyPath, "",
		map[string]string{serviceauth.Header: "service-token"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body)
	}
	// Written as it is, not HTML-escaped at six bytes a character, so a body
	// near the ceiling still fits what the caller reads.
	if !strings.Contains(recorder.Body.String(), "the handbook: a < b && c > d") {
		t.Fatalf("body = %s", recorder.Body)
	}
	if len(verifier.tokens) != 1 || verifier.tokens[0] != "service-token" {
		t.Fatalf("verified %v", verifier.tokens)
	}
}

func TestTheInternalBodyRouteRefusesEverythingElse(t *testing.T) {
	for name, testCase := range map[string]struct {
		verifier *fakeVerifier
		// services nil means the deployment named no callers at all.
		noVerifier bool
		headers    map[string]string
		want       int
	}{
		"a deployment that named no callers": {
			noVerifier: true, headers: map[string]string{serviceauth.Header: "service-token"},
			want: http.StatusNotFound,
		},
		"no service token": {
			verifier: &fakeVerifier{claims: serviceauth.Claims{Purpose: "documents"}},
			headers:  map[string]string{}, want: http.StatusUnauthorized,
		},
		"a member's own token is not a service token": {
			verifier: &fakeVerifier{claims: serviceauth.Claims{Purpose: "documents"}},
			headers:  bearer(), want: http.StatusUnauthorized,
		},
		"a token that does not verify": {
			verifier: &fakeVerifier{err: serviceauth.ErrInvalidToken},
			headers:  map[string]string{serviceauth.Header: "forged"}, want: http.StatusUnauthorized,
		},
		"a service using a purpose it may not": {
			verifier: &fakeVerifier{err: serviceauth.ErrPurposeNotAllowed},
			headers:  map[string]string{serviceauth.Header: "service-token"}, want: http.StatusForbidden,
		},
		"a service whose purpose is another one": {
			verifier: &fakeVerifier{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}},
			headers:  map[string]string{serviceauth.Header: "service-token"}, want: http.StatusForbidden,
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := Config{}
			if !testCase.noVerifier {
				config.Services = testCase.verifier
			}
			service := &fakeService{document: domain.Document{ID: "doc_1", Body: "the handbook"}}
			server := New(service, config, nil)

			recorder := send(server, http.MethodGet, bodyPath, "", testCase.headers)
			if recorder.Code != testCase.want {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, testCase.want, recorder.Body)
			}
			if strings.Contains(recorder.Body.String(), "the handbook") {
				t.Fatalf("a refused call was answered the body anyway: %s", recorder.Body)
			}
		})
	}
}

func (service *fakeService) DocumentBody(_ context.Context, _, _ string) (domain.Document, error) {
	if service.err != nil {
		return domain.Document{}, service.err
	}
	return service.document, nil
}

func (service *fakeService) SyncSource(_ context.Context, credentials authclient.Credentials, _, _ string) (domain.Source, error) {
	service.seen = credentials
	if service.err != nil {
		return domain.Source{}, service.err
	}
	return service.source, nil
}

func TestKindsAreReadableBeforeACallerHasAnything(t *testing.T) {
	recorder := send(testServer(t, &fakeService{}), http.MethodGet, "/v1/sources/kinds", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /v1/sources/kinds = %d, want 200", recorder.Code)
	}
	var answer struct {
		Kinds []domain.KindInfo `json:"kinds"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Kinds) != len(domain.Kinds) || answer.Kinds[0].Label == "" {
		t.Fatalf("kinds = %+v, want every kind a member chooses among, labelled", answer.Kinds)
	}
}

func TestSetSourceKindsPassesTheChoiceThrough(t *testing.T) {
	service := &fakeService{source: registered()}
	recorder := send(testServer(t, service), http.MethodPut, "/v1/organizations/org_1/sources/src_1/kinds",
		`{"include_kinds":["conversation","event_update"]}`, bearer())
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT kinds = %d: %s", recorder.Code, recorder.Body)
	}
	if len(service.kinds) != 2 || service.kinds[1] != "event_update" {
		t.Fatalf("kinds = %v, want the member's choice", service.kinds)
	}
	var answer struct {
		IncludeKinds []string `json:"include_kinds"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil || len(answer.IncludeKinds) != 2 {
		t.Fatalf("answer = %s, want the source with its choice", recorder.Body)
	}

	// Null returns a source to the default.
	recorder = send(testServer(t, service), http.MethodPut, "/v1/organizations/org_1/sources/src_1/kinds",
		`{"include_kinds":null}`, bearer())
	if recorder.Code != http.StatusOK || service.kinds != nil {
		t.Fatalf("PUT null = %d, kinds %v; want the default back", recorder.Code, service.kinds)
	}
}

func TestPolicyDiagnosticCauseIsNotReturnedToCaller(t *testing.T) {
	failure := fmt.Errorf("read policy: %w: invalid stored sensitive diagnostic: %w", domain.ErrProviderUnavailable, domain.ErrInvalid)
	server := testServer(t, &fakeService{})
	response := httptest.NewRecorder()
	server.handleError(response, httptest.NewRequest(http.MethodGet, "/v1/organizations/org_1/knowledge/sources", nil), failure)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "sensitive diagnostic") {
		t.Fatalf("policy error response = %d %s", response.Code, response.Body)
	}
}
