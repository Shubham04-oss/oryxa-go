package signin

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// revokingManifest is acme's manifest revoking in style, at revokePath under
// the stub, with the client authenticated as clientAuth says.
func revokingManifest(style, revokePath, clientAuth string) func(base string) integration.Manifest {
	return func(base string) integration.Manifest {
		manifest := acmeManifest(base)
		oauth := manifest.Auth.Methods[0].OAuth2
		oauth.RevokeStyle, oauth.ClientAuth, oauth.RevokeURL = style, clientAuth, ""
		if revokePath != "" {
			oauth.RevokeURL = base + revokePath
		}
		return manifest
	}
}

func TestRevokeInEachStyle(t *testing.T) {
	t.Parallel()
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(defaultClientID+":"+defaultClientSecret))
	cases := []struct {
		style, path, clientAuth string
		// check reads what the app received, given the tokens it issued.
		check   func(t *testing.T, seen seenRevocation, access, refresh string)
		revoked bool
	}{
		{integration.RevokeRFC7009, "/oauth/revoke", integration.ClientAuthBody,
			func(t *testing.T, seen seenRevocation, _, refresh string) {
				// RFC 7009: every server revokes refresh tokens, and one
				// revokes the grant behind it.
				if seen.form.Get("token") != refresh || seen.form.Get("token_type_hint") != "refresh_token" ||
					seen.form.Get("client_id") != defaultClientID || seen.form.Get("client_secret") != defaultClientSecret || seen.basicID != "" {
					t.Fatalf("rfc7009 sent %+v", seen)
				}
			}, true},
		{integration.RevokeRFC7009, "/oauth/revoke", integration.ClientAuthHeader,
			func(t *testing.T, seen seenRevocation, _, refresh string) {
				if seen.form.Get("token") != refresh || seen.form.Has("client_secret") || seen.basicID != defaultClientID ||
					seen.basicPass != defaultClientSecret {
					t.Fatalf("rfc7009 with header auth sent %+v", seen)
				}
			}, true},
		{integration.RevokeRFC7009JSON, "/oauth/revoke-json", integration.ClientAuthHeader,
			func(t *testing.T, seen seenRevocation, access, _ string) {
				if seen.body["token"] != access || seen.basicID != defaultClientID || seen.body["client_secret"] != "" {
					t.Fatalf("rfc7009_json sent %+v", seen)
				}
			}, true},
		{integration.RevokeGitHubToken, "", integration.ClientAuthBody,
			func(t *testing.T, seen seenRevocation, access, _ string) {
				if seen.method != http.MethodDelete || seen.path != "/api/applications/"+defaultClientID+"/token" ||
					seen.body["access_token"] != access || "Basic "+base64.StdEncoding.EncodeToString([]byte(seen.basicID+":"+seen.basicPass)) != basic {
					t.Fatalf("github_token sent %+v", seen)
				}
			}, true},
		{integration.RevokeSlackAuthRevoke, "/api/auth.revoke", integration.ClientAuthHeader,
			func(t *testing.T, seen seenRevocation, access, _ string) {
				if seen.method != http.MethodPost || seen.bearer != access {
					t.Fatalf("slack_auth_revoke sent %+v", seen)
				}
			}, true},
		{integration.RevokeNone, "", integration.ClientAuthBody, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.style+"_"+tc.clientAuth, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, revokingManifest(tc.style, tc.path, tc.clientAuth))
			h.app.set(func(a *stubApp) { a.clientAuth = tc.clientAuth })
			h.connect("con_1", "acme")
			credential, _ := h.store.stored("con_1")
			access, _ := h.service.open(context.Background(), credentialBinding("con_1", sealedAccessToken), credential.Sealed[sealedAccessToken])
			refresh, _ := h.service.open(context.Background(), credentialBinding("con_1", sealedRefreshToken), credential.Sealed[sealedRefreshToken])

			revocation, err := h.service.Revoke(context.Background(), "con_1")
			if err != nil || revocation != (Revocation{ProviderRevoked: tc.revoked}) {
				t.Fatalf("Revoke() = %+v, %v, want provider_revoked %v", revocation, err, tc.revoked)
			}
			seen := h.app.seenRevocations()
			if tc.check == nil {
				if len(seen) != 0 {
					t.Fatalf("revoke_style none sent %+v", seen)
				}
			} else {
				if len(seen) != 1 {
					t.Fatalf("revocations = %+v, want one", seen)
				}
				tc.check(t, seen[0], access.Reveal(), refresh.Reveal())
			}
			if _, kept := h.store.stored("con_1"); kept {
				t.Fatal("the credential was kept after its revocation")
			}
			// Revoking what is already gone is not an error.
			if again, err := h.service.Revoke(context.Background(), "con_1"); err != nil || again != (Revocation{}) {
				t.Fatalf("Revoke() again = %+v, %v", again, err)
			}
		})
	}
}

