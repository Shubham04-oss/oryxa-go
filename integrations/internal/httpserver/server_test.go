package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/application"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
)

// fakeService implements integrationsService, recording what each call
// received and answering with err when it is set.
type fakeService struct {
	err error

	calls        int
	credentials  authclient.Credentials
	organization string

	// Receive
	receiveProvider string
	receiveRoute    string
	delivery        providers.Delivered
	reply           providers.Reply

	// subscriptions
	connectionID   string
	createInput    application.CreateSubscriptionInput
	subscriptionID string

	// app webhook
	appID  string
	secret string

	// actions
	actionInput application.RunActionInput
}

var testSubscription = domain.Subscription{
	ID: "sub_1", OrganizationID: "org_1", ConnectionID: "con_1", AppID: "app_1", Provider: "github",
	Target: map[string]string{"repo": "acme/widgets"}, Events: []string{"issues.opened"},
	Status: domain.SubscriptionActive, CreatedBy: "user-1",
}

var testWebhookView = application.AppWebhookView{
	AppWebhook: domain.AppWebhook{AppID: "app_1", OrganizationID: "org_1", Provider: "slack", UpdatedBy: "user-1"},
	URL:        "https://integrations.example.test/v1/webhooks/slack/route-1",
}

var testAction = domain.Action{
	ID: "act_1", OrganizationID: "org_1", ConnectionID: "con_1", AppID: "app_1", Provider: "github", Name: "create_issue",
	Scope: "member", MemberID: "user-1", RequestedBy: "user-1", Status: domain.ActionCompleted,
	Resource: &domain.ActionResource{Type: "issue", ID: "12", Name: "Broken build"},
	Result:   json.RawMessage(`{"number":12}`),
}

func (service *fakeService) RunAction(_ context.Context, credentials authclient.Credentials, organizationID string, input application.RunActionInput) (domain.Action, error) {
	service.record(credentials, organizationID)
	service.actionInput = input
	if service.err != nil {
		return domain.Action{}, service.err
	}
	return testAction, nil
}

func (service *fakeService) Catalog() []providers.Description {
	service.calls++
	return []providers.Description{{
		Name: "github", Delivery: providers.DeliveryWebhook,
		Target: []providers.TargetField{{Name: "repository", Description: "The repository to watch.", Required: true}},
		Events: providers.EventsDescription{Names: []string{"issues"}, Open: true},
		Actions: []providers.ActionDescription{{
			Name: "create_issue", Summary: "Open an issue.", Input: json.RawMessage(`{"type":"object"}`), Writes: true,
		}},
		Skill: "# GitHub\n",
	}}
}

func (service *fakeService) record(credentials authclient.Credentials, organizationID string) {
	service.calls++
	service.credentials, service.organization = credentials, organizationID
}

func (service *fakeService) Receive(_ context.Context, provider, route string, delivery providers.Delivered) (providers.Reply, error) {
	service.calls++
	service.receiveProvider, service.receiveRoute, service.delivery = provider, route, delivery
	return service.reply, service.err
}

func (service *fakeService) ListSubscriptions(_ context.Context, credentials authclient.Credentials, organizationID, connectionID string) ([]domain.Subscription, error) {
	service.record(credentials, organizationID)
	service.connectionID = connectionID
	return []domain.Subscription{testSubscription}, service.err
}

func (service *fakeService) CreateSubscription(_ context.Context, credentials authclient.Credentials, organizationID string, input application.CreateSubscriptionInput) (domain.Subscription, error) {
	service.record(credentials, organizationID)
	service.createInput = input
	return testSubscription, service.err
}

func (service *fakeService) GetSubscription(_ context.Context, credentials authclient.Credentials, organizationID, subscriptionID string) (domain.Subscription, error) {
	service.record(credentials, organizationID)
	service.subscriptionID = subscriptionID
	return testSubscription, service.err
}

