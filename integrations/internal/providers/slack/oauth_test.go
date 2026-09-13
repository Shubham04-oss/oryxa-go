package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
)

func TestAuthorizationURLContainsStateScopesAndOptionalPKCEButNoSecret(t *testing.T) {
	t.Parallel()
	provider := OAuth{AuthURL: "https://slack.example/authorize"}
	value, err := provider.AuthorizationURL(oauthflow.AuthorizationRequest{
		ClientID: "client-id", RedirectURI: "https://connect.oryxa.test/callback",
		Scopes: []string{"channels:history", "groups:history"}, State: "random-state", PKCEChallenge: "challenge",
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(value)
	query := parsed.Query()
	if query.Get("client_id") != "client-id" || query.Get("state") != "random-state" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("unexpected authorization query: %s", parsed.RawQuery)
	}
	if query.Get("scope") != "channels:history,groups:history" {
		t.Fatalf("scope = %q", query.Get("scope"))
	}
	if query.Has("client_secret") {
		t.Fatal("client secret leaked into authorization URL")
	}
}

func TestExchangeUsesFormAndMapsSlackGrantAndIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("content-type") != "application/x-www-form-urlencoded" {
			t.Fatalf("unexpected token request: %s %s", request.Method, request.Header.Get("content-type"))
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{
			"client_id": "client-id", "client_secret": "client-secret", "code": "code",
			"redirect_uri": "https://connect.oryxa.test/callback", "code_verifier": "verifier",
		} {
			if got := request.Form.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(`{
			"ok":true,"access_token":"xoxb-secret","token_type":"bot",
			"scope":"channels:history,groups:history",
			"team":{"id":"T123","name":"Oryxa"},
			"authed_user":{"id":"U123","scope":"users:read","access_token":"xoxp-secret"}
		}`))
	}))
	defer server.Close()

	provider := OAuth{Client: server.Client(), TokenURL: server.URL}
	grant, err := provider.Exchange(context.Background(), oauthflow.ExchangeRequest{
		ClientID: "client-id", ClientSecret: "client-secret", Code: "code",
		RedirectURI: "https://connect.oryxa.test/callback", PKCEVerifier: "verifier",
	})
	if err != nil {
		t.Fatal(err)
	}
	if grant.AccessToken != "xoxb-secret" || !slices.Contains(grant.Scopes, "users:read") {
		t.Fatalf("unexpected grant: %+v", grant)
	}
	identity, err := provider.ResolveIdentity(context.Background(), grant)
	if err != nil {
		t.Fatal(err)
	}
	if identity.SubjectID != "U123" || identity.TenantID != "T123" || identity.TenantName != "Oryxa" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
}

func TestExchangeRejectsSlackApplicationErrorWithoutReturningTokens(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"ok":false,"error":"invalid_code"}`))
	}))
	defer server.Close()
	provider := OAuth{Client: server.Client(), TokenURL: server.URL}
	if _, err := provider.Exchange(context.Background(), oauthflow.ExchangeRequest{ClientID: "id", ClientSecret: "secret", Code: "bad"}); err == nil {
		t.Fatal("Slack application error was accepted")
	}
}
