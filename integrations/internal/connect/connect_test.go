package connect

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/egress"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/orgapps"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/vault"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// The service satisfies what the egress proxy asks of it.
var _ egress.Connections = (*Service)(nil)

// fakeKMS wraps a data key as a handle and unwraps it only for the encryption
// context it was generated for, as KMS does.
type fakeKMS struct {
	mu   sync.Mutex
	n    int
	keys map[string]fakeWrapped
}

type fakeWrapped struct {
	key     []byte
	context map[string]string
}

func (f *fakeKMS) GenerateDataKey(_ context.Context, _ string, ec map[string]string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	if f.keys == nil {
		f.keys = map[string]fakeWrapped{}
	}
	key := sha256.Sum256([]byte(fmt.Sprint("data key ", f.n)))
	handle := fmt.Sprint("wrapped-", f.n)
	f.keys[handle] = fakeWrapped{key: key[:], context: maps.Clone(ec)}
	return append([]byte(nil), key[:]...), []byte(handle), nil
}

func (f *fakeKMS) DecryptDataKey(_ context.Context, encrypted []byte, ec map[string]string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.keys[string(encrypted)]
	if !ok || !maps.Equal(w.context, ec) {
		return nil, errors.New("kms context mismatch")
	}
	return append([]byte(nil), w.key...), nil
}

// fakeApp is an OAuth 2 server with the endpoints a connection uses. It
// rotates refresh tokens, as the strictest apps do.
type fakeApp struct {
	*httptest.Server
	mu         sync.Mutex
	challenge  string // the PKCE challenge the last authorization used
	issued     int
	refreshes  atomic.Int32
	revoked    []string
	grantError string        // answered to a refresh_token grant, when set
	down       bool          // answers 503 to a refresh_token grant
	meRefused  bool          // /me refuses every token
	expiresIn  int           // seconds; 0 answers none
	refreshLag time.Duration // how long a refresh takes
	validCode  string
	// revokeStatus answers a revocation, when set; 200 otherwise.
	revokeStatus int
}

func newFakeApp(t *testing.T) *fakeApp {
	t.Helper()
	app := &fakeApp{expiresIn: 3600, validCode: "code-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", app.token)
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		app.mu.Lock()
		defer app.mu.Unlock()
		if app.revokeStatus != 0 {
			w.WriteHeader(app.revokeStatus)
			return
		}
		app.revoked = append(app.revoked, r.Form.Get("token"))
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		app.mu.Lock()
		defer app.mu.Unlock()
		if app.meRefused || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer access-") {
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	app.Server = httptest.NewTLSServer(mux)
	t.Cleanup(app.Close)
	return app
}

