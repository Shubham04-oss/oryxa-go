package signin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// elsewhere is another host: a server the credential must never reach.
type elsewhere struct {
	server *httptest.Server
	mu     sync.Mutex
	seen   []*http.Request
}

func newElsewhere(t *testing.T) *elsewhere {
	t.Helper()
	other := &elsewhere{}
	other.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.mu.Lock()
		other.seen = append(other.seen, r.Clone(context.Background()))
		other.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	t.Cleanup(other.server.Close)
	return other
}

func (e *elsewhere) requests() []*http.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*http.Request(nil), e.seen...)
}

func TestSessionSendsTheCredentialOnlyToItsOwnHost(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	other := newElsewhere(t)
	session, err := h.service.Session(context.Background(), "con_1")
	if err != nil {
		t.Fatal(err)
	}

	// A redirect to another host: http.Client sends the hop through the
	// session's transport, which places nothing on it.
	target := h.app.base + "/api/redirect?to=" + url.QueryEscape(other.server.URL+"/landing")
	if response, err := get(t, session, target); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GET through a redirect = %v", err)
	}
	hops := other.requests()
	if len(hops) != 1 || hops[0].Header.Get("Authorization") != "" || hops[0].Header.Get("Cookie") != "" {
		t.Fatalf("the other host saw %v, want one request with no credential", hops)
	}
	calls := h.app.seenCalls()
	if first := calls[len(calls)-1]; first.path != "/api/redirect" || !strings.HasPrefix(first.authorization, "Bearer at-") {
		t.Fatalf("the credential's own host saw %+v, want the credential", first)
	}

	// A redirect back to its own host carries it again.
	target = h.app.base + "/api/redirect?to=" + url.QueryEscape(h.app.base+"/api/data")
	if _, err := get(t, session, target); err != nil {
		t.Fatal(err)
	}
	calls = h.app.seenCalls()
	if last := calls[len(calls)-1]; last.path != "/api/data" || !strings.HasPrefix(last.authorization, "Bearer at-") {
		t.Fatalf("the redirect home = %+v, want the credential", last)
	}

	// An integration never sets Authorization or Cookie: what it sets is
	// dropped for another host, and replaced for its own.
	for _, host := range []string{other.server.URL, h.app.base} {
		request, _ := http.NewRequest(http.MethodGet, host+"/api/data", nil)
		request.Header.Set("Authorization", "Bearer forged")
		request.Header.Set("Cookie", "session=forged")
		response, err := session.HTTP.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	if hops := other.requests(); hops[len(hops)-1].Header.Get("Authorization") != "" || hops[len(hops)-1].Header.Get("Cookie") != "" {
		t.Fatal("a header the integration set reached another host")
	}
	calls = h.app.seenCalls()
	if last := calls[len(calls)-1]; last.authorization == "Bearer forged" || !strings.HasPrefix(last.authorization, "Bearer at-") {
		t.Fatalf("own host saw %q, want the connection's credential", last.authorization)
	}
}

