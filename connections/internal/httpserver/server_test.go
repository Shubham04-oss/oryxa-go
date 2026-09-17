package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/application"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// fakeService records what each call received and answers with err when it
// is set.
type fakeService struct {
	err error

	credentials  authclient.Credentials
	organization string
	id           string
	createApp    application.CreateAppInput
	updateApp    application.UpdateAppInput
	status       domain.AppStatus
	filter       application.ConnectionFilter
	authorize    application.AuthorizeInput
	callback     []string
	sessionID    string
	returnURI    string
	admin        bool
	reconnected  bool
	calls        int

	createConnection application.CreateConnectionInput
	credentialInput  domain.CredentialInput
	updateConnection application.UpdateConnectionInput
	actor            application.Actor
	tokenRequest     application.TokenRequest
	holder           string
}

// recordActor records an internal-route call, the way record does for
// member-credentialed ones.
func (service *fakeService) recordActor(actor application.Actor, organizationID string) {
	service.calls++
	service.actor, service.organization = actor, organizationID
}

var testApp = domain.App{ID: "app_1", OrganizationID: "org_1", Provider: "github", CredentialType: domain.CredentialOAuth2,
	Scopes: []string{"read:user"}, Status: domain.AppStatusActive, ClientID: "client-1",
	EncryptedSecret: &domain.SealedData{Version: 1, Ciphertext: []byte("sealed-secret")}}

var testConnection = domain.Connection{ID: "con_1", OrganizationID: "org_1", AppID: "app_1", Provider: "github",
	CredentialType: domain.CredentialOAuth2, Scope: domain.ScopeMember, MemberID: "user-1", Usage: domain.UsageMembers,
	ProviderAccount: domain.ProviderAccount{ID: "42", Name: "octocat"}, Scopes: []string{"read:user"}, Status: domain.StatusActive,
	Health:               domain.ConnectionHealth{State: domain.HealthUnknown},
	EncryptedCredentials: &domain.SealedData{Version: 1, Ciphertext: []byte("sealed-grant")}}

var testAuthorization = application.Authorization{URL: "https://github.com/login/oauth/authorize?state=s", ExpiresAt: time.Date(2026, 9, 14, 10, 10, 0, 0, time.UTC)}

var testIssuedToken = domain.IssuedToken{
	TokenType: "Bearer", AccessToken: "token-xyz", Placement: domain.TokenPlacement{In: "header", Name: "Authorization", Prefix: "Bearer "},
	Scopes: []string{"read:user"}, ProviderAccount: domain.ProviderAccount{ID: "42", Name: "octocat"},
}

// fakeVerifier records the token it was asked to verify and answers with
// claims or err.
type fakeVerifier struct {
	claims serviceauth.Claims
	err    error
	token  string
}

func (verifier *fakeVerifier) Verify(_ context.Context, token string) (serviceauth.Claims, error) {
	verifier.token = token
	return verifier.claims, verifier.err
}

func (service *fakeService) record(credentials authclient.Credentials, organizationID string) {
	service.calls++
	service.credentials, service.organization = credentials, organizationID
}

func (service *fakeService) ListApps(_ context.Context, credentials authclient.Credentials, organizationID string) (application.AppList, error) {
	service.record(credentials, organizationID)
	return application.AppList{Apps: []domain.App{testApp}, Admin: service.admin}, service.err
}

func (service *fakeService) CreateApp(_ context.Context, credentials authclient.Credentials, organizationID string, input application.CreateAppInput) (domain.App, error) {
	service.record(credentials, organizationID)
	service.createApp = input
	return testApp, service.err
}

func (service *fakeService) UpdateApp(_ context.Context, credentials authclient.Credentials, organizationID, appID string, input application.UpdateAppInput) (domain.App, error) {
	service.record(credentials, organizationID)
	service.id, service.updateApp = appID, input
	return testApp, service.err
}

func (service *fakeService) SetAppStatus(_ context.Context, credentials authclient.Credentials, organizationID, appID string, status domain.AppStatus) (domain.App, error) {
	service.record(credentials, organizationID)
	service.id, service.status = appID, status
	return testApp, service.err
}

func (service *fakeService) DeleteApp(_ context.Context, credentials authclient.Credentials, organizationID, appID string) error {
	service.record(credentials, organizationID)
	service.id = appID
	return service.err
}

func (service *fakeService) Providers() []application.ProviderDescription {
	service.calls++
	return []application.ProviderDescription{{
		Provider: "atlassian",
		CredentialTypes: []application.CredentialTypeDescription{
			{CredentialType: domain.CredentialOAuth2, Client: true, Authorizes: true, RequiredScopes: []string{"read:me"}},
			{
				CredentialType: domain.CredentialBasic, Fields: []string{"username", "password"},
				Domain: &application.DomainDescription{Required: true, Example: "acme.atlassian.net"},
			},
		},
	}}
}