func (a *fakeApp) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if r.Form.Get("client_id") != "client-1" || r.Form.Get("client_secret") != "shhh-client-secret-value" {
		a.answer(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("code") != a.validCode || base64.RawURLEncoding.EncodeToString(sum[:]) != a.challenge ||
			r.Form.Get("redirect_uri") != "https://integrations.example.com"+CallbackPath {
			a.answer(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
	case "refresh_token":
		a.refreshes.Add(1)
		time.Sleep(a.refreshLag)
		switch {
		case a.down:
			a.answer(w, http.StatusServiceUnavailable, map[string]any{})
			return
		case a.grantError != "":
			a.answer(w, http.StatusBadRequest, map[string]any{"error": a.grantError})
			return
		case r.Form.Get("refresh_token") != fmt.Sprintf("refresh-%d", a.issued):
			// A rotated refresh token is single use: an old one is dead.
			a.answer(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
	}
	a.issued++
	answer := map[string]any{"access_token": fmt.Sprintf("access-%d", a.issued), "refresh_token": fmt.Sprintf("refresh-%d", a.issued),
		"scope": "read write"}
	if a.expiresIn > 0 {
		answer["expires_in"] = a.expiresIn
	}
	a.answer(w, http.StatusOK, answer)
}

func (a *fakeApp) answer(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func (a *fakeApp) manifest() *manifest.Manifest {
	return &manifest.Manifest{
		ID: "google", ConnectionLevels: []string{"personal", "org"}, BaseURL: a.URL, EgressAllowlist: []string{"127.0.0.1"},
		Auth: manifest.Auth{CredentialModes: []string{"oauth2"}, OAuth2: &manifest.OAuth2{
			AuthorizationURL: a.URL + "/authorize", TokenURL: a.URL + "/token", RevokeURL: a.URL + "/revoke", PKCE: true,
			AuthorizationParams: map[string]string{"access_type": "offline", "state": "forged"},
			TokenKinds:          []manifest.TokenKind{{Name: "user", Scopes: []string{"read", "write"}}, {Name: "extra", Scopes: []string{"admin"}, Optional: true}},
		}},
		Verify: &manifest.Verify{Path: "/me"},
	}
}

type rig struct {
	*Service
	app   *fakeApp
	org   string
	now   time.Time
	clock *atomic.Int64
}

const clientSecret = "shhh-client-secret-value"

// testNonce is the nonce the browser that starts every test sign-in keeps,
// and testCookie that browser's cookies.
const testNonce = "browser-nonce-1"

var testBrowserHash = hashState(testNonce)

func testCookie(name string) string {
	if name == BrowserCookieName(testBrowserHash) {
		return testNonce
	}
	return ""
}

// The tests need a Postgres with the schema's migrations applied; see
// integrations/vault_integration_test.go. The rows they make are removed after.
func newRig(t *testing.T) *rig {
	t.Helper()
	url := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: url, ApplicationName: "oryxa-connect-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	sealer, err := vault.New(&fakeKMS{}, "alias/test")
	if err != nil {
		t.Fatal(err)
	}
	apps := orgapps.New(database.Pool, sealer)
	fake := newFakeApp(t)
	r := &rig{app: fake, org: fmt.Sprintf("test_org_%d", time.Now().UnixNano()), clock: &atomic.Int64{}}
	r.clock.Store(time.Now().UnixNano())
	r.Service = New(database.Pool, sealer, apps, Config{
		RedirectURI: "https://integrations.example.com" + CallbackPath, ReturnOrigins: []string{"https://app.example.com"},
		Manifest: func(app string) (*manifest.Manifest, bool) { return fake.manifest(), app == "google" },
		Client:   fake.Client(),
		Now:      func() time.Time { return time.Unix(0, r.clock.Load()) },
	})
	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM integrations.credentials WHERE connection_id IN (SELECT id FROM integrations.connections WHERE org_id = $1)`,
			`DELETE FROM integrations.connections WHERE org_id = $1`,
			`DELETE FROM integrations.oauth_states WHERE org_id = $1`,
			`DELETE FROM integrations.outbox WHERE detail->>'organization_id' = $1`,
			`DELETE FROM integrations.org_apps WHERE org_id = $1`,
		} {
			database.Pool.Exec(context.Background(), sql, r.org)
		}
	})
	if _, _, err := apps.Put(ctx, r.org, "google", orgapps.Input{ClientID: "client-1", Secret: clientSecret}); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) advance(d time.Duration) { r.clock.Add(int64(d)) }

// start begins a sign-in as member m1 and answers its state.
func (r *rig) start(t *testing.T, member string) (state string, authorize *url.URL) {
	t.Helper()
	raw, err := r.Start(context.Background(), StartRequest{BrowserHash: testBrowserHash, Org: r.org, Member: member, App: "google", Level: "personal", ReturnTo: "https://app.example.com/settings"})
	if err != nil {
		t.Fatal(err)
	}
	authorize, err = url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.app.mu.Lock()
	r.app.challenge = authorize.Query().Get("code_challenge")
	r.app.mu.Unlock()
	return authorize.Query().Get("state"), authorize
}

// connected signs member m1 in and answers the connection's id.
func (r *rig) connected(t *testing.T) string {
	t.Helper()
	state, _ := r.start(t, "m1")
	done, err := r.Complete(context.Background(), Callback{Cookie: testCookie, State: state, Code: "code-1"})
	if err != nil {
		t.Fatal(err)
	}
	return done.ConnectionID
}

func (r *rig) status(t *testing.T, id string) (status string, failures int) {
	t.Helper()
	if err := r.pool.QueryRow(context.Background(), `SELECT status, failures FROM integrations.connections WHERE id = $1`, id).Scan(&status, &failures); err != nil {
		t.Fatal(err)
	}
	return status, failures
}

func TestConnectCallbackAndTheFlowAtRest(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	state, authorize := r.start(t, "m1")

	q := authorize.Query()
	if authorize.Path != "/authorize" || q.Get("client_id") != "client-1" || q.Get("response_type") != "code" ||
		q.Get("scope") != "read write" || q.Get("code_challenge_method") != "S256" || q.Get("access_type") != "offline" ||
		q.Get("redirect_uri") != "https://integrations.example.com"+CallbackPath {
		t.Fatalf("authorization URL = %s", authorize)
	}
	if q.Get("state") == "forged" || len(q["state"]) != 1 {
		t.Fatalf("a manifest parameter replaced the state: %s", authorize)
	}
	if strings.Contains(authorize.String(), "client_secret") || strings.Contains(authorize.String(), clientSecret) {
		t.Fatal("the authorization URL holds the client secret")
	}

	// Only the hash of the state, and a sealed verifier, are kept.
	var stored int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM integrations.oauth_states WHERE org_id = $1 AND state_hash = $2 AND pkce_verifier IS NOT NULL`, r.org, hashState(state)).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("state rows = %d, %v", stored, err)
	}
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM integrations.oauth_states WHERE state_hash = $1`, state).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the state itself is stored (%d rows), %v", stored, err)
	}

	done, err := r.Complete(ctx, Callback{Cookie: testCookie, State: state, Code: "code-1"})
	if err != nil || done.ConnectionID == "" || done.ReturnTo != "https://app.example.com/settings" || done.App != "google" {
		t.Fatalf("Complete = %+v, %v", done, err)
	}
	var mode, level, member, status string
	var scopes []string
	if err := r.pool.QueryRow(ctx, `SELECT mode, level, member_id, status, scopes FROM integrations.connections WHERE id = $1`, done.ConnectionID).Scan(&mode, &level, &member, &status, &scopes); err != nil {
		t.Fatal(err)
	}
	if mode != "byo" || level != "personal" || member != "m1" || status != "ok" || strings.Join(scopes, " ") != "read write" {
		t.Fatalf("connection = %s %s %s %s %v", mode, level, member, status, scopes)
	}

	token, err := r.Token(ctx, done.ConnectionID, "access")
	if err != nil || token != "access-1" {
		t.Fatalf("Token = %q, %v", token, err)
	}
	// At rest, only sealed values.
	var sealed []byte
	if err := r.pool.QueryRow(ctx, `SELECT sealed::text::bytea FROM integrations.credentials WHERE connection_id = $1 AND kind = 'refresh'`, done.ConnectionID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "refresh-1") {
		t.Fatal("a refresh token is stored in the clear")
	}
	// A credential moved to another connection does not open.
	other, err := r.sealer.Seal(ctx, credentialWhere(r.org, "someone-else", "access"), []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.sealer.Open(ctx, credentialWhere(r.org, done.ConnectionID, "access"), other); err == nil {
		t.Fatal("a token sealed for another connection opened")
	}

	// The state is single use.
	if _, err := r.Complete(ctx, Callback{Cookie: testCookie, State: state, Code: "code-1"}); !errors.Is(err, ErrBadState) {
		t.Fatalf("replayed state: err = %v", err)
	}
	// Signing in again renews the connection rather than adding a twin.
	again := r.connected(t)
	if again != done.ConnectionID {
		t.Fatalf("a second sign-in made connection %s, want %s", again, done.ConnectionID)
	}
}

func TestSignInFailures(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	var failure *Failure

	t.Run("unknown, empty and expired state", func(t *testing.T) {
		if _, err := r.Complete(ctx, Callback{Cookie: testCookie, State: "nope", Code: "code-1"}); !errors.Is(err, ErrBadState) {
			t.Fatalf("unknown: %v", err)
		}
		if _, err := r.Complete(ctx, Callback{}); !errors.Is(err, ErrBadState) {
			t.Fatalf("empty: %v", err)
		}
		state, _ := r.start(t, "m1")
		r.advance(StateLifetime + time.Second)
		if _, err := r.Complete(ctx, Callback{Cookie: testCookie, State: state, Code: "code-1"}); !errors.Is(err, ErrBadState) {
			t.Fatalf("expired: %v", err)
		}
	})
	t.Run("the member denied it", func(t *testing.T) {
		state, _ := r.start(t, "m1")
		done, err := r.Complete(ctx, Callback{Cookie: testCookie, State: state, Error: "access_denied"})
		if !errors.As(err, &failure) || failure.Reason != ReasonDenied || done.ReturnTo == "" || done.ConnectionID != "" {
			t.Fatalf("Complete = %+v, %v", done, err)
		}
	})
	t.Run("the app refuses the code", func(t *testing.T) {
		state, _ := r.start(t, "m1")
		if _, err := r.Complete(ctx, Callback{Cookie: testCookie, State: state, Code: "wrong"}); !errors.As(err, &failure) || failure.Reason != ReasonDenied {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a token that fails the test call is revoked and kept nowhere", func(t *testing.T) {
		r.app.mu.Lock()
		r.app.meRefused = true
		r.app.mu.Unlock()
		state, _ := r.start(t, "m2")
		_, err := r.Complete(ctx, Callback{Cookie: testCookie, State: state, Code: "code-1"})
		if !errors.As(err, &failure) || failure.Reason != ReasonInvalid {
			t.Fatalf("err = %v", err)
		}
		var n int
		if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM integrations.connections WHERE org_id = $1 AND member_id = 'm2'`, r.org).Scan(&n); err != nil || n != 0 {
			t.Fatalf("connections = %d, %v", n, err)
		}
		r.app.mu.Lock()
		defer r.app.mu.Unlock()
		if len(r.app.revoked) != 1 || !strings.HasPrefix(r.app.revoked[0], "refresh-") {
			t.Fatalf("revoked at the app = %v", r.app.revoked)
		}
	})
}

