package pipedream

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorded is one request the fake Pipedream received.
type recorded struct {
	method      string
	path        string
	query       url.Values
	header      http.Header
	body        map[string]any
	environment string
}

// fake answers each request with the response registered for its method and
// path, after checking the bearer token, and records what it was sent. The
// token endpoint is served unless a test registers its own.
type fake struct {
	t         *testing.T
	mu        sync.Mutex
	requests  []recorded
	responses map[string]func(http.ResponseWriter, *http.Request)
	tokens    atomic.Int64
	expiresIn int
}

func newFake(t *testing.T, responses map[string]func(http.ResponseWriter, *http.Request)) (*Client, *fake) {
	t.Helper()
	server := &fake{t: t, responses: responses, expiresIn: 3600}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	client, err := New(Config{
		ProjectID: "proj_test", Environment: EnvironmentDevelopment,
		ClientID: "client-id", ClientSecret: "client-secret", BaseURL: httpServer.URL + "/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func (server *fake) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	entry := recorded{
		method: request.Method, path: request.URL.EscapedPath(), query: request.URL.Query(),
		header: request.Header.Clone(), environment: request.Header.Get("x-pd-environment"),
	}
	payload, _ := io.ReadAll(request.Body)
	request.Body = io.NopCloser(bytes.NewReader(payload))
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &entry.body)
	}
	server.mu.Lock()
	server.requests = append(server.requests, entry)
	server.mu.Unlock()
	key := request.Method + " " + request.URL.EscapedPath()
	if respond, ok := server.responses[key]; ok {
		respond(writer, request)
		return
	}
	if key == "POST /v1/oauth/token" {
		if entry.body["grant_type"] != "client_credentials" || entry.body["client_id"] != "client-id" || entry.body["client_secret"] != "client-secret" {
			jsonAnswer(http.StatusUnauthorized, `{"error":"invalid_client"}`)(writer, request)
			return
		}
		number := server.tokens.Add(1)
		jsonAnswer(http.StatusOK, `{"access_token":"at_`+strconv.FormatInt(number, 10)+`","token_type":"Bearer","expires_in":`+
			strconv.Itoa(server.expiresIn)+`}`)(writer, request)
		return
	}
	if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer at_") {
		server.t.Errorf("%s was sent without a token", key)
	}
	server.t.Errorf("unexpected request %s", key)
	http.NotFound(writer, request)
}

// last is the last request that was not for a token.
func (server *fake) last() recorded {
	server.mu.Lock()
	defer server.mu.Unlock()
	for index := len(server.requests) - 1; index >= 0; index-- {
		if server.requests[index].path != "/v1/oauth/token" {
			return server.requests[index]
		}
	}
	server.t.Fatal("no request was made")
	return recorded{}
}

func jsonAnswer(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}
}

func TestNewRefusesWhatIsMissingAndAnyHostButPipedreamsOrLoopback(t *testing.T) {
	good := Config{ProjectID: "proj_1", Environment: EnvironmentProduction, ClientID: "id", ClientSecret: "secret"}
	for name, change := range map[string]func(*Config){
		"no project":          func(config *Config) { config.ProjectID = " " },
		"no client ID":        func(config *Config) { config.ClientID = "" },
		"no client secret":    func(config *Config) { config.ClientSecret = "" },
		"no environment":      func(config *Config) { config.Environment = "" },
		"an unknown env":      func(config *Config) { config.Environment = "staging" },
		"a relative base URL": func(config *Config) { config.BaseURL = "api.pipedream.com/v1" },
		"another host":        func(config *Config) { config.BaseURL = "https://evil.example/v1" },
		"a lookalike host":    func(config *Config) { config.BaseURL = "https://api.pipedream.com.evil.example/v1" },
	} {
		config := good
		change(&config)
		if _, err := New(config); err == nil {
			t.Errorf("New() accepted %s", name)
		}
	}
	for _, base := range []string{"", DefaultBaseURL + "/", "http://127.0.0.1:9/v1", "http://localhost:9/v1", "http://[::1]:9/v1"} {
		config := good
		config.BaseURL = base
		if _, err := New(config); err != nil {
			t.Errorf("New(base %q) = %v", base, err)
		}
	}
}

