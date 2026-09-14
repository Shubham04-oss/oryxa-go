package providers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func TestGitHubRevoke(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotMethod, gotPath = request.Method, request.URL.Path
		user, pass, _ := request.BasicAuth()
		gotAuth = user + ":" + pass
		body, _ := io.ReadAll(request.Body)
		gotBody = string(body)
		switch pass {
		case "already-gone":
			writer.WriteHeader(http.StatusNotFound)
		case "unprocessable":
			writer.WriteHeader(http.StatusUnprocessableEntity)
		case "failure":
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, "secret-value")
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	client, flow := testClient(server), pointAt(GitHub(), server)
	provider := GitHub()
	provider.OAuth2 = &flow

	// Revoking a single token, not applications/{client_id}/grant: that
	// endpoint would revoke every token GitHub issued this account for the
	// app, breaking every other connection sharing the authorization.
	revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{ClientID: "client-1", ClientSecret: "secret-1", Grant: Grant{AccessToken: "tok-1"}})
	if err != nil || !revoked {
		t.Fatalf("Revoke() = %v %v", revoked, err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/applications/client-1/token" || gotAuth != "client-1:secret-1" || gotBody != `{"access_token":"tok-1"}` {
		t.Fatalf("revoke request = %s %s auth=%q body=%s", gotMethod, gotPath, gotAuth, gotBody)
	}

	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{ClientID: "client-1", ClientSecret: "already-gone", Grant: Grant{AccessToken: "tok-1"}}); err != nil || !revoked {
		t.Fatalf("404 Revoke() = %v %v", revoked, err)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{ClientID: "client-1", ClientSecret: "unprocessable", Grant: Grant{AccessToken: "tok-1"}}); err != nil || !revoked {
		t.Fatalf("422 Revoke() = %v %v", revoked, err)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{ClientID: "client-1", ClientSecret: "failure", Grant: Grant{AccessToken: "tok-1"}}); revoked || !errors.Is(err, domain.ErrProviderUnavailable) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("failure Revoke() = %v %v", revoked, err)
	}
	if _, err := client.Revoke(context.Background(), provider, RevokeRequest{ClientSecret: "secret-1", Grant: Grant{AccessToken: "tok-1"}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing client ID error = %v, want %v", err, domain.ErrInvalid)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{ClientID: "client-1", ClientSecret: "secret-1", Grant: Grant{}}); revoked || err != nil {
		t.Fatalf("empty grant Revoke() = %v %v, want (false, nil)", revoked, err)
	}
}

func TestGoogleRevoke(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		gotToken = request.Form.Get("token")
		switch gotToken {
		case "already-gone":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":"invalid_token"}`)
		case "failure":
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, "secret-value")
		default:
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client, flow := testClient(server), pointAt(Google(), server)
	flow.RevokeURL = server.URL + "/revoke" // Google's revoke host differs from APIURL.
	provider := Google()
	provider.OAuth2 = &flow

	// The refresh token is preferred: revoking it also invalidates the
	// access token issued alongside it.
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{AccessToken: "access-1", RefreshToken: "refresh-1"}}); err != nil || !revoked || gotToken != "refresh-1" {
		t.Fatalf("Revoke() = %v %v, token sent = %q, want refresh-1", revoked, err, gotToken)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{AccessToken: "access-only"}}); err != nil || !revoked || gotToken != "access-only" {
		t.Fatalf("access-token-only Revoke() = %v %v, token sent = %q", revoked, err, gotToken)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{RefreshToken: "already-gone"}}); err != nil || !revoked {
		t.Fatalf("already-gone Revoke() = %v %v", revoked, err)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{RefreshToken: "failure"}}); revoked || !errors.Is(err, domain.ErrProviderUnavailable) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("failure Revoke() = %v %v", revoked, err)
	}
}