// A forwarded authorize link, finished in another browser, is refused before
// the code is redeemed, and spends the state (#477).
func TestSignInFromAnotherBrowser(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	otherNonce := func(name string) string { return "browser-nonce-2" }
	for name, cookie := range map[string]func(string) string{"no cookie": nil, "another nonce": otherNonce} {
		t.Run(name, func(t *testing.T) {
			state, _ := r.start(t, "m1")
			r.app.mu.Lock()
			issued := r.app.issued
			r.app.mu.Unlock()
			done, err := r.Complete(ctx, Callback{Cookie: cookie, State: state, Code: "code-1"})
			var failure *Failure
			if !errors.As(err, &failure) || failure.Reason != ReasonWrongBrowser || done.ConnectionID != "" {
				t.Fatalf("Complete = %+v, %v", done, err)
			}
			r.app.mu.Lock()
			redeemed := r.app.issued != issued
			r.app.mu.Unlock()
			if redeemed {
				t.Fatal("the code was redeemed for a browser that did not start the sign-in")
			}
			if _, err := r.Complete(ctx, Callback{Cookie: testCookie, State: state, Code: "code-1"}); !errors.Is(err, ErrBadState) {
				t.Fatalf("the state was not spent: %v", err)
			}
		})
	}
}

func TestStartRefusals(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	good := StartRequest{BrowserHash: testBrowserHash, Org: r.org, Member: "m1", App: "google", Level: "personal"}
	for name, tweak := range map[string]func(*StartRequest){
		"no member":        func(s *StartRequest) { s.Member = "" },
		"no browser hash":  func(s *StartRequest) { s.BrowserHash = "" },
		"not a hash":       func(s *StartRequest) { s.BrowserHash = strings.Repeat("Z", 64) },
		"slash in the org": func(s *StartRequest) { s.Org = "a/b" },
		"bad level":        func(s *StartRequest) { s.Level = "team" },
		"foreign return":   func(s *StartRequest) { s.ReturnTo = "https://evil.example.com/" },
		"return with user": func(s *StartRequest) { s.ReturnTo = "https://app.example.com@evil.example.com/" },
		"return, no https": func(s *StartRequest) { s.ReturnTo = "javascript:alert(1)" },
	} {
		request := good
		tweak(&request)
		if _, err := r.Start(ctx, request); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	request := good
	request.App = "notion"
	if _, err := r.Start(ctx, request); !errors.Is(err, ErrNoConnector) {
		t.Errorf("an app with no manifest: err = %v", err)
	}
}

func TestRefreshRotatesAndIsSingleFlight(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id := r.connected(t)
	r.app.refreshLag = 150 * time.Millisecond

	// Eight callers' tokens were refused at once: one asks the app, the rest
	// get its answer.
	var wg sync.WaitGroup
	tokens := make([]secrets.Secret, 8)
	errs := make([]error, 8)
	for i := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = r.Refresh(ctx, id, "access", "access-1")
		}()
	}
	wg.Wait()
	for i := range tokens {
		if errs[i] != nil || tokens[i] != "access-2" {
			t.Fatalf("caller %d got %q, %v", i, tokens[i], errs[i])
		}
	}
	if got := r.app.refreshes.Load(); got != 1 {
		t.Fatalf("the app was asked %d times, want 1", got)
	}

	// The rotated refresh token was saved with the access token: the next
	// refresh uses it, and the app (which kills old ones) accepts.
	r.app.refreshLag = 0
	if token, err := r.Refresh(ctx, id, "access", "access-2"); err != nil || token != "access-3" {
		t.Fatalf("second refresh = %q, %v", token, err)
	}
	if status, failures := r.status(t, id); status != "ok" || failures != 0 {
		t.Fatalf("status = %s, %d", status, failures)
	}
	var version int
	if err := r.pool.QueryRow(ctx, `SELECT version FROM integrations.credentials WHERE connection_id = $1 AND kind = 'access'`, id).Scan(&version); err != nil || version != 3 {
		t.Fatalf("version = %d, %v", version, err)
	}
	// Nothing stale, nothing expiring: no call to the app.
	before := r.app.refreshes.Load()
	if token, err := r.Refresh(ctx, id, "access", ""); err != nil || token != "access-3" || r.app.refreshes.Load() != before {
		t.Fatalf("a fresh token was refreshed: %q, %v", token, err)
	}
	if _, err := r.Refresh(ctx, id, "installation", ""); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("other kind: %v", err)
	}
}