func (service *fakeService) StopSubscription(_ context.Context, credentials authclient.Credentials, organizationID, subscriptionID string) error {
	service.record(credentials, organizationID)
	service.subscriptionID = subscriptionID
	return service.err
}

func (service *fakeService) GetAppWebhook(_ context.Context, credentials authclient.Credentials, organizationID, appID string) (application.AppWebhookView, error) {
	service.record(credentials, organizationID)
	service.appID = appID
	return testWebhookView, service.err
}

func (service *fakeService) PutAppWebhook(_ context.Context, credentials authclient.Credentials, organizationID, appID, secret string) (application.AppWebhookView, error) {
	service.record(credentials, organizationID)
	service.appID, service.secret = appID, secret
	return testWebhookView, service.err
}

func (service *fakeService) DeleteAppWebhook(_ context.Context, credentials authclient.Credentials, organizationID, appID string) error {
	service.record(credentials, organizationID)
	service.appID = appID
	return service.err
}

func serve(service *fakeService, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	return serveConfig(service, Config{AllowedOrigins: []string{"https://app.example.test"}}, method, path, body, headers...)
}

func serveConfig(service *fakeService, config Config, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer token-1")
	for index := 0; index+1 < len(headers); index += 2 {
		if headers[index] == "Authorization" && headers[index+1] == "" {
			request.Header.Del("Authorization")
			continue
		}
		request.Header.Set(headers[index], headers[index+1])
	}
	recorder := httptest.NewRecorder()
	New(service, config, nil).ServeHTTP(recorder, request)
	return recorder
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("response %d is not JSON: %s", recorder.Code, recorder.Body)
	}
	return body
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	body := decodeBody(t, recorder)
	failure, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error body = %s", recorder.Body)
	}
	return fmt.Sprint(failure["code"])
}

func TestPatternsNameEveryRoute(t *testing.T) {
	server := New(&fakeService{}, Config{MCP: http.NotFoundHandler()}, nil)
	patterns := server.Patterns()
	if len(patterns) < 13 {
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

func TestHealthzAndHeaders(t *testing.T) {
	recorder := serve(&fakeService{}, http.MethodGet, "/healthz", "")
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("GET /healthz = %d %v", recorder.Code, recorder.Header())
	}
	if recorder.Header().Get("Cache-Control") != "" {
		t.Fatalf("GET /healthz Cache-Control = %q, want empty (only /v1/ routes set it)", recorder.Header().Get("Cache-Control"))
	}

	recorder = serve(&fakeService{}, http.MethodGet, "/v1/organizations/org_1/subscriptions", "")
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("GET subscriptions headers = %d %v", recorder.Code, recorder.Header())
	}
}

