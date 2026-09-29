package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

// fakeService answers what a test sets and records what it was asked, so
// these tests are about the HTTP surface alone.
type fakeService struct {
	err error

	credentials  authclient.Credentials
	organization string
	connectInput application.ConnectInput
	runInput     application.RunToolInput
	proxyInput   application.ProxyInput
	triggerInput application.TriggerInput
	removed      string
	listedScope  bool
	listedKit    string
	offer        application.OfferPolicy

	connection domain.Connection
	targets    []application.Target
	redirect   string
	completion application.Completion
	trigger    domain.Trigger
	run        application.ToolRun

	webhookHeader http.Header
	webhookBody   []byte
}

func (fake *fakeService) Catalog(context.Context, application.CatalogQuery) ([]application.ToolkitView, string, error) {
	if fake.err != nil {
		return nil, "", fake.err
	}
	return []application.ToolkitView{{Toolkit: "github", Name: "GitHub", Connectable: true}}, "cursor_2", nil
}

func (fake *fakeService) CatalogToolkit(_ context.Context, toolkit string) (application.ToolkitView, error) {
	if fake.err != nil {
		return application.ToolkitView{}, fake.err
	}
	return application.ToolkitView{Toolkit: toolkit}, nil
}

func (fake *fakeService) CatalogTools(context.Context, string, string, string, int) ([]application.ToolView, string, error) {
	if fake.err != nil {
		return nil, "", fake.err
	}
	return []application.ToolView{{Tool: "GITHUB_LIST_ISSUES", ReadOnly: true}}, "", nil
}

func (fake *fakeService) CatalogTriggers(context.Context, string, string, int) ([]application.TriggerTypeView, string, error) {
	if fake.err != nil {
		return nil, "", fake.err
	}
	return []application.TriggerTypeView{{Trigger: "GITHUB_COMMIT_EVENT"}}, "", nil
}

func (fake *fakeService) OfferedTools(_ context.Context, credentials authclient.Credentials, organizationID string) (application.OfferPolicy, error) {
	fake.credentials, fake.organization = credentials, organizationID
	return fake.offer, fake.err
}

func (fake *fakeService) Connect(_ context.Context, credentials authclient.Credentials, organizationID string, input application.ConnectInput) (domain.Connection, string, error) {
	fake.credentials, fake.organization, fake.connectInput = credentials, organizationID, input
	if fake.err != nil {
		return domain.Connection{}, "", fake.err
	}
	return fake.connection, fake.redirect, nil
}

func (fake *fakeService) CompleteConnection(_ context.Context, state, status, accountID string) (application.Completion, error) {
	fake.connectInput = application.ConnectInput{Toolkit: state, ReturnTo: status, Alias: accountID}
	if fake.err != nil {
		return application.Completion{}, fake.err
	}
	return fake.completion, nil
}

func (fake *fakeService) Connections(_ context.Context, credentials authclient.Credentials, organizationID, toolkit string, organization bool) ([]domain.Connection, error) {
	fake.credentials, fake.organization, fake.listedKit, fake.listedScope = credentials, organizationID, toolkit, organization
	if fake.err != nil {
		return nil, fake.err
	}
	if fake.connection.ID == "" {
		return nil, nil
	}
	return []domain.Connection{fake.connection}, nil
}

func (fake *fakeService) Connection(_ context.Context, credentials authclient.Credentials, organizationID, id string) (domain.Connection, error) {
	fake.credentials, fake.organization = credentials, organizationID
	if fake.err != nil {
		return domain.Connection{}, fake.err
	}
	return fake.connection, nil
}

func (fake *fakeService) RemoveConnection(_ context.Context, credentials authclient.Credentials, organizationID, id string) error {
	fake.credentials, fake.organization, fake.removed = credentials, organizationID, id
	return fake.err
}

