package signin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Every stub server answers as example.com, which httptest's certificate
// names: testHTTPClient dials it on loopback. Anything else, such as
// 127.0.0.1, is another host.
const stubHost = "example.com"

// stubApp is an app as signin meets it: an OAuth authorization server, and
// an API with a test call, revocation endpoints and a few paths to call.
type stubApp struct {
	t      *testing.T
	server *httptest.Server
	base   string

	mu sync.Mutex
	// clients are the OAuth clients it knows, by id, with their secrets,
	// and clientAuth how the token endpoint wants them: header or body.
	clients    map[string]string
	clientAuth string
	codes      map[string]pendingCode
	access     map[string]bool
	refresh    map[string]bool
	serial     int
	// rotating answers every refresh with a new refresh token and retires
	// the one presented.
	rotating  bool
	expiresIn int
	scope     string
	extra     map[string]any
	// refuseRefresh answers invalid_grant to every refresh.
	refuseRefresh bool
	// holdRefresh, when set, keeps each refresh waiting until it closes.
	holdRefresh chan struct{}
	// refreshing is told when a refresh arrives.
	refreshing chan struct{}

	exchanges, refreshes, mints int
	presented                   []string
	tokenForms                  []url.Values

	// apiToken, username and password are what the test call accepts
	// beside the access tokens it issued.
	apiToken, username, password string
	label                        string
	meStatus                     int
	meBody                       any
	meCalls                      int

	// requireHeaders are headers the test call refuses to answer without.
	requireHeaders map[string]string
	// fallback serves every path the stub does not.
	fallback func(http.ResponseWriter, *http.Request)

	revocations  []seenRevocation
	revokeStatus map[string]int
	revokeBody   map[string]any

	calls []seenCall
}

type pendingCode struct {
	clientID, redirectURI, challenge string
}

type seenRevocation struct {
	path, method       string
	form               url.Values
	body               map[string]string
	basicID, basicPass string
	bearer             string
}

type seenCall struct {
	host, method, path, query, authorization string
	header                                   http.Header
}

func newStubApp(t *testing.T) *stubApp {
	t.Helper()
	app := &stubApp{
		t: t, clients: map[string]string{}, clientAuth: integration.ClientAuthBody,
		codes: map[string]pendingCode{}, access: map[string]bool{}, refresh: map[string]bool{},
		rotating: true, expiresIn: 3600, label: "octocat",
		revokeStatus: map[string]int{}, revokeBody: map[string]any{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", app.token)
	mux.HandleFunc("POST /oauth/revoke", app.revoke)
	mux.HandleFunc("POST /oauth/revoke-json", app.revoke)
	mux.HandleFunc("DELETE /api/applications/{client}/token", app.revoke)
	mux.HandleFunc("POST /api/auth.revoke", app.revoke)
	mux.HandleFunc("GET /api/me", app.me)
	mux.HandleFunc("GET /api/redirect", func(w http.ResponseWriter, r *http.Request) {
		app.record(r)
		http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		app.record(r)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		app.mu.Lock()
		fallback := app.fallback
		app.mu.Unlock()
		if fallback == nil {
			http.NotFound(w, r)
			return
		}
		fallback(w, r)
	})
	app.server = httptest.NewTLSServer(mux)
	t.Cleanup(app.server.Close)
	_, port, _ := net.SplitHostPort(app.server.Listener.Addr().String())
	app.base = "https://" + stubHost + ":" + port
	return app
}

// testHTTPClient reaches every stub server: it trusts httptest's
// certificate, and dials example.com on loopback.
func testHTTPClient(app *stubApp) *http.Client {
	transport := app.server.Client().Transport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if host, port, err := net.SplitHostPort(address); err == nil && host == stubHost {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		return dialer.DialContext(ctx, network, address)
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (a *stubApp) record(r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, seenCall{
		host: r.Host, method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
		authorization: r.Header.Get("Authorization"), header: r.Header.Clone(),
	})
}

func (a *stubApp) seenCalls() []seenCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]seenCall(nil), a.calls...)
}

