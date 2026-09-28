package httpserver

import (
	"context"
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
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// fakeOryxa answers Oryxa's routes from what a test sets, and records what it
// was asked.
type fakeOryxa struct {
	err error

	credentials  authclient.Credentials
	organization string
	connectInput application.ConnectInput
	connected    application.OryxaConnect
	callback     application.SigninCallback
	completion   application.Completion
	replaced     string
	replaceInput application.ReplaceCredentialInput
	connection   domain.Connection
	manifest     application.ManifestView
	client       string
	clientInput  application.OAuthClientInput
	clientView   application.OAuthClientView
	removed      string
	revealed     string
	token        secrets.Secret
	deployment   application.DeploymentHookView
}

func (fake *fakeOryxa) ConnectOryxa(_ context.Context, credentials authclient.Credentials, organizationID string, input application.ConnectInput) (application.OryxaConnect, error) {
	fake.credentials, fake.organization, fake.connectInput = credentials, organizationID, input
	return fake.connected, fake.err
}

func (fake *fakeOryxa) CompleteSignin(_ context.Context, callback application.SigninCallback) (application.Completion, error) {
	fake.callback = callback
	return fake.completion, fake.err
}

func (fake *fakeOryxa) ReplaceCredential(_ context.Context, credentials authclient.Credentials, organizationID, connectionID string, input application.ReplaceCredentialInput) (domain.Connection, error) {
	fake.credentials, fake.organization, fake.replaced, fake.replaceInput = credentials, organizationID, connectionID, input
	return fake.connection, fake.err
}

func (fake *fakeOryxa) Integrations() []application.IntegrationView {
	return []application.IntegrationView{{Toolkit: "github", Name: "GitHub", Kinds: []string{"oauth2_code", "api_token"}}}
}

func (fake *fakeOryxa) Manifest(toolkit string) (application.ManifestView, error) {
	fake.client = toolkit
	return fake.manifest, fake.err
}

func (fake *fakeOryxa) OAuthClient(_ context.Context, credentials authclient.Credentials, organizationID, client string) (application.OAuthClientView, error) {
	fake.credentials, fake.organization, fake.client = credentials, organizationID, client
	return fake.clientView, fake.err
}

func (fake *fakeOryxa) PutOAuthClient(_ context.Context, credentials authclient.Credentials, organizationID, client string, input application.OAuthClientInput) (application.OAuthClientView, error) {
	fake.credentials, fake.organization, fake.client, fake.clientInput = credentials, organizationID, client, input
	return fake.clientView, fake.err
}

func (fake *fakeOryxa) RemoveOAuthClient(_ context.Context, credentials authclient.Credentials, organizationID, client string) error {
	fake.credentials, fake.organization, fake.removed = credentials, organizationID, client
	return fake.err
}

func (fake *fakeOryxa) RevealVerificationToken(_ context.Context, credentials authclient.Credentials, organizationID, toolkit string) (secrets.Secret, error) {
	fake.credentials, fake.organization, fake.revealed = credentials, organizationID, toolkit
	return fake.token, fake.err
}

func (fake *fakeOryxa) DeploymentHook(_ context.Context, toolkit string) (application.DeploymentHookView, error) {
	fake.revealed = toolkit
	return fake.deployment, fake.err
}

func (fake *fakeOryxa) RevealDeploymentVerificationToken(_ context.Context, toolkit string) (secrets.Secret, error) {
	fake.revealed = toolkit
	return fake.token, fake.err
}

func newOryxaServer(t *testing.T, oryxa *fakeOryxa, config ...func(*Config)) *Server {
	t.Helper()
	settings := Config{AllowedOrigins: []string{"https://app.oryxa.test"}, Oryxa: oryxa}
	for _, change := range config {
		change(&settings)
	}
	return New(&fakeService{}, settings, nil)
}

// cookieNamed finds a Set-Cookie of an answer by its name's prefix.
func cookieNamed(t *testing.T, recorder *httptest.ResponseRecorder, prefix string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, prefix) {
			return cookie
		}
	}
	return nil
}