func TestTokenRefreshesAnExpiringTokenProactively(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id := r.connected(t)
	if token, _ := r.Token(ctx, id, "access"); token != "access-1" || r.app.refreshes.Load() != 0 {
		t.Fatalf("a fresh token was refreshed")
	}
	r.advance(3600*time.Second - 5*time.Minute) // inside the ten minutes
	token, err := r.Token(ctx, id, "access")
	if err != nil || token != "access-2" || r.app.refreshes.Load() != 1 {
		t.Fatalf("Token = %q, %v after %d refreshes", token, err, r.app.refreshes.Load())
	}
}

func TestInvalidGrantNeedsReauth(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id := r.connected(t)
	r.app.grantError = "invalid_grant"
	if _, err := r.Refresh(ctx, id, "access", "access-1"); !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("err = %v", err)
	}
	if status, _ := r.status(t, id); status != "needs_reauth" {
		t.Fatalf("status = %s", status)
	}
	// Nothing more is asked of the app, and the token is no longer handed out.
	asked := r.app.refreshes.Load()
	if _, err := r.Refresh(ctx, id, "access", "access-1"); !errors.Is(err, ErrNeedsReauth) || r.app.refreshes.Load() != asked {
		t.Fatalf("err = %v, asked %d more", err, r.app.refreshes.Load()-asked)
	}
	if _, err := r.Token(ctx, id, "access"); !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("Token: %v", err)
	}
	// Signing in again brings it back.
	if again := r.connected(t); again != id {
		t.Fatalf("connection %s, want %s", again, id)
	}
	if status, _ := r.status(t, id); status != "ok" {
		t.Fatalf("status after sign-in = %s", status)
	}
}

