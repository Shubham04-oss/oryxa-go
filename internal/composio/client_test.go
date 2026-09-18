package composio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// recorded is one request the fake Composio received.
type recorded struct {
	method string
	path   string
	query  url.Values
	apiKey string
	body   map[string]any
}

// fakeComposio answers each request with the response registered for its
// method and path, and records what it was sent.
func fakeComposio(t *testing.T, responses map[string]func(http.ResponseWriter)) (*Client, *[]recorded) {
	t.Helper()
	var requests []recorded
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		entry := recorded{
			method: request.Method, path: request.URL.Path, query: request.URL.Query(),
			apiKey: request.Header.Get("x-api-key"),
		}
		if payload, _ := io.ReadAll(request.Body); len(payload) > 0 {
			if err := json.Unmarshal(payload, &entry.body); err != nil {
				t.Errorf("%s %s sent a body that is not a JSON object: %s", request.Method, request.URL.Path, payload)
			}
		}
		requests = append(requests, entry)
		respond, ok := responses[request.Method+" "+request.URL.Path]
		if !ok {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		respond(writer)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{APIKey: "test-key", BaseURL: server.URL + "/api/v3.1"})
	if err != nil {
		t.Fatal(err)
	}
	return client, &requests
}

func jsonAnswer(status int, body string) func(http.ResponseWriter) {
	return func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}
}

func TestNewRefusesAMissingKeyAndDefaultsTheBaseURL(t *testing.T) {
	if _, err := New(Config{APIKey: "  "}); err == nil {
		t.Fatal("New() accepted an empty API key")
	}
	if _, err := New(Config{APIKey: "k", BaseURL: "backend.composio.dev"}); err == nil {
		t.Fatal("New() accepted a relative base URL")
	}
	client, err := New(Config{APIKey: "k"})
	if err != nil || client.baseURL != DefaultBaseURL {
		t.Fatalf("New() = %v, %v, want the default base URL", client, err)
	}
}

