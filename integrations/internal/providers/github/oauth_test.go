package github

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

func TestGitHubOAuthExchangeIdentityAndRefresh(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			if request.Header.Get("accept") != "application/json" || request.ParseForm() != nil || request.Form.Get("client_secret") != "secret" {
				t.Error("invalid token request")
			}
			writer.Header().Set("content-type", "application/json")
			if request.Form.Get("grant_type") == "refresh_token" {
				_, _ = writer.Write([]byte(`{"access_token":"new-token","refresh_token":"new-refresh","scope":"repo,read:user","token_type":"bearer","expires_in":28800}`))
				return
			}
			_, _ = writer.Write([]byte(`{"access_token":"token","refresh_token":"refresh","scope":"repo,read:user","token_type":"bearer","expires_in":28800}`))
		case "/user":
			if request.Header.Get("authorization") != "Bearer token" {
				t.Error("missing bearer token")
			}
			_, _ = writer.Write([]byte(`{"id":42,"login":"octocat","name":"Mona"}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	provider := OAuth{Client: server.Client(), AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token", APIURL: server.URL, Now: func() time.Time { return now }}

	authorization, err := provider.AuthorizationURL(oauthflow.AuthorizationRequest{ClientID: "client", RedirectURI: "https://connect.example/callback", Scopes: []string{"repo", "read:user"}, State: "state"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authorization)
	if parsed.Query().Get("state") != "state" || parsed.Query().Get("scope") != "repo read:user" {
		t.Fatalf("authorization URL = %s", authorization)
	}
	grant, err := provider.Exchange(context.Background(), oauthflow.ExchangeRequest{ClientID: "client", ClientSecret: "secret", Code: "code", RedirectURI: "https://connect.example/callback"})
	if err != nil || grant.AccessToken != "token" || grant.RefreshToken != "refresh" || !slices.Contains(grant.Scopes, "repo") || !grant.Expiry.Equal(now.Add(8*time.Hour)) {
		t.Fatalf("grant = %+v/%v", grant, err)
	}
	identity, err := provider.ResolveIdentity(context.Background(), grant)
	if err != nil || identity.SubjectID != "42" || identity.AccountID != "octocat" || identity.AccountName != "Mona" {
		t.Fatalf("identity = %+v/%v", identity, err)
	}
	refreshed, err := provider.Refresh(context.Background(), oauthflow.RefreshRequest{ClientID: "client", ClientSecret: "secret", RefreshToken: "refresh"})
	if err != nil || refreshed.AccessToken != "new-token" || refreshed.RefreshToken != "new-refresh" {
		t.Fatalf("refresh = %+v/%v", refreshed, err)
	}
}

func TestGitHubRefreshClassifiesOnlyStableProviderCode(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"bad_refresh_token","error_description":"sensitive description"}`))
	}))
	defer server.Close()
	provider := OAuth{Client: server.Client(), TokenURL: server.URL}
	_, err := provider.Refresh(context.Background(), oauthflow.RefreshRequest{ClientID: "client", ClientSecret: "secret", RefreshToken: "bad"})
	var refreshError oauthflow.RefreshError
	if !errors.As(err, &refreshError) || !refreshError.Permanent || refreshError.Code != "bad_refresh_token" || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("error = %#v", err)
	}
}
