package providers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
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
		"airtable":  {domain.CredentialOAuth2, domain.CredentialBearer},
		"asana":     {domain.CredentialOAuth2, domain.CredentialBearer},
		"atlassian": {domain.CredentialOAuth2, domain.CredentialBasic},
		"box":       {domain.CredentialOAuth2},
		"clickup":   {domain.CredentialOAuth2, domain.CredentialAPIKey},
		"dropbox":   {domain.CredentialOAuth2},
		"figma":     {domain.CredentialOAuth2, domain.CredentialAPIKey},
		"github":    {domain.CredentialOAuth2, domain.CredentialInstallation, domain.CredentialBearer},
		"google":    {domain.CredentialOAuth2},
		"linear":    {domain.CredentialOAuth2, domain.CredentialAPIKey},
		"microsoft": {domain.CredentialOAuth2, domain.CredentialOAuth2ClientCredentials},
		"notion":    {domain.CredentialOAuth2, domain.CredentialBearer},
		"slack":     {domain.CredentialOAuth2},
	}
	if len(registry) != len(credentialTypes) {
		t.Fatalf("Builtin() has %d providers, want %d", len(registry), len(credentialTypes))
	}
	for _, name := range slices.Sorted(maps.Keys(credentialTypes)) {
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
		if _, err := domain.AppScopes(nil, provider.Scopes); err != nil {
			t.Fatalf("%s required scopes are outside its ceiling: %v", name, err)
		}
		for narrow, broader := range provider.Scopes.Implications {
			for _, scope := range append([]string{narrow}, broader...) {
				if !slices.Contains(provider.Scopes.Ceiling, scope) {
					t.Fatalf("%s implication names %q, which is not in its ceiling", name, scope)
				}
			}
		}
		for _, scope := range provider.Scopes.Exclusive {
			if !slices.Contains(provider.Scopes.Ceiling, scope) {
				t.Fatalf("%s exclusive scope %q is not in its ceiling", name, scope)
			}
		}
		// A credential type whose URLs name {domain} has a rule whose example
		// fills them into absolute HTTPS URLs.
		for _, credentialType := range credentialTypes[name] {
			value := ""
			if provider.NeedsDomain(credentialType) {
				if provider.Domain == nil {
					t.Fatalf("%s %s URLs name a domain but it has no domain rule", name, credentialType)
				}
				value = provider.Domain.Example
			}
			if accepted, err := provider.AppDomain(credentialType, value); err != nil || accepted != value {
				t.Fatalf("%s %s AppDomain(%q) = %q %v", name, credentialType, value, accepted, err)
			}
			filled := provider.WithDomain(value)
			for _, endpoint := range filled.endpoints(credentialType) {
				if endpoint == "" {
					continue
				}
				if parsed, err := url.Parse(endpoint); err != nil || parsed.Scheme != "https" || parsed.Host == "" || strings.Contains(endpoint, "{domain}") {
					t.Fatalf("%s %s endpoint %q is not an absolute HTTPS URL", name, credentialType, endpoint)
				}
			}
			if credentialType.KeyBased() && filled.Keys[credentialType].Identity == nil {
				t.Fatalf("%s has no identity check for %s credentials", name, credentialType)
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

func TestAtlassianFlow(t *testing.T) {
	rotated := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			// Atlassian documents only JSON token requests.
			var body map[string]string
			if request.Header.Get("Content-Type") != "application/json" || json.NewDecoder(request.Body).Decode(&body) != nil ||
				body["client_id"] != "client" || body["client_secret"] != "secret-value" {
				t.Errorf("token request = %v %v", request.Header, body)
			}
			switch {
			case body["grant_type"] == "authorization_code" && body["code"] == "code":
				// The granted scopes may leave out offline_access.
				_, _ = io.WriteString(writer, `{"access_token":"token","refresh_token":"refresh-1","scope":"read:jira-work read:me","token_type":"Bearer","expires_in":3600}`)
			case body["grant_type"] == "refresh_token" && body["refresh_token"] == "refresh-1" && !rotated:
				rotated = true
				_, _ = io.WriteString(writer, `{"access_token":"token-2","refresh_token":"refresh-2","scope":"offline_access read:jira-work read:me","expires_in":3600}`)
			default:
				writer.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(writer, `{"error":"invalid_grant","error_description":"Unknown or invalid refresh token."}`)
			}
		case "/api/me":
			if request.Header.Get("Authorization") != "Bearer token" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(writer, `{"account_id":"557058:f58131cb","email":"ada@example.test","name":"Ada Lovelace","nickname":"ada"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Atlassian(), server)

	query := authorizationQuery(t, client, flow, "read:me", "read:jira-work", "offline_access")
	if query.Get("audience") != "api.atlassian.com" || query.Get("prompt") != "consent" ||
		query.Get("scope") != "offline_access read:jira-work read:me" || query.Has("code_challenge") {
		t.Fatalf("Atlassian authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code", "offline_access", "read:jira-work", "read:me"))
	if err != nil || grant.RefreshToken != "refresh-1" || !slices.Equal(grant.Scopes, []string{"offline_access", "read:jira-work", "read:me"}) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	account, err := client.Identity(ctx, flow, grant)
	if err != nil || account != (domain.ProviderAccount{ID: "557058:f58131cb", Name: "ada@example.test"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	// Refresh tokens rotate, and one that was used is refused for good.
	refresh := RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "refresh-1", Scopes: grant.Scopes}
	refreshed, err := client.Refresh(ctx, flow, refresh)
	if err != nil || refreshed.AccessToken != "token-2" || refreshed.RefreshToken != "refresh-2" {
		t.Fatalf("Refresh() = %+v %v", refreshed, err)
	}
	if _, err := client.Refresh(ctx, flow, refresh); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("reused refresh token error = %v, want rejected", err)
	}
	if _, err := client.Identity(ctx, flow, Grant{AccessToken: "revoked"}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("revoked token error = %v", err)
	}
}

func TestAsanaFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if err := request.ParseForm(); err != nil || request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
				request.PostForm.Get("code_verifier") != "verifier" || request.PostForm.Get("client_secret") != "secret-value" {
				t.Errorf("token request = %v %v", request.Header, request.PostForm)
			}
			// Asana names the member in the token response but sends no scope.
			_, _ = io.WriteString(writer, `{"access_token":"token","refresh_token":"refresh","token_type":"bearer","expires_in":3600,"data":{"id":"4673218951","gid":"4673218951","name":"Ada Lovelace","email":"ada@example.test"}}`)
		case "/api/users/me":
			if request.Header.Get("Authorization") != "Bearer token" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(writer, `{"data":{"gid":"4673218951","email":"ada@example.test","name":"Ada Lovelace","resource_type":"user"}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Asana(), server)

	query := authorizationQuery(t, client, flow, "users:read", "tasks:read")
	if query.Get("scope") != "tasks:read users:read" || query.Get("code_challenge") != "challenge-1" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("Asana authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code", "tasks:read", "users:read"))
	if err != nil || grant.RefreshToken != "refresh" || !grant.Expiry.Equal(testNow.Add(time.Hour)) ||
		!slices.Equal(grant.Scopes, []string{"tasks:read", "users:read"}) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	account, err := client.Identity(ctx, flow, grant)
	if err != nil || account != (domain.ProviderAccount{ID: "4673218951", Name: "ada@example.test"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	if _, err := client.Identity(ctx, flow, Grant{AccessToken: "revoked"}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("revoked token error = %v", err)
	}
}

func TestClickUpFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if err := request.ParseForm(); err != nil || request.PostForm.Get("client_id") != "client" ||
				request.PostForm.Get("client_secret") != "secret-value" || request.PostForm.Get("code") != "code" {
				t.Errorf("token request = %v", request.PostForm)
			}
			// ClickUp answers with the access token alone.
			_, _ = io.WriteString(writer, `{"access_token":"token"}`)
		case "/api/user":
			if request.Header.Get("Authorization") != "Bearer token" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(writer, `{"user":{"id":183,"username":"ada","email":"ada@example.test","color":"#827718"}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(ClickUp(), server)

	if query := authorizationQuery(t, client, flow); query.Has("scope") || query.Has("code_challenge") {
		t.Fatalf("ClickUp authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code"))
	if err != nil || grant.AccessToken != "token" || grant.RefreshToken != "" || !grant.Expiry.IsZero() || len(grant.Scopes) != 0 {
		t.Fatalf("Exchange() = %+v %v, want a token that never expires", grant, err)
	}
	account, err := client.Identity(ctx, flow, grant)
	if err != nil || account != (domain.ProviderAccount{ID: "183", Name: "ada@example.test"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	if _, err := client.Identity(ctx, flow, Grant{AccessToken: "revoked"}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("revoked token error = %v", err)
	}
}

func TestTokenRequestsCanAuthenticateTheClientWithBasic(t *testing.T) {
	type seen struct {
		user, password string
		basic          bool
		fields         map[string]string
	}
	var requests []seen
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		user, password, basic := request.BasicAuth()
		fields := map[string]string{}
		if request.Header.Get("Content-Type") == "application/json" {
			_ = json.NewDecoder(request.Body).Decode(&fields)
		} else if request.ParseForm() == nil {
			for name := range request.PostForm {
				fields[name] = request.PostForm.Get(name)
			}
		}
		requests = append(requests, seen{user, password, basic, fields})
		_, _ = io.WriteString(writer, `{"access_token":"token","refresh_token":"refresh","expires_in":3600}`)
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(GitHub(), server)
	flow.ClientSecretBasic = true

	secret := "secret value+/:"
	if _, err := client.Exchange(ctx, flow, ExchangeRequest{ClientID: "client id", ClientSecret: secret, Code: "code", RedirectURI: "https://connect.example.test/callback"}); err != nil {
		t.Fatal(err)
	}
	flow.JSONTokenRequests = true
	if _, err := client.Refresh(ctx, flow, RefreshRequest{ClientID: "client id", ClientSecret: secret, RefreshToken: "refresh"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].fields["code"] != "code" || requests[1].fields["refresh_token"] != "refresh" {
		t.Fatalf("token requests = %+v", requests)
	}
	for _, request := range requests {
		// The ID and secret are form-encoded, then sent only as Basic credentials.
		if !request.basic || request.user != "client+id" || request.password != "secret+value%2B%2F%3A" {
			t.Errorf("Basic credentials = %q %q %v", request.user, request.password, request.basic)
		}
		for _, name := range []string{"client_id", "client_secret"} {
			if _, carried := request.fields[name]; carried {
				t.Errorf("token request still carries %s: %v", name, request.fields)
			}
		}
	}
}

func TestGrantsKeepTheTokenMetadataTheProviderNames(t *testing.T) {
	var refreshResponse string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.ParseForm() == nil && request.PostForm.Get("grant_type") == "refresh_token" {
			_, _ = io.WriteString(writer, refreshResponse)
			return
		}
		_, _ = io.WriteString(writer, `{"access_token":"token","refresh_token":"refresh","expires_in":3600,
			"instance_url":"https://acme.my.salesforce.test","issued_at":1278448832702,"signature":"line\nbreak","unnamed":"kept out"}`)
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(GitHub(), server)
	flow.Metadata = []string{"instance_url", "issued_at", "signature", "workspace_id"}

	// Only named fields that are safe strings are kept.
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code"))
	if err != nil || !maps.Equal(grant.Metadata, map[string]string{"instance_url": "https://acme.my.salesforce.test"}) {
		t.Fatalf("Exchange() metadata = %v %v", grant.Metadata, err)
	}
	refresh := RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "refresh",
		Metadata: map[string]string{"instance_url": "https://acme.my.salesforce.test", "dropped": "no longer named"}}

	// A refresh that leaves a field out keeps it; one that sends it replaces it.
	refreshResponse = `{"access_token":"token-2","expires_in":3600}`
	if refreshed, err := client.Refresh(ctx, flow, refresh); err != nil ||
		!maps.Equal(refreshed.Metadata, map[string]string{"instance_url": "https://acme.my.salesforce.test"}) {
		t.Fatalf("Refresh() metadata = %v %v", refreshed.Metadata, err)
	}
	refreshResponse = `{"access_token":"token-3","expires_in":3600,"instance_url":"https://acme2.my.salesforce.test"}`
	if refreshed, err := client.Refresh(ctx, flow, refresh); err != nil ||
		!maps.Equal(refreshed.Metadata, map[string]string{"instance_url": "https://acme2.my.salesforce.test"}) {
		t.Fatalf("Refresh() replaced metadata = %v %v", refreshed.Metadata, err)
	}

	// A provider that names no metadata keeps none.
	flow.Metadata = nil
	if grant, err := client.Exchange(ctx, flow, exchangeRequest("code")); err != nil || grant.Metadata != nil {
		t.Fatalf("Exchange() without named metadata = %v %v", grant.Metadata, err)
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

func TestAsanaFullPermissions(t *testing.T) {
	rules := Asana().Scopes
	// An app with full permissions allows default alone, which covers the
	// identity check's users:read and every specific scope a caller asks for.
	if scopes, err := domain.AppScopes([]string{"default"}, rules); err != nil || !slices.Equal(scopes, []string{"default"}) {
		t.Fatalf("AppScopes(default) = %v %v", scopes, err)
	}
	if _, err := domain.AppScopes([]string{"default", "tasks:read"}, rules); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("AppScopes(default with tasks:read) error = %v", err)
	}
	if missing := domain.MissingScopes([]string{"tasks:write", "projects:read"}, []string{"default"}, []string{"default"}, rules.Implications); len(missing) != 0 {
		t.Fatalf("scopes default does not cover = %v", missing)
	}
	if missing := domain.MissingScopes([]string{"openid"}, []string{"default"}, []string{"default"}, rules.Implications); !slices.Equal(missing, []string{"openid"}) {
		t.Fatalf("default covered OpenID scopes: missing = %v", missing)
	}
	if _, err := domain.AppScopes([]string{"tasks:read"}, rules); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("AppScopes(specific scopes without users:read) error = %v", err)
	}
}

func TestFigmaFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token", "/refresh":
			user, password, basic := request.BasicAuth()
			if request.ParseForm() != nil || !basic || user != "client" || password != "secret-value" || request.PostForm.Has("client_secret") {
				t.Errorf("%s request = %v %v", request.URL.Path, request.Header, request.PostForm)
			}
			switch {
			case request.URL.Path == "/token":
				_, _ = io.WriteString(writer, `{"user_id_string":"12345","access_token":"token","token_type":"bearer","expires_in":7776000,"refresh_token":"refresh"}`)
			case request.PostForm.Get("refresh_token") == "refresh":
				// A refresh returns no new refresh token.
				_, _ = io.WriteString(writer, `{"access_token":"token-2","token_type":"bearer","expires_in":7776000}`)
			default:
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":true,"status":400,"message":"Invalid refresh token"}`)
			}
		case "/api/me":
			if request.Header.Get("Authorization") != "Bearer token" {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = io.WriteString(writer, `{"id":"12345","handle":"ada","img_url":"https://example.test/ada.png","email":"ada@example.test"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Figma(), server)
	flow.RefreshURL = server.URL + "/refresh"

	query := authorizationQuery(t, client, flow, "file_content:read", "current_user:read")
	if query.Get("scope") != "current_user:read file_content:read" || query.Get("code_challenge") != "challenge-1" {
		t.Fatalf("Figma authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code", "current_user:read", "file_content:read"))
	if err != nil || grant.RefreshToken != "refresh" || !grant.Expiry.Equal(testNow.Add(90*24*time.Hour)) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	if account, err := client.Identity(ctx, flow, grant); err != nil || account != (domain.ProviderAccount{ID: "12345", Name: "ada@example.test"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	refreshed, err := client.Refresh(ctx, flow, RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "refresh"})
	if err != nil || refreshed.AccessToken != "token-2" || refreshed.RefreshToken != "refresh" {
		t.Fatalf("Refresh() = %+v %v, want the refresh token kept", refreshed, err)
	}
	// Figma documents no refresh error codes, so any refusal is for good.
	if _, err := client.Refresh(ctx, flow, RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "revoked"}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("refused refresh error = %v", err)
	}
}

func TestNotionFlow(t *testing.T) {
	const botID, workspaceID = "b1f8e3c5-0000-4000-8000-000000000001", "1d2c3b4a-0000-4000-8000-000000000002"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			user, password, basic := request.BasicAuth()
			body := map[string]string{}
			if request.Header.Get("Notion-Version") != notionVersion || request.Header.Get("Content-Type") != "application/json" ||
				!basic || user != "client" || password != "secret-value" || json.NewDecoder(request.Body).Decode(&body) != nil {
				t.Errorf("token request = %v %v", request.Header, body)
			}
			if _, carried := body["client_secret"]; carried {
				t.Errorf("token request carries the client secret: %v", body)
			}
			if body["grant_type"] == "refresh_token" {
				// Notion reports errors in its API format.
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"object":"error","status":400,"code":"invalid_grant","message":"Invalid refresh token."}`)
				return
			}
			_, _ = io.WriteString(writer, `{"access_token":"token","token_type":"bearer","refresh_token":null,"bot_id":"`+botID+`",
				"workspace_name":"Ada's Notion","workspace_icon":null,"workspace_id":"`+workspaceID+`",
				"owner":{"type":"user","user":{"object":"user","id":"u1"}},"duplicated_template_id":null,"request_id":"r1"}`)
		case "/api/users/me":
			if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("Notion-Version") != notionVersion {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			// A bot a member owns carries no workspace name of its own.
			_, _ = io.WriteString(writer, `{"object":"user","id":"`+botID+`","name":"Oryxa","type":"bot","bot":{"owner":{"type":"user","user":{"object":"user","id":"u1"}},"workspace_name":null}}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Notion(), server)

	if query := authorizationQuery(t, client, flow); query.Get("owner") != "user" || query.Has("scope") || query.Has("code_challenge") {
		t.Fatalf("Notion authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code"))
	metadata := map[string]string{"bot_id": botID, "workspace_id": workspaceID, "workspace_name": "Ada's Notion"}
	if err != nil || grant.AccessToken != "token" || grant.RefreshToken != "" || !grant.Expiry.IsZero() || !maps.Equal(grant.Metadata, metadata) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	if account, err := client.Identity(ctx, flow, grant); err != nil || account != (domain.ProviderAccount{ID: botID, Name: "Ada's Notion"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	if _, err := client.Refresh(ctx, flow, RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "refresh"}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("refused refresh error = %v, want invalid_grant read from code", err)
	}
	if _, err := client.Identity(ctx, flow, Grant{AccessToken: "revoked"}); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("revoked token error = %v", err)
	}
}

func TestBoxFlow(t *testing.T) {
	rotated := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if request.ParseForm() != nil || request.PostForm.Get("client_secret") != "secret-value" {
				t.Errorf("token request = %v", request.PostForm)
			}
			switch {
			case request.PostForm.Get("grant_type") == "authorization_code":
				_, _ = io.WriteString(writer, `{"access_token":"token","expires_in":3600,"restricted_to":[],"refresh_token":"refresh-1","token_type":"bearer"}`)
			case request.PostForm.Get("refresh_token") == "refresh-1" && !rotated:
				rotated = true
				_, _ = io.WriteString(writer, `{"access_token":"token-2","expires_in":3600,"refresh_token":"refresh-2","token_type":"bearer"}`)
			default:
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":"invalid_grant","error_description":"Invalid refresh token"}`)
			}
		case "/api/users/me":
			if request.Header.Get("Authorization") != "Bearer token" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(writer, `{"type":"user","id":"11446498","name":"Aaron Levie","login":"ceo@example.test"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Box(), server)

	if query := authorizationQuery(t, client, flow, "root_readwrite"); query.Get("scope") != "root_readwrite" || query.Has("code_challenge") {
		t.Fatalf("Box authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code", "root_readwrite"))
	if err != nil || grant.RefreshToken != "refresh-1" || !grant.Expiry.Equal(testNow.Add(time.Hour)) || !slices.Equal(grant.Scopes, []string{"root_readwrite"}) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	if account, err := client.Identity(ctx, flow, grant); err != nil || account != (domain.ProviderAccount{ID: "11446498", Name: "ceo@example.test"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	// Refresh tokens work once.
	refresh := RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "refresh-1"}
	if refreshed, err := client.Refresh(ctx, flow, refresh); err != nil || refreshed.RefreshToken != "refresh-2" {
		t.Fatalf("Refresh() = %+v %v", refreshed, err)
	}
	if _, err := client.Refresh(ctx, flow, refresh); !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("reused refresh token error = %v", err)
	}
}

func TestAirtableFlow(t *testing.T) {
	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			user, password, basic := request.BasicAuth()
			if request.ParseForm() != nil || !basic || user != "client" || password != "secret-value" ||
				request.PostForm.Has("client_id") || request.PostForm.Has("client_secret") {
				t.Errorf("token request = %v %v", request.Header, request.PostForm)
			}
			if request.PostForm.Get("grant_type") == "authorization_code" {
				if request.PostForm.Get("code_verifier") != "verifier" {
					t.Errorf("exchange without its PKCE verifier: %v", request.PostForm)
				}
				_, _ = io.WriteString(writer, `{"access_token":"token","refresh_token":"refresh-1","token_type":"Bearer ","scope":"data.records:read user.email:read","expires_in":3600,"refresh_expires_in":5184000}`)
				return
			}
			refreshes++
			if refreshes == 1 {
				// Refreshing again too soon conflicts, which is worth retrying.
				writer.WriteHeader(http.StatusConflict)
				return
			}
			_, _ = io.WriteString(writer, `{"access_token":"token-2","refresh_token":"refresh-2","token_type":"Bearer ","scope":"data.records:read user.email:read","expires_in":3600,"refresh_expires_in":5184000}`)
		case "/api/meta/whoami":
			if request.Header.Get("Authorization") != "Bearer token" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(writer, `{"id":"usrL2PNC5o3H4lBEi","email":"ada@example.test","scopes":["data.records:read","user.email:read"]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Airtable(), server)

	query := authorizationQuery(t, client, flow, "user.email:read", "data.records:read")
	if query.Get("scope") != "data.records:read user.email:read" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("Airtable authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code", "data.records:read", "user.email:read"))
	if err != nil || grant.RefreshToken != "refresh-1" || !slices.Equal(grant.Scopes, []string{"data.records:read", "user.email:read"}) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	if account, err := client.Identity(ctx, flow, grant); err != nil || account != (domain.ProviderAccount{ID: "usrL2PNC5o3H4lBEi", Name: "ada@example.test"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	refresh := RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "refresh-1"}
	if _, err := client.Refresh(ctx, flow, refresh); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("conflicting refresh error = %v, want unavailable", err)
	}
	if refreshed, err := client.Refresh(ctx, flow, refresh); err != nil || refreshed.RefreshToken != "refresh-2" {
		t.Fatalf("Refresh() = %+v %v", refreshed, err)
	}
}

func TestDropboxFlow(t *testing.T) {
	const accountID = "dbid:AAH4f99T0taONIb-OurWxbNQ6ywGRopQngc"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if request.ParseForm() != nil || request.PostForm.Get("client_secret") != "secret-value" {
				t.Errorf("token request = %v", request.PostForm)
			}
			if request.PostForm.Get("grant_type") == "refresh_token" {
				_, _ = io.WriteString(writer, `{"access_token":"sl.token-2","expires_in":14400,"token_type":"bearer"}`)
				return
			}
			_, _ = io.WriteString(writer, `{"access_token":"sl.token","expires_in":14400,"token_type":"bearer","scope":"account_info.read files.content.read","account_id":"`+accountID+`","refresh_token":"refresh","uid":"12345"}`)
		case "/api/users/get_current_account":
			// An RPC endpoint without arguments takes no body.
			if request.Method != http.MethodPost || request.ContentLength > 0 || request.Header.Get("Authorization") != "Bearer sl.token" {
				t.Errorf("identity request = %s %d %v", request.Method, request.ContentLength, request.Header)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(writer, `{"account_id":"`+accountID+`","name":{"display_name":"Franz Ferdinand (Personal)"},"email":"franz@example.test"}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	ctx, client, flow := context.Background(), testClient(server), pointAt(Dropbox(), server)

	query := authorizationQuery(t, client, flow, "files.content.read", "account_info.read")
	if query.Get("token_access_type") != "offline" || query.Get("scope") != "account_info.read files.content.read" {
		t.Fatalf("Dropbox authorization query = %v", query)
	}
	grant, err := client.Exchange(ctx, flow, exchangeRequest("code", "account_info.read", "files.content.read"))
	if err != nil || grant.RefreshToken != "refresh" || !grant.Expiry.Equal(testNow.Add(4*time.Hour)) {
		t.Fatalf("Exchange() = %+v %v", grant, err)
	}
	if account, err := client.Identity(ctx, flow, grant); err != nil || account != (domain.ProviderAccount{ID: accountID, Name: "franz@example.test"}) {
		t.Fatalf("Identity() = %+v %v", account, err)
	}
	if refreshed, err := client.Refresh(ctx, flow, RefreshRequest{ClientID: "client", ClientSecret: "secret-value", RefreshToken: "refresh"}); err != nil ||
		refreshed.AccessToken != "sl.token-2" || refreshed.RefreshToken != "refresh" {
		t.Fatalf("Refresh() = %+v %v, want the refresh token kept", refreshed, err)
	}
}