func TestOryxaOAuthConnectAnswersTheLinkAndSetsTheStateCookie(t *testing.T) {
	t.Parallel()
	oryxa := &fakeOryxa{connected: application.OryxaConnect{
		Connection: domain.Connection{ID: "apc_1", Toolkit: "github", Broker: domain.BrokerOryxa, Status: domain.ConnectionPending,
			MemberID: "user-1", CreatedAt: testClock, UpdatedAt: testClock},
		RedirectURL: "https://github.com/login/oauth/authorize?client_id=Iv1.oryxa&state=opaque-state",
		State:       "opaque-state", StateExpiresAt: testClock.Add(10 * time.Minute),
	}}
	server := newOryxaServer(t, oryxa)

	request := httptest.NewRequest(http.MethodPost, "https://app.oryxa.test/v1/organizations/org_1/apps/connections",
		strings.NewReader(`{"toolkit":"github","broker":"oryxa","return_to":"https://app.oryxa.test/home"}`))
	request.Header.Set("Cookie", authclient.AccessCookieName+"=member-session")
	request.Header.Set("Origin", "https://app.oryxa.test")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if oryxa.connectInput.Toolkit != "github" || oryxa.connectInput.ReturnTo != "https://app.oryxa.test/home" ||
		oryxa.connectInput.Host != "app.oryxa.test" || oryxa.organization != "org_1" || oryxa.credentials.AccessCookie != "member-session" {
		t.Fatalf("input = %+v, organization %q", oryxa.connectInput, oryxa.organization)
	}
	body := decodeBody(t, recorder)
	if body["redirect_url"] != oryxa.connected.RedirectURL {
		t.Fatalf("redirect = %v", body["redirect_url"])
	}
	view := body["connection"].(map[string]any)
	if view["broker"] != "oryxa" || view["status"] != "pending" {
		t.Fatalf("view = %v", view)
	}
	cookie := cookieNamed(t, recorder, signinCookiePrefix)
	if cookie == nil {
		t.Fatalf("no state cookie: %v", recorder.Header().Values("Set-Cookie"))
	}
	if cookie.Name != signinCookieName("opaque-state") || cookie.Value != "opaque-state" || cookie.Path != "/v1/apps/oauth/callback" ||
		cookie.MaxAge != 600 || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" {
		t.Fatalf("cookie = %+v", cookie)
	}
	// The state is in the cookie and the app's URL, never in the body.
	if strings.Contains(fmt.Sprint(view), "opaque-state") {
		t.Fatalf("the view carries the state: %v", view)
	}
}

func TestOryxaKeyConnectAnswersTheActiveConnectionAndNoCookie(t *testing.T) {
	t.Parallel()
	oryxa := &fakeOryxa{connected: application.OryxaConnect{Connection: domain.Connection{
		ID: "apc_1", Toolkit: "github", Broker: domain.BrokerOryxa, CredentialKind: "api_token",
		Status: domain.ConnectionActive, Label: "octocat", CreatedAt: testClock, UpdatedAt: testClock,
	}}}
	server := newOryxaServer(t, oryxa)

	recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/connections",
		`{"toolkit":"github","broker":"oryxa","kind":"api_token","fields":{"token":"ghp_value"}}`, "Authorization", "Bearer member")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if oryxa.connectInput.Kind != "api_token" || oryxa.connectInput.Fields["token"] != "ghp_value" {
		t.Fatalf("input = %+v", oryxa.connectInput)
	}
	body := decodeBody(t, recorder)
	if _, ok := body["redirect_url"]; ok {
		t.Fatalf("a key answered a link: %v", body)
	}
	view := body["connection"].(map[string]any)
	if view["status"] != "active" || view["credential_kind"] != "api_token" || view["broker"] != "oryxa" {
		t.Fatalf("view = %v", view)
	}
	if len(recorder.Result().Cookies()) != 0 {
		t.Fatalf("a key set a cookie: %v", recorder.Result().Cookies())
	}
	if strings.Contains(recorder.Body.String(), "ghp_value") {
		t.Fatal("the answer carries the key")
	}
}