func TestSessionPlacesEachKindOfCredential(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(base string) integration.Manifest {
		manifest := acmeManifest(base)
		fields := []integration.Field{
			{Name: "region", Label: "Region", Level: integration.LevelConnection,
				Validate: integration.Validation{Required: true, Enum: []string{"us", "eu"}},
				Use:      []integration.Use{{In: integration.InHeader, Name: "X-Region"}}},
			{Name: "signing_key", Label: "Signing key", Secret: true, Level: integration.LevelConnection,
				Use: []integration.Use{{In: integration.InQuery, Name: "sig", Template: "k-{value}"}}},
		}
		manifest.Auth.Methods[1].Place = &integration.Place{In: integration.InQuery, Name: "api_key", Template: "{token}"}
		manifest.Auth.Methods[1].Fields = fields
		manifest.Auth.Methods[2].Fields = fields
		return manifest
	})
	h.app.set(func(a *stubApp) {
		a.apiToken, a.username, a.password = "pat-0123456789abcdef", "ada@acme.example", "correct horse"
	})
	other := newElsewhere(t)
	typed := map[string]map[string]string{
		integration.KindAPIToken: {"token": "pat-0123456789abcdef", "region": "eu", "signing_key": "s3cret-key"},
		integration.KindBasic:    {"username": "ada@acme.example", "password": "correct horse", "region": "eu", "signing_key": "s3cret-key"},
	}
	for kind, fields := range typed {
		connection := "con_" + kind
		h.store.addConnection(connection, testOrganization, testMember, "acme", "pending")
		if _, err := h.service.SaveCredential(context.Background(), CredentialRequest{
			OrganizationID: testOrganization, ConnectionID: connection, Toolkit: "acme", Kind: kind, Fields: fields,
		}); err != nil {
			t.Fatalf("%s: SaveCredential() = %v", kind, err)
		}
		h.store.setStatus(connection, "active")
		session, err := h.service.Session(context.Background(), connection)
		if err != nil {
			t.Fatal(err)
		}
		before := len(h.app.seenCalls())
		if _, err := get(t, session, h.app.base+"/api/data?page=2"); err != nil {
			t.Fatal(err)
		}
		call := h.app.seenCalls()[before]
		query, _ := url.ParseQuery(call.query)
		if query.Get("page") != "2" || query.Get("sig") != "k-s3cret-key" {
			t.Fatalf("%s: query = %q", kind, call.query)
		}
		switch kind {
		case integration.KindAPIToken:
			if query.Get("api_key") != "pat-0123456789abcdef" || call.authorization != "" {
				t.Fatalf("api_token placed %q / %q", call.query, call.authorization)
			}
		case integration.KindBasic:
			if !strings.HasPrefix(call.authorization, "Basic ") {
				t.Fatalf("basic placed %q", call.authorization)
			}
		}
		if _, err := get(t, session, other.server.URL+"/api/data"); err != nil {
			t.Fatal(err)
		}
		hops := other.requests()
		last := hops[len(hops)-1]
		if last.URL.RawQuery != "" || last.Header.Get("Authorization") != "" || last.Header.Get("X-Region") != "" {
			t.Fatalf("%s: another host saw %q %v", kind, last.URL.RawQuery, last.Header)
		}
	}
}

func TestReadToolSessionRefusesWrites(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	other := newElsewhere(t)
	session, err := h.service.Session(context.Background(), "con_1", ReadOnly("/search", "/databases/{}/query"))
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, target string) error {
		request, err := http.NewRequest(method, target, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := session.HTTP.Do(request)
		if err == nil {
			_ = response.Body.Close()
		}
		return err
	}
	base := h.app.base + "/api"
	for _, allowed := range []struct{ method, target string }{
		{http.MethodGet, base + "/items"},
		{http.MethodHead, base + "/items"},
		{http.MethodPost, base + "/search"},
		{http.MethodPost, base + "/databases/db_1/query"},
		{http.MethodGet, other.server.URL + "/anything"},
	} {
		if err := send(allowed.method, allowed.target); err != nil {
			t.Errorf("%s %s = %v, want it sent", allowed.method, allowed.target, err)
		}
	}
	sent := len(h.app.seenCalls()) + len(other.requests())
	for _, refused := range []struct{ method, target string }{
		{http.MethodPost, base + "/items"},
		{http.MethodPut, base + "/items/1"},
		{http.MethodPatch, base + "/items/1"},
		{http.MethodDelete, base + "/items/1"},
		{http.MethodOptions, base + "/items"},
		{http.MethodPost, base + "/search/extra"},
		{http.MethodPost, base + "/databases/a%2Fb/query"},
		{http.MethodPost, base + "/databases/../query"},
		{http.MethodPost, base + "/databases//query"},
		{http.MethodPost, other.server.URL + "/api/search"},
	} {
		err := send(refused.method, refused.target)
		if !errors.Is(err, ErrReadOnly) || !errors.Is(err, domain.ErrNotReadOnly) {
			t.Errorf("%s %s = %v, want ErrReadOnly", refused.method, refused.target, err)
		}
	}
	if now := len(h.app.seenCalls()) + len(other.requests()); now != sent {
		t.Fatalf("%d refused requests were sent anyway", now-sent)
	}
	// A session for a write tool sends them.
	writer, err := h.service.Session(context.Background(), "con_1")
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, base+"/items", strings.NewReader(`{}`))
	if response, err := writer.HTTP.Do(request); err != nil {
		t.Fatalf("a write tool's POST = %v", err)
	} else {
		_ = response.Body.Close()
	}
}

func TestSessionOnlyForAnActiveConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	for status, want := range map[string]error{
		"pending": ErrNotActive, "failed": ErrNotActive, "removed": ErrNotActive,
		"expired": integration.ErrNeedsReconnect,
	} {
		h.store.setStatus("con_1", status)
		if _, err := h.service.Session(context.Background(), "con_1"); !errors.Is(err, want) {
			t.Errorf("a %s connection = %v, want %v", status, err, want)
		}
	}
	if _, err := h.service.Session(context.Background(), "con_none"); !errors.Is(err, ErrNoCredential) || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a connection with no credential = %v, want ErrNoCredential", err)
	}
}

