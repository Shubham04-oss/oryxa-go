package microsoft

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
)

func TestMicrosoftOAuthPKCEOfflineExchangeIdentityAndRefresh(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if request.Header.Get("content-type") != "application/x-www-form-urlencoded" || request.Form.Get("client_secret") != "secret" {
				t.Error("invalid token request")
			}
			if request.Form.Get("grant_type") == "refresh_token" {
				if request.Form.Get("refresh_token") != "refresh" || request.Form.Get("scope") != "Mail.Send User.Read" {
					t.Errorf("refresh form = %#v", request.Form)
				}
				_, _ = writer.Write([]byte(`{"access_token":"new-token","refresh_token":"rotated-refresh","scope":"User.Read Mail.Send","token_type":"Bearer","expires_in":1800}`))
				return
			}
			if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("code") != "code" || request.Form.Get("redirect_uri") != "https://connect.example/callback" || request.Form.Get("code_verifier") != "verifier" {
				t.Errorf("exchange form = %#v", request.Form)
			}
			// Microsoft commonly reports only resource scopes here even when the
			// authorization request also included OIDC and offline_access scopes.
			_, _ = writer.Write([]byte(`{"access_token":"token","refresh_token":"refresh","scope":"User.Read Mail.Send","token_type":"Bearer","expires_in":3600}`))
		case "/graph/me":
			if request.Header.Get("authorization") != "Bearer token" || request.URL.Query().Get("$select") != "id,displayName,userPrincipalName,mail" {
				t.Errorf("identity request = %#v", request)
			}
			_, _ = writer.Write([]byte(`{"id":"user-42","displayName":"Member","userPrincipalName":"member@example.com","mail":"member@example.com"}`))
		case "/graph/organization":
			if request.Header.Get("authorization") != "Bearer token" || request.URL.Query().Get("$select") != "id,displayName" {
				t.Errorf("tenant request = %#v", request)
			}
			_, _ = writer.Write([]byte(`{"value":[{"id":"tenant-42","displayName":"Example Corp"}]}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	provider := OAuth{Client: server.Client(), AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token", GraphURL: server.URL + "/graph", Now: func() time.Time { return now }}
	authorization, err := provider.AuthorizationURL(oauthflow.AuthorizationRequest{
		ClientID: "client", RedirectURI: "https://connect.example/callback", Scopes: []string{"Mail.Send", "User.Read"}, State: "state", PKCEChallenge: "challenge",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorization)
	if parsed.Query().Get("response_type") != "code" || parsed.Query().Get("state") != "state" || parsed.Query().Get("code_challenge") != "challenge" || parsed.Query().Get("code_challenge_method") != "S256" || !slices.Equal(strings.Fields(parsed.Query().Get("scope")), []string{"Mail.Send", "User.Read", "offline_access"}) {
		t.Fatalf("authorization URL = %s", authorization)
	}

	grant, err := provider.Exchange(context.Background(), oauthflow.ExchangeRequest{
		ClientID: "client", ClientSecret: "secret", Code: "code", RedirectURI: "https://connect.example/callback", PKCEVerifier: "verifier", Scopes: []string{"User.Read", "Mail.Send", "openid", "profile", "email", "offline_access"},
	})
	if err != nil || grant.AccessToken != "token" || grant.RefreshToken != "refresh" || !slices.Equal(grant.Scopes, []string{"Mail.Send", "User.Read", "email", "offline_access", "openid", "profile"}) || !grant.Expiry.Equal(now.Add(time.Hour)) {
		t.Fatalf("grant = %+v/%v", grant, err)
	}
	identity, err := provider.ResolveIdentity(context.Background(), grant)
	if err != nil || identity.SubjectID != "user-42" || identity.AccountID != "member@example.com" || identity.AccountName != "Member" || identity.TenantID != "tenant-42" || identity.TenantName != "Example Corp" {
		t.Fatalf("identity = %+v/%v", identity, err)
	}
	refreshed, err := provider.Refresh(context.Background(), oauthflow.RefreshRequest{ClientID: "client", ClientSecret: "secret", RefreshToken: "refresh", Scopes: []string{"User.Read", "Mail.Send"}})
	if err != nil || refreshed.AccessToken != "new-token" || refreshed.RefreshToken != "rotated-refresh" || !slices.Equal(refreshed.Scopes, []string{"Mail.Send", "User.Read"}) || !refreshed.Expiry.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("refresh = %+v/%v", refreshed, err)
	}
}

func TestMicrosoftScopesUseTokenResponseAndPreserveProtocolScopes(t *testing.T) {
	t.Parallel()
	if scopes := normalizedScopes("User.Read Mail.Send", []string{"User.Read", "Calendars.Read", "openid", "profile", "email", "offline_access"}); !slices.Equal(scopes, []string{"Mail.Send", "User.Read", "email", "offline_access", "openid", "profile"}) {
		t.Fatalf("granted scopes = %+v", scopes)
	}
	if scopes := normalizedScopes("", []string{"User.Read", "Mail.Send", "User.Read"}); !slices.Equal(scopes, []string{"Mail.Send", "User.Read"}) {
		t.Fatalf("fallback scopes = %+v", scopes)
	}
}

func TestMicrosoftExchangeRequiresRefreshTokenForDurableGrant(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"access_token":"access-token-value","scope":"User.Read","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()
	_, err := (OAuth{Client: server.Client(), TokenURL: server.URL}).Exchange(context.Background(), oauthflow.ExchangeRequest{
		ClientID: "client", ClientSecret: "client-secret-value", Code: "authorization-code-value", RedirectURI: "https://connect.example/callback", Scopes: []string{"User.Read"},
	})
	if err == nil || strings.Contains(err.Error(), "access-token-value") || strings.Contains(err.Error(), "client-secret-value") || strings.Contains(err.Error(), "authorization-code-value") {
		t.Fatalf("unsafe missing-refresh error = %v", err)
	}
}

func TestMicrosoftDefaultHTTPClientHasBoundedTimeout(t *testing.T) {
	t.Parallel()
	if client := (OAuth{}).client(); client == http.DefaultClient || client.Timeout != defaultClientTimeout || client.Timeout <= 0 {
		t.Fatalf("default client = %#v", client)
	}
}

func TestMicrosoftAuthorizationAddsOfflineAccessOnce(t *testing.T) {
	t.Parallel()
	authorization, err := (OAuth{AuthURL: "https://example.test/authorize"}).AuthorizationURL(oauthflow.AuthorizationRequest{Scopes: []string{"offline_access", "User.Read", "offline_access"}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorization)
	if !slices.Equal(strings.Fields(parsed.Query().Get("scope")), []string{"User.Read", "offline_access"}) {
		t.Fatalf("authorization URL = %s", authorization)
	}
}

func TestMicrosoftPermanentRefreshFailureIsSafeAndClassified(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"invalid_grant","error_description":"refresh-token-value must never escape"}`))
	}))
	defer server.Close()
	_, err := (OAuth{Client: server.Client(), TokenURL: server.URL}).Refresh(context.Background(), oauthflow.RefreshRequest{ClientID: "client", ClientSecret: "secret", RefreshToken: "refresh-token-value"})
	var refreshError oauthflow.RefreshError
	if !errors.As(err, &refreshError) || !refreshError.Permanent || refreshError.Code != "invalid_grant" || strings.Contains(err.Error(), "refresh-token-value") {
		t.Fatalf("error = %#v", err)
	}
}