// authorize is the member consenting at the app: it checks the
// authorization URL StartOAuth built and answers the code the app would
// send back.
func (a *stubApp) authorize(t *testing.T, authorizationURL string) (code, state string) {
	t.Helper()
	parsed, err := url.Parse(authorizationURL)
	if err != nil {
		t.Fatalf("parse the authorization URL: %v", err)
	}
	query := parsed.Query()
	if query.Get("response_type") != "code" {
		t.Fatalf("response_type = %q, want code", query.Get("response_type"))
	}
	if method := query.Get("code_challenge_method"); query.Get("code_challenge") != "" && method != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", method)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.serial++
	code = fmt.Sprintf("code-%d", a.serial)
	a.codes[code] = pendingCode{
		clientID: query.Get("client_id"), redirectURI: query.Get("redirect_uri"), challenge: query.Get("code_challenge"),
	}
	return code, query.Get("state")
}

func (a *stubApp) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tokenForms = append(a.tokenForms, r.PostForm)
	clientID, ok := a.authenticate(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		a.exchanges++
		code, known := a.codes[r.PostForm.Get("code")]
		delete(a.codes, r.PostForm.Get("code"))
		verifier := r.PostForm.Get("code_verifier")
		switch {
		case !known || code.clientID != clientID || code.redirectURI != r.PostForm.Get("redirect_uri"):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		case code.challenge != "" && s256(verifier) != code.challenge:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "PKCE"})
			return
		case code.challenge == "" && verifier != "":
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		a.issue(w, true)
	case "refresh_token":
		a.refreshes++
		presented := r.PostForm.Get("refresh_token")
		a.presented = append(a.presented, presented)
		if a.refreshing != nil {
			a.refreshing <- struct{}{}
		}
		if hold := a.holdRefresh; hold != nil {
			a.mu.Unlock()
			<-hold
			a.mu.Lock()
		}
		if a.refuseRefresh || !a.refresh[presented] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
		if a.rotating {
			delete(a.refresh, presented)
		}
		a.issue(w, a.rotating)
	case "client_credentials":
		a.mints++
		a.issue(w, false)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
	}
}

// authenticate checks the client the way the token endpoint wants it, and
// refuses the other way: a secret must never travel both.
func (a *stubApp) authenticate(r *http.Request) (string, bool) {
	basicID, basicSecret, basic := r.BasicAuth()
	formID, formSecret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	var id, secret string
	switch a.clientAuth {
	case integration.ClientAuthHeader:
		if !basic || formSecret != "" {
			return "", false
		}
		id, _ = url.QueryUnescape(basicID)
		secret, _ = url.QueryUnescape(basicSecret)
	default:
		if basic || formSecret == "" {
			return "", false
		}
		id, secret = formID, formSecret
	}
	want, known := a.clients[id]
	return id, known && want == secret
}

func (a *stubApp) issue(w http.ResponseWriter, withRefresh bool) {
	a.serial++
	access := fmt.Sprintf("at-%d-secret-access", a.serial)
	a.access[access] = true
	answer := map[string]any{"access_token": access, "token_type": "bearer"}
	if a.expiresIn > 0 {
		answer["expires_in"] = a.expiresIn
	}
	if withRefresh {
		refresh := fmt.Sprintf("rt-%d-secret-refresh", a.serial)
		a.refresh[refresh] = true
		answer["refresh_token"] = refresh
	}
	if a.scope != "" {
		answer["scope"] = a.scope
	}
	maps.Copy(answer, a.extra)
	writeJSON(w, http.StatusOK, answer)
}

func (a *stubApp) me(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.meCalls++
	a.calls = append(a.calls, seenCall{host: r.Host, method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
		authorization: r.Header.Get("Authorization")})
	for name, value := range a.requireHeaders {
		if r.Header.Get(name) != value {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "missing " + name})
			return
		}
	}
	switch {
	case a.meStatus != 0:
		writeJSON(w, a.meStatus, map[string]string{"message": "not now"})
	case !a.authorized(r):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
	case a.meBody != nil:
		writeJSON(w, http.StatusOK, a.meBody)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"login": a.label, "id": 42})
	}
}