func (fake *fakeService) RunTool(_ context.Context, credentials authclient.Credentials, organizationID string, input application.RunToolInput) (application.ToolRun, error) {
	fake.credentials, fake.organization, fake.runInput = credentials, organizationID, input
	if fake.err != nil {
		return application.ToolRun{}, fake.err
	}
	return fake.run, nil
}

func (fake *fakeService) Proxy(_ context.Context, credentials authclient.Credentials, organizationID string, input application.ProxyInput) (application.ProxyResult, error) {
	fake.credentials, fake.organization, fake.proxyInput = credentials, organizationID, input
	if fake.err != nil {
		return application.ProxyResult{}, fake.err
	}
	return application.ProxyResult{Status: 200, Data: json.RawMessage(`{"items":[1]}`)}, nil
}

func (fake *fakeService) CreateTrigger(_ context.Context, credentials authclient.Credentials, organizationID string, input application.TriggerInput) (domain.Trigger, error) {
	fake.credentials, fake.organization, fake.triggerInput = credentials, organizationID, input
	if fake.err != nil {
		return domain.Trigger{}, fake.err
	}
	return fake.trigger, nil
}

func (fake *fakeService) ConnectionTargets(_ context.Context, credentials authclient.Credentials, organizationID, connectionID string) ([]application.Target, error) {
	fake.credentials, fake.organization = credentials, organizationID
	if fake.err != nil {
		return nil, fake.err
	}
	return fake.targets, nil
}

func (fake *fakeService) Triggers(_ context.Context, credentials authclient.Credentials, organizationID, connectionID string) ([]domain.Trigger, error) {
	fake.credentials, fake.organization = credentials, organizationID
	if fake.err != nil {
		return nil, fake.err
	}
	if fake.trigger.ID == "" {
		return nil, nil
	}
	return []domain.Trigger{fake.trigger}, nil
}

func (fake *fakeService) RemoveTrigger(_ context.Context, credentials authclient.Credentials, organizationID, id string) error {
	fake.credentials, fake.organization, fake.removed = credentials, organizationID, id
	return fake.err
}

func (fake *fakeService) ReceiveWebhook(_ context.Context, header http.Header, body []byte) error {
	fake.webhookHeader, fake.webhookBody = header, body
	return fake.err
}

func newServer(t *testing.T, fake *fakeService) *Server {
	t.Helper()
	return New(fake, Config{AllowedOrigins: []string{"https://app.oryxa.test"}}, nil)
}

// call sends a request and answers the recorded response.
func call(server *Server, method, target string, body string, headers ...string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

// jsonNUL is how a JSON document spells a NUL inside a string.
const jsonNUL = `\u` + `0000`

func TestConnectionsRefuseANulInTheToolkit(t *testing.T) {
	recorder := call(newServer(t, &fakeService{}), http.MethodPost, "/v1/organizations/org_1/apps/connections",
		`{"toolkit":"git`+jsonNUL+`hub"}`, "Authorization", "Bearer token")
	if recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_request" {
		t.Fatalf("status = %d code = %q, want 400 invalid_request: %s", recorder.Code, errorCode(t, recorder), recorder.Body)
	}
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", recorder.Body.String(), err)
	}
	return body
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	body := decodeBody(t, recorder)
	failure, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body has no error: %s", recorder.Body.String())
	}
	return failure["code"].(string)
}

func TestRoutesNeedACaller(t *testing.T) {
	t.Parallel()
	server := newServer(t, &fakeService{})
	for _, route := range []struct{ method, target string }{
		{http.MethodGet, "/v1/organizations/org_1/apps/offer"},
		{http.MethodGet, "/v1/organizations/org_1/apps/connections"},
		{http.MethodPost, "/v1/organizations/org_1/apps/connections"},
		{http.MethodGet, "/v1/organizations/org_1/apps/connections/apc_1"},
		{http.MethodDelete, "/v1/organizations/org_1/apps/connections/apc_1"},
		{http.MethodPost, "/v1/organizations/org_1/apps/tools/GITHUB_LIST_ISSUES/run"},
		{http.MethodPost, "/v1/organizations/org_1/apps/read"},
		{http.MethodGet, "/v1/organizations/org_1/apps/triggers"},
		{http.MethodPost, "/v1/organizations/org_1/apps/triggers"},
		{http.MethodDelete, "/v1/organizations/org_1/apps/triggers/apt_1"},
	} {
		recorder := call(server, route.method, route.target, `{}`)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", route.method, route.target, recorder.Code)
		}
	}
}