func (service *fakeService) ListConnections(_ context.Context, credentials authclient.Credentials, organizationID string, filter application.ConnectionFilter) ([]domain.Connection, error) {
	service.record(credentials, organizationID)
	service.filter = filter
	return []domain.Connection{testConnection}, service.err
}

func (service *fakeService) Authorize(_ context.Context, credentials authclient.Credentials, organizationID string, input application.AuthorizeInput) (application.Authorization, error) {
	service.record(credentials, organizationID)
	service.authorize = input
	return testAuthorization, service.err
}

func (service *fakeService) Callback(_ context.Context, provider string, params application.CallbackParams) (string, error) {
	service.calls++
	service.callback = []string{provider, params.State, params.Code, params.Error}
	return "https://app.example.test/done?session_id=handle-1", service.err
}

func (service *fakeService) Complete(_ context.Context, credentials authclient.Credentials, organizationID, sessionID string) (application.Completion, error) {
	service.record(credentials, organizationID)
	service.sessionID = sessionID
	return application.Completion{Connection: testConnection, Reconnected: service.reconnected}, service.err
}

func (service *fakeService) GetConnection(_ context.Context, credentials authclient.Credentials, organizationID, connectionID string) (domain.Connection, error) {
	service.record(credentials, organizationID)
	service.id = connectionID
	return testConnection, service.err
}

func (service *fakeService) Reconnect(_ context.Context, credentials authclient.Credentials, organizationID, connectionID, returnURI string) (application.Authorization, error) {
	service.record(credentials, organizationID)
	service.id, service.returnURI = connectionID, returnURI
	return testAuthorization, service.err
}

func (service *fakeService) Disconnect(_ context.Context, credentials authclient.Credentials, organizationID, connectionID string) error {
	service.record(credentials, organizationID)
	service.id = connectionID
	return service.err
}

func (service *fakeService) CreateConnection(_ context.Context, credentials authclient.Credentials, organizationID string, input application.CreateConnectionInput) (domain.Connection, error) {
	service.record(credentials, organizationID)
	service.createConnection = input
	return testConnection, service.err
}

func (service *fakeService) ReplaceCredential(_ context.Context, credentials authclient.Credentials, organizationID, connectionID string, credential domain.CredentialInput) (domain.Connection, error) {
	service.record(credentials, organizationID)
	service.id, service.credentialInput = connectionID, credential
	return testConnection, service.err
}

func (service *fakeService) UpdateConnection(_ context.Context, credentials authclient.Credentials, organizationID, connectionID string, input application.UpdateConnectionInput) (domain.Connection, error) {
	service.record(credentials, organizationID)
	service.id, service.updateConnection = connectionID, input
	return testConnection, service.err
}

func (service *fakeService) IssueToken(_ context.Context, actor application.Actor, connectionID string, request application.TokenRequest) (domain.IssuedToken, error) {
	service.recordActor(actor, request.OrganizationID)
	service.id, service.tokenRequest = connectionID, request
	return testIssuedToken, service.err
}

func (service *fakeService) InternalConnection(_ context.Context, actor application.Actor, organizationID, connectionID string) (domain.Connection, error) {
	service.recordActor(actor, organizationID)
	service.id = connectionID
	return testConnection, service.err
}

func (service *fakeService) AddHold(_ context.Context, actor application.Actor, organizationID, connectionID, holder string) error {
	service.recordActor(actor, organizationID)
	service.id, service.holder = connectionID, holder
	return service.err
}

func (service *fakeService) ReleaseHold(_ context.Context, actor application.Actor, organizationID, connectionID, holder string) error {
	service.recordActor(actor, organizationID)
	service.id, service.holder = connectionID, holder
	return service.err
}