func TestTheTokenIsMadeOnceSharedAndReplacedBeforeItLapses(t *testing.T) {
	client, server := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"DELETE /v1/connect/proj_test/accounts/apn_1": jsonAnswer(http.StatusNoContent, ""),
	})
	var wait sync.WaitGroup
	for range 20 {
		wait.Go(func() {
			if err := client.DeleteAccount(context.Background(), "apn_1"); err != nil {
				t.Error(err)
			}
		})
	}
	wait.Wait()
	if made := server.tokens.Load(); made != 1 {
		t.Fatalf("20 concurrent calls made %d tokens, want 1", made)
	}
	if got := server.last(); got.header.Get("Authorization") != "Bearer at_1" || got.environment != EnvironmentDevelopment {
		t.Fatalf("the call carried %q in %q", got.header.Get("Authorization"), got.environment)
	}

	// An hour's token is kept until a minute before it lapses.
	start := time.Now()
	client.now = func() time.Time { return start.Add(58 * time.Minute) }
	_ = client.DeleteAccount(context.Background(), "apn_1")
	if made := server.tokens.Load(); made != 1 {
		t.Fatalf("a token with two minutes left was replaced")
	}
	client.now = func() time.Time { return start.Add(59*time.Minute + 30*time.Second) }
	_ = client.DeleteAccount(context.Background(), "apn_1")
	if made := server.tokens.Load(); made != 2 || server.last().header.Get("Authorization") != "Bearer at_2" {
		t.Fatalf("a token with 30 seconds left was kept")
	}
}

func TestARefusedTokenIsForgottenAndARefusedClientIsUnauthorized(t *testing.T) {
	calls := 0
	client, server := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"DELETE /v1/connect/proj_test/accounts/apn_1": func(writer http.ResponseWriter, request *http.Request) {
			calls++
			if calls == 1 {
				jsonAnswer(http.StatusUnauthorized, `{"error":"token expired"}`)(writer, request)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
		},
	})
	if err := client.DeleteAccount(context.Background(), "apn_1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("DeleteAccount(refused) = %v, want ErrUnauthorized", err)
	}
	if err := client.DeleteAccount(context.Background(), "apn_1"); err != nil || server.tokens.Load() != 2 {
		t.Fatalf("after a refusal: %v, %d tokens made, want a new one", err, server.tokens.Load())
	}

	client.clientSecret = "wrong"
	client.token = ""
	if err := client.DeleteAccount(context.Background(), "apn_1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a refused client = %v, want ErrUnauthorized", err)
	}
}

func TestErrorsCarryPipedreamsEnvelopeAndKind(t *testing.T) {
	client, _ := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /v1/connect/proj_test/accounts/apn_404": jsonAnswer(http.StatusNotFound, `{"error":"record not found"}`),
		"GET /v1/connect/proj_test/accounts/apn_429": func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Retry-After", "7")
			jsonAnswer(http.StatusTooManyRequests, `{"error":"Throttled"}`)(writer, request)
		},
		"GET /v1/connect/proj_test/accounts/apn_400": jsonAnswer(http.StatusBadRequest, `{"error":"bad id","code":"invalid_id"}`),
		"GET /v1/connect/proj_test/accounts/apn_403": jsonAnswer(http.StatusForbidden, `{"error":"scope"}`),
		"GET /v1/connect/proj_test/accounts/apn_502": jsonAnswer(http.StatusBadGateway, `<html>bad gateway</html>`),
		"GET /v1/connect/proj_test/accounts/apn_bad": jsonAnswer(http.StatusOK, `{"id":`),
	})
	ctx := context.Background()
	var failure *Error
	_, err := client.GetAccount(ctx, "apn_404")
	if !errors.Is(err, ErrNotFound) || !errors.As(err, &failure) || failure.Message != "record not found" {
		t.Fatalf("404 = %#v", err)
	}
	_, err = client.GetAccount(ctx, "apn_429")
	if !errors.Is(err, ErrRateLimited) || !errors.As(err, &failure) || failure.RetryAfter != 7*time.Second || !failure.Retryable() {
		t.Fatalf("429 = %#v", err)
	}
	_, err = client.GetAccount(ctx, "apn_400")
	if !errors.Is(err, ErrInvalid) || !errors.As(err, &failure) || failure.Code != "invalid_id" || failure.Retryable() {
		t.Fatalf("400 = %#v", err)
	}
	if _, err = client.GetAccount(ctx, "apn_403"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("403 = %v", err)
	}
	if _, err = client.GetAccount(ctx, "apn_502"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("502 = %v", err)
	}
	if _, err = client.GetAccount(ctx, "apn_bad"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unreadable answer = %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = client.GetAccount(cancelled, "apn_404"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled call = %v", err)
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	offline, _ := New(Config{ProjectID: "proj_test", Environment: EnvironmentDevelopment, ClientID: "id", ClientSecret: "s", BaseURL: gone.URL})
	if _, err = offline.GetAccount(ctx, "apn_1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an unreachable Pipedream = %v", err)
	}
}