func TestOfferedToolsReturnsOrgScopedPolicy(t *testing.T) {
	offer := []string{"github"}
	fake := &fakeService{offer: application.OfferPolicy{Offer: &offer, AutoSubscribeDefault: false}}
	recorder := call(newServer(t, fake), http.MethodGet, "/v1/organizations/org_1/apps/offer", "", "Authorization", "Bearer token")
	if recorder.Code != http.StatusOK || fake.organization != "org_1" || fake.credentials.Authorization != "Bearer token" {
		t.Fatalf("offered tools: status=%d org=%q credentials=%+v", recorder.Code, fake.organization, fake.credentials)
	}
	var body application.OfferPolicy
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Offer == nil || len(*body.Offer) != 1 || (*body.Offer)[0] != "github" || body.AutoSubscribeDefault {
		t.Fatalf("offer response = %+v, %v", body, err)
	}
}

func TestCatalogNeedsNoCaller(t *testing.T) {
	t.Parallel()
	server := newServer(t, &fakeService{})

	recorder := call(server, http.MethodGet, "/v1/apps/catalog?search=git", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	body := decodeBody(t, recorder)
	if body["next_cursor"] != "cursor_2" {
		t.Fatalf("next cursor = %v", body["next_cursor"])
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", recorder.Header())
	}
	for _, target := range []string{
		"/v1/apps/catalog/github", "/v1/apps/catalog/github/tools", "/v1/apps/catalog/github/triggers",
	} {
		if recorder := call(server, http.MethodGet, target, ""); recorder.Code != http.StatusOK {
			t.Errorf("%s = %d: %s", target, recorder.Code, recorder.Body)
		}
	}
}

func TestCookieAuthenticationNeedsAnAllowedOrigin(t *testing.T) {
	t.Parallel()
	fake := &fakeService{connection: domain.Connection{ID: "apc_1"}, redirect: "https://connect.composio.dev/link/lt_1"}
	server := newServer(t, fake)
	cookie := authclient.AccessCookieName + "=token-value"

	// A GET needs no origin: it changes nothing.
	if recorder := call(server, http.MethodGet, "/v1/organizations/org_1/apps/connections", "", "Cookie", cookie); recorder.Code != http.StatusOK {
		t.Fatalf("GET with a cookie = %d: %s", recorder.Code, recorder.Body)
	}
	// A mutation from an unknown page is refused, so another site cannot
	// connect apps in a logged-in member's name.
	recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/connections", `{"toolkit":"github"}`,
		"Cookie", cookie, "Origin", "https://evil.example")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation = %d, want 403", recorder.Code)
	}
	recorder = call(server, http.MethodPost, "/v1/organizations/org_1/apps/connections", `{"toolkit":"github"}`, "Cookie", cookie)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("mutation without an origin = %d, want 403", recorder.Code)
	}
	recorder = call(server, http.MethodPost, "/v1/organizations/org_1/apps/connections", `{"toolkit":"github"}`,
		"Cookie", cookie, "Origin", "https://app.oryxa.test")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("same-origin mutation = %d: %s", recorder.Code, recorder.Body)
	}
	// A bearer token needs no origin at all.
	recorder = call(server, http.MethodPost, "/v1/organizations/org_1/apps/connections", `{"toolkit":"github"}`,
		"Authorization", "Bearer member")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("bearer mutation = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.credentials.Authorization != "Bearer member" {
		t.Fatalf("credentials = %+v", fake.credentials)
	}
}

