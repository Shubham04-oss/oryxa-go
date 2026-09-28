package httpserver_test

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/slack"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// TestOryxaSigninEndToEnd runs Oryxa's own sign-in as apps serves it: the
// HTTP API, the application, signin and hooks, on apps' own Postgres schema,
// against a stand-in OAuth app. It walks what a member does (connect with
// OAuth, come back through the callback with the state cookie, connect a
// key, replace it, disconnect) and what the deployment's app does (verify
// its webhook URL), and checks what reached the outbox.
//
// It needs a throwaway database, as apps' store tests do:
// APPS_TEST_DATABASE_URL=postgres://… go test ./apps/internal/httpserver/
func TestOryxaSigninEndToEnd(t *testing.T) {
	store := openThrowawayStore(t)
	ctx := context.Background()
	provider := newStubProvider(t)

	registry := integration.NewRegistry()
	for _, registered := range []integration.Integration{stubGitHub{provider.server.URL}, slack.New()} {
		if err := registry.Register(registered); err != nil {
			t.Fatal(err)
		}
	}
	envelope := secrets.Envelope{Keys: plainKeys{}, KeyID: "alias/test", Service: signin.SealService}
	returnAllowed, err := application.ReturnChecker([]string{"https://app.oryxa.test"})
	if err != nil {
		t.Fatal(err)
	}
	const redirectURI = "https://app.oryxa.test/v1/apps/oauth/callback"
	signinService, err := signin.New(signin.Dependencies{
		Store: store, Sealer: envelope, Integrations: registry, HTTP: provider.server.Client(),
	}, signin.Config{RedirectURI: redirectURI, ReturnAllowed: returnAllowed})
	if err != nil {
		t.Fatal(err)
	}
	hooksService, err := hooks.New(hooks.Dependencies{
		Store: store, Sealer: envelope, Integrations: registry, Subscriptions: application.HookSubscriptions{},
	}, hooks.Config{
		PublicBaseURL:     "https://api.oryxa.test",
		DeploymentSecrets: map[string][]secrets.Secret{"slack": {"slack-signing-secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := application.New(application.Dependencies{
		Store: store, Directory: stubDirectory{},
		Oryxa: &application.Oryxa{
			Store: store, Signin: signinService, Hooks: hooksService, Integrations: registry,
			Toolkits: []string{"github", "slack"}, RedirectURI: redirectURI,
		},
		Logger: slog.New(slog.DiscardHandler),
	}, application.Config{PublicBaseURL: "https://api.oryxa.test", ReturnOrigins: []string{"https://app.oryxa.test"}})
	if err != nil {
		t.Fatal(err)
	}
	server := httpserver.New(app, httpserver.Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, Oryxa: app, Hooks: hooksService.Handler(),
	}, slog.New(slog.DiscardHandler))
	// Oryxa's own GitHub app, which members sign in through by default.
	if _, _, err := signinService.PutClient(ctx, signin.ClientInput{
		Client: "github", ClientID: "oryxa-github", ClientSecret: "oryxa-github-client-secret",
	}); err != nil {
		t.Fatal(err)
	}

	// 1. A member connects GitHub from the web app, which calls apps on its
	// own origin. The answer is the app's authorization URL, and the state
	// cookie for the callback's path.
	recorder := serve(server, http.MethodPost, "https://app.oryxa.test/v1/organizations/org_1/apps/connections",
		`{"toolkit":"github","broker":"oryxa","return_to":"https://app.oryxa.test/home?view=tools"}`,
		"Cookie", authclient.AccessCookieName+"=member-session", "Origin", "https://app.oryxa.test")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("connect = %d: %s", recorder.Code, recorder.Body)
	}
	var connected struct {
		Connection struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Broker string `json:"broker"`
		} `json:"connection"`
		RedirectURL string `json:"redirect_url"`
	}
	decodeInto(t, recorder, &connected)
	if connected.Connection.Status != "pending" || connected.Connection.Broker != "oryxa" {
		t.Fatalf("connection = %+v", connected.Connection)
	}
	authorize, err := url.Parse(connected.RedirectURL)
	if err != nil {
		t.Fatal(err)
	}
	query := authorize.Query()
	if !strings.HasPrefix(connected.RedirectURL, provider.server.URL+"/authorize?") || query.Get("client_id") != "oryxa-github" ||
		query.Get("redirect_uri") != redirectURI || query.Get("code_challenge_method") != "S256" || query.Get("state") == "" {
		t.Fatalf("authorization URL = %s", connected.RedirectURL)
	}
	stateCookie := findCookie(recorder, "oryxa_apps_signin_")
	if stateCookie == nil || stateCookie.Value != query.Get("state") || stateCookie.Path != "/v1/apps/oauth/callback" {
		t.Fatalf("state cookie = %+v", stateCookie)
	}

	// 2. The member grants access at the app, which sends the browser back
	// to the callback with a code; the browser brings the state cookie and
	// its own session.
	code := provider.grant(query.Get("code_challenge"))
	callback := "https://app.oryxa.test/v1/apps/oauth/callback?state=" + url.QueryEscape(query.Get("state")) + "&code=" + code
	recorder = serve(server, http.MethodGet, callback, "",
		"Cookie", stateCookie.Name+"="+stateCookie.Value+"; "+authclient.AccessCookieName+"=member-session")
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d: %s", recorder.Code, recorder.Body)
	}
	back, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if back.Query().Get("app_connection") != connected.Connection.ID || back.Query().Get("status") != "active" ||
		back.Query().Get("view") != "tools" {
		t.Fatalf("sent back to %s", back)
	}
	connection := getConnection(t, server, connected.Connection.ID, "Bearer member")
	if connection["status"] != "active" || connection["label"] != "octocat" || connection["credential_kind"] != "oauth2_code" {
		t.Fatalf("connection = %v", connection)
	}
	// The state is spent: the same callback again finds no sign-in.
	recorder = serve(server, http.MethodGet, callback, "", "Cookie", stateCookie.Name+"="+stateCookie.Value)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("a replayed callback = %d: %s", recorder.Code, recorder.Body)
	}

	// 3. Another member connects GitHub with a personal access token instead,
	// then replaces it: a wrong one is refused naming the field, and the
	// right one is kept and announced.
	recorder = serve(server, http.MethodPost, "https://api.oryxa.test/v1/organizations/org_1/apps/connections",
		`{"toolkit":"github","broker":"oryxa","kind":"api_token","fields":{"token":"pat-first-0123456789"}}`,
		"Authorization", "Bearer other")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("key connect = %d: %s", recorder.Code, recorder.Body)
	}
	var keyed struct {
		Connection struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"connection"`
	}
	decodeInto(t, recorder, &keyed)
	if keyed.Connection.Status != "active" {
		t.Fatalf("key connection = %+v", keyed.Connection)
	}
	replace := "https://api.oryxa.test/v1/organizations/org_1/apps/connections/" + keyed.Connection.ID + "/credentials"
	recorder = serve(server, http.MethodPut, replace, `{"fields":{"token":"pat-wrong-0123456789"}}`, "Authorization", "Bearer other")
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_credentials"`) ||
		strings.Contains(recorder.Body.String(), "pat-wrong") {
		t.Fatalf("a wrong key = %d: %s", recorder.Code, recorder.Body)
	}
	recorder = serve(server, http.MethodPut, replace, `{"fields":{"token":"pat-second-0123456789"}}`, "Authorization", "Bearer other")
	if recorder.Code != http.StatusOK {
		t.Fatalf("replace = %d: %s", recorder.Code, recorder.Body)
	}

	// 4. The first member disconnects: the token is revoked at the app.
	recorder = serve(server, http.MethodDelete, "https://api.oryxa.test/v1/organizations/org_1/apps/connections/"+connected.Connection.ID, "",
		"Authorization", "Bearer member")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("remove = %d: %s", recorder.Code, recorder.Body)
	}
	if revoked := provider.revokedCount(); revoked != 1 {
		t.Fatalf("revocations at the app = %d", revoked)
	}

	// 5. The deployment's own Slack app verifies its request URL: a signed
	// url_verification is answered with its challenge, and an unsigned one
	// is refused.
	endpoint, err := app.DeploymentHook(ctx, "slack")
	if err != nil {
		t.Fatal(err)
	}
	hookURL, err := url.Parse(endpoint.URL)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"type":"url_verification","challenge":"challenge-token"}`
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("slack-signing-secret"))
	mac.Write([]byte("v0:" + timestamp + ":" + body))
	recorder = serve(server, http.MethodPost, "https://api.oryxa.test"+hookURL.Path, body,
		"X-Slack-Request-Timestamp", timestamp, "X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)), "Content-Type", "application/json")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "challenge-token") {
		t.Fatalf("a signed url_verification = %d: %s", recorder.Code, recorder.Body)
	}
	recorder = serve(server, http.MethodPost, "https://api.oryxa.test"+hookURL.Path, body, "X-Slack-Request-Timestamp", timestamp)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("an unsigned delivery = %d: %s", recorder.Code, recorder.Body)
	}

	// 6. The key's owner revokes it at the app. The daily health check, run
	// by the upkeep worker once it is due, finds the app refusing it and
	// expires the connection.
	provider.forget("pat-second-0123456789")
	if _, err := store.Pool().Exec(ctx, `UPDATE apps_credentials SET next_check_at = now() - interval '1 minute' WHERE connection_id = $1`,
		keyed.Connection.ID); err != nil {
		t.Fatal(err)
	}
	upkeep, stopUpkeep := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() { app.RunOryxa(upkeep); close(stopped) }()
	deadline := time.Now().Add(20 * time.Second)
	for getConnection(t, server, keyed.Connection.ID, "Bearer other")["status"] != "expired" {
		if time.Now().After(deadline) {
			t.Fatal("the upkeep did not expire a connection whose key the app refused")
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopUpkeep()
	<-stopped

	// 7. That member leaves the organization, which takes the connection
	// with it; a key is its owner's to revoke, so nothing is revoked.
	if err := app.MemberRemoved(ctx, authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-2"}); err != nil {
		t.Fatal(err)
	}

	// 8. What reached the outbox, in order, for the two connections.
	events := outbox(t, store)
	want := []string{
		appsv1.ConnectionInitiatedDetailType, appsv1.ConnectionConnectedDetailType, // the OAuth sign-in
		appsv1.ConnectionInitiatedDetailType, appsv1.ConnectionConnectedDetailType, // the key
		appsv1.CredentialReplacedDetailType,
		appsv1.ConnectionRemovedDetailType, // the OAuth sign-in, disconnected
		appsv1.ConnectionExpiredDetailType, // the key, refused
		appsv1.ConnectionRemovedDetailType, // the key, with its member
	}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", detailTypes(events), want)
	}
	for index, event := range events {
		if event.DetailType != want[index] {
			t.Fatalf("events = %v, want %v", detailTypes(events), want)
		}
		if event.DetailType != appsv1.CredentialReplacedDetailType && event.Data["broker"] != appsv1.BrokerOryxa {
			t.Fatalf("event %s has no broker: %v", event.DetailType, event.Data)
		}
		for _, secret := range []string{"pat-first", "pat-second", "oryxa-github-client-secret", "at-", "rt-"} {
			if strings.Contains(fmt.Sprint(event.Data), secret) {
				t.Fatalf("event %s carries %s: %v", event.DetailType, secret, event.Data)
			}
		}
	}
	if removed := events[5].Data; removed["provider_revoked"] != true || removed["reason"] != appsv1.RemovedByMember {
		t.Fatalf("removed = %v", removed)
	}
	if expired := events[6].Data; expired["reason"] != appsv1.ExpiredCredentialRejected || expired["connection_id"] != keyed.Connection.ID {
		t.Fatalf("expired = %v", expired)
	}
	if left := events[7].Data; left["provider_revoked"] != false || left["reason"] != appsv1.RemovedMemberLeft {
		t.Fatalf("left = %v", left)
	}
	// Nothing of either connection's credential is left behind.
	var credentials int
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM apps_credentials`).Scan(&credentials); err != nil || credentials != 0 {
		t.Fatalf("credentials left = %d, %v", credentials, err)
	}
}