func (a *stubApp) authorized(r *http.Request) bool {
	if user, password, ok := r.BasicAuth(); ok {
		return a.username != "" && user == a.username && password == a.password
	}
	token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !bearer {
		token = r.URL.Query().Get("api_key")
	}
	return token != "" && (a.access[token] || (a.apiToken != "" && token == a.apiToken))
}

func (a *stubApp) revoke(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	seen := seenRevocation{path: r.URL.Path, method: r.Method}
	seen.basicID, seen.basicPass, _ = r.BasicAuth()
	seen.bearer, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(body, &seen.body)
	} else {
		seen.form, _ = url.ParseQuery(string(body))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revocations = append(a.revocations, seen)
	for _, token := range []string{seen.form.Get("token"), seen.body["token"], seen.body["access_token"], seen.bearer} {
		delete(a.access, token)
		delete(a.refresh, token)
	}
	key := r.URL.Path
	if strings.HasPrefix(key, "/api/applications/") {
		key = "github"
	}
	status, answer := a.revokeStatus[key], a.revokeBody[key]
	if status == 0 {
		status = http.StatusOK
		if key == "github" {
			status = http.StatusNoContent
		}
	}
	if answer == nil {
		answer = map[string]any{"ok": true}
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, answer)
}

func (a *stubApp) seenRevocations() []seenRevocation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]seenRevocation(nil), a.revocations...)
}

func (a *stubApp) counts() (exchanges, refreshes, mints int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exchanges, a.refreshes, a.mints
}

func (a *stubApp) set(change func(*stubApp)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	change(a)
}

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// fakeIntegration is an integration with a manifest and no tools, as every
// phase 1 integration is.
type fakeIntegration struct{ manifest integration.Manifest }

func (f fakeIntegration) Manifest() integration.Manifest         { return f.manifest }
func (f fakeIntegration) Tools() map[string]integration.ToolFunc { return nil }

// acmeManifest is a GitHub-like app on the stub: OAuth with PKCE, form
// client authentication and rotating refresh tokens, revoked RFC 7009's way;
// or a personal token; or a password.
func acmeManifest(base string) integration.Manifest {
	return integration.Manifest{
		Toolkit: "acme", Version: 1, Name: "Acme", APIBase: base + "/api",
		Auth: integration.Auth{
			Methods: []integration.Method{
				{Kind: integration.KindOAuth2Code, OAuth2: &integration.OAuth2{
					Client: "acme", AuthURL: base + "/oauth/authorize", TokenURL: base + "/oauth/token",
					RevokeURL: base + "/oauth/revoke", RevokeStyle: integration.RevokeRFC7009,
					Scopes: []string{"read", "write"}, PKCE: true, ClientAuth: integration.ClientAuthBody, Rotating: true,
				}},
				{Kind: integration.KindAPIToken, Place: &integration.Place{In: integration.InHeader, Name: "Authorization", Template: "Bearer {token}"}},
				{Kind: integration.KindBasic},
			},
			Test: integration.Test{Path: "/me", Label: "/login", AccountRef: "/id"},
		},
	}
}

// Test clients: Oryxa's default, and an organization's own.
const (
	defaultClientID     = "oryxa-acme"
	defaultClientSecret = "oryxa-acme-client-secret-0001"
	testRedirectURI     = "https://api.oryxa.test/v1/apps/oauth/callback"
	testOrganization    = "org_1"
	testMember          = "mem_1"
	testReturnTo        = "https://app.oryxa.test/tools"
)

type harness struct {
	t       *testing.T
	app     *stubApp
	store   *memStore
	keys    *fakeKeys
	clock   *testClock
	service *Service
	// allowReturn is what Config.ReturnAllowed answers.
	mu          sync.Mutex
	allowReturn bool
	logs        *lockedBuffer
}