func TestConnectPassesTheMembersAskOnAndAnswersTheLink(t *testing.T) {
	t.Parallel()
	connected := testClock
	fake := &fakeService{
		connection: domain.Connection{
			ID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github", Label: "work",
			ComposioUserID: "oryxa.org_1.user-1", ComposioAccountID: "ca_1", Status: domain.ConnectionActive,
			CreatedAt: testClock, UpdatedAt: testClock, ConnectedAt: &connected, ReturnTo: "https://app.oryxa.test/apps",
			AuthConfigID: "ac_1",
		},
		redirect: "https://connect.composio.dev/link/lt_1",
	}
	server := newServer(t, fake)

	recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/connections",
		`{"toolkit":"github","return_to":"https://app.oryxa.test/apps","alias":"work"}`, "Authorization", "Bearer member")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.connectInput.Toolkit != "github" || fake.connectInput.ReturnTo != "https://app.oryxa.test/apps" || fake.connectInput.Alias != "work" {
		t.Fatalf("input = %+v", fake.connectInput)
	}
	if fake.organization != "org_1" {
		t.Fatalf("organization = %q", fake.organization)
	}
	body := decodeBody(t, recorder)
	if body["redirect_url"] != "https://connect.composio.dev/link/lt_1" {
		t.Fatalf("redirect = %v", body["redirect_url"])
	}
	// The view says what a member needs and nothing about the round trip
	// or the project's auth config.
	view := body["connection"].(map[string]any)
	for _, key := range []string{"id", "toolkit", "status", "member_id", "created_at"} {
		if _, ok := view[key]; !ok {
			t.Errorf("view has no %s: %v", key, view)
		}
	}
	for _, key := range []string{"return_to", "state", "state_hash", "auth_config_id", "composio_user_id"} {
		if _, ok := view[key]; ok {
			t.Errorf("view carries %s: %v", key, view)
		}
	}
}

func TestConnectRefusesABodyItDoesNotUnderstand(t *testing.T) {
	t.Parallel()
	server := newServer(t, &fakeService{})
	for name, body := range map[string]string{
		"an unknown field": `{"toolkit":"github","scopes":["repo"]}`,
		"not an object":    `["github"]`,
		"two objects":      `{"toolkit":"github"}{"toolkit":"slack"}`,
		"nothing at all":   ``,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/connections", body, "Authorization", "Bearer member")
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body)
			}
			if code := errorCode(t, recorder); code != "invalid_request" {
				t.Fatalf("code = %q", code)
			}
		})
	}
}

func TestConnectCallbackSendsTheBrowserBack(t *testing.T) {
	t.Parallel()
	fake := &fakeService{completion: application.Completion{
		Connection: domain.Connection{ID: "apc_1", Status: domain.ConnectionActive},
		ReturnTo:   "https://app.oryxa.test/apps?tab=connected",
	}}
	server := newServer(t, fake)

	recorder := call(server, http.MethodGet, "/v1/apps/connect/callback?state=opaque&status=success&connected_account_id=ca_1", "")
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	// The callback's own query is passed on as a hint, not trusted.
	if fake.connectInput.Toolkit != "opaque" || fake.connectInput.ReturnTo != "success" || fake.connectInput.Alias != "ca_1" {
		t.Fatalf("callback arguments = %+v", fake.connectInput)
	}
	target, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if target.Host != "app.oryxa.test" || target.Path != "/apps" {
		t.Fatalf("location = %q", target)
	}
	query := target.Query()
	if query.Get("app_connection") != "apc_1" || query.Get("status") != "active" || query.Get("tab") != "connected" {
		t.Fatalf("query = %v", query)
	}
}