func (service *fakeService) Report(_ context.Context, actor application.Actor, organizationID, connectionID string) error {
	service.recordActor(actor, organizationID)
	service.id = connectionID
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

// serveInternal calls an internal route as integrations, with a service
// token the fake verifier accepts for purpose. headers may forward a member.
func serveInternal(service *fakeService, purpose, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	config := Config{Services: &fakeVerifier{claims: serviceauth.Claims{Service: "integrations", Purpose: purpose}}}
	return serveConfig(service, config, method, path, body, append([]string{"Authorization", "", serviceauth.Header, "svc-token-1"}, headers...)...)
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
	server := New(&fakeService{}, Config{}, nil)
	patterns := server.Patterns()
	if len(patterns) < 23 {
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
	recorder = serve(&fakeService{}, http.MethodGet, "/v1/organizations/org_1/connections", "")
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET connections = %d %v", recorder.Code, recorder.Header())
	}
}

func TestCredentialsAndOrigin(t *testing.T) {
	service := &fakeService{}
	if recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/apps", "", "Authorization", ""); recorder.Code != http.StatusUnauthorized ||
		errorCode(t, recorder) != "unauthorized" || service.calls != 0 {
		t.Fatalf("no credentials = %d %s", recorder.Code, recorder.Body)
	}
	if recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/apps", "", "Authorization", "Basic abc"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("non-bearer authorization = %d", recorder.Code)
	}

	cookie := authclient.AccessCookieName + "=cookie-1"
	if recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/apps", "", "Authorization", "", "Cookie", cookie); recorder.Code != http.StatusOK ||
		service.credentials.AccessCookie != "cookie-1" {
		t.Fatalf("cookie GET = %d %+v", recorder.Code, service.credentials)
	}
	for _, origin := range []string{"", "https://evil.example.test"} {
		recorder := serve(service, http.MethodDelete, "/v1/organizations/org_1/connections/con_1", "", "Authorization", "", "Cookie", cookie, "Origin", origin)
		if recorder.Code != http.StatusForbidden || errorCode(t, recorder) != "forbidden" {
			t.Fatalf("cookie DELETE from %q = %d", origin, recorder.Code)
		}
	}
	if recorder := serve(service, http.MethodDelete, "/v1/organizations/org_1/connections/con_1", "", "Authorization", "", "Cookie", cookie,
		"Origin", "https://app.example.test"); recorder.Code != http.StatusNoContent || service.id != "con_1" {
		t.Fatalf("cookie DELETE from the app = %d", recorder.Code)
	}
	if recorder := serve(service, http.MethodDelete, "/v1/organizations/org_1/connections/con_2", ""); recorder.Code != http.StatusNoContent ||
		service.credentials.Authorization != "Bearer token-1" || service.id != "con_2" {
		t.Fatalf("bearer DELETE = %d %+v", recorder.Code, service.credentials)
	}
}

func TestProviders(t *testing.T) {
	service := &fakeService{}
	recorder := serve(service, http.MethodGet, "/v1/providers", "")
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, `"provider":"atlassian"`) ||
		!strings.Contains(body, `"credential_type":"basic"`) || !strings.Contains(body, `"example":"acme.atlassian.net"`) {
		t.Fatalf("GET providers = %d %s", recorder.Code, recorder.Body)
	}
	// The catalogue is the same for everyone, so it answers without a session
	// and names no organization.
	recorder = serve(service, http.MethodGet, "/v1/providers", "", "Authorization", "")
	if recorder.Code != http.StatusOK || service.organization != "" {
		t.Fatalf("GET providers without credentials = %d %q", recorder.Code, service.organization)
	}
}

