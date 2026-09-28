package signin

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

func TestOAuthSignsInWithACodeAndPKCE(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	started := h.start("con_1", "acme")

	parsed, err := url.Parse(started.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	for name, want := range map[string]string{
		"client_id": defaultClientID, "redirect_uri": testRedirectURI, "response_type": "code",
		"scope": "read write", "state": started.State, "code_challenge_method": "S256",
	} {
		if got := query.Get(name); got != want {
			t.Errorf("authorization URL %s = %q, want %q", name, got, want)
		}
	}
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != h.app.base+"/oauth/authorize" {
		t.Errorf("authorization URL = %s", started.AuthorizationURL)
	}
	// 32 random bytes, and only their hash kept.
	if len(started.State) != 43 {
		t.Errorf("state is %d characters, want 43 (32 bytes, base64url)", len(started.State))
	}
	flow, found, _ := h.store.flow(hashState(started.State))
	if !found {
		t.Fatal("no flow is stored under the state's hash")
	}
	if flow.CodeVerifier == "" || s256(flow.CodeVerifier) != query.Get("code_challenge") {
		t.Error("the code challenge is not S256 of the stored verifier")
	}
	if !flow.ExpiresAt.Equal(h.clock.Now().Add(10*time.Minute)) || flow.RedirectURI != testRedirectURI || flow.ReturnTo != testReturnTo {
		t.Errorf("flow = %+v", flow)
	}

	code, state := h.app.authorize(t, started.AuthorizationURL)
	completion, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code})
	if err != nil {
		t.Fatalf("CompleteOAuth() = %v", err)
	}
	want := Completion{
		ConnectionID: "con_1", OrganizationID: testOrganization, MemberID: testMember, Toolkit: "acme",
		Kind: integration.KindOAuth2Code, ReturnTo: testReturnTo, Account: Account{Label: "octocat", Ref: "42"},
		Scopes: []string{"read", "write"},
	}
	if completion.ConnectionID != want.ConnectionID || completion.ReturnTo != want.ReturnTo ||
		completion.Account != want.Account || !slices.Equal(completion.Scopes, want.Scopes) || completion.Kind != want.Kind {
		t.Fatalf("completion = %+v, want %+v", completion, want)
	}

	credential, ok := h.store.stored("con_1")
	if !ok {
		t.Fatal("no credential was kept")
	}
	if credential.Version != 1 || credential.Kind != integration.KindOAuth2Code || credential.ExpiresAt == nil ||
		credential.Settings[integration.SettingAccountLabel] != "octocat" || credential.Settings[integration.SettingAccountRef] != "42" {
		t.Fatalf("credential = %+v", credential)
	}
	client, err := h.store.LiveOAuthClient(context.Background(), "", "acme")
	if err != nil || credential.OAuthClientID != client.ID {
		t.Fatalf("the credential names client %q, want Oryxa's default %q (%v)", credential.OAuthClientID, client.ID, err)
	}
	// Each token is sealed on its own, and opens only as itself.
	access, err := h.service.open(context.Background(), credentialBinding("con_1", sealedAccessToken), credential.Sealed[sealedAccessToken])
	if err != nil || !strings.HasPrefix(access.Reveal(), "at-") {
		t.Fatalf("open the access token = %v", err)
	}
	if _, err := h.service.open(context.Background(), credentialBinding("con_1", sealedRefreshToken), credential.Sealed[sealedAccessToken]); err == nil {
		t.Fatal("the access token opened as the refresh token")
	}
	if _, err := h.service.open(context.Background(), credentialBinding("con_2", sealedAccessToken), credential.Sealed[sealedAccessToken]); err == nil {
		t.Fatal("the access token opened as another connection's")
	}
	if exchanges, _, _ := h.app.counts(); exchanges != 1 {
		t.Fatalf("exchanges = %d, want 1", exchanges)
	}
}

func TestOAuthStateIsSpentOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	started := h.start("con_1", "acme")
	code, state := h.app.authorize(t, started.AuthorizationURL)
	callback := Callback{State: state, CookieState: started.State, Code: code}
	if _, err := h.service.CompleteOAuth(context.Background(), callback); err != nil {
		t.Fatalf("CompleteOAuth() = %v", err)
	}
	for range 2 {
		if _, err := h.service.CompleteOAuth(context.Background(), callback); !errors.Is(err, ErrFlowNotFound) {
			t.Fatalf("a reused state = %v, want ErrFlowNotFound", err)
		}
	}
	if exchanges, _, _ := h.app.counts(); exchanges != 1 {
		t.Fatalf("exchanges = %d, want 1", exchanges)
	}
}

func TestOAuthRefusesAnExpiredFlow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	started := h.start("con_1", "acme")
	code, state := h.app.authorize(t, started.AuthorizationURL)
	h.clock.Advance(FlowLifetime)
	if _, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code}); !errors.Is(err, ErrFlowNotFound) {
		t.Fatalf("an expired flow = %v, want ErrFlowNotFound", err)
	}
	if exchanges, _, _ := h.app.counts(); exchanges != 0 {
		t.Fatalf("exchanges = %d, want 0", exchanges)
	}
}

func TestOAuthRefusesACookieThatIsNotTheState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	started := h.start("con_1", "acme")
	code, state := h.app.authorize(t, started.AuthorizationURL)
	for _, cookie := range []string{"", "another-state", started.State + "x"} {
		_, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: cookie, Code: code})
		if !errors.Is(err, ErrBadState) || !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("cookie %q = %v, want ErrBadState", cookie, err)
		}
	}
	if exchanges, _, _ := h.app.counts(); exchanges != 0 {
		t.Fatalf("exchanges = %d, want 0", exchanges)
	}
	// Nothing was spent: the member's own browser still finishes.
	if _, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code}); err != nil {
		t.Fatalf("CompleteOAuth() after refusals = %v", err)
	}
}

func TestOAuthAuthenticatesTheClientAsTheManifestSays(t *testing.T) {
	t.Parallel()
	for _, clientAuth := range []string{integration.ClientAuthHeader, integration.ClientAuthBody} {
		t.Run(clientAuth, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, func(base string) integration.Manifest {
				manifest := acmeManifest(base)
				manifest.Auth.Methods[0].OAuth2.ClientAuth = clientAuth
				return manifest
			})
			// The stub refuses a client authenticated the other way, and a
			// secret sent both ways.
			h.app.set(func(a *stubApp) { a.clientAuth = clientAuth })
			completion := h.connect("con_1", "acme")
			if completion.Account.Label != "octocat" {
				t.Fatalf("completion = %+v", completion)
			}
			form := h.app.tokenForm(0)
			if clientAuth == integration.ClientAuthHeader && (form.Has("client_secret") || form.Has("client_id")) {
				t.Fatalf("header client auth sent the client in the body: %v", form)
			}
			if clientAuth == integration.ClientAuthBody && form.Get("client_secret") != defaultClientSecret {
				t.Fatal("body client auth did not send the secret in the body")
			}
		})
	}

	// A manifest that says body, for an app that wants the header, fails.
	h := newHarness(t)
	h.app.set(func(a *stubApp) { a.clientAuth = integration.ClientAuthHeader })
	started := h.start("con_1", "acme")
	code, state := h.app.authorize(t, started.AuthorizationURL)
	_, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != ReasonDenied {
		t.Fatalf("the wrong client auth = %v, want a denied_at_provider Failure", err)
	}
}