func TestConnectCallbackAnswersJSONWithoutAReturnPage(t *testing.T) {
	t.Parallel()
	fake := &fakeService{completion: application.Completion{
		Connection: domain.Connection{ID: "apc_1", Status: domain.ConnectionPending},
	}}
	server := newServer(t, fake)

	recorder := call(server, http.MethodGet, "/v1/apps/connect/callback?state=opaque", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	view := decodeBody(t, recorder)["connection"].(map[string]any)
	if view["status"] != "pending" {
		t.Fatalf("view = %v", view)
	}
}

func TestListingsAreEmptyArraysNotNull(t *testing.T) {
	t.Parallel()
	server := newServer(t, &fakeService{})

	for _, target := range []string{
		"/v1/organizations/org_1/apps/connections", "/v1/organizations/org_1/apps/triggers",
	} {
		recorder := call(server, http.MethodGet, target, "", "Authorization", "Bearer member")
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", target, recorder.Code, recorder.Body)
		}
		if strings.Contains(recorder.Body.String(), "null") {
			t.Fatalf("%s answered %s", target, recorder.Body)
		}
	}
}

func TestListConnectionsPassesTheScopeOn(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	server := newServer(t, fake)

	call(server, http.MethodGet, "/v1/organizations/org_1/apps/connections?scope=organization&toolkit=github", "", "Authorization", "Bearer admin")
	if !fake.listedScope || fake.listedKit != "github" {
		t.Fatalf("scope = %v, toolkit = %q", fake.listedScope, fake.listedKit)
	}
	call(server, http.MethodGet, "/v1/organizations/org_1/apps/connections?scope=mine", "", "Authorization", "Bearer admin")
	if fake.listedScope {
		t.Fatal("scope=mine widened the listing")
	}
}

func TestRunToolAnswersWhatTheRunDid(t *testing.T) {
	t.Parallel()
	fake := &fakeService{run: application.ToolRun{
		Action: domain.Action{ID: "apa_1", Tool: "GITHUB_CREATE_AN_ISSUE", ConnectionID: "apc_1", Status: domain.ActionSucceeded},
		Data:   json.RawMessage(`{"number":7}`),
	}}
	server := newServer(t, fake)

	recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/tools/GITHUB_CREATE_AN_ISSUE/run",
		`{"connection_id":"apc_1","arguments":{"title":"bug"},"idempotency_key":"issue-2026-09-17"}`,
		"Authorization", "Bearer member")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.runInput.Tool != "GITHUB_CREATE_AN_ISSUE" || fake.runInput.ConnectionID != "apc_1" ||
		fake.runInput.IdempotencyKey != "issue-2026-09-17" || string(fake.runInput.Arguments) != `{"title":"bug"}` {
		t.Fatalf("input = %+v", fake.runInput)
	}
	// A member's own call is not restricted to reads; only a caller that
	// says so is.
	if fake.runInput.OnlyReads {
		t.Fatal("the API restricted a member's own run to reads")
	}
	body := decodeBody(t, recorder)
	if body["successful"] != true || body["action_id"] != "apa_1" || body["replayed"] != false {
		t.Fatalf("body = %v", body)
	}
	data := body["data"].(map[string]any)
	if data["number"].(float64) != 7 {
		t.Fatalf("data = %v", data)
	}
}

func TestRunToolAnswersAFailedRunAsAnAnswer(t *testing.T) {
	t.Parallel()
	fake := &fakeService{run: application.ToolRun{
		Action: domain.Action{ID: "apa_1", Tool: "GITHUB_CREATE_AN_ISSUE", Status: domain.ActionFailed, Error: "Validation failed"},
	}}
	server := newServer(t, fake)

	recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/tools/GITHUB_CREATE_AN_ISSUE/run",
		`{"idempotency_key":"issue-2026-09-17"}`, "Authorization", "Bearer member")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	body := decodeBody(t, recorder)
	if body["successful"] != false || body["error"] != "Validation failed" {
		t.Fatalf("body = %v", body)
	}
}