func TestTransientFailuresCoolDownThenDegrade(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id := r.connected(t)
	r.app.down = true

	if _, err := r.Refresh(ctx, id, "access", "access-1"); !errors.Is(err, errUnavailable) {
		t.Fatalf("err = %v", err)
	}
	// In the cooldown the app is not asked again.
	asked := r.app.refreshes.Load()
	if _, err := r.Refresh(ctx, id, "access", "access-1"); !errors.Is(err, ErrCooldown) || r.app.refreshes.Load() != asked {
		t.Fatalf("err = %v, asked %d more", err, r.app.refreshes.Load()-asked)
	}
	// A token that has not expired is still used while the app is down.
	r.advance(3600*time.Second - 5*time.Minute) // the token is now expiring, not expired
	if token, err := r.Token(ctx, id, "access"); err != nil || token != "access-1" {
		t.Fatalf("Token during an outage = %q, %v", token, err)
	}
	for range degradedAfter - 1 {
		r.advance(maxCooldown + time.Second)
		if _, err := r.Refresh(ctx, id, "access", "access-1"); !errors.Is(err, errUnavailable) {
			t.Fatalf("err = %v", err)
		}
	}
	if status, failures := r.status(t, id); status != "degraded" || failures < degradedAfter {
		t.Fatalf("status = %s after %d failures", status, failures)
	}
	// The app comes back: the connection is ok again.
	r.app.down = false
	r.advance(maxCooldown + time.Second)
	if token, err := r.Refresh(ctx, id, "access", "access-1"); err != nil || token != "access-2" {
		t.Fatalf("recovery = %q, %v", token, err)
	}
	if status, failures := r.status(t, id); status != "ok" || failures != 0 {
		t.Fatalf("status = %s, %d", status, failures)
	}
}