func TestReceive(t *testing.T) {
	service := &fakeService{reply: providers.Reply{ContentType: "text/plain", Body: []byte("ok")}}
	request := httptest.NewRequest(http.MethodPost, "/v1/webhooks/github/route-1?a=b", strings.NewReader(`{"hello":"world"}`))
	request.Header.Set("X-Hub-Signature-256", "sha256=abc")
	recorder := httptest.NewRecorder()
	New(service, Config{}, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/plain" || recorder.Body.String() != "ok" {
		t.Fatalf("POST webhook = %d %v %s", recorder.Code, recorder.Header(), recorder.Body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("POST webhook headers = %v", recorder.Header())
	}
	if service.receiveProvider != "github" || service.receiveRoute != "route-1" || service.delivery.Header.Get("X-Hub-Signature-256") != "sha256=abc" ||
		service.delivery.Query.Get("a") != "b" || string(service.delivery.Body) != `{"hello":"world"}` {
		t.Fatalf("Receive() got provider = %q route = %q delivery = %+v", service.receiveProvider, service.receiveRoute, service.delivery)
	}
}

func TestReceiveDefaultStatusIs200(t *testing.T) {
	service := &fakeService{reply: providers.Reply{}}
	recorder := httptest.NewRecorder()
	New(service, Config{}, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/webhooks/github/route-1", strings.NewReader("{}")))
	if recorder.Code != http.StatusOK {
		t.Fatalf("Reply{} status = %d, want 200", recorder.Code)
	}
}

func TestReceiveErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{domain.ErrNotFound, http.StatusNotFound},
		{fmt.Errorf("bad signature: %w", domain.ErrUnauthorized), http.StatusUnauthorized},
		{domain.ErrInvalid, http.StatusBadRequest},
		{errors.New("boom"), http.StatusInternalServerError},
	} {
		service := &fakeService{err: test.err}
		recorder := httptest.NewRecorder()
		New(service, Config{}, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/webhooks/github/route-1", strings.NewReader("{}")))
		if recorder.Code != test.status || recorder.Body.Len() != 0 {
			t.Fatalf("%v = %d %q, want %d and an empty body", test.err, recorder.Code, recorder.Body, test.status)
		}
	}
}

func TestReceiveTooLargeNeverCallsTheService(t *testing.T) {
	service := &fakeService{}
	body := strings.Repeat("a", maxDeliveryBytes+1)
	recorder := httptest.NewRecorder()
	New(service, Config{}, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/webhooks/github/route-1", strings.NewReader(body)))
	if recorder.Code != http.StatusRequestEntityTooLarge || service.calls != 0 {
		t.Fatalf("oversized delivery = %d, service calls = %d, want 413 and 0 calls", recorder.Code, service.calls)
	}
}

func TestCredentialsAndOrigin(t *testing.T) {
	service := &fakeService{}
	if recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/subscriptions", "", "Authorization", ""); recorder.Code != http.StatusUnauthorized ||
		errorCode(t, recorder) != "unauthorized" || service.calls != 0 {
		t.Fatalf("no credentials = %d %s", recorder.Code, recorder.Body)
	}

	cookie := authclient.AccessCookieName + "=cookie-1"
	if recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/subscriptions", "", "Authorization", "", "Cookie", cookie); recorder.Code != http.StatusOK ||
		service.credentials.AccessCookie != "cookie-1" {
		t.Fatalf("cookie GET = %d %+v", recorder.Code, service.credentials)
	}

	for _, origin := range []string{"", "https://evil.example.test"} {
		recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/subscriptions",
			`{"connection_id":"con_1","target":{},"events":[]}`, "Authorization", "", "Cookie", cookie, "Origin", origin)
		if recorder.Code != http.StatusForbidden || errorCode(t, recorder) != "forbidden" {
			t.Fatalf("cookie POST from %q = %d %s", origin, recorder.Code, recorder.Body)
		}
	}
	if recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/subscriptions",
		`{"connection_id":"con_1","target":{},"events":[]}`, "Authorization", "", "Cookie", cookie, "Origin", "https://app.example.test"); recorder.Code != http.StatusCreated {
		t.Fatalf("cookie POST from the app = %d %s", recorder.Code, recorder.Body)
	}

	// A bearer-authenticated POST needs no Origin.
	if recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/subscriptions",
		`{"connection_id":"con_1","target":{},"events":[]}`); recorder.Code != http.StatusCreated || service.credentials.Authorization != "Bearer token-1" {
		t.Fatalf("bearer POST = %d %+v", recorder.Code, service.credentials)
	}
}

func TestListSubscriptions(t *testing.T) {
	service := &fakeService{}
	recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/subscriptions?connection_id=con_9", "")
	body := decodeBody(t, recorder)
	subscriptions, _ := body["subscriptions"].([]any)
	if recorder.Code != http.StatusOK || len(subscriptions) != 1 || service.connectionID != "con_9" || service.organization != "org_1" {
		t.Fatalf("GET subscriptions = %d %s", recorder.Code, recorder.Body)
	}
}