func TestReadRouteProxiesTheAppsOwnAPI(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	server := newServer(t, fake)

	recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/read",
		`{"connection_id":"apc_1","toolkit":"notion","endpoint":"/v1/search","method":"POST","body":{"page_size":100},"headers":{"Notion-Version":"2022-06-28"}}`,
		"Authorization", "Bearer member")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.proxyInput.ConnectionID != "apc_1" || fake.proxyInput.Toolkit != "notion" ||
		fake.proxyInput.Endpoint != "/v1/search" || fake.proxyInput.Method != "POST" {
		t.Fatalf("input = %+v", fake.proxyInput)
	}
	if string(fake.proxyInput.Body) != `{"page_size":100}` || fake.proxyInput.Headers["Notion-Version"] != "2022-06-28" {
		t.Fatalf("input = %+v", fake.proxyInput)
	}
	body := decodeBody(t, recorder)
	if body["status"].(float64) != 200 {
		t.Fatalf("body = %v", body)
	}

	// It needs a caller like every other organization route.
	if recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/read", `{"endpoint":"/v1/search"}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("without a caller = %d", recorder.Code)
	}
	// A write is refused with the code that says why.
	refusing := newServer(t, &fakeService{err: fmt.Errorf("POST /v1/pages is not a read: %w", domain.ErrNotReadOnly)})
	recorder = call(refusing, http.MethodPost, "/v1/organizations/org_1/apps/read",
		`{"toolkit":"notion","endpoint":"/v1/pages","method":"POST"}`, "Authorization", "Bearer member")
	if recorder.Code != http.StatusForbidden || errorCode(t, recorder) != "not_read_only" {
		t.Fatalf("a write = %d %s", recorder.Code, recorder.Body)
	}
}

func TestTriggerRoutes(t *testing.T) {
	t.Parallel()
	fake := &fakeService{trigger: domain.Trigger{
		ID: "apt_1", ConnectionID: "apc_1", Toolkit: "github", Slug: "GITHUB_COMMIT_EVENT",
		Config: json.RawMessage(`{"repo":"oryxa"}`), Status: domain.TriggerActive, MemberID: "user-1",
		CreatedAt: testClock, UpdatedAt: testClock,
	}}
	server := newServer(t, fake)

	recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/triggers",
		`{"connection_id":"apc_1","trigger":"GITHUB_COMMIT_EVENT","config":{"repo":"oryxa"}}`, "Authorization", "Bearer member")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.triggerInput.ConnectionID != "apc_1" || fake.triggerInput.Trigger != "GITHUB_COMMIT_EVENT" ||
		string(fake.triggerInput.Config) != `{"repo":"oryxa"}` {
		t.Fatalf("input = %+v", fake.triggerInput)
	}
	view := decodeBody(t, recorder)
	if view["trigger"] != "GITHUB_COMMIT_EVENT" || view["status"] != "active" {
		t.Fatalf("view = %v", view)
	}

	if recorder := call(server, http.MethodDelete, "/v1/organizations/org_1/apps/triggers/apt_1", "", "Authorization", "Bearer member"); recorder.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.removed != "apt_1" {
		t.Fatalf("removed = %q", fake.removed)
	}
}

func TestRemoveConnectionAnswersNoContent(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	server := newServer(t, fake)

	recorder := call(server, http.MethodDelete, "/v1/organizations/org_1/apps/connections/apc_1", "", "Authorization", "Bearer member")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if fake.removed != "apc_1" {
		t.Fatalf("removed = %q", fake.removed)
	}
}