func TestCreateConnectTokenSendsTheUserAndRedirects(t *testing.T) {
	client, server := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /v1/connect/proj_test/tokens": jsonAnswer(http.StatusOK, `{
			"token": "ctok_0123456789abcdef0123456789abcdef", "expires_at": "2026-10-13T12:00:00Z",
			"connect_link_url": "https://pipedream.com/_static/connect.html?token=ctok_0123456789abcdef0123456789abcdef&connectLink=true"
		}`),
	})
	token, err := client.CreateConnectToken(context.Background(), "org_1.user_1", "https://oryxa.test/ok", "https://oryxa.test/no", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	body := server.last().body
	if body["external_user_id"] != "org_1.user_1" || body["success_redirect_uri"] != "https://oryxa.test/ok" ||
		body["error_redirect_uri"] != "https://oryxa.test/no" || body["expires_in"] != float64(600) {
		t.Fatalf("body = %v", body)
	}
	if !strings.HasPrefix(token.Token, "ctok_") || !strings.Contains(token.ConnectLinkURL, "connect.html") || token.ExpiresAt.IsZero() {
		t.Fatalf("token = %+v", token)
	}

	if _, err := client.CreateConnectToken(context.Background(), "u", "", "", 5*time.Hour); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a five-hour token = %v, want ErrInvalid", err)
	}
	if _, err := client.CreateConnectToken(context.Background(), "u", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, sent := server.last().body["success_redirect_uri"]; sent {
		t.Fatal("an empty redirect was sent")
	}
}

// accountJSON is an account as the reference shows one, credentials
// included, so a test proves nothing from them is kept.
const accountJSON = `{
	"id": "apn_1", "name": "jane@example.com", "external_id": "org_1.user_1", "healthy": true, "dead": null,
	"app": {"id": "oa_1", "name_slug": "gmail", "name": "Gmail", "auth_type": "oauth", "img_src": "https://logo",
		"custom_fields_json": "[]", "categories": ["Email"], "featured_weight": 10},
	"created_at": "2026-10-13T10:00:00.000Z", "updated_at": "2026-10-13T10:05:00.000Z", "error": null,
	"credentials": {"oauth_access_token": "ya29.secret", "oauth_refresh_token": "1//refresh"}
}`

func TestAccountsAreListedReadAndDeletedWithoutCredentials(t *testing.T) {
	client, server := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /v1/connect/proj_test/users/org_1.user%2F1/accounts": jsonAnswer(http.StatusOK, `[`+accountJSON+`]`),
		"GET /v1/connect/proj_test/users/stranger/accounts":       jsonAnswer(http.StatusNotFound, `{"error":"Not Found"}`),
		"GET /v1/connect/proj_test/accounts/apn_1":                jsonAnswer(http.StatusOK, accountJSON),
		"DELETE /v1/connect/proj_test/accounts/apn_1":             jsonAnswer(http.StatusNoContent, ""),
		"DELETE /v1/connect/proj_test/users/org_1.user_1":         jsonAnswer(http.StatusNoContent, ""),
	})
	ctx := context.Background()
	accounts, err := client.ListAccounts(ctx, "org_1.user/1", "gmail")
	if err != nil || len(accounts) != 1 {
		t.Fatalf("ListAccounts = %v, %v", accounts, err)
	}
	if got := server.last(); got.query.Get("app") != "gmail" || got.query.Has("include_credentials") {
		t.Fatalf("query = %v", got.query)
	}
	account := accounts[0]
	if account.ID != "apn_1" || !account.Healthy || account.Dead || account.App.NameSlug != "gmail" || account.CreatedAt.IsZero() {
		t.Fatalf("account = %+v", account)
	}
	if encoded, _ := json.Marshal(account); strings.Contains(string(encoded), "ya29") || strings.Contains(string(encoded), "refresh") {
		t.Fatalf("the account kept a credential: %s", encoded)
	}
	if accounts, err := client.ListAccounts(ctx, "stranger", ""); err != nil || len(accounts) != 0 {
		t.Fatalf("ListAccounts(a user Pipedream does not know) = %v, %v, want none", accounts, err)
	}
	if account, err := client.GetAccount(ctx, "apn_1"); err != nil || account.ExternalID != "org_1.user_1" {
		t.Fatalf("GetAccount = %+v, %v", account, err)
	}
	if err := client.DeleteAccount(ctx, "apn_1"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteUser(ctx, "org_1.user_1"); err != nil || server.last().method != http.MethodDelete {
		t.Fatal(err)
	}
}

func TestListAppsPagesByCursor(t *testing.T) {
	client, server := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /v1/connect/apps": func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Query().Get("after") == "" {
				jsonAnswer(http.StatusOK, `{"data":[{"name_slug":"gmail","name":"Gmail","categories":["Email"],"featured_weight":5}],
					"page_info":{"count":1,"total_count":2,"start_cursor":"a","end_cursor":"c1"}}`)(writer, request)
				return
			}
			jsonAnswer(http.StatusOK, `{"data":[{"name_slug":"slack","name":"Slack"}],
				"page_info":{"count":1,"total_count":2,"start_cursor":"c1","end_cursor":null}}`)(writer, request)
		},
	})
	page, err := client.ListApps(context.Background(), AppQuery{Search: "mail", Limit: 1, HasActions: true})
	if err != nil || len(page.Items) != 1 || page.Items[0].NameSlug != "gmail" || page.NextCursor != "c1" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	if query := server.last().query; query.Get("q") != "mail" || query.Get("limit") != "1" || query.Get("has_actions") != "true" || query.Has("after") {
		t.Fatalf("query = %v", query)
	}
	page, err = client.ListApps(context.Background(), AppQuery{After: page.NextCursor})
	if err != nil || len(page.Items) != 1 || page.NextCursor != "" || server.last().query.Get("after") != "c1" {
		t.Fatalf("last page = %+v, %v", page, err)
	}
}

func TestAnEmptyPageEndsAListingWhateverItsCursor(t *testing.T) {
	client, _ := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /v1/connect/apps": jsonAnswer(http.StatusOK, `{"data":[],"page_info":{"count":0,"end_cursor":"again"}}`),
	})
	if page, err := client.ListApps(context.Background(), AppQuery{}); err != nil || page.NextCursor != "" {
		t.Fatalf("an empty page = %+v, %v", page, err)
	}
}

func TestComponentsAreListedByAppWithTheirAnnotations(t *testing.T) {
	components := `{"data":[
		{"key":"gmail-find-email","name":"Find Email","version":"0.1.0","component_type":"action",
			"configurable_props":[{"name":"gmail","type":"app","app":"gmail"}],
			"annotations":{"readOnlyHint":true,"destructiveHint":false,"title":"Find Email"}},
		{"key":"gmail-send-email","name":"Send Email","version":"0.2.0","component_type":"action","configurable_props":[],
			"annotations":{"readOnlyHint":false}},
		{"key":"gmail-legacy","name":"Legacy","version":"0.0.1","configurable_props":[]}
	],"page_info":{"count":3,"end_cursor":null}}`
	client, server := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /v1/connect/proj_test/actions":  jsonAnswer(http.StatusOK, components),
		"GET /v1/connect/proj_test/triggers": jsonAnswer(http.StatusOK, `{"data":[{"key":"gmail-new-email-received","name":"New Email","version":"0.1.0","component_type":"source","configurable_props":[]}],"page_info":{"end_cursor":null}}`),
	})
	actions, err := client.ListActions(context.Background(), ComponentQuery{App: "gmail", Search: "email", Limit: 50, After: "c0"})
	if err != nil || len(actions.Items) != 3 || actions.NextCursor != "" {
		t.Fatalf("ListActions = %+v, %v", actions, err)
	}
	if query := server.last().query; query.Get("app") != "gmail" || query.Get("q") != "email" || query.Get("limit") != "50" || query.Get("after") != "c0" {
		t.Fatalf("query = %v", query)
	}
	if !actions.Items[0].ReadOnly() || actions.Items[1].ReadOnly() || actions.Items[2].ReadOnly() {
		t.Fatal("ReadOnly() does not follow readOnlyHint, or assumed it when absent")
	}
	if !strings.Contains(string(actions.Items[0].ConfigurableProps), `"type":"app"`) || actions.Items[0].Annotations.Title != "Find Email" {
		t.Fatalf("action = %+v", actions.Items[0])
	}
	triggers, err := client.ListTriggers(context.Background(), ComponentQuery{App: "gmail"})
	if err != nil || len(triggers.Items) != 1 || triggers.Items[0].Key != "gmail-new-email-received" {
		t.Fatalf("ListTriggers = %+v, %v", triggers, err)
	}
}

func TestRunActionReturnsWhatItReturnedOrWhereItFailed(t *testing.T) {
	client, server := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /v1/connect/proj_test/actions/run": func(writer http.ResponseWriter, request *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			switch body["id"] {
			case "gmail-find-email":
				jsonAnswer(http.StatusOK, `{"exports":{"$summary":"Found 2"},"os":[],"ret":[{"id":"m1"},{"id":"m2"}],"stash_id":null}`)(writer, request)
			case "gmail-flaky":
				jsonAnswer(http.StatusOK, `{"exports":{},"ret":null,"error":{"name":"AxiosError","message":"Request failed with status code 503",
					"attribution":{"origin":"upstream_api","network_calls_count":1,"last_call":{"host":"gmail.googleapis.com","status":503}}}}`)(writer, request)
			default:
				jsonAnswer(http.StatusBadRequest, `{"error":{"name":"ConfigurationError","message":"to is required",
					"attribution":{"origin":"component_code"}}}`)(writer, request)
			}
		},
	})
	ctx := context.Background()
	props := map[string]any{"gmail": map[string]any{"authProvisionId": "apn_1"}, "q": "from:jane"}
	result, err := client.RunAction(ctx, "gmail-find-email", "org_1.user_1", props)
	if err != nil || string(result.Ret) != `[{"id":"m1"},{"id":"m2"}]` || string(result.Exports["$summary"]) != `"Found 2"` {
		t.Fatalf("RunAction = %+v, %v", result, err)
	}
	body := server.last().body
	if body["external_user_id"] != "org_1.user_1" || body["configured_props"].(map[string]any)["q"] != "from:jane" {
		t.Fatalf("body = %v", body)
	}

	var failure *Error
	_, err = client.RunAction(ctx, "gmail-flaky", "u", nil)
	if !errors.Is(err, ErrApp) || !errors.As(err, &failure) || failure.Origin != OriginUpstreamAPI ||
		failure.UpstreamStatus != 503 || !failure.Retryable() || !strings.Contains(failure.Message, "503") {
		t.Fatalf("an upstream 503 = %#v", err)
	}
	if server.last().body["configured_props"] == nil {
		t.Fatal("nil props were sent as null")
	}
	_, err = client.RunAction(ctx, "gmail-send-email", "u", nil)
	if !errors.Is(err, ErrApp) || !errors.As(err, &failure) || failure.Origin != OriginComponentCode || failure.Retryable() {
		t.Fatalf("a component error = %#v", err)
	}
}

func TestDeployTriggerAsksForTheWebhookAndKeepsItsSigningKey(t *testing.T) {
	client, server := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /v1/connect/proj_test/triggers/deploy": jsonAnswer(http.StatusOK, `{"data":{
			"type":"DeployedComponent","id":"dc_1","owner_id":"o_1","component_id":"sc_1","component_key":"gmail-new-email-received",
			"configurable_props":[],"configured_props":{},"active":true,"created_at":1760000000000,"updated_at":1760000000000,
			"name":"New Email","name_slug":"new-email","webhook_signing_key":"whsk_1"}}`),
		"DELETE /v1/connect/proj_test/deployed-triggers/dc_1": jsonAnswer(http.StatusNoContent, ""),
	})
	ctx := context.Background()
	props := map[string]any{"gmail": map[string]any{"authProvisionId": "apn_1"}}
	trigger, err := client.DeployTrigger(ctx, "gmail-new-email-received", "org_1.user_1", props, "https://oryxa.test/hooks/pipedream", 15*time.Minute)
	if err != nil || trigger.ID != "dc_1" || trigger.SigningKey != "whsk_1" || !trigger.Active || trigger.ComponentKey != "gmail-new-email-received" {
		t.Fatalf("DeployTrigger = %+v, %v", trigger, err)
	}
	body := server.last().body
	configured := body["configured_props"].(map[string]any)
	if body["webhook_url"] != "https://oryxa.test/hooks/pipedream" || body["emit_on_deploy"] != false ||
		configured["timer"].(map[string]any)["intervalSeconds"] != float64(900) || configured["gmail"] == nil {
		t.Fatalf("body = %v", body)
	}
	if _, set := props["timer"]; set {
		t.Fatal("DeployTrigger changed the caller's props")
	}
	if err := client.DeleteDeployedTrigger(ctx, "dc_1", "org_1.user_1"); err != nil || server.last().query.Get("external_user_id") != "org_1.user_1" {
		t.Fatalf("DeleteDeployedTrigger = %v, query %v", err, server.last().query)
	}
}

func TestUsageReadsTheWindowsCredits(t *testing.T) {
	client, server := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /v1/connect/usage": jsonAnswer(http.StatusOK, `{"data":[{"credits_used":12,"action_run_credits_used":10,
			"proxy_credits_used":2,"source_emit_credits_used":null,"usage_start_ts":1760000000,"usage_end_ts":1760086400}]}`),
	})
	start, end := time.Unix(1760000000, 0), time.Unix(1760086400, 0)
	records, err := client.Usage(context.Background(), start, end)
	if err != nil || len(records) != 1 || records[0].Credits != 12 || records[0].ActionRunCredits != 10 ||
		records[0].ProxyCredits != 2 || !records[0].Start.Equal(start) || !records[0].End.Equal(end) {
		t.Fatalf("Usage = %+v, %v", records, err)
	}
	if query := server.last().query; query.Get("start_ts") != "1760000000" || query.Get("end_ts") != "1760086400" {
		t.Fatalf("query = %v", query)
	}
}

func TestProxyEncodesTheURLAndForwardsOnlyTheCallersOwnHeaders(t *testing.T) {
	target := "https://gmail.googleapis.com/gmail/v1/users/me/messages?q=from:jane"
	encoded := base64.RawURLEncoding.EncodeToString([]byte(target))
	client, server := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /v1/connect/proj_test/proxy/" + encoded: func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("X-Upstream", "yes")
			jsonAnswer(http.StatusTeapot, `{"upstream":"said no"}`)(writer, request)
		},
	})
	request, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(`{"raw":"x"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer the-members-own")
	request.Header.Set("Cookie", "session=1")
	request.Header.Set("X-Custom", "hello")
	request.Header.Set("x-pd-environment", "production")
	response, err := client.Proxy(context.Background(), "org_1.user_1", "apn_1", request)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusTeapot || string(payload) != `{"upstream":"said no"}` ||
		response.Header.Get("X-Upstream") != "yes" {
		t.Fatalf("response = %d %q", response.StatusCode, payload)
	}
	got := server.last()
	if got.query.Get("external_user_id") != "org_1.user_1" || got.query.Get("account_id") != "apn_1" {
		t.Fatalf("query = %v", got.query)
	}
	if got.header.Get("Authorization") != "Bearer at_1" || got.environment != EnvironmentDevelopment {
		t.Fatalf("the proxy call carried %q in %q", got.header.Get("Authorization"), got.environment)
	}
	if got.header.Get("X-Pd-Proxy-X-Custom") != "hello" || got.header.Get("X-Pd-Proxy-Content-Type") != "application/json" ||
		got.header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", got.header)
	}
	for name := range got.header {
		if lower := strings.ToLower(name); strings.Contains(lower, "authorization") && lower != "authorization" ||
			strings.Contains(lower, "cookie") || lower == "x-pd-proxy-x-pd-environment" {
			t.Fatalf("%s was forwarded", name)
		}
	}
	if got.body["raw"] != "x" {
		t.Fatalf("body = %v", got.body)
	}
}