func TestRevoke(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id := r.connected(t)

	revoked, err := r.Revoke(ctx, id)
	if err != nil || !revoked {
		t.Fatalf("Revoke = %v, %v", revoked, err)
	}
	r.app.mu.Lock()
	got := append([]string(nil), r.app.revoked...)
	r.app.mu.Unlock()
	if len(got) != 1 || got[0] != "refresh-1" {
		t.Fatalf("the app revoked %v", got)
	}
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM integrations.credentials WHERE connection_id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d credentials remain, %v", n, err)
	}
	if status, _ := r.status(t, id); status != "revoked" {
		t.Fatalf("status = %s", status)
	}
	if _, err := r.Token(ctx, id, "access"); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Token after revoke: %v", err)
	}
	if _, err := r.Refresh(ctx, id, "access", ""); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Refresh after revoke: %v", err)
	}
	// Idempotent, and an unknown connection is not found.
	if revoked, err := r.Revoke(ctx, id); err != nil || revoked {
		t.Fatalf("second Revoke = %v, %v", revoked, err)
	}
	if _, err := r.Revoke(ctx, "cn_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	// A revoked connection no longer blocks a new one.
	if again := r.connected(t); again == id {
		t.Fatal("a revoked connection was renewed")
	}
}

func TestRevokeWhenTheAppIsUnreachableStillRemoves(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id := r.connected(t)
	r.app.Close()
	revoked, err := r.Revoke(ctx, id)
	if err != nil || revoked {
		t.Fatalf("Revoke = %v, %v", revoked, err)
	}
	if status, _ := r.status(t, id); status != "revoked" {
		t.Fatalf("status = %s", status)
	}
}

type allowAll struct{}

func (allowAll) Verify(context.Context, string) (serviceauth.Claims, error) {
	return serviceauth.Claims{Service: "apps"}, nil
}

