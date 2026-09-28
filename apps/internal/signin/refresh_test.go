package signin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// untilDue moves the clock to where the connection's access token is due
// for renewal: within two minutes of expiring.
func (h *harness) untilDue(connectionID string) {
	h.t.Helper()
	credential, ok := h.store.stored(connectionID)
	if !ok || credential.ExpiresAt == nil {
		h.t.Fatalf("%s holds no expiring credential", connectionID)
	}
	h.clock.Advance(credential.ExpiresAt.Sub(h.clock.Now()) - time.Minute)
}

func TestRefreshRotatesTheRefreshToken(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	first, _ := h.store.stored("con_1")

	for round := 1; round <= 2; round++ {
		h.untilDue("con_1")
		session, err := h.service.Session(context.Background(), "con_1")
		if err != nil {
			t.Fatalf("round %d: Session() = %v", round, err)
		}
		if response, err := get(t, session, h.app.base+"/api/data"); err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("round %d: call = %v", round, err)
		}
	}
	_, refreshes, _ := h.app.counts()
	if refreshes != 2 {
		t.Fatalf("refreshes = %d, want 2", refreshes)
	}
	// Each refresh presented the refresh token the one before it answered,
	// which the stub retires once used.
	presented := h.app.presentedTokens()
	if len(presented) != 2 || presented[0] == presented[1] {
		t.Fatalf("refresh tokens presented = %v, want two different ones", presented)
	}
	credential, _ := h.store.stored("con_1")
	if credential.Version != 3 || !credential.ExpiresAt.After(*first.ExpiresAt) {
		t.Fatalf("credential version %d, expiry %v; want version 3 and a later expiry", credential.Version, credential.ExpiresAt)
	}
	refresh, err := h.service.open(context.Background(), credentialBinding("con_1", sealedRefreshToken), credential.Sealed[sealedRefreshToken])
	if err != nil || refresh.Reveal() == presented[1] || !h.app.refreshLive(refresh.Reveal()) {
		t.Fatalf("the stored refresh token is not the latest one the app issued (%v)", err)
	}
}

func TestRefreshKeepsATokenTheAppDidNotReplace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.app.set(func(a *stubApp) { a.rotating = false })
	h.connect("con_1", "acme")
	before, _ := h.store.stored("con_1")
	h.untilDue("con_1")
	if _, err := h.service.Session(context.Background(), "con_1"); err != nil {
		t.Fatalf("Session() = %v", err)
	}
	after, _ := h.store.stored("con_1")
	if after.Version != 2 || !slices.Equal(after.Sealed[sealedRefreshToken].Ciphertext, before.Sealed[sealedRefreshToken].Ciphertext) {
		t.Fatal("a refresh token the app did not replace was sealed again, or lost")
	}
}

func TestRefreshInvalidGrantNeedsReconnecting(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	session, err := h.service.Session(context.Background(), "con_1")
	if err != nil {
		t.Fatal(err)
	}
	h.app.set(func(a *stubApp) { a.refuseRefresh = true })
	h.untilDue("con_1")

	// Through the session's HTTP, as an SDK meets it.
	_, err = get(t, session, h.app.base+"/api/data")
	if !errors.Is(err, integration.ErrNeedsReconnect) || !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("a call after invalid_grant = %v, want ErrNeedsReconnect", err)
	}
	if reason := ReconnectReason(err); reason != ExpiredRefreshRejected {
		t.Fatalf("reason = %q, want refresh_rejected", reason)
	}
	// And through a new session, and Session.Token.
	if _, err := h.service.Session(context.Background(), "con_1"); ReconnectReason(err) != ExpiredRefreshRejected {
		t.Fatalf("Session() = %v, want refresh_rejected", err)
	}
	if _, err := session.Token(context.Background()); ReconnectReason(err) != ExpiredRefreshRejected {
		t.Fatalf("Token() = %v, want refresh_rejected", err)
	}
	// Nothing was sent to the API with a dead token.
	for _, call := range h.app.seenCalls() {
		if call.path == "/api/data" {
			t.Fatalf("the API was called: %+v", call)
		}
	}
}

func TestRefreshWithNothingToRenewItNeedsReconnecting(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.app.set(func(a *stubApp) { a.rotating = false })
	h.connect("con_1", "acme")
	// A token that expires, and no refresh token to renew it with.
	h.store.mu.Lock()
	delete(h.store.credentials["con_1"].credential.Sealed, sealedRefreshToken)
	h.store.mu.Unlock()
	h.untilDue("con_1")
	if _, err := h.service.Session(context.Background(), "con_1"); err != nil {
		t.Fatalf("a token still inside its last two minutes = %v, want it used", err)
	}
	h.clock.Advance(2 * time.Minute)
	if _, err := h.service.Session(context.Background(), "con_1"); ReconnectReason(err) != ExpiredRefreshRejected {
		t.Fatalf("an expired token with no refresh token = %v, want refresh_rejected", err)
	}
}

func TestConcurrentRefreshesExchangeOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	// A second replica: its own process, cache and in-flight refreshes,
	// sharing the database and the app.
	other, err := New(Dependencies{
		Store: h.store, Sealer: secrets.Envelope{Keys: h.keys, KeyID: "alias/test", Service: SealService},
		Integrations: h.service.manifests, HTTP: testHTTPClient(h.app), Clock: h.clock.Now,
	}, Config{RedirectURI: testRedirectURI})
	if err != nil {
		t.Fatal(err)
	}
	other.timings = h.service.timings
	h.untilDue("con_1")

	hold := make(chan struct{})
	arrived := make(chan struct{}, 8)
	h.app.set(func(a *stubApp) { a.holdRefresh, a.refreshing = hold, arrived })

	replicas := []*Service{h.service, other, h.service, other, h.service, other}
	tokens := make([]string, len(replicas))
	errs := make([]error, len(replicas))
	var wg sync.WaitGroup
	for index, replica := range replicas {
		wg.Go(func() {
			session, err := replica.Session(context.Background(), "con_1")
			if err != nil {
				errs[index] = err
				return
			}
			tokens[index], errs[index] = session.Token(context.Background())
		})
	}
	// Let every caller find the lease held before the one refresh answers.
	<-arrived
	time.Sleep(50 * time.Millisecond)
	close(hold)
	wg.Wait()

	for index, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", index, err)
		}
	}
	if _, refreshes, _ := h.app.counts(); refreshes != 1 {
		t.Fatalf("refreshes = %d, want exactly 1 across both replicas", refreshes)
	}
	for _, token := range tokens {
		if token != tokens[0] || token == "" {
			t.Fatalf("tokens = %v, want every caller to get the one renewed token", tokens)
		}
	}
	if credential, _ := h.store.stored("con_1"); credential.Version != 2 {
		t.Fatalf("version = %d, want 2", credential.Version)
	}
}

func TestACrashBetweenTheAnswerAndTheSaveEndsExpiredNotCorrupt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	before, _ := h.store.stored("con_1")
	h.untilDue("con_1")

	// The app rotates the refresh token, and the process dies before it
	// saves the answer: the lease is never released.
	h.store.mu.Lock()
	h.store.crashOnSave = true
	h.store.mu.Unlock()
	h.clock.Advance(2 * time.Minute) // past expiry, so nothing still works
	if _, err := h.service.Session(context.Background(), "con_1"); err == nil || ReconnectReason(err) != "" {
		t.Fatalf("Session() during the crash = %v, want a failure that is not a rejection", err)
	}
	if _, refreshes, _ := h.app.counts(); refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes)
	}

	// What is stored is untouched and still opens: not corrupt.
	after, _ := h.store.stored("con_1")
	if after.Version != before.Version {
		t.Fatalf("version = %d, want %d", after.Version, before.Version)
	}
	for _, name := range []string{sealedAccessToken, sealedRefreshToken} {
		if _, err := h.service.open(context.Background(), credentialBinding("con_1", name), after.Sealed[name]); err != nil {
			t.Fatalf("open %s after the crash = %v", name, err)
		}
	}

	// A restarted process: the lease holds until it expires.
	h.store.mu.Lock()
	h.store.crashOnSave = false
	h.store.mu.Unlock()
	restarted, err := New(Dependencies{
		Store: h.store, Sealer: secrets.Envelope{Keys: h.keys, KeyID: "alias/test", Service: SealService},
		Integrations: h.service.manifests, HTTP: testHTTPClient(h.app), Clock: h.clock.Now,
	}, Config{RedirectURI: testRedirectURI})
	if err != nil {
		t.Fatal(err)
	}
	restarted.timings = h.service.timings
	restarted.timings.leaseWait = 50 * time.Millisecond
	if _, err := restarted.Session(context.Background(), "con_1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Session() while the dead lease holds = %v, want ErrUnavailable", err)
	}
	if _, refreshes, _ := h.app.counts(); refreshes != 1 {
		t.Fatalf("refreshes = %d, want no second one under a held lease", refreshes)
	}
	// Once it expires, the next refresh presents the rotated-away token,
	// which the app refuses: the connection ends expired.
	h.clock.Advance(h.service.timings.lease)
	_, err = restarted.Session(context.Background(), "con_1")
	if ReconnectReason(err) != ExpiredRefreshRejected {
		t.Fatalf("Session() after the lease = %v, want refresh_rejected", err)
	}
	presented := h.app.presentedTokens()
	if len(presented) != 2 || presented[0] != presented[1] {
		t.Fatalf("presented = %v, want the same stored refresh token twice", presented)
	}
}