// newHarness builds a service on the stub app, with the manifests build
// makes from the stub's base URL (acme's alone by default), and Oryxa's
// default client saved for every registration they use.
func newHarness(t *testing.T, build ...func(base string) integration.Manifest) *harness {
	t.Helper()
	app := newStubApp(t)
	if len(build) == 0 {
		build = append(build, acmeManifest)
	}
	registry := integration.NewRegistry()
	registrations := map[string]bool{}
	for _, manifestFor := range build {
		manifest := manifestFor(app.base)
		if err := registry.Register(fakeIntegration{manifest: manifest}); err != nil {
			t.Fatalf("register %s: %v", manifest.Toolkit, err)
		}
		for _, method := range manifest.Auth.Methods {
			if method.OAuth2 != nil && method.OAuth2.Client != "" {
				registrations[method.OAuth2.Client] = true
			}
		}
	}
	h := &harness{
		t: t, app: app, store: newMemStore(), keys: &fakeKeys{},
		clock:       &testClock{now: time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)},
		allowReturn: true, logs: &lockedBuffer{},
	}
	service, err := New(Dependencies{
		Store:        h.store,
		Sealer:       secrets.Envelope{Keys: h.keys, KeyID: "alias/test", Service: SealService},
		Integrations: registry,
		HTTP:         testHTTPClient(app),
		Logger:       slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock:        h.clock.Now,
	}, Config{
		RedirectURI: testRedirectURI,
		ReturnAllowed: func(returnTo string) bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.allowReturn && strings.HasPrefix(returnTo, "https://app.oryxa.test/")
		},
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	service.timings = timings{lease: time.Minute, refresh: 10 * time.Second, leaseWait: 3 * time.Second, leasePoll: 5 * time.Millisecond}
	h.service = service
	app.clients[defaultClientID] = defaultClientSecret
	for registration := range registrations {
		// A registration whose client needs fields of its own is left to
		// the test to save.
		fields, _ := service.registration(registration)
		if slices.ContainsFunc(fields, func(field integration.Field) bool { return field.Validate.Required }) {
			continue
		}
		if _, _, err := service.PutClient(context.Background(), ClientInput{
			Client: registration, ClientID: defaultClientID, ClientSecret: defaultClientSecret,
		}); err != nil {
			t.Fatalf("save Oryxa's default %s client: %v", registration, err)
		}
	}
	return h
}

func (h *harness) setReturnAllowed(allowed bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.allowReturn = allowed
}

// start makes a pending connection and starts signing it in.
func (h *harness) start(connectionID, toolkit string) Started {
	h.t.Helper()
	h.store.addConnection(connectionID, testOrganization, testMember, toolkit, "pending")
	started, err := h.service.StartOAuth(context.Background(), StartRequest{
		OrganizationID: testOrganization, MemberID: testMember, ConnectionID: connectionID,
		Toolkit: toolkit, ReturnTo: testReturnTo,
	})
	if err != nil {
		h.t.Fatalf("StartOAuth() = %v", err)
	}
	return started
}

// connect signs a connection in through OAuth, and makes it active as
// apps' application layer would.
func (h *harness) connect(connectionID, toolkit string) Completion {
	h.t.Helper()
	started := h.start(connectionID, toolkit)
	code, state := h.app.authorize(h.t, started.AuthorizationURL)
	completion, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code})
	if err != nil {
		h.t.Fatalf("CompleteOAuth() = %v", err)
	}
	h.store.setStatus(connectionID, "active")
	return completion
}

// get calls path under the stub through a session's HTTP.
func get(t *testing.T, session integration.Session, target string) (*http.Response, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := session.HTTP.Do(request)
	if err == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	return response, err
}

// lockedBuffer collects log lines from any goroutine.
type lockedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func (a *stubApp) presentedTokens() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.presented...)
}

func (a *stubApp) refreshLive(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refresh[token]
}

func (a *stubApp) tokenForm(index int) url.Values {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tokenForms[index]
}

func (a *stubApp) accessLive(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.access[token]
}