// TestSlackRevoke proves the corrected behavior: only the member's own user
// token is ever revoked. The shared bot token (grant.AccessToken) must never
// be sent to auth.revoke, since Slack issues one bot token per workspace and
// app shared by every member who connected it -- revoking it on one member's
// disconnect would break every other connection to that workspace.
func TestSlackRevoke(t *testing.T) {
	var requests int
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		gotAuth = request.Header.Get("Authorization")
		switch gotAuth {
		case "Bearer already-gone":
			_, _ = io.WriteString(writer, `{"ok":false,"error":"token_revoked"}`)
		case "Bearer failure":
			_, _ = io.WriteString(writer, `{"ok":false,"error":"rate_limited"}`)
		case "Bearer leaky":
			_, _ = io.WriteString(writer, `{"ok":false,"error":"secret-value!!"}`)
		default:
			_, _ = io.WriteString(writer, `{"ok":true,"revoked":true}`)
		}
	}))
	defer server.Close()

	client, flow := testClient(server), pointAt(Slack(), server)
	provider := Slack()
	provider.OAuth2 = &flow

	requests = 0
	revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{AccessToken: "xoxb-bot"}})
	if err != nil || revoked || requests != 0 {
		t.Fatalf("bot-token-only Revoke() = %v %v, requests = %d, want (false, nil) and no request", revoked, err, requests)
	}

	requests = 0
	revoked, err = client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{AccessToken: "xoxb-bot", Tokens: map[string]string{"user": "xoxp-user"}}})
	if err != nil || !revoked || requests != 1 || gotAuth != "Bearer xoxp-user" {
		t.Fatalf("Revoke() = %v %v, requests = %d, auth = %q, want exactly the user token used", revoked, err, requests, gotAuth)
	}

	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{Tokens: map[string]string{"user": "already-gone"}}}); err != nil || !revoked {
		t.Fatalf("already-gone Revoke() = %v %v", revoked, err)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{Tokens: map[string]string{"user": "failure"}}}); revoked || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("failure Revoke() = %v %v", revoked, err)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{Tokens: map[string]string{"user": "leaky"}}}); revoked || !errors.Is(err, domain.ErrProviderUnavailable) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("leaky Revoke() = %v %v, error must not echo the provider body", revoked, err)
	}
}

func TestLinearRevoke(t *testing.T) {
	var gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAuth, gotPath = request.Header.Get("Authorization"), request.URL.Path
		switch gotAuth {
		case "Bearer already-gone":
			writer.WriteHeader(http.StatusBadRequest)
		case "Bearer unauthorized":
			writer.WriteHeader(http.StatusUnauthorized)
		case "Bearer failure":
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, "secret-value")
		default:
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client, flow := testClient(server), pointAt(Linear(), server)
	provider := Linear()
	provider.OAuth2 = &flow

	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{AccessToken: "tok-1"}}); err != nil || !revoked || gotPath != "/api/oauth/revoke" || gotAuth != "Bearer tok-1" {
		t.Fatalf("Revoke() = %v %v, path = %q auth = %q", revoked, err, gotPath, gotAuth)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{AccessToken: "already-gone"}}); err != nil || !revoked {
		t.Fatalf("400 Revoke() = %v %v", revoked, err)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{AccessToken: "unauthorized"}}); err != nil || !revoked {
		t.Fatalf("401 Revoke() = %v %v", revoked, err)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{AccessToken: "failure"}}); revoked || !errors.Is(err, domain.ErrProviderUnavailable) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("failure Revoke() = %v %v", revoked, err)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{}}); revoked || err != nil {
		t.Fatalf("empty grant Revoke() = %v %v, want (false, nil)", revoked, err)
	}
}

// TestRevokeWithNoHookOrNoTokens covers the two cases that never reach a
// provider hook at all: a provider with none configured (Microsoft), and a
// grant with nothing to revoke.
func TestRevokeWithNoHookOrNoTokens(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()

	microsoftFlow := pointAt(Microsoft(), server)
	microsoftProvider := Microsoft()
	microsoftProvider.OAuth2 = &microsoftFlow
	client := testClient(server)

	if revoked, err := client.Revoke(context.Background(), microsoftProvider, RevokeRequest{Grant: Grant{AccessToken: "tok-1"}}); err != nil || revoked || called {
		t.Fatalf("Microsoft (no hook) Revoke() = %v %v, called = %v, want (false, nil) and no request", revoked, err, called)
	}

	githubFlow := pointAt(GitHub(), server)
	githubProvider := GitHub()
	githubProvider.OAuth2 = &githubFlow
	if revoked, err := client.Revoke(context.Background(), githubProvider, RevokeRequest{ClientID: "c", ClientSecret: "s", Grant: Grant{}}); err != nil || revoked || called {
		t.Fatalf("empty-grant Revoke() = %v %v, called = %v, want (false, nil) and no request", revoked, err, called)
	}
}