func TestApps(t *testing.T) {
	service := &fakeService{}
	recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/apps", "")
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "client_id") || strings.Contains(recorder.Body.String(), "sealed") {
		t.Fatalf("member GET apps = %d %s", recorder.Code, recorder.Body)
	}
	service.admin = true
	recorder = serve(service, http.MethodGet, "/v1/organizations/org_1/apps", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"client_id":"client-1"`) ||
		strings.Contains(recorder.Body.String(), "sealed") || service.organization != "org_1" {
		t.Fatalf("admin GET apps = %d %s", recorder.Code, recorder.Body)
	}

	recorder = serve(service, http.MethodPost, "/v1/organizations/org_1/apps",
		`{"provider":"github","credential_type":"oauth2","client_id":"client-1","client_secret":"secret-1","scopes":["repo"]}`)
	if recorder.Code != http.StatusCreated || strings.Contains(recorder.Body.String(), "secret-1") ||
		service.createApp.Provider != "github" || service.createApp.ClientSecret != "secret-1" || service.createApp.CredentialType != domain.CredentialOAuth2 {
		t.Fatalf("POST apps = %d %s %+v", recorder.Code, recorder.Body, service.createApp)
	}
	if recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/apps", `{"provider":"github","bogus_field":"x"}`); recorder.Code != http.StatusBadRequest ||
		errorCode(t, recorder) != "invalid_request" {
		t.Fatalf("POST apps with an unknown field = %d", recorder.Code)
	}
	if recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/apps", `{"provider":"github"} {}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST apps with two objects = %d", recorder.Code)
	}

	recorder = serve(service, http.MethodPost, "/v1/organizations/org_1/apps",
		`{"provider":"github","credential_type":"installation","github_app_id":"12345","github_app_slug":"oryxa-bot","private_key":"-----BEGIN KEY-----"}`)
	if recorder.Code != http.StatusCreated || strings.Contains(recorder.Body.String(), "BEGIN KEY") ||
		service.createApp.GitHubAppID != "12345" || service.createApp.GitHubAppSlug != "oryxa-bot" || service.createApp.PrivateKey != "-----BEGIN KEY-----" {
		t.Fatalf("POST apps with a GitHub App = %d %s %+v", recorder.Code, recorder.Body, service.createApp)
	}
	recorder = serve(service, http.MethodPost, "/v1/organizations/org_1/apps", `{"provider":"atlassian","credential_type":"basic","domain":"acme.atlassian.net"}`)
	if recorder.Code != http.StatusCreated || service.createApp.Domain != "acme.atlassian.net" || service.createApp.CredentialType != domain.CredentialBasic {
		t.Fatalf("POST apps with a domain = %d %s %+v", recorder.Code, recorder.Body, service.createApp)
	}

	recorder = serve(service, http.MethodPatch, "/v1/organizations/org_1/apps/app_1", `{"client_secret":"secret-2"}`)
	if recorder.Code != http.StatusOK || service.id != "app_1" || service.updateApp.ClientSecret != "secret-2" || service.updateApp.Scopes != nil {
		t.Fatalf("PATCH app = %d %+v", recorder.Code, service.updateApp)
	}
	if recorder := serve(service, http.MethodPatch, "/v1/organizations/org_1/apps/app_1", `{"scopes":[]}`); recorder.Code != http.StatusOK ||
		service.updateApp.Scopes == nil {
		t.Fatalf("PATCH app with empty scopes = %d %+v", recorder.Code, service.updateApp)
	}
	recorder = serve(service, http.MethodPatch, "/v1/organizations/org_1/apps/app_1", `{"private_key":"-----BEGIN KEY 2-----"}`)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "BEGIN KEY") || service.updateApp.PrivateKey != "-----BEGIN KEY 2-----" {
		t.Fatalf("PATCH app private_key = %d %s %+v", recorder.Code, recorder.Body, service.updateApp)
	}
	for path, status := range map[string]domain.AppStatus{"disable": domain.AppStatusDisabled, "enable": domain.AppStatusActive} {
		if recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/apps/app_1/"+path, ""); recorder.Code != http.StatusOK || service.status != status {
			t.Fatalf("POST %s = %d %s", path, recorder.Code, service.status)
		}
	}
	if recorder := serve(service, http.MethodDelete, "/v1/organizations/org_1/apps/app_1", ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("DELETE app = %d", recorder.Code)
	}
}

func TestConnectionRoutes(t *testing.T) {
	service := &fakeService{}
	recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/connections?app_id=app_1&scope=team&team_id=team_1", "")
	body := decodeBody(t, recorder)
	connections, _ := body["connections"].([]any)
	if recorder.Code != http.StatusOK || len(connections) != 1 || service.filter != (application.ConnectionFilter{AppID: "app_1", Scope: domain.ScopeTeam, TeamID: "team_1"}) ||
		strings.Contains(recorder.Body.String(), "sealed") {
		t.Fatalf("GET connections = %d %s %+v", recorder.Code, recorder.Body, service.filter)
	}
	if recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/connections?scope=everyone", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("GET connections with an unknown scope = %d", recorder.Code)
	}

	recorder = serve(service, http.MethodPost, "/v1/organizations/org_1/connections/authorize",
		`{"app_id":"app_1","scope":"organization","usage":"admins","return_uri":"https://app.example.test/done"}`)
	body = decodeBody(t, recorder)
	if recorder.Code != http.StatusOK || body["authorization_url"] != testAuthorization.URL || body["expires_at"] != "2026-09-14T10:10:00Z" ||
		service.authorize != (application.AuthorizeInput{AppID: "app_1", Scope: domain.ScopeOrganization, Usage: domain.UsageAdmins, ReturnURI: "https://app.example.test/done"}) {
		t.Fatalf("POST authorize = %d %s %+v", recorder.Code, recorder.Body, service.authorize)
	}

	recorder = serve(service, http.MethodPost, "/v1/organizations/org_1/connections/complete", `{"session_id":"handle-1"}`)
	body = decodeBody(t, recorder)
	if recorder.Code != http.StatusCreated || body["id"] != "con_1" || body["member_id"] != "user-1" || service.sessionID != "handle-1" ||
		strings.Contains(recorder.Body.String(), "sealed") || strings.Contains(recorder.Body.String(), "version") {
		t.Fatalf("POST complete = %d %s", recorder.Code, recorder.Body)
	}
	service.reconnected = true
	if recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/connections/complete", `{"session_id":"handle-2"}`); recorder.Code != http.StatusOK {
		t.Fatalf("POST complete for a reconnect = %d", recorder.Code)
	}

	if recorder := serve(service, http.MethodGet, "/v1/organizations/org_1/connections/con_1", ""); recorder.Code != http.StatusOK || service.id != "con_1" {
		t.Fatalf("GET connection = %d", recorder.Code)
	}
	recorder = serve(service, http.MethodPost, "/v1/organizations/org_1/connections/con_1/reconnect", `{"return_uri":"https://app.example.test/again"}`)
	if recorder.Code != http.StatusOK || service.id != "con_1" || service.returnURI != "https://app.example.test/again" {
		t.Fatalf("POST reconnect = %d %s", recorder.Code, recorder.Body)
	}
}

