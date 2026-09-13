package google

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
)

func TestGoogleOAuthPKCEOfflineExchangeIdentityAndRefresh(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if request.ParseForm() != nil || request.Form.Get("client_secret") != "secret" {
				t.Error("invalid token request")
			}
			if request.Form.Get("grant_type") == "refresh_token" {
				_, _ = writer.Write([]byte(`{"access_token":"new-token","scope":"openid email","token_type":"Bearer","expires_in":3600}`))
				return
			}
			if request.Form.Get("code_verifier") != "verifier" {
				t.Error("PKCE verifier missing")
			}
			_, _ = writer.Write([]byte(`{"access_token":"token","refresh_token":"refresh","scope":"openid email","token_type":"Bearer","expires_in":3600}`))
		case "/userinfo":
			if request.Header.Get("authorization") != "Bearer token" {
				t.Error("missing bearer token")
			}
			_, _ = writer.Write([]byte(`{"sub":"google-42","email":"member@example.com","email_verified":true,"name":"Member","hd":"example.com"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	provider := OAuth{Client: server.Client(), AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token", UserInfoURL: server.URL + "/userinfo", Now: func() time.Time { return now }}
	authorization, err := provider.AuthorizationURL(oauthflow.AuthorizationRequest{ClientID: "client", RedirectURI: "https://connect.example/callback", Scopes: []string{"openid", "email"}, State: "state", PKCEChallenge: "challenge"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorization)
	if parsed.Query().Get("access_type") != "offline" || parsed.Query().Get("code_challenge") != "challenge" || parsed.Query().Get("prompt") != "consent" {
		t.Fatalf("authorization URL = %s", authorization)
	}
	grant, err := provider.Exchange(context.Background(), oauthflow.ExchangeRequest{ClientID: "client", ClientSecret: "secret", Code: "code", RedirectURI: "https://connect.example/callback", PKCEVerifier: "verifier", Scopes: []string{"openid", "email"}})
	if err != nil || grant.AccessToken != "token" || grant.RefreshToken != "refresh" || !slices.Contains(grant.Scopes, "email") || !grant.Expiry.Equal(now.Add(time.Hour)) {
		t.Fatalf("grant = %+v/%v", grant, err)
	}
	identity, err := provider.ResolveIdentity(context.Background(), grant)
	if err != nil || identity.SubjectID != "google-42" || identity.AccountID != "member@example.com" || identity.TenantID != "example.com" {
		t.Fatalf("identity = %+v/%v", identity, err)
	}
	refreshed, err := provider.Refresh(context.Background(), oauthflow.RefreshRequest{ClientID: "client", ClientSecret: "secret", RefreshToken: "refresh"})
	if err != nil || refreshed.AccessToken != "new-token" || !refreshed.Expiry.Equal(now.Add(time.Hour)) {
		t.Fatalf("refresh = %+v/%v", refreshed, err)
	}
}

func TestGoogleScopesFallBackToReviewedRequestAndNormalizeOIDCAliases(t *testing.T) {
	t.Parallel()
	if scopes := normalizedScopes("", []string{"openid", "email"}); !slices.Equal(scopes, []string{"email", "openid"}) {
		t.Fatalf("fallback scopes = %+v", scopes)
	}
	aliases := normalizedScopes("openid https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile", nil)
	if !slices.Equal(aliases, []string{"email", "openid", "profile"}) {
		t.Fatalf("normalized scopes = %+v", aliases)
	}
}

func TestGooglePermanentRefreshFailureIsClassified(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"invalid_grant","error_description":"do not leak"}`))
	}))
	defer server.Close()
	_, err := (OAuth{Client: server.Client(), TokenURL: server.URL}).Refresh(context.Background(), oauthflow.RefreshRequest{ClientID: "client", ClientSecret: "secret", RefreshToken: "bad"})
	var refreshError oauthflow.RefreshError
	if !errors.As(err, &refreshError) || !refreshError.Permanent || refreshError.Code != "invalid_grant" {
		t.Fatalf("error = %#v", err)
	}
}