func TestCreateSubscription(t *testing.T) {
	service := &fakeService{}
	recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/subscriptions",
		`{"connection_id":"con_1","target":{"repo":"acme/widgets"},"events":["issues.opened"]}`)
	body := decodeBody(t, recorder)
	wantTarget := map[string]string{"repo": "acme/widgets"}
	wantEvents := []string{"issues.opened"}
	if recorder.Code != http.StatusCreated || body["id"] != "sub_1" || service.organization != "org_1" || service.createInput.ConnectionID != "con_1" ||
		!maps.Equal(service.createInput.Target, wantTarget) || !slices.Equal(service.createInput.Events, wantEvents) {
		t.Fatalf("POST subscriptions = %d %s %+v", recorder.Code, recorder.Body, service.createInput)
	}

	unknownField := &fakeService{}
	if recorder := serve(unknownField, http.MethodPost, "/v1/organizations/org_1/subscriptions", `{"connection_id":"con_1","bogus":1}`); recorder.Code != http.StatusBadRequest ||
		errorCode(t, recorder) != "invalid_request" || unknownField.calls != 0 {
		t.Fatalf("POST subscriptions with an unknown field = %d %s, calls = %d", recorder.Code, recorder.Body, unknownField.calls)
	}
}

func TestGetAndStopSubscription(t *testing.T) {
	service := &fakeService{}
	if recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/subscriptions/sub_1", ""); recorder.Code != http.StatusOK ||
		service.subscriptionID != "sub_1" || service.organization != "org_1" {
		t.Fatalf("GET subscription = %d %s", recorder.Code, recorder.Body)
	}
	if recorder := serve(service, http.MethodDelete, "/v1/organizations/org_1/subscriptions/sub_1", ""); recorder.Code != http.StatusNoContent || service.subscriptionID != "sub_1" {
		t.Fatalf("DELETE subscription = %d", recorder.Code)
	}
}

func TestAppWebhook(t *testing.T) {
	service := &fakeService{}
	if recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/apps/app_1/webhook", ""); recorder.Code != http.StatusOK || service.appID != "app_1" {
		t.Fatalf("GET app webhook = %d %s", recorder.Code, recorder.Body)
	}

	recorder := serve(service, http.MethodPut, "/v1/organizations/org_1/apps/app_1/webhook", `{"secret":"a-very-long-signing-secret-value"}`)
	body := decodeBody(t, recorder)
	if recorder.Code != http.StatusOK || service.appID != "app_1" || service.secret != "a-very-long-signing-secret-value" ||
		body["url"] != testWebhookView.URL {
		t.Fatalf("PUT app webhook = %d %s", recorder.Code, recorder.Body)
	}

	if recorder := serve(service, http.MethodDelete, "/v1/organizations/org_1/apps/app_1/webhook", ""); recorder.Code != http.StatusNoContent || service.appID != "app_1" {
		t.Fatalf("DELETE app webhook = %d", recorder.Code)
	}
}

func TestErrorCodes(t *testing.T) {
	for _, test := range []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{fmt.Errorf("target field bogus is not a lowercase name: %w", domain.ErrInvalid), http.StatusBadRequest, "invalid_request", "target field bogus is not a lowercase name"},
		{domain.ErrUnauthorized, http.StatusUnauthorized, "unauthorized", ""},
		{domain.ErrForbidden, http.StatusForbidden, "forbidden", ""},
		{domain.ErrNotFound, http.StatusNotFound, "not_found", ""},
		{domain.ErrNeedsReconnect, http.StatusConflict, "needs_reconnect", ""},
		{domain.ErrWebhookSecretRequired, http.StatusConflict, "webhook_secret_required", ""},
		{domain.ErrConflict, http.StatusConflict, "conflict", ""},
		{domain.ErrCredentialRejected, http.StatusUnprocessableEntity, "credential_rejected", ""},
		{domain.ErrProviderUnavailable, http.StatusServiceUnavailable, "provider_unavailable", ""},
		{errors.New("pq: connection to db at secret-host failed"), http.StatusInternalServerError, "internal", "internal server error"},
	} {
		recorder := serve(&fakeService{err: test.err}, http.MethodGet, "/v1/organizations/org_1/subscriptions", "")
		body := decodeBody(t, recorder)
		failure, _ := body["error"].(map[string]any)
		if recorder.Code != test.status || failure["code"] != test.code || (test.message != "" && failure["message"] != test.message) {
			t.Fatalf("%v = %d %s, want status %d code %q message %q", test.err, recorder.Code, recorder.Body, test.status, test.code, test.message)
		}
	}
}