func TestConnectSaysWhichBrokerTakesWhat(t *testing.T) {
	t.Parallel()
	server := newOryxaServer(t, &fakeOryxa{})
	for name, body := range map[string]string{
		"kind for Composio":   `{"toolkit":"github","kind":"api_token"}`,
		"fields for Composio": `{"toolkit":"github","broker":"composio","fields":{"token":"x"}}`,
		"an unknown broker":   `{"toolkit":"github","broker":"nango"}`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/connections", body, "Authorization", "Bearer member")
			if recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_request" {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
			}
		})
	}
	// Composio's connect answers as it always has, and a Composio view says so.
	fake := &fakeService{connection: domain.Connection{ID: "apc_2", Status: domain.ConnectionPending}, redirect: "https://connect.composio.dev/link/lt_1"}
	composioServer := New(fake, Config{Oryxa: &fakeOryxa{}}, nil)
	recorder := call(composioServer, http.MethodPost, "/v1/organizations/org_1/apps/connections", `{"toolkit":"github"}`, "Authorization", "Bearer member")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("composio = %d: %s", recorder.Code, recorder.Body)
	}
	if view := decodeBody(t, recorder)["connection"].(map[string]any); view["broker"] != "composio" {
		t.Fatalf("view = %v", view)
	}
}

func TestInvalidCredentialsNamesTheFieldAndNeverTheValue(t *testing.T) {
	t.Parallel()
	oryxa := &fakeOryxa{err: fmt.Errorf("connect github: %w", &domain.InvalidCredentials{Field: "token", Rule: "was refused by the app"})}
	server := newOryxaServer(t, oryxa)

	recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/connections",
		`{"toolkit":"github","broker":"oryxa","kind":"api_token","fields":{"token":"ghp_typed_value"}}`, "Authorization", "Bearer member")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	failure := decodeBody(t, recorder)["error"].(map[string]any)
	if failure["code"] != "invalid_credentials" || failure["field"] != "token" || failure["message"] != "connect github: token was refused by the app" {
		t.Fatalf("error = %v", failure)
	}
	if strings.Contains(recorder.Body.String(), "ghp_typed_value") {
		t.Fatal("the answer quotes the key")
	}
}

func TestOryxaRoutesSayWhenItIsOff(t *testing.T) {
	t.Parallel()
	server := newServer(t, &fakeService{})
	for _, route := range []struct{ method, target, body string }{
		{http.MethodPost, "/v1/organizations/org_1/apps/connections", `{"toolkit":"github","broker":"oryxa"}`},
		{http.MethodGet, "/v1/apps/oauth/callback?state=s&code=c", ""},
		{http.MethodGet, "/v1/apps/integrations/github/manifest", ""},
		{http.MethodPut, "/v1/organizations/org_1/apps/connections/apc_1/credentials", `{"fields":{"token":"x"}}`},
		{http.MethodGet, "/v1/organizations/org_1/apps/oauth-clients/github", ""},
		{http.MethodPut, "/v1/organizations/org_1/apps/oauth-clients/github", `{"client_id":"x"}`},
		{http.MethodDelete, "/v1/organizations/org_1/apps/oauth-clients/github", ""},
		{http.MethodPost, "/v1/organizations/org_1/apps/webhooks/notion/verification-token", ""},
	} {
		recorder := call(server, route.method, route.target, route.body, "Authorization", "Bearer member")
		if recorder.Code != http.StatusUnprocessableEntity || errorCode(t, recorder) != "not_connectable" {
			t.Errorf("%s %s = %d: %s", route.method, route.target, recorder.Code, recorder.Body)
		}
	}
	recorder := call(server, http.MethodGet, "/v1/apps/integrations", "")
	if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != `{"integrations":[]}` {
		t.Fatalf("integrations = %d %s", recorder.Code, recorder.Body)
	}
	// With no ingress every endpoint is unknown, as the ingress answers one.
	recorder = call(server, http.MethodPost, "/v1/apps/hooks/aph_aaaaaaaaaaaaaaaaaaaaaaaa", `{}`)
	if recorder.Code != http.StatusNotFound || errorCode(t, recorder) != "not_found" {
		t.Fatalf("hooks = %d %s", recorder.Code, recorder.Body)
	}
}