func TestRefreshedTokenIsCachedByVersion(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	// CompleteOAuth cached what it kept: no KMS call to use it.
	decrypts := h.keys.decrypts()
	for range 3 {
		if _, err := h.service.Session(context.Background(), "con_1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.keys.decrypts(); got != decrypts {
		t.Fatalf("KMS decrypts = %d, want %d: the token is cached", got, decrypts)
	}
	// A process without it opens the token once, then caches it.
	h.service.cache.forget("con_1")
	for range 3 {
		if _, err := h.service.Session(context.Background(), "con_1"); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.keys.decrypts(); got != decrypts+1 {
		t.Fatalf("KMS decrypts = %d, want %d", got, decrypts+1)
	}
	// The cache lets go two minutes before expiry, and the renewed token
	// is cached under the new version.
	h.untilDue("con_1")
	if _, err := h.service.Session(context.Background(), "con_1"); err != nil {
		t.Fatal(err)
	}
	credential, _ := h.store.stored("con_1")
	if _, ok := h.service.cache.get("con_1", credential.Version, h.clock.Now()); !ok || credential.Version != 2 {
		t.Fatalf("the renewed token is not cached under version %d", credential.Version)
	}
	if _, ok := h.service.cache.get("con_1", 1, h.clock.Now()); ok {
		t.Fatal("the old version is still answered")
	}
}

func TestClientCredentialsAreMintedAgain(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(base string) integration.Manifest {
		manifest := acmeManifest(base)
		manifest.Auth.Methods = []integration.Method{{Kind: integration.KindOAuth2ClientCredentials, OAuth2: &integration.OAuth2{
			TokenURL: base + "/oauth/token", RevokeStyle: integration.RevokeNone, Scopes: []string{"read"},
			ClientAuth: integration.ClientAuthHeader,
		}}}
		return manifest
	})
	h.app.set(func(a *stubApp) {
		a.clientAuth = integration.ClientAuthHeader
		a.clients["member-client"] = "member-client-secret-00000001"
	})
	h.store.addConnection("con_1", testOrganization, testMember, "acme", "pending")
	saved, err := h.service.SaveCredential(context.Background(), CredentialRequest{
		OrganizationID: testOrganization, ConnectionID: "con_1", Toolkit: "acme",
		Fields: map[string]string{"client_id": "member-client", "client_secret": "member-client-secret-00000001"},
	})
	if err != nil || saved.Account.Label != "octocat" || !slices.Equal(saved.Scopes, []string{"read"}) {
		t.Fatalf("SaveCredential() = %+v, %v", saved, err)
	}
	if form := h.app.tokenForm(0); form.Get("scope") != "read" || form.Get("grant_type") != "client_credentials" {
		t.Fatalf("the mint asked %v", form)
	}
	h.store.setStatus("con_1", "active")
	h.untilDue("con_1")
	if _, err := h.service.Session(context.Background(), "con_1"); err != nil {
		t.Fatalf("Session() = %v", err)
	}
	if _, _, mints := h.app.counts(); mints != 2 {
		t.Fatalf("mints = %d, want 2", mints)
	}
	// The member's client, refused, is the credential refused.
	h.app.set(func(a *stubApp) { delete(a.clients, "member-client") })
	h.untilDue("con_1")
	if _, err := h.service.Session(context.Background(), "con_1"); ReconnectReason(err) != ExpiredCredentialRejected {
		t.Fatalf("Session() with the client refused = %v, want credential_rejected", err)
	}
}

func TestOperatorsClientRefusedIsNotTheMembersFault(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connect("con_1", "acme")
	h.untilDue("con_1")
	// Oryxa's secret went wrong at the app: a refresh is refused with
	// invalid_client. The member's connection must not expire for it.
	h.app.set(func(a *stubApp) { a.clients[defaultClientID] = "rotated-at-the-app-0000001" })
	_, err := h.service.Session(context.Background(), "con_1")
	if err != nil {
		t.Fatalf("a token still inside its last two minutes = %v, want it used while the client is refused", err)
	}
	h.clock.Advance(2 * time.Minute)
	_, err = h.service.Session(context.Background(), "con_1")
	if ReconnectReason(err) != "" || errors.Is(err, integration.ErrNeedsReconnect) ||
		!errors.Is(err, ErrUnavailable) || !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("Session() = %v, want unavailable, never a reconnect", err)
	}
	if !strings.Contains(h.logs.String(), `level=ERROR msg="an app refused an OAuth client's refresh"`) {
		t.Fatal("an operator's refused client was not logged as an error")
	}
}