func TestMicrosoftRejectsMalformedAndOversizedTokenResponsesWithoutLeakingCredentials(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		body func(http.ResponseWriter)
	}{
		{name: "malformed", body: func(writer http.ResponseWriter) { _, _ = writer.Write([]byte(`not-json`)) }},
		{name: "oversized", body: func(writer http.ResponseWriter) { _, _ = writer.Write([]byte(strings.Repeat("x", maxResponseBytes+1))) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { test.body(writer) }))
			defer server.Close()
			_, err := (OAuth{Client: server.Client(), TokenURL: server.URL}).Exchange(context.Background(), oauthflow.ExchangeRequest{
				ClientID: "client", ClientSecret: "client-secret-value", Code: "authorization-code-value", RedirectURI: "https://connect.example/callback",
			})
			if err == nil || strings.Contains(err.Error(), "client-secret-value") || strings.Contains(err.Error(), "authorization-code-value") {
				t.Fatalf("unsafe token error = %v", err)
			}
		})
	}
}

func TestMicrosoftIdentityRejectsIncompleteTenantWithoutLeakingToken(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/me":
			_, _ = writer.Write([]byte(`{"id":"user-42","userPrincipalName":"member@example.com"}`))
		case "/organization":
			writer.WriteHeader(http.StatusForbidden)
			_, _ = writer.Write([]byte(`{"error":{"message":"access-token-value"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	_, err := (OAuth{Client: server.Client(), GraphURL: server.URL}).ResolveIdentity(context.Background(), oauthflow.Grant{AccessToken: "access-token-value"})
	if err == nil || strings.Contains(err.Error(), "access-token-value") {
		t.Fatalf("error leaked sensitive data: %v", err)
	}
}

func TestMicrosoftRejectsMalformedAndOversizedGraphResponsesWithoutLeakingToken(t *testing.T) {
	t.Parallel()
	validUser := `{"id":"user-42","userPrincipalName":"member@example.com"}`
	for _, test := range []struct {
		name       string
		userBody   string
		tenantBody string
	}{
		{name: "malformed user", userBody: `not-json`},
		{name: "oversized user", userBody: strings.Repeat("x", maxResponseBytes+1)},
		{name: "malformed tenant", userBody: validUser, tenantBody: `not-json`},
		{name: "oversized tenant", userBody: validUser, tenantBody: strings.Repeat("x", maxResponseBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/me":
					_, _ = writer.Write([]byte(test.userBody))
				case "/organization":
					_, _ = writer.Write([]byte(test.tenantBody))
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()
			_, err := (OAuth{Client: server.Client(), GraphURL: server.URL}).ResolveIdentity(context.Background(), oauthflow.Grant{AccessToken: "access-token-value"})
			if err == nil || strings.Contains(err.Error(), "access-token-value") {
				t.Fatalf("unsafe Graph error = %v", err)
			}
		})
	}
}