func TestCreateConnection(t *testing.T) {
	service := &fakeService{}
	recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/connections",
		`{"app_id":"app_1","scope":"team","team_id":"team_1","usage":"members","label":"Team key","credential":{"api_key":"key-1"}}`)
	body := decodeBody(t, recorder)
	wantInput := application.CreateConnectionInput{
		AppID: "app_1", Scope: domain.ScopeTeam, TeamID: "team_1", Usage: domain.UsageMembers, Label: "Team key",
		Credential: domain.CredentialInput{APIKey: "key-1"},
	}
	if recorder.Code != http.StatusCreated || body["id"] != "con_1" || service.createConnection != wantInput || service.organization != "org_1" {
		t.Fatalf("POST connections = %d %s %+v", recorder.Code, recorder.Body, service.createConnection)
	}

	if recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/connections",
		`{"app_id":"app_1","credential":{"api_key":"key-1","bogus":"y"}}`); recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_request" {
		t.Fatalf("POST connections with an unknown nested field = %d", recorder.Code)
	}
	if recorder := serve(service, http.MethodPost, "/v1/organizations/org_1/connections",
		`{"app_id":"app_1","bogus":"y","credential":{"api_key":"key-1"}}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST connections with an unknown top-level field = %d", recorder.Code)
	}
}

func TestUpdateConnection(t *testing.T) {
	service := &fakeService{}
	if recorder := serve(service, http.MethodPatch, "/v1/organizations/org_1/connections/con_1", `{}`); recorder.Code != http.StatusBadRequest ||
		errorCode(t, recorder) != "invalid_request" {
		t.Fatalf("PATCH connection with no fields = %d %s", recorder.Code, recorder.Body)
	}

	recorder := serve(service, http.MethodPatch, "/v1/organizations/org_1/connections/con_1", `{"default":false}`)
	if recorder.Code != http.StatusOK || service.id != "con_1" || service.updateConnection.Label != nil ||
		service.updateConnection.Default == nil || *service.updateConnection.Default != false {
		t.Fatalf("PATCH connection default:false = %d %+v", recorder.Code, service.updateConnection)
	}

	recorder = serve(service, http.MethodPatch, "/v1/organizations/org_1/connections/con_1", `{"label":"New label"}`)
	if recorder.Code != http.StatusOK || service.updateConnection.Default != nil ||
		service.updateConnection.Label == nil || *service.updateConnection.Label != "New label" {
		t.Fatalf("PATCH connection label = %d %+v", recorder.Code, service.updateConnection)
	}

	if recorder := serve(service, http.MethodPatch, "/v1/organizations/org_1/connections/con_1", `{"label":"x","bogus":1}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("PATCH connection with an unknown field = %d", recorder.Code)
	}
}