func TestSessionAnswersWhatAnSDKNeeds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	session, err := h.service.Session(context.Background(), "con_1")
	if err != nil {
		t.Fatal(err)
	}
	if session.BaseURL != h.app.base+"/api" {
		t.Fatalf("BaseURL = %q", session.BaseURL)
	}
	if session.Settings[integration.SettingAccountLabel] != "octocat" || session.Settings[integration.SettingAccountRef] != "42" {
		t.Fatalf("Settings = %v", session.Settings)
	}
	token, err := session.Token(context.Background())
	if err != nil || !strings.HasPrefix(token, "at-") {
		t.Fatalf("Token() = %q, %v", token, err)
	}
	// A session is its own: changing one's settings changes no other.
	session.Settings["region"] = "moon"
	again, _ := h.service.Session(context.Background(), "con_1")
	if again.Settings["region"] != "" {
		t.Fatal("a session's settings leaked into another's")
	}
}

func TestSuccessfulUsePutsOffTheCheck(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	session, err := h.service.Session(context.Background(), "con_1")
	if err != nil {
		t.Fatal(err)
	}
	// A check came due, and one had failed.
	h.store.mu.Lock()
	h.store.credentials["con_1"].credential.NextCheckAt = h.clock.Now()
	h.store.credentials["con_1"].credential.CheckFailures = 2
	h.store.mu.Unlock()

	if _, err := get(t, session, h.app.base+"/api/data"); err != nil {
		t.Fatal(err)
	}
	credential, _ := h.store.stored("con_1")
	if want := h.clock.Now().Add(12 * time.Hour); credential.NextCheckAt.Before(want) || credential.CheckFailures != 0 {
		t.Fatalf("next check %v with %d failures, want at least %v and none", credential.NextCheckAt, credential.CheckFailures, want)
	}
	// Within half an hour it is not written again.
	h.store.setNextCheck("con_1", h.clock.Now())
	h.clock.Advance(10 * time.Minute)
	if _, err := get(t, session, h.app.base+"/api/data"); err != nil {
		t.Fatal(err)
	}
	if credential, _ := h.store.stored("con_1"); !credential.NextCheckAt.Equal(h.clock.Now().Add(-10 * time.Minute)) {
		t.Fatal("a second use within half an hour was written down")
	}
	// A call that fails proves nothing.
	h.clock.Advance(time.Hour)
	h.app.set(func(a *stubApp) { a.meStatus = http.StatusUnauthorized })
	if _, err := get(t, session, h.app.base+"/api/me"); err != nil {
		t.Fatal(err)
	}
	if credential, _ := h.store.stored("con_1"); credential.NextCheckAt.After(h.clock.Now()) {
		t.Fatal("a refused call put off the check")
	}
}

// TestByDefaultAppsAreReachedOnThePublicInternetOnly builds signin with no
// HTTP client of its own: it falls back to safehttp's, which refuses a
// loopback app before dialing it.
func TestByDefaultAppsAreReachedOnThePublicInternetOnly(t *testing.T) {
	t.Parallel()
	app := newStubApp(t)
	app.set(func(a *stubApp) { a.apiToken = testToken })
	loopback := strings.Replace(app.base, stubHost, "127.0.0.1", 1)
	registry := integration.NewRegistry()
	if err := registry.Register(fakeIntegration{manifest: acmeManifest(loopback)}); err != nil {
		t.Fatal(err)
	}
	store := newMemStore()
	service, err := New(Dependencies{
		Store: store, Sealer: secrets.Envelope{Keys: &fakeKeys{}, KeyID: "alias/test", Service: SealService},
		Integrations: registry,
	}, Config{RedirectURI: testRedirectURI})
	if err != nil {
		t.Fatal(err)
	}
	store.addConnection("con_1", testOrganization, testMember, "acme", "pending")
	_, err = service.SaveCredential(context.Background(), CredentialRequest{
		OrganizationID: testOrganization, ConnectionID: "con_1", Toolkit: "acme", Kind: integration.KindAPIToken,
		Fields: map[string]string{"token": testToken},
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != ReasonInvalidCredentials || !strings.Contains(err.Error(), "public internet") {
		t.Fatalf("a key tested against a loopback app = %v, want it refused before anything is sent", err)
	}
	if calls := app.seenCalls(); len(calls) != 0 {
		t.Fatalf("the loopback app was reached: %+v", calls)
	}
}