func TestOAuthFailuresNameTheirConnection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		prepare  func(h *harness)
		callback func(code, state, cookie string) Callback
		reason   string
	}{
		{"the member refused", nil, func(_, state, cookie string) Callback {
			return Callback{State: state, CookieState: cookie, Error: "access_denied"}
		}, ReasonDenied},
		{"the app refused the code", nil, func(_, state, cookie string) Callback {
			return Callback{State: state, CookieState: cookie, Code: "code-that-was-never-issued"}
		}, ReasonDenied},
		{"another member's browser", nil, func(code, state, cookie string) Callback {
			return Callback{State: state, CookieState: cookie, Code: code, Verify: func(_ context.Context, organizationID, memberID string) error {
				if organizationID != testOrganization || memberID != testMember {
					t.Errorf("Verify saw %s/%s", organizationID, memberID)
				}
				return errors.New("the access cookie is someone else's")
			}}
		}, ReasonWrongAccount},
		{"the app is down", func(h *harness) { h.app.set(func(a *stubApp) { a.meStatus = 503 }) },
			func(code, state, cookie string) Callback {
				return Callback{State: state, CookieState: cookie, Code: code}
			},
			ReasonUnavailable},
		{"the test call is refused", func(h *harness) { h.app.set(func(a *stubApp) { a.meStatus = 401 }) },
			func(code, state, cookie string) Callback {
				return Callback{State: state, CookieState: cookie, Code: code}
			},
			ReasonInvalidCredentials},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			if tc.prepare != nil {
				tc.prepare(h)
			}
			started := h.start("con_1", "acme")
			code, state := h.app.authorize(t, started.AuthorizationURL)
			completion, err := h.service.CompleteOAuth(context.Background(), tc.callback(code, state, started.State))
			var failure *Failure
			if !errors.As(err, &failure) || failure.Reason != tc.reason {
				t.Fatalf("CompleteOAuth() = %v, want a %s Failure", err, tc.reason)
			}
			if completion.ConnectionID != "con_1" || completion.ReturnTo != testReturnTo {
				t.Fatalf("a failure's completion = %+v, want its connection and return page", completion)
			}
			if _, kept := h.store.stored("con_1"); kept {
				t.Fatal("a failed sign-in kept a credential")
			}
			if _, _, spent := h.store.flow(hashState(started.State)); !spent {
				t.Fatal("a failed sign-in left its flow unspent")
			}
		})
	}
}

func TestOAuthTestCallMustNameTheAccount(t *testing.T) {
	t.Parallel()
	// Slack answers 200 and "ok": false to refuse, so a label that does not
	// resolve fails the sign-in, and the token the app issued is revoked,
	// since nothing will keep it.
	h := newHarness(t, func(base string) integration.Manifest {
		manifest := acmeManifest(base)
		manifest.Auth.Methods[0].OAuth2.RevokeStyle = integration.RevokeGitHubToken
		manifest.Auth.Methods[0].OAuth2.RevokeURL = ""
		return manifest
	})
	h.app.set(func(a *stubApp) { a.meBody = map[string]any{"ok": false, "error": "invalid_auth"} })
	started := h.start("con_1", "acme")
	code, state := h.app.authorize(t, started.AuthorizationURL)
	_, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != ReasonInvalidCredentials {
		t.Fatalf("a label that does not resolve = %v, want invalid_credentials", err)
	}
	if _, kept := h.store.stored("con_1"); kept {
		t.Fatal("a credential whose test call failed was kept")
	}
	revocations := h.app.seenRevocations()
	if len(revocations) != 1 || revocations[0].method != "DELETE" || !strings.HasPrefix(revocations[0].body["access_token"], "at-") {
		t.Fatalf("revocations = %+v, want the unkept token revoked", revocations)
	}

	// An account ref the manifest declares must resolve too.
	h = newHarness(t)
	h.app.set(func(a *stubApp) { a.meBody = map[string]any{"login": "octocat"} })
	started = h.start("con_1", "acme")
	code, state = h.app.authorize(t, started.AuthorizationURL)
	if _, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code}); !errors.As(err, &failure) {
		t.Fatalf("an account ref that does not resolve = %v, want a Failure", err)
	}
	// An app that revokes one token at a time has the unkept one revoked
	// even with no account to name: rfc7009 revokes the refresh token.
	if revocations := h.app.seenRevocations(); len(revocations) != 1 || revocations[0].form.Get("token_type_hint") != "refresh_token" {
		t.Fatalf("revocations = %+v, want the unkept refresh token revoked", revocations)
	}
}