func TestWebhookRouteAnswersComposio(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	server := newServer(t, fake)
	body := `{"id":"wh_1","type":"composio.trigger.message"}`

	recorder := call(server, http.MethodPost, "/v1/apps/webhooks/composio", body,
		"webhook-id", "wh_1", "webhook-signature", "v1,signature")
	// 200 exactly: Composio treats a 204 as a failed delivery and sends it
	// again.
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	// The body reaches the application byte for byte: its signature is over
	// exactly these bytes.
	if string(fake.webhookBody) != body {
		t.Fatalf("body = %q", fake.webhookBody)
	}
	if fake.webhookHeader.Get("webhook-id") != "wh_1" {
		t.Fatalf("headers = %v", fake.webhookHeader)
	}
	// No caller is needed: the signature is the credential.
	if fake.credentials.Present() {
		t.Fatal("the webhook route read credentials")
	}
}

func TestWebhookRouteRefusesWhatComposioDidNotSign(t *testing.T) {
	t.Parallel()
	server := newServer(t, &fakeService{err: fmt.Errorf("checking: %w", application.ErrUnverified)})

	recorder := call(server, http.MethodPost, "/v1/apps/webhooks/composio", `{"id":"wh_1"}`)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if code := errorCode(t, recorder); code != "unauthorized" {
		t.Fatalf("code = %q", code)
	}
}

func TestWebhookRouteRefusesAnOversizedDelivery(t *testing.T) {
	t.Parallel()
	fake := &fakeService{}
	server := newServer(t, fake)

	recorder := call(server, http.MethodPost, "/v1/apps/webhooks/composio",
		`{"data":"`+strings.Repeat("x", maxWebhookBytes)+`"}`)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
	if fake.webhookBody != nil {
		t.Fatal("an oversized delivery reached the application")
	}
}

func TestErrorsBecomeTheDocumentedCodes(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"invalid":          {err: domain.ErrInvalid, status: http.StatusBadRequest, code: "invalid_request"},
		"unauthorized":     {err: domain.ErrUnauthorized, status: http.StatusUnauthorized, code: "unauthorized"},
		"forbidden":        {err: domain.ErrForbidden, status: http.StatusForbidden, code: "forbidden"},
		"not read-only":    {err: domain.ErrNotReadOnly, status: http.StatusForbidden, code: "not_read_only"},
		"not found":        {err: domain.ErrNotFound, status: http.StatusNotFound, code: "not_found"},
		"not connected":    {err: domain.ErrNotConnected, status: http.StatusConflict, code: "not_connected"},
		"choose one":       {err: domain.ErrChooseConnection, status: http.StatusConflict, code: "choose_connection"},
		"needs reconnect":  {err: domain.ErrNeedsReconnect, status: http.StatusConflict, code: "needs_reconnect"},
		"not connectable":  {err: domain.ErrNotConnectable, status: http.StatusUnprocessableEntity, code: "not_connectable"},
		"conflict":         {err: domain.ErrConflict, status: http.StatusConflict, code: "conflict"},
		"rate limited":     {err: domain.ErrRateLimited, status: http.StatusTooManyRequests, code: "rate_limited"},
		"unavailable":      {err: domain.ErrUnavailable, status: http.StatusServiceUnavailable, code: "unavailable"},
		"something broken": {err: errors.New("the database is on fire"), status: http.StatusInternalServerError, code: "internal"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := newServer(t, &fakeService{err: fmt.Errorf("run GITHUB_CREATE_AN_ISSUE: %w", testCase.err)})

			recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/tools/GITHUB_CREATE_AN_ISSUE/run",
				`{"idempotency_key":"issue-2026-09-17"}`, "Authorization", "Bearer member")
			if recorder.Code != testCase.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, testCase.status, recorder.Body)
			}
			if code := errorCode(t, recorder); code != testCase.code {
				t.Fatalf("code = %q, want %q", code, testCase.code)
			}
			// What went wrong inside apps stays inside apps.
			if testCase.status == http.StatusInternalServerError && strings.Contains(recorder.Body.String(), "on fire") {
				t.Fatalf("body leaks the reason: %s", recorder.Body)
			}
		})
	}
}