func TestSigninCallbackReadsItsCookieClearsItAndSendsTheBrowserBack(t *testing.T) {
	t.Parallel()
	oryxa := &fakeOryxa{completion: application.Completion{
		Connection: domain.Connection{ID: "apc_1", Status: domain.ConnectionActive},
		ReturnTo:   "https://app.oryxa.test/home?org=org_1&view=tools",
	}}
	server := newOryxaServer(t, oryxa)
	cookie := signinCookieName("opaque-state") + "=opaque-state; " + authclient.AccessCookieName + "=member-session; " +
		signinCookieName("another-flow") + "=another-flow"

	recorder := call(server, http.MethodGet, "/v1/apps/oauth/callback?state=opaque-state&code=code-1", "", "Cookie", cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if oryxa.callback.State != "opaque-state" || oryxa.callback.CookieState != "opaque-state" ||
		oryxa.callback.Code != "code-1" || oryxa.callback.AccessCookie != "member-session" {
		t.Fatalf("callback = %+v", oryxa.callback)
	}
	target, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := target.Query()
	// Exactly what the Composio callback sends back, so web-next's
	// parseConnectReturn reads it unchanged, and the return page's own
	// parameters kept.
	if target.Host != "app.oryxa.test" || target.Path != "/home" || query.Get("app_connection") != "apc_1" ||
		query.Get("status") != "active" || query.Get("view") != "tools" || query.Get("org") != "org_1" || len(query) != 4 {
		t.Fatalf("location = %s", target)
	}
	cleared := cookieNamed(t, recorder, signinCookieName("opaque-state"))
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Path != "/v1/apps/oauth/callback" || !cleared.HttpOnly || !cleared.Secure {
		t.Fatalf("cleared = %+v", cleared)
	}
	if other := cookieNamed(t, recorder, signinCookieName("another-flow")); other != nil {
		t.Fatalf("another sign-in's cookie was touched: %+v", other)
	}
	if recorder.Header().Get("Referrer-Policy") != "no-referrer" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers = %v", recorder.Header())
	}
}

func TestSigninCallbackAnswersJSONWithoutAReturnPageAndErrorsAsTheAPIDoes(t *testing.T) {
	t.Parallel()
	oryxa := &fakeOryxa{completion: application.Completion{Connection: domain.Connection{ID: "apc_1", Status: domain.ConnectionFailed}}}
	server := newOryxaServer(t, oryxa)

	recorder := call(server, http.MethodGet, "/v1/apps/oauth/callback?state=s&error=access_denied", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if view := decodeBody(t, recorder)["connection"].(map[string]any); view["status"] != "failed" {
		t.Fatalf("view = %v", view)
	}
	if oryxa.callback.Error != "access_denied" || oryxa.callback.CookieState != "" {
		t.Fatalf("callback = %+v", oryxa.callback)
	}

	oryxa.err = fmt.Errorf("signin: the callback's state does not match its cookie: %w", domain.ErrForbidden)
	recorder = call(server, http.MethodGet, "/v1/apps/oauth/callback?state=s&code=c", "", "Cookie", signinCookieName("s")+"=other")
	if recorder.Code != http.StatusForbidden || errorCode(t, recorder) != "forbidden" {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if cleared := cookieNamed(t, recorder, signinCookieName("s")); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("a refused callback left its cookie: %v", recorder.Header().Values("Set-Cookie"))
	}
}

func TestSigninCallbackIsRateLimitedPerAddress(t *testing.T) {
	t.Parallel()
	server := newOryxaServer(t, &fakeOryxa{completion: application.Completion{Connection: domain.Connection{ID: "apc_1"}}})
	for attempt := range application.CallbackBudget {
		recorder := call(server, http.MethodGet, "/v1/apps/oauth/callback?state=s", "", "CF-Connecting-IP", "203.0.113.7")
		if recorder.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was limited", attempt+1)
		}
	}
	recorder := call(server, http.MethodGet, "/v1/apps/oauth/callback?state=s", "", "CF-Connecting-IP", "203.0.113.7")
	if recorder.Code != http.StatusTooManyRequests || errorCode(t, recorder) != "rate_limited" {
		t.Fatalf("attempt %d = %d: %s", application.CallbackBudget+1, recorder.Code, recorder.Body)
	}
	if recorder := call(server, http.MethodGet, "/v1/apps/oauth/callback?state=s", "", "CF-Connecting-IP", "198.51.100.2"); recorder.Code == http.StatusTooManyRequests {
		t.Fatal("another address was limited")
	}
}