func TestRevokeCountsAlreadyGoneAsRevoked(t *testing.T) {
	t.Parallel()
	cases := []struct {
		style, path, key string
		status           int
		body             any
	}{
		{integration.RevokeGitHubToken, "", "github", http.StatusNotFound, map[string]any{"message": "Not Found"}},
		{integration.RevokeGitHubToken, "", "github", http.StatusUnprocessableEntity, map[string]any{"message": "gone"}},
		{integration.RevokeSlackAuthRevoke, "/api/auth.revoke", "/api/auth.revoke", http.StatusOK, map[string]any{"ok": false, "error": "token_revoked"}},
		{integration.RevokeRFC7009, "/oauth/revoke", "/oauth/revoke", http.StatusBadRequest, map[string]any{"error": "invalid_token"}},
	}
	for _, tc := range cases {
		h := newHarness(t, revokingManifest(tc.style, tc.path, integration.ClientAuthBody))
		h.app.set(func(a *stubApp) { a.revokeStatus[tc.key], a.revokeBody[tc.key] = tc.status, tc.body })
		h.connect("con_1", "acme")
		if revocation, err := h.service.Revoke(context.Background(), "con_1"); err != nil || !revocation.ProviderRevoked {
			t.Fatalf("%s answering %d = %+v, %v, want revoked", tc.style, tc.status, revocation, err)
		}
	}
}

func TestRevokeSkipsAGrantAnotherConnectionShares(t *testing.T) {
	t.Parallel()
	// Two Google toolkits, one Google client: revoking either's token
	// revokes the grant both hold for the same account.
	google := func(toolkit string) func(base string) integration.Manifest {
		return func(base string) integration.Manifest {
			manifest := acmeManifest(base)
			manifest.Toolkit, manifest.Name = toolkit, toolkit
			manifest.Auth.Methods = manifest.Auth.Methods[:1]
			manifest.Auth.Methods[0].OAuth2.Client = "google"
			return manifest
		}
	}
	h := newHarness(t, google("gmail"), google("googledrive"))
	h.app.set(func(a *stubApp) { a.label = "ada@example.com" })
	h.connect("con_mail", "gmail")
	h.connect("con_drive", "googledrive")

	revocation, err := h.service.Revoke(context.Background(), "con_drive")
	if err != nil || revocation != (Revocation{}) {
		t.Fatalf("Revoke() of a shared grant = %+v, %v, want nothing revoked", revocation, err)
	}
	if seen := h.app.seenRevocations(); len(seen) != 0 {
		t.Fatalf("a shared grant was revoked: %+v", seen)
	}
	if _, kept := h.store.stored("con_drive"); kept {
		t.Fatal("the credential was kept")
	}
	h.store.setStatus("con_drive", "removed")
	// With the other connection gone, the grant is this one's alone.
	revocation, err = h.service.Revoke(context.Background(), "con_mail")
	if err != nil || !revocation.ProviderRevoked || len(h.app.seenRevocations()) != 1 {
		t.Fatalf("Revoke() of a grant no longer shared = %+v, %v", revocation, err)
	}

	// A sign-in that fails names no account, so its unkept token is left
	// to lapse rather than revoked: the grant may be another connection's.
	h.app.set(func(a *stubApp) { a.meBody = map[string]any{"error": "no account"} })
	started := h.start("con_failed_drive", "googledrive")
	code, state := h.app.authorize(t, started.AuthorizationURL)
	if _, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code}); err == nil {
		t.Fatal("a sign-in naming no account succeeded")
	}
	if seen := h.app.seenRevocations(); len(seen) != 1 {
		t.Fatalf("revocations = %+v, want the failed sign-in's token left alone", seen)
	}
	h.app.set(func(a *stubApp) { a.meBody = nil })

	// Another account's grant is its own.
	h.app.set(func(a *stubApp) { a.label = "grace@example.com" })
	h.connect("con_other_mail", "gmail")
	h.app.set(func(a *stubApp) { a.label = "linus@example.com" })
	h.connect("con_other_drive", "googledrive")
	if revocation, err := h.service.Revoke(context.Background(), "con_other_drive"); err != nil || !revocation.ProviderRevoked {
		t.Fatalf("Revoke() of another account's grant = %+v, %v, want revoked", revocation, err)
	}
}

func TestRevokeOneTokenAppsRevokeEvenForTheSameAccount(t *testing.T) {
	t.Parallel()
	// One toolkit per client: the app revokes the one token, so the same
	// account connected twice (a member in two organizations) is no reason
	// to leave it.
	h := newHarness(t)
	h.connect("con_1", "acme")
	h.connect("con_2", "acme")
	if revocation, err := h.service.Revoke(context.Background(), "con_1"); err != nil || !revocation.ProviderRevoked {
		t.Fatalf("Revoke() = %+v, %v, want revoked", revocation, err)
	}
}