func TestMessageKeepsTheSentinelOutOfTheWording(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		err  error
		want string
	}{
		"wrapped onto the end": {
			err: fmt.Errorf("%s changes things, so it runs only once the member confirms: %w",
				"GITHUB_CREATE_AN_ISSUE", domain.ErrNotReadOnly),
			want: "GITHUB_CREATE_AN_ISSUE changes things, so it runs only once the member confirms",
		},
		// A validator joins the kind and the advice, so the wording arrives on
		// two lines with the sentinel on one of them.
		"joined on its own line": {
			err:  errors.Join(domain.ErrInvalid, errors.New("an idempotency key is 8 to 128 characters")),
			want: "an idempotency key is 8 to 128 characters",
		},
		"joined and then wrapped": {
			err: fmt.Errorf("run GITHUB_CREATE_AN_ISSUE: %w",
				errors.Join(domain.ErrInvalid, errors.New("a tool is an uppercase slug such as GITHUB_CREATE_AN_ISSUE"))),
			want: "run GITHUB_CREATE_AN_ISSUE: a tool is an uppercase slug such as GITHUB_CREATE_AN_ISSUE",
		},
		"nothing but the sentinel": {err: domain.ErrInvalid, want: domain.ErrInvalid.Error()},
	} {
		t.Run(name, func(t *testing.T) {
			message := Message(testCase.err)
			if message != testCase.want {
				t.Fatalf("message = %q, want %q", message, testCase.want)
			}
			// Whatever the shape, a model never reads a newline mid-sentence.
			if strings.Contains(message, "\n") {
				t.Fatalf("message carries a newline: %q", message)
			}
		})
	}
}

func TestMCPIsMountedOnlyWhenThereIsAHandler(t *testing.T) {
	t.Parallel()
	reached := make(chan string, 2)
	mcp := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		reached <- request.URL.Path
		writer.WriteHeader(http.StatusOK)
	})
	server := New(&fakeService{}, Config{MCP: mcp}, nil)

	for _, target := range []string{"/v1/mcp/apps", "/v1/organizations/org_1/apps/mcp"} {
		if recorder := call(server, http.MethodPost, target, `{}`); recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d", target, recorder.Code)
		}
		if path := <-reached; path != target {
			t.Fatalf("handler saw %q", path)
		}
	}
	patterns := strings.Join(server.Patterns(), " ")
	if !strings.Contains(patterns, "/v1/mcp/apps") {
		t.Fatalf("patterns = %v", server.Patterns())
	}

	// Without a handler the paths are simply not there, rather than
	// answering something an agent might believe.
	bare := newServer(t, &fakeService{})
	if recorder := call(bare, http.MethodPost, "/v1/mcp/apps", `{}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("unmounted MCP = %d", recorder.Code)
	}
	if strings.Contains(strings.Join(bare.Patterns(), " "), "mcp") {
		t.Fatalf("patterns = %v", bare.Patterns())
	}
}

func TestPatternsAreAllOfApps(t *testing.T) {
	t.Parallel()
	for _, pattern := range newServer(t, &fakeService{}).Patterns() {
		_, path, _ := strings.Cut(pattern, " ")
		if !strings.HasPrefix(path, "/v1/apps/") && !strings.HasPrefix(path, "/v1/organizations/{organizationID}/apps") {
			t.Errorf("pattern %q is not under apps' own paths", pattern)
		}
	}
}

func TestPolicyFailureCauseIsNotReturnedToClient(t *testing.T) {
	cause := fmt.Errorf("invalid stored policy with sensitive diagnostics: %w", domain.ErrInvalid)
	failure := fmt.Errorf("read workspace policy: %w: %w", domain.ErrUnavailable, cause)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org_1/apps/offer", nil)
	newServer(t, &fakeService{}).handleError(response, request, failure)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), cause.Error()) || !strings.Contains(response.Body.String(), `"code":"unavailable"`) {
		t.Fatalf("policy error response: %d %s", response.Code, response.Body)
	}
}