func serve(server http.Handler, method, target, body string, headers ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func decodeInto(t *testing.T, recorder *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), into); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body, err)
	}
}

func findCookie(recorder *httptest.ResponseRecorder, prefix string) *http.Cookie {
	for _, cookie := range recorder.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, prefix) {
			return cookie
		}
	}
	return nil
}

func getConnection(t *testing.T, server http.Handler, id, authorization string) map[string]any {
	t.Helper()
	recorder := serve(server, http.MethodGet, "https://api.oryxa.test/v1/organizations/org_1/apps/connections/"+id, "", "Authorization", authorization)
	if recorder.Code != http.StatusOK {
		t.Fatalf("get connection = %d: %s", recorder.Code, recorder.Body)
	}
	var view map[string]any
	decodeInto(t, recorder, &view)
	return view
}

type outboxEvent struct {
	DetailType string
	Data       map[string]any
}

func outbox(t *testing.T, store *postgres.Store) []outboxEvent {
	t.Helper()
	rows, err := store.Pool().Query(context.Background(), `SELECT detail_type, detail FROM apps_outbox ORDER BY occurred_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []outboxEvent
	for rows.Next() {
		var event outboxEvent
		var detail []byte
		if err := rows.Scan(&event.DetailType, &detail); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(detail, &event.Data); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func detailTypes(events []outboxEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.DetailType)
	}
	return types
}

// openThrowawayStore migrates a schema of its own in APPS_TEST_DATABASE_URL
// and drops it afterwards, as apps' store tests do.
func openThrowawayStore(t *testing.T) *postgres.Store {
	t.Helper()
	databaseURL := os.Getenv("APPS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("APPS_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schemaName := "apps_test_" + hex.EncodeToString(random)
	schema := pgx.Identifier{schemaName}.Sanitize()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		_ = admin.Close(cleanup)
	})
	scoped, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	values := scoped.Query()
	values.Set("search_path", schemaName+",public")
	scoped.RawQuery = values.Encode()
	store, err := postgres.Open(ctx, scoped.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store
}

// plainKeys stands in for KMS: a wrapped key unwraps only with the
// encryption context it was made for.
type plainKeys struct{}

func (plainKeys) GenerateDataKey(_ context.Context, _ string, encryptionContext map[string]string) ([]byte, []byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, nil, err
	}
	wrapped, err := json.Marshal(map[string]any{"key": key, "context": encryptionContext})
	return key, wrapped, err
}

func (plainKeys) DecryptDataKey(_ context.Context, wrapped []byte, encryptionContext map[string]string) ([]byte, error) {
	var unwrapped struct {
		Key     []byte            `json:"key"`
		Context map[string]string `json:"context"`
	}
	if err := json.Unmarshal(wrapped, &unwrapped); err != nil || !maps.Equal(unwrapped.Context, encryptionContext) {
		return nil, errors.New("the encryption context does not match")
	}
	return unwrapped.Key, nil
}

// stubDirectory is auth: two members of org_1, by bearer token, and the
// first one's browser session.
type stubDirectory struct{}

func (stubDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	members := map[string]string{"Bearer member": "user-1", "Bearer other": "user-2", "cookie:member-session": "user-1"}
	key := credentials.Authorization
	if key == "" {
		key = "cookie:" + credentials.AccessCookie
	}
	user, ok := members[key]
	if !ok {
		return domain.OrgMembership{}, domain.ErrUnauthorized
	}
	if organizationID != "org_1" {
		return domain.OrgMembership{}, domain.ErrForbidden
	}
	return domain.OrgMembership{OrganizationID: organizationID, UserID: user, Role: domain.OrgRoleMember}, nil
}

// stubGitHub is GitHub's sign-in, pointed at the stand-in app.
type stubGitHub struct{ base string }

func (stub stubGitHub) Manifest() integration.Manifest {
	return integration.Manifest{
		Toolkit: "github", Version: 1, Name: "GitHub", APIBase: stub.base + "/api",
		Auth: integration.Auth{
			Methods: []integration.Method{
				{Kind: integration.KindOAuth2Code, OAuth2: &integration.OAuth2{
					Client: "github", AuthURL: stub.base + "/authorize", TokenURL: stub.base + "/token", RevokeURL: stub.base + "/revoke",
					Scopes: []string{"repo"}, PKCE: true, ClientAuth: integration.ClientAuthBody, Rotating: true,
				}},
				{Kind: integration.KindAPIToken, Place: &integration.Place{In: integration.InHeader, Name: "Authorization", Template: "Bearer {token}"}},
			},
			Test: integration.Test{Path: "/me", Label: "/login", AccountRef: "/id"},
		},
	}
}

func (stubGitHub) Tools() map[string]integration.ToolFunc { return nil }

// stubProvider is an OAuth app with PKCE, a test call and RFC 7009
// revocation.
type stubProvider struct {
	server *httptest.Server

	mu         sync.Mutex
	challenges map[string]string // code → challenge
	live       map[string]bool
	serial     int
	revoked    int
}

func newStubProvider(t *testing.T) *stubProvider {
	t.Helper()
	provider := &stubProvider{challenges: map[string]string{}, live: map[string]bool{"pat-first-0123456789": true, "pat-second-0123456789": true}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", provider.token)
	mux.HandleFunc("GET /api/me", provider.me)
	mux.HandleFunc("POST /revoke", provider.revoke)
	provider.server = httptest.NewTLSServer(mux)
	t.Cleanup(provider.server.Close)
	return provider
}

// grant is the member granting access: a code, bound to the PKCE challenge
// the authorization request carried.
func (provider *stubProvider) grant(challenge string) string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.serial++
	code := fmt.Sprintf("code-%d", provider.serial)
	provider.challenges[code] = challenge
	return code
}

func (provider *stubProvider) token(writer http.ResponseWriter, request *http.Request) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	_ = request.ParseForm()
	if request.PostForm.Get("client_secret") != "oryxa-github-client-secret" || request.PostForm.Get("client_id") != "oryxa-github" {
		http.Error(writer, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	code := request.PostForm.Get("code")
	sum := sha256.Sum256([]byte(request.PostForm.Get("code_verifier")))
	challenge, ok := provider.challenges[code]
	delete(provider.challenges, code)
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":"invalid_grant"}`)
		return
	}
	provider.serial++
	access, refresh := fmt.Sprintf("at-%d", provider.serial), fmt.Sprintf("rt-%d", provider.serial)
	provider.live[access], provider.live[refresh] = true, true
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"access_token": access, "refresh_token": refresh, "token_type": "bearer", "expires_in": 3600,
	})
}

func (provider *stubProvider) me(writer http.ResponseWriter, request *http.Request) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if !provider.live[strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")] {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	_, _ = io.WriteString(writer, `{"login":"octocat","id":583231}`)
}

func (provider *stubProvider) revoke(writer http.ResponseWriter, request *http.Request) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	_ = request.ParseForm()
	delete(provider.live, request.PostForm.Get("token"))
	provider.revoked++
	writer.WriteHeader(http.StatusOK)
}

// forget is the app no longer accepting a credential.
func (provider *stubProvider) forget(credential string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	delete(provider.live, credential)
}

func (provider *stubProvider) revokedCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.revoked
}