func TestReplaceCredential(t *testing.T) {
	service := &fakeService{}
	recorder := serve(service, http.MethodPut, "/v1/organizations/org_1/connections/con_1/credential", `{"credential":{"token":"tok-1"}}`)
	body := decodeBody(t, recorder)
	if recorder.Code != http.StatusOK || body["id"] != "con_1" || service.id != "con_1" ||
		service.credentialInput != (domain.CredentialInput{Token: "tok-1"}) {
		t.Fatalf("PUT credential = %d %s %+v", recorder.Code, recorder.Body, service.credentialInput)
	}
	if recorder := serve(service, http.MethodPut, "/v1/organizations/org_1/connections/con_1/credential",
		`{"credential":{"token":"tok-1","extra":"z"}}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("PUT credential with an unknown nested field = %d", recorder.Code)
	}
}

func TestCallback(t *testing.T) {
	service := &fakeService{}
	request := httptest.NewRequest(http.MethodGet, "/v1/oauth/callback/github?state=state-1&code=code-1", nil)
	recorder := httptest.NewRecorder()
	New(service, Config{}, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "https://app.example.test/done?session_id=handle-1" ||
		recorder.Header().Get("Referrer-Policy") != "no-referrer" || recorder.Header().Get("Cache-Control") != "no-store" ||
		strings.Join(service.callback, ",") != "github,state-1,code-1," {
		t.Fatalf("callback = %d %v %v", recorder.Code, recorder.Header(), service.callback)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/oauth/callback/github?state=state-1&error=access_denied", nil)
	recorder = httptest.NewRecorder()
	New(service, Config{}, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusSeeOther || service.callback[3] != "access_denied" {
		t.Fatalf("denied callback = %d %v", recorder.Code, service.callback)
	}

	for failure, status := range map[error]int{domain.ErrNotFound: http.StatusBadRequest, errors.New("database down"): http.StatusInternalServerError} {
		service.err = failure
		request := httptest.NewRequest(http.MethodGet, "/v1/oauth/callback/github?state=unknown&code=code-1", nil)
		recorder := httptest.NewRecorder()
		New(service, Config{}, nil).ServeHTTP(recorder, request)
		if recorder.Code != status || recorder.Header().Get("Location") != "" || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/plain") ||
			strings.Contains(recorder.Body.String(), "database") || strings.Contains(recorder.Body.String(), "code-1") {
			t.Fatalf("callback failing with %v = %d %v %s", failure, recorder.Code, recorder.Header(), recorder.Body)
		}
	}
}

func TestErrorCodes(t *testing.T) {
	for _, test := range []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{fmt.Errorf("return_uri must use https: %w", domain.ErrInvalid), http.StatusBadRequest, "invalid_request", "return_uri must use https"},
		{domain.ErrUnauthorized, http.StatusUnauthorized, "unauthorized", ""},
		{domain.ErrForbidden, http.StatusForbidden, "forbidden", ""},
		{domain.ErrAgentForbidden, http.StatusForbidden, "forbidden", "an agent cannot change this"},
		{domain.ErrNotFound, http.StatusNotFound, "not_found", ""},
		{domain.ErrAlreadyConnected, http.StatusConflict, "already_connected", ""},
		{domain.ErrAccountChanged, http.StatusConflict, "account_changed", ""},
		{domain.ErrNeedsReconnect, http.StatusConflict, "needs_reconnect", ""},
		{domain.ErrAppDisabled, http.StatusConflict, "app_disabled", ""},
		{fmt.Errorf("update app: %w", domain.ErrConflict), http.StatusConflict, "conflict", ""},
		{domain.ScopeRequiredError{Missing: []string{"repo", "workflow"}}, http.StatusUnprocessableEntity, "scope_required", "scope required: repo workflow"},
		{fmt.Errorf("%w: invalid_grant", domain.ErrCredentialRejected), http.StatusUnprocessableEntity, "credential_rejected", "the provider rejected the credential"},
		{domain.ErrSessionExpired, http.StatusBadRequest, "session_expired", ""},
		{fmt.Errorf("%w: token endpoint returned 502", domain.ErrProviderUnavailable), http.StatusServiceUnavailable, "provider_unavailable", ""},
		{domain.ErrRateLimited, http.StatusTooManyRequests, "rate_limited", "too many requests; retry later"},
		{errors.New("open client secret: kms: access denied"), http.StatusInternalServerError, "internal", "internal server error"},
	} {
		recorder := serve(&fakeService{err: test.err}, http.MethodPost, "/v1/organizations/org_1/connections/complete", `{"session_id":"handle-1"}`)
		body := decodeBody(t, recorder)
		failure, _ := body["error"].(map[string]any)
		if recorder.Code != test.status || failure["code"] != test.code || (test.message != "" && failure["message"] != test.message) {
			t.Fatalf("%v = %d %s", test.err, recorder.Code, recorder.Body)
		}
	}
}

func TestIssueToken(t *testing.T) {
	service := &fakeService{}
	recorder := serveInternal(service, "action", http.MethodPost, "/v1/internal/connections/con_1/token",
		`{"organization_id":"org_1","purpose":"action","acting_member_id":"user-1","scopes":["read:user"],"token":"user"}`)
	body := decodeBody(t, recorder)
	request := service.tokenRequest
	if recorder.Code != http.StatusOK || recorder.Header().Get("Pragma") != "no-cache" || body["access_token"] != "token-xyz" ||
		body["token_type"] != "Bearer" || service.id != "con_1" || request.OrganizationID != "org_1" || request.Purpose != domain.PurposeAction ||
		request.ActingMemberID != "user-1" || !slices.Equal(request.Scopes, []string{"read:user"}) || request.Token != "user" ||
		service.actor.Service != "integrations" || service.actor.Purpose != domain.PurposeAction || service.actor.Credentials.Present() {
		t.Fatalf("POST token = %d %s %+v %+v", recorder.Code, recorder.Body, service.tokenRequest, service.actor)
	}
	if recorder := serveInternal(service, "action", http.MethodPost, "/v1/internal/connections/con_1/token", `{"organization_id":"org_1","bogus":1}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST token with an unknown field = %d", recorder.Code)
	}
}