func TestOAuthDiscoversFieldsFromTheTokenAnswer(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(base string) integration.Manifest {
		manifest := acmeManifest(base)
		manifest.APIBase = "{instance_url}/api"
		manifest.Auth.Methods = manifest.Auth.Methods[:1]
		manifest.Auth.Methods[0].Fields = []integration.Field{
			{Name: "instance_url", Label: "Instance", Level: integration.LevelDiscovered, Discover: "/instance_url",
				Validate: integration.Validation{Required: true, Format: integration.FormatHTTPSURL}, Use: []integration.Use{{In: integration.InURL}}},
			{Name: "team", Label: "Team", Level: integration.LevelDiscovered, Discover: "/team/id",
				Validate: integration.Validation{Pattern: "T[0-9]+"}, Use: []integration.Use{{In: integration.InHeader, Name: "X-Team"}}},
		}
		return manifest
	})
	h.app.set(func(a *stubApp) {
		a.extra = map[string]any{"instance_url": h.app.base, "team": map[string]any{"id": "T42"}}
	})
	completion := h.connect("con_1", "acme")
	if completion.Account.Label != "octocat" {
		t.Fatalf("completion = %+v", completion)
	}
	credential, _ := h.store.stored("con_1")
	if credential.Settings["instance_url"] != h.app.base || credential.Settings["team"] != "T42" {
		t.Fatalf("settings = %v, want the discovered fields", credential.Settings)
	}
	session, err := h.service.Session(context.Background(), "con_1")
	if err != nil {
		t.Fatal(err)
	}
	if session.BaseURL != h.app.base+"/api" || session.Settings["team"] != "T42" {
		t.Fatalf("session base %q, settings %v", session.BaseURL, session.Settings)
	}

	// A required field the answer lacks, or one that breaks its rule, fails.
	for name, extra := range map[string]map[string]any{
		"missing":       {},
		"not https":     {"instance_url": "http://" + stubHost},
		"not a pattern": {"instance_url": h.app.base, "team": map[string]any{"id": "nope"}},
	} {
		h.app.set(func(a *stubApp) { a.extra = extra })
		started := h.start("con_"+strings.ReplaceAll(name, " ", "_"), "acme")
		code, state := h.app.authorize(t, started.AuthorizationURL)
		_, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Reason != ReasonInvalidCredentials || failure.Field == "" {
			t.Fatalf("%s: CompleteOAuth() = %v, want invalid_credentials naming the field", name, err)
		}
	}
}

func TestOAuthChecksTheReturnPageAtBothEnds(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.store.addConnection("con_1", testOrganization, testMember, "acme", "pending")
	for _, returnTo := range []string{"https://evil.example/", "javascript:alert(1)", "https://app.oryxa.test.evil.example/"} {
		_, err := h.service.StartOAuth(context.Background(), StartRequest{
			OrganizationID: testOrganization, MemberID: testMember, ConnectionID: "con_1", Toolkit: "acme", ReturnTo: returnTo,
		})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("return_to %q = %v, want ErrInvalid", returnTo, err)
		}
	}
	started := h.start("con_2", "acme")
	code, state := h.app.authorize(t, started.AuthorizationURL)
	// The allowed pages changed while the member was away.
	h.setReturnAllowed(false)
	completion, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code})
	if err != nil {
		t.Fatalf("CompleteOAuth() = %v", err)
	}
	if completion.ReturnTo != "" {
		t.Fatalf("return page = %q, want none once it is no longer allowed", completion.ReturnTo)
	}
}