func TestHTTPConnectThenCallback(t *testing.T) {
	r := newRig(t)
	server := httpserver.New(allowAll{}, nil)
	Routes(server, r.Service, nil)
	browser := &http.Cookie{Name: BrowserCookieName(testBrowserHash), Value: testNonce}
	do := func(method, target, body string, internal bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		if internal {
			request.Header.Set(serviceauth.Header, "t")
		} else {
			request.AddCookie(browser)
		}
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		return recorder
	}

	body := fmt.Sprintf(`{"org":%q,"member":"m1","app":"google","level":"personal","return_to":"https://app.example.com/settings?tab=apps","browser_hash":%q}`, r.org, testBrowserHash)
	if got := do("POST", "/v1/connect", body, false); got.Code != http.StatusUnauthorized {
		t.Fatalf("connect without a service token answered %d", got.Code)
	}
	if got := do("POST", "/v1/connect", `{"org":"o","surprise":1}`, true); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown field answered %d", got.Code)
	}
	if got := do("POST", "/v1/connect", strings.Replace(body, `"google"`, `"notion"`, 1), true); got.Code != http.StatusNotFound {
		t.Fatalf("an app with no connector answered %d", got.Code)
	}
	got := do("POST", "/v1/connect", body, true)
	if got.Code != http.StatusOK {
		t.Fatalf("connect answered %d: %s", got.Code, got.Body)
	}
	var answer struct{ URL string }
	if err := json.Unmarshal(got.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	authorize, _ := url.Parse(answer.URL)
	r.app.mu.Lock()
	r.app.challenge = authorize.Query().Get("code_challenge")
	r.app.mu.Unlock()

	callback := do("GET", CallbackPath+"?state="+url.QueryEscape(authorize.Query().Get("state"))+"&code=code-1", "", false)
	location, _ := url.Parse(callback.Header().Get("Location"))
	if callback.Code != http.StatusSeeOther || location.Host != "app.example.com" || location.Query().Get("tab") != "apps" ||
		location.Query().Get("status") != "connected" || location.Query().Get("connection") == "" {
		t.Fatalf("callback answered %d to %s", callback.Code, callback.Header().Get("Location"))
	}
	if callback.Header().Get("Cache-Control") != "no-store" || callback.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("callback headers = %v", callback.Header())
	}
	// Replayed: refused, and nowhere to redirect to.
	replay := do("GET", CallbackPath+"?state="+url.QueryEscape(authorize.Query().Get("state"))+"&code=code-1", "", false)
	if replay.Code != http.StatusBadRequest || replay.Header().Get("Location") != "" {
		t.Fatalf("replay answered %d", replay.Code)
	}

	// A denied sign-in goes back to the page with the reason.
	second := do("POST", "/v1/connect", body, true)
	json.Unmarshal(second.Body.Bytes(), &answer)
	authorize, _ = url.Parse(answer.URL)
	denied := do("GET", CallbackPath+"?state="+url.QueryEscape(authorize.Query().Get("state"))+"&error=access_denied", "", false)
	location, _ = url.Parse(denied.Header().Get("Location"))
	if denied.Code != http.StatusSeeOther || location.Query().Get("status") != "failed" || location.Query().Get("reason") != ReasonDenied {
		t.Fatalf("denied callback answered %d to %s", denied.Code, denied.Header().Get("Location"))
	}

	// Forwarded to a browser without the cookie: refused, back to the page.
	third := do("POST", "/v1/connect", body, true)
	json.Unmarshal(third.Body.Bytes(), &answer)
	authorize, _ = url.Parse(answer.URL)
	browser = &http.Cookie{Name: "unrelated", Value: "x"}
	forwarded := do("GET", CallbackPath+"?state="+url.QueryEscape(authorize.Query().Get("state"))+"&code=code-1", "", false)
	location, _ = url.Parse(forwarded.Header().Get("Location"))
	if forwarded.Code != http.StatusSeeOther || location.Query().Get("status") != "failed" || location.Query().Get("reason") != ReasonWrongBrowser {
		t.Fatalf("forwarded callback answered %d to %s", forwarded.Code, forwarded.Header().Get("Location"))
	}
}