// TestIssueTokenForwardsAnAgentToken proves the internal API places no extra
// restriction on a forwarded agent token: connections never resolves a
// membership for an internal route at all, so token issuance for one runs
// exactly as it does for any forwarded member, which integrations relies on.
func TestIssueTokenForwardsAnAgentToken(t *testing.T) {
	service := &fakeService{}
	recorder := serveInternal(service, "action", http.MethodPost, "/v1/internal/connections/con_1/token",
		`{"organization_id":"org_1","purpose":"action","acting_member_id":"user-1"}`, "Authorization", "Bearer agent-access-token")
	if recorder.Code != http.StatusOK || service.actor.Service != "integrations" ||
		service.actor.Credentials.Authorization != "Bearer agent-access-token" {
		t.Fatalf("POST token with a forwarded agent token = %d %+v", recorder.Code, service.actor)
	}
}

func TestInternalConnection(t *testing.T) {
	service := &fakeService{}
	recorder := serveInternal(service, "action", http.MethodGet, "/v1/internal/connections/con_1?organization_id=org_1", "")
	if recorder.Code != http.StatusOK || service.id != "con_1" || service.organization != "org_1" || service.actor.Service != "integrations" {
		t.Fatalf("GET internal connection = %d %s", recorder.Code, recorder.Body)
	}
}

func TestHolds(t *testing.T) {
	service := &fakeService{}
	if recorder := serveInternal(service, "cleanup", http.MethodPost, "/v1/internal/connections/con_1/holds", `{"organization_id":"org_1","holder":"integrations"}`); recorder.Code != http.StatusNoContent ||
		service.id != "con_1" || service.organization != "org_1" || service.holder != "integrations" || service.actor.Service != "integrations" {
		t.Fatalf("POST holds = %d %s", recorder.Code, recorder.Body)
	}

	service = &fakeService{}
	if recorder := serveInternal(service, "cleanup", http.MethodDelete, "/v1/internal/connections/con_1/holds/integrations?organization_id=org_1", ""); recorder.Code != http.StatusNoContent ||
		service.id != "con_1" || service.organization != "org_1" || service.holder != "integrations" {
		t.Fatalf("DELETE hold = %d %s", recorder.Code, recorder.Body)
	}
}

func TestReport(t *testing.T) {
	service := &fakeService{}
	if recorder := serveInternal(service, "action", http.MethodPost, "/v1/internal/connections/con_1/report", `{"organization_id":"org_1"}`); recorder.Code != http.StatusNoContent ||
		service.id != "con_1" || service.organization != "org_1" || service.actor.Service != "integrations" {
		t.Fatalf("POST report = %d %s", recorder.Code, recorder.Body)
	}
}