// The member's own limit is the application's, per member; the API answers
// it as every other limit.
func TestConnectAndKeyReplacementAnswerTheMembersLimit(t *testing.T) {
	t.Parallel()
	server := newOryxaServer(t, &fakeOryxa{err: fmt.Errorf("connecting is limited: %w", domain.ErrRateLimited)})
	for _, route := range []struct{ method, target, body string }{
		{http.MethodPost, "/v1/organizations/org_1/apps/connections", `{"toolkit":"github","broker":"oryxa"}`},
		{http.MethodPut, "/v1/organizations/org_1/apps/connections/apc_1/credentials", `{"fields":{"token":"x"}}`},
	} {
		recorder := call(server, route.method, route.target, route.body, "Authorization", "Bearer member")
		if recorder.Code != http.StatusTooManyRequests || errorCode(t, recorder) != "rate_limited" {
			t.Errorf("%s %s = %d: %s", route.method, route.target, recorder.Code, recorder.Body)
		}
	}
}

func TestReplaceCredentialRoute(t *testing.T) {
	t.Parallel()
	oryxa := &fakeOryxa{connection: domain.Connection{ID: "apc_1", Toolkit: "github", Broker: domain.BrokerOryxa,
		CredentialKind: "api_token", Status: domain.ConnectionActive, Label: "hubot"}}
	server := newOryxaServer(t, oryxa)

	recorder := call(server, http.MethodPut, "/v1/organizations/org_1/apps/connections/apc_1/credentials",
		`{"kind":"api_token","fields":{"token":"ghp_new"}}`, "Authorization", "Bearer member")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if oryxa.replaced != "apc_1" || oryxa.replaceInput.Kind != "api_token" || oryxa.replaceInput.Fields["token"] != "ghp_new" {
		t.Fatalf("replaced %q with %+v", oryxa.replaced, oryxa.replaceInput)
	}
	if view := decodeBody(t, recorder)["connection"].(map[string]any); view["label"] != "hubot" {
		t.Fatalf("view = %v", view)
	}
	if strings.Contains(recorder.Body.String(), "ghp_new") {
		t.Fatal("the answer carries the key")
	}
	// A cookie needs the web app's origin, as every mutation does.
	recorder = call(server, http.MethodPut, "/v1/organizations/org_1/apps/connections/apc_1/credentials",
		`{"fields":{"token":"x"}}`, "Cookie", authclient.AccessCookieName+"=member-session", "Origin", "https://evil.example")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin = %d", recorder.Code)
	}
	if recorder := call(server, http.MethodPut, "/v1/organizations/org_1/apps/connections/apc_1/credentials", `{"token":"x"}`,
		"Authorization", "Bearer member"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field = %d", recorder.Code)
	}
}