func TestRunAction(t *testing.T) {
	service := &fakeService{}
	recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/actions",
		`{"connection_id":"con_1","action":"create_issue","input":{"repository":"acme/api","title":"Broken build"},"idempotency_key":"chat-turn-7788"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("POST actions = %d %s, want 200", recorder.Code, recorder.Body)
	}
	var answered domain.Action
	if err := json.Unmarshal(recorder.Body.Bytes(), &answered); err != nil || answered.ID != "act_1" || answered.Status != domain.ActionCompleted {
		t.Fatalf("POST actions = %s, %v", recorder.Body, err)
	}
	if answered.Resource == nil || answered.Resource.ID != "12" {
		t.Fatalf("POST actions answered resource = %+v", answered.Resource)
	}
	input := service.actionInput
	if input.ConnectionID != "con_1" || input.Action != "create_issue" || input.IdempotencyKey != "chat-turn-7788" {
		t.Fatalf("RunAction() input = %+v", input)
	}
	var sent map[string]string
	if err := json.Unmarshal(input.Input, &sent); err != nil || sent["repository"] != "acme/api" || sent["title"] != "Broken build" {
		t.Fatalf("RunAction() input JSON = %s, %v", input.Input, err)
	}
	if service.credentials.Authorization != "Bearer token-1" || service.organization != "org_1" {
		t.Fatalf("RunAction() credentials = %+v, organization = %q", service.credentials, service.organization)
	}
}

func TestRunActionRefusals(t *testing.T) {
	t.Run("without a member", func(t *testing.T) {
		service := &fakeService{}
		recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/actions", `{"connection_id":"con_1","action":"create_issue"}`, "Authorization", "")
		if recorder.Code != http.StatusUnauthorized || errorCode(t, recorder) != "unauthorized" || service.calls != 0 {
			t.Fatalf("POST actions without credentials = %d %s, calls = %d", recorder.Code, recorder.Body, service.calls)
		}
	})
	t.Run("with a field the API does not take", func(t *testing.T) {
		service := &fakeService{}
		recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/actions", `{"connection_id":"con_1","action":"create_issue","retries":3}`)
		if recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_request" || service.calls != 0 {
			t.Fatalf("POST actions with an unknown field = %d %s, calls = %d", recorder.Code, recorder.Body, service.calls)
		}
	})
	t.Run("the service refuses", func(t *testing.T) {
		service := &fakeService{err: domain.ErrNeedsReconnect}
		recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/actions", `{"connection_id":"con_1","action":"create_issue"}`)
		if recorder.Code != http.StatusConflict || errorCode(t, recorder) != "needs_reconnect" {
			t.Fatalf("POST actions when the connection needs reconnecting = %d %s", recorder.Code, recorder.Body)
		}
	})
}

func TestCatalogTakesNoCredentials(t *testing.T) {
	service := &fakeService{}
	recorder := serve(service, http.MethodGet, "/v1/catalog", "", "Authorization", "")

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET catalog = %d %s", recorder.Code, recorder.Body)
	}
	var answered struct {
		Providers []providers.Description `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answered); err != nil || len(answered.Providers) != 1 {
		t.Fatalf("GET catalog = %s, %v", recorder.Body, err)
	}
	described := answered.Providers[0]
	if described.Name != "github" || len(described.Target) != 1 || len(described.Actions) != 1 || described.Skill == "" {
		t.Fatalf("GET catalog described %+v", described)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("GET catalog headers = %v", recorder.Header())
	}
}
