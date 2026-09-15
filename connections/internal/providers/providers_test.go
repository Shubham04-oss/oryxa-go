package providers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

var testNow = time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)

// pointAt aims a provider's OAuth2 flow at a test server.
func pointAt(provider Provider, server *httptest.Server) OAuth2 {
	flow := *provider.OAuth2
	flow.AuthorizationURL = server.URL + "/authorize"
	flow.TokenURL = server.URL + "/token"
	flow.APIURL = server.URL + "/api"
	return flow
}

func testClient(server *httptest.Server) Client {
	return Client{HTTP: server.Client(), Now: func() time.Time { return testNow }}
}

func exchangeRequest(code string, scopes ...string) ExchangeRequest {
	return ExchangeRequest{ClientID: "client", ClientSecret: "secret-value", Code: code,
		RedirectURI: "https://connect.example.test/v1/oauth/callback/test", PKCEVerifier: "verifier", Scopes: scopes}
}

func authorizationQuery(t *testing.T, client Client, flow OAuth2, scopes ...string) url.Values {
	t.Helper()
	value, err := client.AuthorizationURL(flow, AuthorizationRequest{
		ClientID: "client", RedirectURI: "https://connect.example.test/v1/oauth/callback/test",
		Scopes: scopes, State: "state-1", PKCEChallenge: "challenge-1",
	})
	if err != nil {
		t.Fatalf("AuthorizationURL() error = %v", err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("client_id") != "client" || query.Get("state") != "state-1" || query.Get("response_type") != "code" || query.Has("client_secret") {
		t.Fatalf("authorization query = %v", query)
	}
	return query
}

func TestBuiltinProvidersAreConsistent(t *testing.T) {
	registry := Builtin()
	// Every provider supports oauth2 first (apps default to it); some also
	// support other credential types.
	credentialTypes := map[string][]domain.CredentialType{
		"github":    {domain.CredentialOAuth2, domain.CredentialInstallation, domain.CredentialBearer},
		"google":    {domain.CredentialOAuth2},
		"linear":    {domain.CredentialOAuth2, domain.CredentialAPIKey},
		"microsoft": {domain.CredentialOAuth2, domain.CredentialOAuth2ClientCredentials},
		"slack":     {domain.CredentialOAuth2},
	}
	for _, name := range []string{"github", "google", "linear", "microsoft", "slack"} {
		provider, ok := registry.Lookup(" " + strings.ToUpper(name) + " ")
		if !ok || provider.Name != name {
			t.Fatalf("Lookup(%q) = %+v %v", name, provider, ok)
		}
		if !slices.Equal(provider.CredentialTypes, credentialTypes[name]) {
			t.Fatalf("%s credential types = %v, want %v", name, provider.CredentialTypes, credentialTypes[name])
		}
		for _, credentialType := range credentialTypes[name] {
			if !provider.Supports(credentialType) {
				t.Fatalf("%s does not support its own %s credential type", name, credentialType)
			}
		}
		if _, err := domain.AppScopes(nil, provider.Scopes.Ceiling, provider.Scopes.Required); err != nil {
			t.Fatalf("%s required scopes are outside its ceiling: %v", name, err)
		}
		for narrow, broader := range provider.Scopes.Implications {
			for _, scope := range append([]string{narrow}, broader...) {
				if !slices.Contains(provider.Scopes.Ceiling, scope) {
					t.Fatalf("%s implication names %q, which is not in its ceiling", name, scope)
				}
			}
		}
		for _, endpoint := range []string{provider.OAuth2.AuthorizationURL, provider.OAuth2.TokenURL, provider.OAuth2.APIURL} {
			if parsed, err := url.Parse(endpoint); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				t.Fatalf("%s endpoint %q is not an absolute HTTPS URL", name, endpoint)
			}
		}
		if provider.OAuth2.Identity == nil {
			t.Fatalf("%s has no identity check", name)
		}
	}
	if _, ok := registry.Lookup("jira"); ok {
		t.Fatal("Lookup(jira) found a provider")
	}
	first, second := GitHub(), GitHub()
	first.OAuth2.TokenURL = "https://changed.example.test"
	if second.OAuth2.TokenURL == first.OAuth2.TokenURL {
		t.Fatal("GitHub() shares its OAuth2 config between calls")
	}
}

func TestGitHubFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if request.ParseForm() != nil || request.Header.Get("Accept") != "application/json" || request.Form.Get("client_secret") != "secret-value" {
				t.Errorf("token request = %v %v", request.Header, request.Form)
			}
			switch {
			case request.Form.Get("refresh_token") == "revoked":
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":"bad_refresh_token","error_description":"sensitive detail"}`)
			case request.Form.Get("grant_type") == "refresh_token":
				_, _ = io.WriteString(writer, `{"access_token":"new-token","refresh_token":"new-refresh","scope":"repo,read:user","token_type":"bearer","expires_in":28800}`)
			case request.Form.Get("code") == "bad-code":
				// GitHub reports exchange errors with a 200 status.
				_, _ = io.WriteString(writer, `{"error":"bad_verification_code"}`)
			default:
				_, _ = io.WriteString(writer, `{"access_token":"token","refresh_token":"refresh","scope":"repo,read:user","token_type":"bearer","expires_in":28800}`)
			}
		case "/api/user":
			if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
				t.Errorf("identity request headers = %v", request.Header)
			}
			_, _ = io.WriteString(writer, `{"id":42,"login":"octocat","name":"Mona"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(GitHub(), server)

	query := authorizationQuery(t, client, flow, "repo", "read:user")
	if query.Get("scope") != "read:user repo" || query.Has("code_challenge") {
		t.Fatalf("GitHub authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code"))
	if err != nil || grant.AccessToken != "token" || grant.RefreshToken != "refresh" ||
		!slices.Equal(grant.Scopes, []string{"read:user", "repo"}) || !grant.Expiry.Equal(testNow.Add(8*time.Hour)) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	if _, err := client.Exchange(ctx, flow, exchangeRequest("bad-code")); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("bad code error = %v", err)
	}
	account, err := client.Identity(ctx, flow, grant)
	if err != nil || account != (domain.ProviderAccount{ID: "42", Name: "octocat"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	refreshed, err := client.Refresh(ctx, flow, RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "refresh"})
	if err != nil || refreshed.AccessToken != "new-token" || refreshed.RefreshToken != "new-refresh" {
		t.Fatalf("Refresh() = %+v %v", refreshed, err)
	}
	_, err = client.Refresh(ctx, flow, RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "revoked"})
	if !errors.Is(err, domain.ErrCredentialRejected) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("revoked refresh error = %v", err)
	}
}

func TestGoogleFlow(t *testing.T) {
	gmailSend := googleScopePrefix + "gmail.send"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			_ = request.ParseForm()
			switch {
			case request.Form.Get("grant_type") == "refresh_token":
				if request.Form.Has("scope") {
					t.Errorf("Google refresh sent scope %q", request.Form.Get("scope"))
				}
				// Google does not rotate refresh tokens or repeat the scope.
				_, _ = io.WriteString(writer, `{"access_token":"new-token","token_type":"Bearer","expires_in":3599}`)
			case request.Form.Get("code") == "no-refresh":
				_, _ = io.WriteString(writer, `{"access_token":"token","scope":"openid","token_type":"Bearer","expires_in":3599}`)
			default:
				if request.Form.Get("code_verifier") != "verifier" {
					t.Errorf("code_verifier = %q", request.Form.Get("code_verifier"))
				}
				_, _ = io.WriteString(writer, `{"access_token":"token","refresh_token":"refresh","token_type":"Bearer","expires_in":"3599",
					"scope":"openid https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/gmail.send"}`)
			}
		case "/api/userinfo":
			if request.Header.Get("Authorization") == "Bearer unverified" {
				_, _ = io.WriteString(writer, `{"sub":"google-7","email":"new@example.com","email_verified":false}`)
				return
			}
			_, _ = io.WriteString(writer, `{"sub":"google-42","email":"asha@example.com","email_verified":true,"name":"Asha"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Google(), server)

	query := authorizationQuery(t, client, flow, "openid", "email", "profile", gmailSend)
	if query.Get("access_type") != "offline" || query.Get("prompt") != "consent" || query.Get("include_granted_scopes") != "true" ||
		query.Get("code_challenge") != "challenge-1" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("Google authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code", "openid", "email", "profile", gmailSend))
	if err != nil || !slices.Equal(grant.Scopes, []string{"email", gmailSend, "openid", "profile"}) ||
		!grant.Expiry.Equal(testNow.Add(3599*time.Second)) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	if _, err := client.Exchange(ctx, flow, exchangeRequest("no-refresh", "openid")); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("exchange without a refresh token error = %v", err)
	}
	account, err := client.Identity(ctx, flow, grant)
	if err != nil || account != (domain.ProviderAccount{ID: "google-42", Name: "asha@example.com"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	if _, err := client.Identity(ctx, flow, Grant{AccessToken: "unverified"}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("unverified email error = %v", err)
	}
	refreshed, err := client.Refresh(ctx, flow, RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "refresh", Scopes: grant.Scopes})
	if err != nil || refreshed.AccessToken != "new-token" || refreshed.RefreshToken != "refresh" || !slices.Equal(refreshed.Scopes, grant.Scopes) {
		t.Fatalf("Refresh() = %+v %v", refreshed, err)
	}
}

func TestMicrosoftFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			_ = request.ParseForm()
			switch {
			case request.Form.Get("grant_type") == "refresh_token":
				if request.Form.Get("scope") != "Mail.Send User.Read offline_access openid profile" {
					t.Errorf("refresh scope = %q", request.Form.Get("scope"))
				}
				if request.Form.Get("refresh_token") == "expired" {
					writer.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(writer, `{"error":"invalid_grant","error_description":"refresh token expired-value"}`)
					return
				}
				_, _ = io.WriteString(writer, `{"access_token":"new-token","refresh_token":"rotated","scope":"User.Read Mail.Send","token_type":"Bearer","expires_in":1800}`)
			case request.Form.Get("code") == "no-refresh":
				_, _ = io.WriteString(writer, `{"access_token":"token","scope":"User.Read","token_type":"Bearer","expires_in":3600}`)
			default:
				_, _ = io.WriteString(writer, `{"access_token":"token","refresh_token":"refresh","scope":"User.Read Mail.Send","token_type":"Bearer","expires_in":3600}`)
			}
		case "/api/me":
			if request.URL.Query().Get("$select") != "id,displayName,userPrincipalName,mail" {
				t.Errorf("identity query = %v", request.URL.Query())
			}
			_, _ = io.WriteString(writer, `{"id":"user-42","displayName":"Asha","userPrincipalName":"asha@example.com"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Microsoft(), server)

	query := authorizationQuery(t, client, flow, "User.Read", "offline_access", "Mail.Send", "openid", "profile", "offline_access")
	if !slices.Equal(strings.Fields(query.Get("scope")), []string{"Mail.Send", "User.Read", "offline_access", "openid", "profile"}) {
		t.Fatalf("Microsoft authorization scope = %q", query.Get("scope"))
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code", "Mail.Send", "User.Read", "openid", "profile"))
	// The token response leaves out the protocol scopes that were requested.
	want := []string{"Mail.Send", "User.Read", "offline_access", "openid", "profile"}
	if err != nil || !slices.Equal(grant.Scopes, want) || grant.RefreshToken != "refresh" {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	if _, err := client.Exchange(ctx, flow, exchangeRequest("no-refresh", "User.Read")); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("exchange without a refresh token error = %v", err)
	}
	account, err := client.Identity(ctx, flow, grant)
	if err != nil || account != (domain.ProviderAccount{ID: "user-42", Name: "asha@example.com"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	refreshed, err := client.Refresh(ctx, flow, RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "refresh", Scopes: grant.Scopes})
	if err != nil || refreshed.RefreshToken != "rotated" || !slices.Equal(refreshed.Scopes, want) || !refreshed.Expiry.Equal(testNow.Add(30*time.Minute)) {
		t.Fatalf("Refresh() = %+v %v", refreshed, err)
	}
	_, err = client.Refresh(ctx, flow, RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "expired", Scopes: grant.Scopes})
	if !errors.Is(err, domain.ErrCredentialRejected) || strings.Contains(err.Error(), "expired-value") {
		t.Fatalf("expired refresh error = %v", err)
	}
}

func TestSlackFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			_ = request.ParseForm()
			switch request.Form.Get("code") {
			case "bad-code":
				_, _ = io.WriteString(writer, `{"ok":false,"error":"invalid_code"}`)
			case "bot-only":
				_, _ = io.WriteString(writer, `{"ok":true,"access_token":"xoxb-bot","token_type":"bot","scope":"channels:history","team":{"id":"T123"},"authed_user":{"id":"U123"}}`)
			default:
				_, _ = io.WriteString(writer, `{"ok":true,"access_token":"xoxb-bot","token_type":"bot","scope":"channels:history,groups:history",
					"team":{"id":"T123","name":"Oryxa"},"authed_user":{"id":"U123","scope":"users:read,chat:write","access_token":"xoxp-user","token_type":"user"}}`)
			}
		case "/api/auth.test":
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer xoxp-user" {
				t.Errorf("auth.test request = %s %v", request.Method, request.Header)
			}
			_, _ = io.WriteString(writer, `{"ok":true,"team":"Oryxa","user":"asha","team_id":"T123","user_id":"U123"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Slack(), server)

	query := authorizationQuery(t, client, flow, "groups:history", "channels:history")
	if query.Get("scope") != "channels:history,groups:history" || !strings.HasPrefix(query.Get("user_scope"), "search:read,") || query.Has("code_challenge") {
		t.Fatalf("Slack authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code"))
	if err != nil || grant.AccessToken != "xoxb-bot" || grant.Tokens["user"] != "xoxp-user" ||
		!slices.Equal(grant.Scopes, []string{"channels:history", "chat:write", "groups:history", "users:read"}) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	account, err := client.Identity(ctx, flow, grant)
	if err != nil || account != (domain.ProviderAccount{ID: "T123:U123", Name: "asha (Oryxa)"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	if _, err := client.Exchange(ctx, flow, exchangeRequest("bad-code")); !errors.Is(err, domain.ErrCredentialRejected) || !strings.Contains(err.Error(), "invalid_code") {
		t.Fatalf("Slack error = %v", err)
	}
	botOnly, err := client.Exchange(ctx, flow, exchangeRequest("bot-only"))
	if err != nil {
		t.Fatalf("bot-only Exchange() error = %v", err)
	}
	if _, err := client.Identity(ctx, flow, botOnly); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("grant without a user token error = %v", err)
	}
}

func TestLinearFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			_, _ = io.WriteString(writer, `{"access_token":"token","refresh_token":"refresh","scope":"read write","token_type":"Bearer","expires_in":86399}`)
		case "/api/graphql":
			if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
				t.Errorf("GraphQL request = %s %v", request.Method, request.Header)
			}
			if request.Header.Get("Authorization") == "Bearer revoked" {
				_, _ = io.WriteString(writer, `{"errors":[{"message":"Authentication required","extensions":{"code":"UNAUTHENTICATED"}}]}`)
				return
			}
			_, _ = io.WriteString(writer, `{"data":{"viewer":{"id":"11111111-1111-1111-1111-111111111111","name":"ada","displayName":"Ada"},"organization":{"name":"Oryxa"}}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Linear(), server)

	query := authorizationQuery(t, client, flow, "write", "read")
	if query.Get("scope") != "read,write" || query.Get("actor") != "user" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("Linear authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code"))
	if err != nil || !slices.Equal(grant.Scopes, []string{"read", "write"}) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	account, err := client.Identity(ctx, flow, grant)
	if err != nil || account != (domain.ProviderAccount{ID: "11111111-1111-1111-1111-111111111111", Name: "Ada (Oryxa)"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	if _, err := client.Identity(ctx, flow, Grant{AccessToken: "revoked"}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("revoked token error = %v", err)
	}
}

func TestProviderFailuresAreUnavailableAndNeverLeak(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{"server error", func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, `{"error":"secret-value"}`)
		}, domain.ErrProviderUnavailable},
		{"throttled", func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusTooManyRequests)
		}, domain.ErrProviderUnavailable},
		{"malformed", func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, `not json code-value`)
		}, domain.ErrProviderUnavailable},
		{"oversized", func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, strings.Repeat("x", maxResponseBytes+1))
		}, domain.ErrProviderUnavailable},
		{"refused without JSON", func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `<html>secret-value</html>`)
		}, domain.ErrCredentialRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			_, err := testClient(server).Exchange(context.Background(), pointAt(GitHub(), server), exchangeRequest("code-value"))
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "code-value") {
				t.Fatalf("Exchange() error = %v, want %v", err, tc.want)
			}
		})
	}
	server := httptest.NewServer(http.NotFoundHandler())
	client, flow := testClient(server), pointAt(GitHub(), server)
	server.Close()
	if _, err := client.Exchange(context.Background(), flow, exchangeRequest("code")); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("closed server error = %v", err)
	}
	if _, err := client.Identity(context.Background(), flow, Grant{AccessToken: "token"}); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("closed server identity error = %v", err)
	}
}

func TestClientGuards(t *testing.T) {
	if _, err := (Client{}).Refresh(context.Background(), *GitHub().OAuth2, RefreshRequest{ClientID: "client"}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("refresh without a refresh token error = %v", err)
	}
	if _, err := (Client{}).Exchange(context.Background(), *GitHub().OAuth2, ExchangeRequest{ClientID: "client"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("exchange without a code error = %v", err)
	}
	_, err := (Client{}).AuthorizationURL(*Google().OAuth2, AuthorizationRequest{ClientID: "client", RedirectURI: "https://connect.example.test/cb", State: "state"})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("PKCE provider without a challenge error = %v", err)
	}
	verifier, challenge, err := NewPKCE(nil)
	sum := sha256.Sum256([]byte(verifier))
	if err != nil || len(verifier) != 43 || challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatalf("NewPKCE() = %q %q %v", verifier, challenge, err)
	}
}

// TestHTTPClientIsBoundedAndDoesNotFollowRedirects checks NewHTTPClient's
// static configuration directly rather than by dialing a real server: every
// httptest server listens on loopback, and dialer_test.go already proves
// NewHTTPClient refuses to connect there, so a real round trip through it
// could never reach far enough to observe the redirect policy.
func TestHTTPClientIsBoundedAndDoesNotFollowRedirects(t *testing.T) {
	client := NewHTTPClient()
	if client.Timeout != requestTimeout {
		t.Fatalf("Timeout = %s, want %s", client.Timeout, requestTimeout)
	}
	if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() error = %v, want %v", err, http.ErrUseLastResponse)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("Transport = %+v, want a *http.Transport with no Proxy", client.Transport)
	}
}