func TestRevokeThatFailsIsRetriedAndNeverBlocks(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.app.set(func(a *stubApp) { a.revokeStatus["/oauth/revoke"] = http.StatusServiceUnavailable })
	h.connect("con_1", "acme")

	revocation, err := h.service.Revoke(context.Background(), "con_1")
	if err != nil || revocation != (Revocation{Pending: true}) {
		t.Fatalf("Revoke() while the app is down = %+v, %v, want pending", revocation, err)
	}
	credential, kept := h.store.stored("con_1")
	if !kept || credential.CheckFailures != 1 || !credential.NextCheckAt.Equal(h.clock.Now().Add(5*time.Minute)) {
		t.Fatalf("the kept credential = %+v, want one failure and a retry in 5 minutes", credential)
	}
	// Apps removes the connection; nothing can use the credential now.
	h.store.setStatus("con_1", "removed")
	if _, err := h.service.Session(context.Background(), "con_1"); err == nil {
		t.Fatal("a removed connection's credential made a session")
	}
	if tried, err := h.service.RetryRevocations(context.Background(), 10); err != nil || tried != 0 {
		t.Fatalf("RetryRevocations() before it is due = %d, %v", tried, err)
	}
	h.clock.Advance(5 * time.Minute)
	if tried, err := h.service.RetryRevocations(context.Background(), 10); err != nil || tried != 1 {
		t.Fatalf("RetryRevocations() = %d, %v", tried, err)
	}
	if credential, _ := h.store.stored("con_1"); credential.CheckFailures != 2 || !credential.NextCheckAt.Equal(h.clock.Now().Add(10*time.Minute)) {
		t.Fatalf("after a second failure = %+v, want a retry in 10 minutes", credential)
	}
	h.app.set(func(a *stubApp) { delete(a.revokeStatus, "/oauth/revoke") })
	h.clock.Advance(10 * time.Minute)
	if tried, _ := h.service.RetryRevocations(context.Background(), 10); tried != 1 {
		t.Fatalf("RetryRevocations() = %d, want 1", tried)
	}
	if _, kept := h.store.stored("con_1"); kept {
		t.Fatal("the credential was kept after the app revoked it")
	}

	// An app that never answers is given up on, and the credential
	// forgotten anyway.
	h.app.set(func(a *stubApp) { a.revokeStatus["/oauth/revoke"] = http.StatusBadGateway })
	h.connect("con_2", "acme")
	if revocation, _ := h.service.Revoke(context.Background(), "con_2"); !revocation.Pending {
		t.Fatal("want pending")
	}
	h.store.setStatus("con_2", "failed")
	for attempt := 0; attempt < maxRevokeAttempts; attempt++ {
		h.clock.Advance(maxBackoff)
		if _, err := h.service.RetryRevocations(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
	}
	if _, kept := h.store.stored("con_2"); kept {
		t.Fatal("a credential whose revocation never succeeds was kept for ever")
	}
}

func TestRevokeKeysOnlyForgetsThem(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.app.set(func(a *stubApp) { a.apiToken = "pat-0123456789abcdef" })
	h.store.addConnection("con_1", testOrganization, testMember, "acme", "pending")
	if _, err := h.service.SaveCredential(context.Background(), CredentialRequest{
		OrganizationID: testOrganization, ConnectionID: "con_1", Toolkit: "acme", Kind: integration.KindAPIToken,
		Fields: map[string]string{"token": "pat-0123456789abcdef"},
	}); err != nil {
		t.Fatal(err)
	}
	revocation, err := h.service.Revoke(context.Background(), "con_1")
	if err != nil || revocation != (Revocation{}) || len(h.app.seenRevocations()) != 0 {
		t.Fatalf("Revoke() of a key = %+v, %v, want it only forgotten", revocation, err)
	}
	if _, kept := h.store.stored("con_1"); kept {
		t.Fatal("the key was kept")
	}
}

func TestRevokeRenewsAnExpiredTokenToRevokeWhatIsLive(t *testing.T) {
	t.Parallel()
	h := newHarness(t, revokingManifest(integration.RevokeGitHubToken, "", integration.ClientAuthBody))
	h.connect("con_1", "acme")
	h.untilDue("con_1")
	h.clock.Advance(2 * time.Minute)
	revocation, err := h.service.Revoke(context.Background(), "con_1")
	if err != nil || !revocation.ProviderRevoked {
		t.Fatalf("Revoke() = %+v, %v", revocation, err)
	}
	if _, refreshes, _ := h.app.counts(); refreshes != 1 {
		t.Fatalf("refreshes = %d, want the expired token renewed first", refreshes)
	}
	seen := h.app.seenRevocations()
	if len(seen) != 1 || !strings.HasPrefix(seen[0].body["access_token"], "at-") || h.app.accessLive(seen[0].body["access_token"]) {
		t.Fatalf("revoked %+v, want the renewed, live token", seen)
	}
}