func TestOAuthSignsInThroughTheOrganizationsOwnClient(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.app.set(func(a *stubApp) { a.clients["org-acme"] = "org-acme-client-secret-000001" })
	own, changed, err := h.service.PutClient(context.Background(), ClientInput{
		OrganizationID: testOrganization, Client: "acme", ClientID: "org-acme",
		ClientSecret: "org-acme-client-secret-000001", ActorID: "admin_1",
	})
	if err != nil || !changed {
		t.Fatalf("PutClient() = %v, %v", changed, err)
	}
	started := h.start("con_1", "acme")
	if !strings.Contains(started.AuthorizationURL, "client_id=org-acme") {
		t.Fatalf("authorization URL = %s, want the organization's client", started.AuthorizationURL)
	}
	// The organization replaces its client while the member is away: the
	// code is still exchanged by the client it was issued to.
	h.app.set(func(a *stubApp) { a.clients["org-acme-2"] = "org-acme-client-secret-000002" })
	if _, _, err := h.service.PutClient(context.Background(), ClientInput{
		OrganizationID: testOrganization, Client: "acme", ClientID: "org-acme-2", ClientSecret: "org-acme-client-secret-000002",
	}); err != nil {
		t.Fatal(err)
	}
	code, state := h.app.authorize(t, started.AuthorizationURL)
	if _, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code}); err != nil {
		t.Fatalf("CompleteOAuth() = %v", err)
	}
	credential, _ := h.store.stored("con_1")
	if credential.OAuthClientID != own.ID {
		t.Fatalf("the credential names client %q, want the one the flow began with, %q", credential.OAuthClientID, own.ID)
	}

	// Refusing Oryxa's default without a client of its own stops sign-ins.
	if _, err := h.service.RemoveClient(context.Background(), testOrganization, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.service.PutClient(context.Background(), ClientInput{OrganizationID: testOrganization, Client: "acme", RefuseDefault: true}); err != nil {
		t.Fatal(err)
	}
	h.store.addConnection("con_2", testOrganization, testMember, "acme", "pending")
	_, err = h.service.StartOAuth(context.Background(), StartRequest{
		OrganizationID: testOrganization, MemberID: testMember, ConnectionID: "con_2", Toolkit: "acme",
	})
	if !errors.Is(err, ErrNoClient) || !errors.Is(err, domain.ErrNotConnectable) {
		t.Fatalf("StartOAuth() with the default refused = %v, want ErrNoClient", err)
	}
}

func TestOAuthRevokesWhatItCannotKeep(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	started := h.start("con_1", "acme")
	code, state := h.app.authorize(t, started.AuthorizationURL)
	// The member removed the pending connection while at the app.
	h.store.setStatus("con_1", "removed")
	completion, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code})
	if !errors.Is(err, ErrNotPending) || completion.ConnectionID != "con_1" {
		t.Fatalf("CompleteOAuth() = %+v, %v, want ErrNotPending", completion, err)
	}
	revocations := h.app.seenRevocations()
	if len(revocations) != 1 || revocations[0].form.Get("token_type_hint") != "refresh_token" {
		t.Fatalf("revocations = %+v, want the issued refresh token revoked", revocations)
	}
}

func TestStartOAuthRefusesWhatItCannotStart(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.store.addConnection("con_1", testOrganization, testMember, "acme", "pending")
	for name, request := range map[string]StartRequest{
		"no organization": {MemberID: testMember, ConnectionID: "con_1", Toolkit: "acme"},
		"a key's kind":    {OrganizationID: testOrganization, MemberID: testMember, ConnectionID: "con_1", Toolkit: "acme", Kind: integration.KindAPIToken},
		"another kind":    {OrganizationID: testOrganization, MemberID: testMember, ConnectionID: "con_1", Toolkit: "acme", Kind: "oauth1"},
		"an unknown app":  {OrganizationID: testOrganization, MemberID: testMember, ConnectionID: "con_1", Toolkit: "zeta"},
		"a stray field":   {OrganizationID: testOrganization, MemberID: testMember, ConnectionID: "con_1", Toolkit: "acme", Fields: map[string]string{"region": "eu"}},
	} {
		if _, err := h.service.StartOAuth(context.Background(), request); err == nil {
			t.Errorf("%s: StartOAuth() succeeded", name)
		}
	}
}

func TestOAuthFinishesOnceItsFlowIsSpentEvenIfTheBrowserLeaves(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	started := h.start("con_1", "acme")
	code, state := h.app.authorize(t, started.AuthorizationURL)
	// The member's browser went away as the callback arrived. The memory
	// store spends the flow regardless, as Postgres would have had the
	// statement already run.
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	completion, err := h.service.CompleteOAuth(gone, Callback{State: state, CookieState: started.State, Code: code})
	if err != nil || completion.Account.Label != "octocat" {
		t.Fatalf("CompleteOAuth() with the browser gone = %+v, %v, want it finished", completion, err)
	}
	if _, kept := h.store.stored("con_1"); !kept {
		t.Fatal("the credential was not kept")
	}
}