// Connecting, replacing a key and managing OAuth clients are refused to an
// agent: the application says forbidden, and the API answers 403.
func TestAgentsAreRefusedTheSigninRoutes(t *testing.T) {
	t.Parallel()
	server := newOryxaServer(t, &fakeOryxa{err: fmt.Errorf("an agent cannot connect an app: %w", domain.ErrForbidden)})
	for _, route := range []struct{ method, target, body string }{
		{http.MethodPost, "/v1/organizations/org_1/apps/connections", `{"toolkit":"github","broker":"oryxa"}`},
		{http.MethodPut, "/v1/organizations/org_1/apps/connections/apc_1/credentials", `{"fields":{"token":"x"}}`},
		{http.MethodGet, "/v1/organizations/org_1/apps/oauth-clients/github", ""},
		{http.MethodPut, "/v1/organizations/org_1/apps/oauth-clients/github", `{"client_id":"x"}`},
		{http.MethodDelete, "/v1/organizations/org_1/apps/oauth-clients/github", ""},
		{http.MethodPost, "/v1/organizations/org_1/apps/webhooks/notion/verification-token", ""},
	} {
		recorder := call(server, route.method, route.target, route.body, "Authorization", "Bearer agent")
		if recorder.Code != http.StatusForbidden || errorCode(t, recorder) != "forbidden" {
			t.Errorf("%s %s = %d: %s", route.method, route.target, recorder.Code, recorder.Body)
		}
	}
}

func TestOAuthClientRoutesKeepTheSecretWriteOnly(t *testing.T) {
	t.Parallel()
	created := testClock
	oryxa := &fakeOryxa{clientView: application.OAuthClientView{
		Client: "github", Saved: true, ClientID: "Iv1.own", SecretSet: true, SecretHint: "wxyz",
		Fields: map[string]string{}, SecretFields: map[string]string{}, RefuseDefault: true, OryxaDefault: true,
		RedirectURI: "https://app.oryxa.test/v1/apps/oauth/callback", Toolkits: []string{"github"},
		Webhooks: []application.OrganizationWebhookView{}, CreatedAt: &created, UpdatedAt: &created,
	}}
	server := newOryxaServer(t, oryxa)

	recorder := call(server, http.MethodPut, "/v1/organizations/org_1/apps/oauth-clients/github",
		`{"client_id":"Iv1.own","client_secret":"own-client-secret-wxyz","refuse_default":true,"fields":{"audience":"https://api.example"},"webhook_secrets":{"slack":"signing-secret-value-1"}}`,
		"Authorization", "Bearer admin")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	input := oryxa.clientInput
	if oryxa.client != "github" || input.ClientID != "Iv1.own" || input.ClientSecret.Reveal() != "own-client-secret-wxyz" ||
		!input.RefuseDefault || input.Fields["audience"] != "https://api.example" || input.WebhookSecrets["slack"].Reveal() != "signing-secret-value-1" {
		t.Fatalf("input = %+v", input)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "own-client-secret") || strings.Contains(body, "signing-secret-value") {
		t.Fatalf("the answer carries a secret: %s", body)
	}
	view := decodeBody(t, recorder)
	if view["client_id"] != "Iv1.own" || view["secret_hint"] != "wxyz" || view["refuse_default"] != true || view["oryxa_default"] != true {
		t.Fatalf("view = %v", view)
	}

	if recorder := call(server, http.MethodGet, "/v1/organizations/org_1/apps/oauth-clients/github", "", "Authorization", "Bearer admin"); recorder.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", recorder.Code, recorder.Body)
	}
	if recorder := call(server, http.MethodDelete, "/v1/organizations/org_1/apps/oauth-clients/github", "", "Authorization", "Bearer admin"); recorder.Code != http.StatusNoContent || oryxa.removed != "github" {
		t.Fatalf("delete = %d, removed %q", recorder.Code, oryxa.removed)
	}

	// Admins only: the application refuses anyone else, and the API says so.
	oryxa.err = fmt.Errorf("only an organization's admins manage its OAuth apps: %w", domain.ErrForbidden)
	if recorder := call(server, http.MethodGet, "/v1/organizations/org_1/apps/oauth-clients/github", "", "Authorization", "Bearer member"); recorder.Code != http.StatusForbidden {
		t.Fatalf("a member = %d", recorder.Code)
	}
	// And like every other route, it needs a caller.
	if recorder := call(server, http.MethodGet, "/v1/organizations/org_1/apps/oauth-clients/github", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no caller = %d", recorder.Code)
	}
}