func TestErrorsCarryComposiosEnvelopeAndKind(t *testing.T) {
	client, _ := fakeComposio(t, map[string]func(http.ResponseWriter){
		"GET /api/v3.1/connected_accounts/ca_1": jsonAnswer(http.StatusNotFound,
			`{"error":{"message":"Connected account not found","status":404,"request_id":"req_1","slug":"ConnectedAccount_NotFound"}}`),
		"GET /api/v3.1/connected_accounts/ca_2": func(writer http.ResponseWriter) {
			writer.Header().Set("Retry-After", "7")
			jsonAnswer(http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`)(writer)
		},
		"GET /api/v3.1/connected_accounts/ca_3": jsonAnswer(http.StatusBadGateway, `upstream failure`),
	})
	_, err := client.ConnectedAccount(context.Background(), "ca_1")
	var failure *Error
	if !errors.Is(err, ErrNotFound) || !errors.As(err, &failure) ||
		failure.Message != "Connected account not found" || failure.RequestID != "req_1" || failure.Code != "ConnectedAccount_NotFound" {
		t.Fatalf("ConnectedAccount(missing) error = %#v", err)
	}
	_, err = client.ConnectedAccount(context.Background(), "ca_2")
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &failure) || failure.RetryAfter.Seconds() != 7 {
		t.Fatalf("ConnectedAccount(rate limited) error = %#v", err)
	}
	if _, err = client.ConnectedAccount(context.Background(), "ca_3"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ConnectedAccount(bad gateway) error = %v, want ErrUnavailable", err)
	}
}

func TestToolkitsListsTheCatalogWithItsCategories(t *testing.T) {
	client, requests := fakeComposio(t, map[string]func(http.ResponseWriter){
		"GET /api/v3.1/toolkits": jsonAnswer(http.StatusOK, `{
			"items": [{
				"slug": "github", "name": "GitHub", "type": "native",
				"auth_schemes": ["OAUTH2"], "composio_managed_auth_schemes": ["OAUTH2"], "is_local_toolkit": false,
				"deprecated": {"toolkitId": "x"},
				"meta": {"created_at": "", "updated_at": "", "description": "Code hosting", "logo": "https://logo",
					"app_url": null, "categories": [{"id": "developer-tools", "name": "Developer Tools"}],
					"triggers_count": 5, "tools_count": 800, "version": "20260901_00"}
			}],
			"next_cursor": null, "total_pages": 1, "current_page": 1, "total_items": 1
		}`),
	})
	page, err := client.Toolkits(context.Background(), ToolkitQuery{Search: "git", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	got := (*requests)[0]
	if got.apiKey != "test-key" || got.query.Get("search") != "git" || got.query.Get("limit") != "20" {
		t.Fatalf("request = %+v", got)
	}
	if len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatalf("page = %+v", page)
	}
	toolkit := page.Items[0]
	if toolkit.Slug != "github" || toolkit.ToolsCount != 800 || toolkit.TriggersCount != 5 ||
		len(toolkit.Categories) != 1 || toolkit.Categories[0].ID != "developer-tools" ||
		!slices.Equal(toolkit.ComposioManagedAuthSchemes, []string{"OAUTH2"}) {
		t.Fatalf("toolkit = %+v", toolkit)
	}
}

func TestCreateConnectLinkSendsTheUserAndCallback(t *testing.T) {
	client, requests := fakeComposio(t, map[string]func(http.ResponseWriter){
		"POST /api/v3.1/connected_accounts/link": jsonAnswer(http.StatusCreated, `{
			"link_token": "lt_1", "redirect_url": "https://connect.composio.dev/link/lt_1",
			"expires_at": "2026-09-17T12:00:00Z", "connected_account_id": "ca_1"
		}`),
	})
	link, err := client.CreateConnectLink(context.Background(), LinkRequest{
		AuthConfigID: "ac_1", UserID: "oryxa.org_1.user-1", CallbackURL: "https://oryxa.test/v1/apps/callback?state=s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := (*requests)[0].body
	if body["auth_config_id"] != "ac_1" || body["user_id"] != "oryxa.org_1.user-1" ||
		body["callback_url"] != "https://oryxa.test/v1/apps/callback?state=s1" {
		t.Fatalf("body = %v", body)
	}
	if _, sent := body["alias"]; sent {
		t.Fatal("an empty alias was sent")
	}
	if link.RedirectURL != "https://connect.composio.dev/link/lt_1" || link.ConnectedAccountID != "ca_1" || link.ExpiresAt.IsZero() {
		t.Fatalf("link = %+v", link)
	}
}

// accountJSON is a connected account as the API reference shows one, state
// included, so a test proves nothing from it is kept.
const accountJSON = `{
	"id": "ca_1", "user_id": "oryxa.org_1.user-1", "status": "ACTIVE", "status_reason": null,
	"toolkit": {"slug": "github"},
	"auth_config": {"id": "ac_1", "auth_scheme": "OAUTH2", "is_composio_managed": true, "is_disabled": false},
	"word_id": "github_red-castle", "alias": null, "is_disabled": false,
	"created_at": "2026-09-17T10:00:00Z", "updated_at": "2026-09-17T10:05:00Z",
	"state": {"authScheme": "OAUTH2", "val": {"status": "ACTIVE", "access_token": "gho_..."}},
	"data": {}, "params": {}
}`

func TestConnectedAccountKeepsNoCredential(t *testing.T) {
	client, _ := fakeComposio(t, map[string]func(http.ResponseWriter){
		"GET /api/v3.1/connected_accounts/ca_1": jsonAnswer(http.StatusOK, accountJSON),
	})
	account, err := client.ConnectedAccount(context.Background(), "ca_1")
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != "ca_1" || account.Status != StatusActive || account.Toolkit != "github" ||
		account.AuthConfigID != "ac_1" || !account.IsComposioManaged || account.WordID != "github_red-castle" ||
		account.UserID != "oryxa.org_1.user-1" || account.CreatedAt.IsZero() {
		t.Fatalf("account = %+v", account)
	}
	encoded, _ := json.Marshal(account)
	if strings.Contains(string(encoded), "gho_") {
		t.Fatalf("the account kept something from its state: %s", encoded)
	}
}

func TestConnectedAccountsRepeatsArrayParameters(t *testing.T) {
	client, requests := fakeComposio(t, map[string]func(http.ResponseWriter){
		"GET /api/v3.1/connected_accounts": jsonAnswer(http.StatusOK, `{"items": [`+accountJSON+`], "next_cursor": "c2"}`),
	})
	page, err := client.ConnectedAccounts(context.Background(), AccountQuery{
		UserIDs: []string{"u1", "u2"}, Toolkits: []string{"github"}, Statuses: []string{StatusActive},
	})
	if err != nil {
		t.Fatal(err)
	}
	query := (*requests)[0].query
	if !slices.Equal(query["user_ids"], []string{"u1", "u2"}) || query.Get("toolkit_slugs") != "github" || query.Get("statuses") != "ACTIVE" {
		t.Fatalf("query = %v", query)
	}
	if len(page.Items) != 1 || page.NextCursor != "c2" {
		t.Fatalf("page = %+v", page)
	}
}

func TestDeleteConnectedAccountRevokesOnlyWhenAsked(t *testing.T) {
	client, requests := fakeComposio(t, map[string]func(http.ResponseWriter){
		"DELETE /api/v3.1/connected_accounts/ca_1": jsonAnswer(http.StatusOK, `{"success": true, "revoke_job_id": "job_1"}`),
	})
	if err := client.DeleteConnectedAccount(context.Background(), "ca_1", true); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteConnectedAccount(context.Background(), "ca_1", false); err != nil {
		t.Fatal(err)
	}
	if (*requests)[0].query.Get("revoke_on_delete") != "true" || (*requests)[1].query.Has("revoke_on_delete") {
		t.Fatalf("queries = %v, %v", (*requests)[0].query, (*requests)[1].query)
	}
}

func TestToolsReadTheirHints(t *testing.T) {
	client, requests := fakeComposio(t, map[string]func(http.ResponseWriter){
		"GET /api/v3.1/tools": jsonAnswer(http.StatusOK, `{"items": [
			{"slug": "GITHUB_LIST_ISSUES", "name": "List issues", "description": "d", "toolkit": {"slug": "github", "name": "GitHub", "logo": ""},
			 "input_parameters": {"type": "object"}, "output_parameters": {}, "tags": ["readOnlyHint", "important"], "scopes": [],
			 "no_auth": false, "available_versions": [], "version": "v1", "is_deprecated": false},
			{"slug": "GITHUB_DELETE_A_REPOSITORY", "name": "Delete", "description": "d", "toolkit": {"slug": "github", "name": "GitHub", "logo": ""},
			 "input_parameters": {"type": "object"}, "output_parameters": {}, "tags": ["destructiveHint"], "scopes": [],
			 "no_auth": false, "available_versions": [], "version": "v1", "is_deprecated": false}
		], "next_cursor": null}`),
	})
	page, err := client.Tools(context.Background(), ToolQuery{
		Toolkit: "github", Search: "issue", Important: true, Slugs: []string{"A", "B"}, Tags: []string{HintReadOnly},
	})
	if err != nil {
		t.Fatal(err)
	}
	query := (*requests)[0].query
	if query.Get("toolkit_slug") != "github" || query.Get("query") != "issue" || query.Get("important") != "true" ||
		query.Get("tool_slugs") != "A,B" || query.Get("tags") != HintReadOnly {
		t.Fatalf("query = %v", query)
	}
	if !page.Items[0].ReadOnly() || page.Items[0].Destructive() || page.Items[1].ReadOnly() || !page.Items[1].Destructive() {
		t.Fatalf("hints = %+v", page.Items)
	}
	if string(page.Items[0].InputParameters) != `{"type": "object"}` {
		t.Fatalf("input parameters = %s", page.Items[0].InputParameters)
	}
}

func TestExecuteToolSendsArgumentsAndReportsAFailedRun(t *testing.T) {
	client, requests := fakeComposio(t, map[string]func(http.ResponseWriter){
		"POST /api/v3.1/tools/execute/GITHUB_CREATE_AN_ISSUE": jsonAnswer(http.StatusOK,
			`{"data": {}, "error": "Repository not found", "successful": false, "log_id": "log_1"}`),
	})
	result, err := client.ExecuteTool(context.Background(), "GITHUB_CREATE_AN_ISSUE", ExecuteRequest{
		ConnectedAccountID: "ca_1", UserID: "u1", Arguments: json.RawMessage(`{"owner":"acme","repo":"api","title":"Hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := (*requests)[0].body
	arguments, _ := body["arguments"].(map[string]any)
	if body["connected_account_id"] != "ca_1" || body["user_id"] != "u1" || arguments["title"] != "Hi" {
		t.Fatalf("body = %v", body)
	}
	if _, sent := body["version"]; sent {
		t.Fatal("an empty version was sent")
	}
	if result.Successful || result.Error != "Repository not found" || result.LogID != "log_1" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := client.ExecuteTool(context.Background(), "X", ExecuteRequest{Arguments: json.RawMessage(`{`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ExecuteTool(invalid arguments) error = %v", err)
	}
}

func TestProxyReturnsTheToolkitsAnswerAsItCame(t *testing.T) {
	client, requests := fakeComposio(t, map[string]func(http.ResponseWriter){
		"POST /api/v3.1/tools/execute/proxy": jsonAnswer(http.StatusOK,
			`{"status": 403, "data": {"message": "rate limited"}, "headers": {"x-ratelimit-remaining": "0", "retry-after": 30}}`),
	})
	response, err := client.Proxy(context.Background(), ProxyRequest{
		ConnectedAccountID: "ca_1", Endpoint: "https://api.github.com/repos/acme/api/issues", Method: "get",
		Parameters: []ProxyParameter{{Name: "per_page", Value: "100", Type: "query"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := (*requests)[0].body
	parameters, _ := body["parameters"].([]any)
	if body["method"] != "GET" || body["endpoint"] != "https://api.github.com/repos/acme/api/issues" || len(parameters) != 1 {
		t.Fatalf("body = %v", body)
	}
	if _, sent := body["body"]; sent {
		t.Fatal("an empty body was sent")
	}
	if response.Status != 403 || response.Headers["retry-after"] != "30" || response.Headers["x-ratelimit-remaining"] != "0" {
		t.Fatalf("response = %+v", response)
	}
}

func TestTriggersSubscribeListAndManage(t *testing.T) {
	client, requests := fakeComposio(t, map[string]func(http.ResponseWriter){
		"GET /api/v3.1/triggers_types": jsonAnswer(http.StatusOK, `{"items": [{"slug": "GITHUB_COMMIT_EVENT", "name": "Commit",
			"description": "d", "instructions": "i", "type": "webhook", "toolkit": {"slug": "github", "name": "GitHub", "logo": ""},
			"config": {"type": "object"}, "payload": {}, "version": "v1", "requires_webhook_endpoint_setup": false}], "next_cursor": null}`),
		"POST /api/v3.1/trigger_instances/GITHUB_COMMIT_EVENT/upsert": jsonAnswer(http.StatusCreated, `{"trigger_id": "ti_1"}`),
		"GET /api/v3.1/trigger_instances/active": jsonAnswer(http.StatusOK, `{"items": [{"id": "ti_1", "trigger_name": "GITHUB_COMMIT_EVENT",
			"connected_account_id": "ca_1", "user_id": "u1", "trigger_config": {"owner": "acme"}, "version": "v1",
			"updated_at": "2026-09-17T10:00:00Z", "disabled_at": "2026-09-17T11:00:00Z", "egress_url": null, "state": null}], "next_cursor": null}`),
		"PATCH /api/v3.1/trigger_instances/manage/ti_1":  jsonAnswer(http.StatusOK, `{"status": "success"}`),
		"DELETE /api/v3.1/trigger_instances/manage/ti_1": jsonAnswer(http.StatusOK, `{"trigger_id": "ti_1"}`),
	})
	ctx := context.Background()
	types, err := client.TriggerTypes(ctx, TriggerTypeQuery{Toolkits: []string{"github"}})
	if err != nil || len(types.Items) != 1 || types.Items[0].Kind != "webhook" || types.Items[0].Toolkit != "github" {
		t.Fatalf("TriggerTypes() = %+v, %v", types, err)
	}
	id, err := client.UpsertTrigger(ctx, "GITHUB_COMMIT_EVENT", TriggerSubscription{
		ConnectedAccountID: "ca_1", UserID: "u1", Config: json.RawMessage(`{"owner":"acme","repo":"api"}`),
	})
	if err != nil || id != "ti_1" {
		t.Fatalf("UpsertTrigger() = %q, %v", id, err)
	}
	upsert := (*requests)[1].body
	// The user goes with the account: a project with two-factor protection
	// refuses a subscription without it.
	if upsert["connected_account_id"] != "ca_1" || upsert["user_id"] != "u1" ||
		upsert["trigger_config"].(map[string]any)["repo"] != "api" {
		t.Fatalf("upsert body = %v", upsert)
	}
	instances, err := client.Triggers(ctx, TriggerQuery{TriggerIDs: []string{"ti_1"}, ShowDisabled: true})
	if err != nil || len(instances.Items) != 1 || instances.Items[0].DisabledAt == nil || instances.Items[0].ConnectedAccountID != "ca_1" {
		t.Fatalf("Triggers() = %+v, %v", instances, err)
	}
	if query := (*requests)[2].query; query.Get("trigger_ids") != "ti_1" || query.Get("show_disabled") != "true" {
		t.Fatalf("Triggers() query = %v", query)
	}
	if err := client.SetTriggerEnabled(ctx, "ti_1", false); err != nil || (*requests)[3].body["status"] != "disable" {
		t.Fatalf("SetTriggerEnabled() = %v, body %v", err, (*requests)[3].body)
	}
	if err := client.DeleteTrigger(ctx, "ti_1"); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookSubscriptionsWalkEveryPageAndCreateInTheCurrentVersion(t *testing.T) {
	calls := 0
	client, requests := fakeComposio(t, map[string]func(http.ResponseWriter){
		"GET /api/v3.1/webhook_subscriptions": func(writer http.ResponseWriter) {
			calls++
			if calls == 1 {
				jsonAnswer(http.StatusOK, `{"items": [{"id": "ws_1", "webhook_url": "https://a", "version": "V3", "enabled_events": [], "secret": "s1"}], "next_cursor": "c2"}`)(writer)
				return
			}
			jsonAnswer(http.StatusOK, `{"items": [{"id": "ws_2", "webhook_url": "https://b", "version": "V3", "enabled_events": [], "secret": "s2"}], "next_cursor": null}`)(writer)
		},
		"POST /api/v3.1/webhook_subscriptions": jsonAnswer(http.StatusCreated,
			`{"id": "ws_3", "webhook_url": "https://c", "version": "V3", "enabled_events": ["composio.trigger.message"], "secret": "s3", "created_at": "", "updated_at": ""}`),
	})
	subscriptions, err := client.WebhookSubscriptions(context.Background())
	if err != nil || len(subscriptions) != 2 || subscriptions[1].Secret != "s2" {
		t.Fatalf("WebhookSubscriptions() = %+v, %v", subscriptions, err)
	}
	created, err := client.CreateWebhookSubscription(context.Background(), "https://c", []string{EventTriggerMessage})
	if err != nil || created.Secret != "s3" {
		t.Fatalf("CreateWebhookSubscription() = %+v, %v", created, err)
	}
	if body := (*requests)[2].body; body["version"] != WebhookVersion || body["webhook_url"] != "https://c" {
		t.Fatalf("create body = %v", body)
	}
}