// TestServiceIdentity covers how an internal route resolves its caller: always
// a service that proves its identity with serviceauth.Header, together with the
// bearer token of the member it acts for when it forwards one.
func TestServiceIdentity(t *testing.T) {
	accepting := func() *fakeVerifier {
		return &fakeVerifier{claims: serviceauth.Claims{Service: "integrations", Purpose: "action"}}
	}

	// A valid service token sets Actor.Service and Actor.Purpose, and no
	// member credentials.
	service := &fakeService{}
	verifier := accepting()
	recorder := serveConfig(service, Config{Services: verifier}, http.MethodGet, "/v1/internal/connections/con_1?organization_id=org_1", "",
		"Authorization", "", serviceauth.Header, "svc-token-1")
	if recorder.Code != http.StatusOK || service.actor.Service != "integrations" || service.actor.Purpose != domain.PurposeAction ||
		service.actor.Credentials.Present() || verifier.token != "svc-token-1" {
		t.Fatalf("valid service token = %d %+v", recorder.Code, service.actor)
	}

	// Internal routes hand out credentials, so a member's own bearer token or
	// session cookie is never enough on any of them, even from the app's
	// origin.
	cookie := authclient.AccessCookieName + "=cookie-1"
	for _, route := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/internal/connections/con_1/token", `{"organization_id":"org_1","purpose":"action"}`},
		{http.MethodGet, "/v1/internal/connections/con_1?organization_id=org_1", ""},
		{http.MethodPost, "/v1/internal/connections/con_1/holds", `{"organization_id":"org_1","holder":"integrations"}`},
		{http.MethodDelete, "/v1/internal/connections/con_1/holds/integrations?organization_id=org_1", ""},
		{http.MethodPost, "/v1/internal/connections/con_1/report", `{"organization_id":"org_1"}`},
	} {
		for name, headers := range map[string][]string{
			"a member's bearer token": nil,
			"a session cookie":        {"Authorization", "", "Cookie", cookie, "Origin", "https://app.example.test"},
		} {
			service = &fakeService{}
			if recorder := serveConfig(service, Config{Services: accepting()}, route.method, route.path, route.body, headers...); recorder.Code != http.StatusUnauthorized ||
				errorCode(t, recorder) != "unauthorized" || service.calls != 0 {
				t.Fatalf("%s alone on %s %s = %d %s", name, route.method, route.path, recorder.Code, recorder.Body)
			}
		}
	}

	// A service carrying out a member's action forwards the member's bearer
	// token with its own token. The session cookie is never read.
	service = &fakeService{}
	if recorder := serveConfig(service, Config{Services: accepting()}, http.MethodPost, "/v1/internal/connections/con_1/token",
		`{"organization_id":"org_1","purpose":"action"}`, serviceauth.Header, "svc-token-1", "Cookie", cookie); recorder.Code != http.StatusOK ||
		service.actor.Service != "integrations" || service.actor.Credentials.Authorization != "Bearer token-1" || service.actor.Credentials.AccessCookie != "" {
		t.Fatalf("service token with a forwarded member = %d %+v", recorder.Code, service.actor)
	}

	// A forwarded member must be a bearer token.
	service = &fakeService{}
	if recorder := serveConfig(service, Config{Services: accepting()}, http.MethodPost, "/v1/internal/connections/con_1/token",
		`{"organization_id":"org_1","purpose":"action"}`, "Authorization", "Basic dXNlcjpwYXNz", serviceauth.Header, "svc-token-1"); recorder.Code != http.StatusBadRequest ||
		errorCode(t, recorder) != "invalid_request" || service.calls != 0 {
		t.Fatalf("non-bearer forwarded member = %d %s", recorder.Code, recorder.Body)
	}

	// An invalid token is 401.
	service = &fakeService{}
	if recorder := serveConfig(service, Config{Services: &fakeVerifier{err: serviceauth.ErrInvalidToken}}, http.MethodGet,
		"/v1/internal/connections/con_1?organization_id=org_1", "", "Authorization", "", serviceauth.Header, "bad-token"); recorder.Code != http.StatusUnauthorized ||
		errorCode(t, recorder) != "unauthorized" || service.calls != 0 {
		t.Fatalf("invalid service token = %d %s", recorder.Code, recorder.Body)
	}

	// A purpose the caller may not use is 403.
	if recorder := serveConfig(&fakeService{}, Config{Services: &fakeVerifier{err: serviceauth.ErrPurposeNotAllowed}}, http.MethodGet,
		"/v1/internal/connections/con_1?organization_id=org_1", "", "Authorization", "", serviceauth.Header, "token-1"); recorder.Code != http.StatusForbidden ||
		errorCode(t, recorder) != "forbidden" {
		t.Fatalf("purpose not allowed = %d %s", recorder.Code, recorder.Body)
	}

	// Any other verifier error, such as KMS being unavailable, is 503, and
	// its detail is never sent to the caller.
	if recorder := serveConfig(&fakeService{}, Config{Services: &fakeVerifier{err: errors.New("kms: unavailable")}}, http.MethodGet,
		"/v1/internal/connections/con_1?organization_id=org_1", "", "Authorization", "", serviceauth.Header, "token-1"); recorder.Code != http.StatusServiceUnavailable ||
		errorCode(t, recorder) != "unavailable" || strings.Contains(recorder.Body.String(), "kms") {
		t.Fatalf("verifier error = %d %s", recorder.Code, recorder.Body)
	}

	// No verifier configured means the internal API accepts no one.
	if recorder := serveConfig(&fakeService{}, Config{}, http.MethodGet, "/v1/internal/connections/con_1?organization_id=org_1", "",
		"Authorization", "", serviceauth.Header, "token-1"); recorder.Code != http.StatusUnauthorized || errorCode(t, recorder) != "unauthorized" {
		t.Fatalf("no verifier configured = %d %s", recorder.Code, recorder.Body)
	}
}