func TestAnAdminRevealsTheirAppsVerificationTokenOnce(t *testing.T) {
	t.Parallel()
	oryxa := &fakeOryxa{token: "notion-verification-token"}
	server := newOryxaServer(t, oryxa)

	recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/webhooks/notion/verification-token", "", "Authorization", "Bearer admin")
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d, headers %v: %s", recorder.Code, recorder.Header(), recorder.Body)
	}
	body := decodeBody(t, recorder)
	if body["verification_token"] != "notion-verification-token" || body["toolkit"] != "notion" || oryxa.revealed != "notion" {
		t.Fatalf("body = %v", body)
	}
	oryxa.err = fmt.Errorf("the verification token was already shown once: %w", domain.ErrConflict)
	if recorder := call(server, http.MethodPost, "/v1/organizations/org_1/apps/webhooks/notion/verification-token", "", "Authorization", "Bearer admin"); recorder.Code != http.StatusConflict {
		t.Fatalf("again = %d", recorder.Code)
	}
}

func TestTheHooksRouteIsTheIngress(t *testing.T) {
	t.Parallel()
	reached := make(chan string, 1)
	ingress := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		reached <- request.PathValue("endpointID")
		writer.WriteHeader(http.StatusOK)
	})
	server := newOryxaServer(t, &fakeOryxa{}, func(config *Config) { config.Hooks = ingress })

	// No caller: the delivery's signature is its credential, which the
	// ingress checks.
	recorder := call(server, http.MethodPost, "/v1/apps/hooks/aph_abcdefghijklmnopqrstuvwx", `{"type":"url_verification"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	if id := <-reached; id != "aph_abcdefghijklmnopqrstuvwx" {
		t.Fatalf("endpoint = %q", id)
	}
	if recorder := call(server, http.MethodGet, "/v1/apps/hooks/aph_abcdefghijklmnopqrstuvwx", ""); recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("a GET = %d", recorder.Code)
	}
}

// fakeServices verifies the service tokens a test names.
type fakeServices struct{ claims map[string]serviceauth.Claims }

func (fake fakeServices) Verify(_ context.Context, token string) (serviceauth.Claims, error) {
	switch token {
	case "kms-down":
		return serviceauth.Claims{}, errors.New("kms did not answer")
	case "not-allowed":
		return serviceauth.Claims{}, serviceauth.ErrPurposeNotAllowed
	}
	claims, ok := fake.claims[token]
	if !ok {
		return serviceauth.Claims{}, serviceauth.ErrInvalidToken
	}
	return claims, nil
}

// A deployment endpoint serves Oryxa's own app for every organization, so
// its URL and captured token are an operator's, reached with a service
// token: never a member's.
func TestInternalRoutesTakeOnlyAServiceToken(t *testing.T) {
	t.Parallel()
	oryxa := &fakeOryxa{
		deployment: application.DeploymentHookView{Toolkit: "notion", URL: "https://api.oryxa.test/v1/apps/hooks/aph_x", VerificationToken: "ready"},
		token:      "deployment-token",
	}
	services := fakeServices{claims: map[string]serviceauth.Claims{
		"operator": {Service: "operator", Purpose: InternalPurpose},
		"indexer":  {Service: "knowledge", Purpose: "documents"},
	}}
	server := newOryxaServer(t, oryxa, func(config *Config) { config.Services = services })

	for name, test := range map[string]struct {
		header []string
		status int
	}{
		"no token":             {status: http.StatusUnauthorized},
		"a member's own token": {header: []string{"Authorization", "Bearer admin"}, status: http.StatusUnauthorized},
		"a forged token":       {header: []string{serviceauth.Header, "forged"}, status: http.StatusUnauthorized},
		"another purpose":      {header: []string{serviceauth.Header, "indexer"}, status: http.StatusForbidden},
		"a purpose refused":    {header: []string{serviceauth.Header, "not-allowed"}, status: http.StatusForbidden},
		"KMS down":             {header: []string{serviceauth.Header, "kms-down"}, status: http.StatusServiceUnavailable},
		"the operator's":       {header: []string{serviceauth.Header, "operator"}, status: http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := call(server, http.MethodGet, "/v1/internal/apps/hooks/notion", "", test.header...)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.status, recorder.Body)
			}
		})
	}
	recorder := call(server, http.MethodPost, "/v1/internal/apps/hooks/notion/verification-token", "", serviceauth.Header, "operator")
	if recorder.Code != http.StatusOK || decodeBody(t, recorder)["verification_token"] != "deployment-token" {
		t.Fatalf("reveal = %d: %s", recorder.Code, recorder.Body)
	}
	// Without named callers there is no internal route at all.
	bare := newOryxaServer(t, oryxa)
	if recorder := call(bare, http.MethodGet, "/v1/internal/apps/hooks/notion", "", serviceauth.Header, "operator"); recorder.Code != http.StatusNotFound {
		t.Fatalf("without callers = %d", recorder.Code)
	}
	for _, pattern := range bare.Patterns() {
		if strings.Contains(pattern, "/internal/") {
			t.Fatalf("an internal route without callers: %s", pattern)
		}
	}
}

func TestIntegrationsAndManifestNeedNoCaller(t *testing.T) {
	t.Parallel()
	oryxa := &fakeOryxa{manifest: application.ManifestView{Toolkit: "github", Name: "GitHub", Methods: []application.MethodView{}}}
	server := newOryxaServer(t, oryxa)
	recorder := call(server, http.MethodGet, "/v1/apps/integrations", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"toolkit":"github"`) {
		t.Fatalf("integrations = %d: %s", recorder.Code, recorder.Body)
	}
	recorder = call(server, http.MethodGet, "/v1/apps/integrations/github/manifest", "")
	if recorder.Code != http.StatusOK || oryxa.client != "github" {
		t.Fatalf("manifest = %d: %s", recorder.Code, recorder.Body)
	}
	oryxa.err = fmt.Errorf("slack is not switched on for Oryxa's own sign-in: %w", domain.ErrNotConnectable)
	if recorder := call(server, http.MethodGet, "/v1/apps/integrations/slack/manifest", ""); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("not switched on = %d", recorder.Code)
	}
}

func TestOryxasRoutesAreAppsOwn(t *testing.T) {
	t.Parallel()
	patterns := strings.Join(newOryxaServer(t, &fakeOryxa{}).Patterns(), "\n")
	for _, route := range []string{
		"GET /v1/apps/oauth/callback",
		"GET /v1/apps/integrations",
		"GET /v1/apps/integrations/{toolkit}/manifest",
		"PUT /v1/organizations/{organizationID}/apps/connections/{connectionID}/credentials",
		"GET /v1/organizations/{organizationID}/apps/oauth-clients/{client}",
		"PUT /v1/organizations/{organizationID}/apps/oauth-clients/{client}",
		"DELETE /v1/organizations/{organizationID}/apps/oauth-clients/{client}",
		"POST /v1/organizations/{organizationID}/apps/webhooks/{toolkit}/verification-token",
		"POST /v1/apps/hooks/{endpointID}",
	} {
		if !strings.Contains(patterns, route+"\n") && !strings.HasSuffix(patterns, route) {
			t.Errorf("no route %s in:\n%s", route, patterns)
		}
	}
}
