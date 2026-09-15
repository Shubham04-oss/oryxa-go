package providers

import (
	"context"
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

	// HubSpot's refresh token and Intercom's installation may be shared by
	// other connections to the same account, so neither is revoked.
	for _, provider := range []Provider{HubSpot(), Intercom()} {
		flow := pointAt(provider, server)
		provider.OAuth2 = &flow
		grant := Grant{AccessToken: "tok-1", RefreshToken: "refresh-1"}
		if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{ClientID: "c", ClientSecret: "s", Grant: grant}); err != nil || revoked || called {
			t.Fatalf("%s Revoke() = %v %v, called = %v, want (false, nil) and no request", provider.Name, revoked, err, called)
		}
	}
}

func TestSalesforceRevoke(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/revoke" || request.ParseForm() != nil {
			t.Errorf("revoke request = %s", request.URL.Path)
		}
		got = request.PostForm
		switch got.Get("token") {
		case "already-gone":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":"invalid_token","error_description":"Token was invalid"}`)
		case "unsupported":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":"unsupported_token_type","error_description":"Token type not supported"}`)
		}
	}))
	defer server.Close()

	provider := Salesforce().WithDomain("acme.my.salesforce.com")
	flow := *provider.OAuth2
	flow.RevokeURL = server.URL + "/revoke"
	provider.OAuth2 = &flow
	revoke := func(grant Grant) (bool, error) {
		return testClient(server).Revoke(context.Background(), provider, RevokeRequest{ClientID: "client", ClientSecret: "secret-value", Grant: grant})
	}
	// Revoking the refresh token revokes its access tokens too, and needs no client.
	if revoked, err := revoke(Grant{AccessToken: "access-1", RefreshToken: "refresh-1"}); err != nil || !revoked ||
		!maps.EqualFunc(got, url.Values{"token": {"refresh-1"}}, slices.Equal) {
		t.Fatalf("Revoke() = %v %v, form %v", revoked, err, got)
	}
	if revoked, err := revoke(Grant{AccessToken: "access-only"}); err != nil || !revoked || got.Get("token") != "access-only" {
		t.Fatalf("access token Revoke() = %v %v, form %v", revoked, err, got)
	}
	if revoked, err := revoke(Grant{RefreshToken: "already-gone"}); err != nil || !revoked {
		t.Fatalf("already gone Revoke() = %v %v", revoked, err)
	}
	if revoked, err := revoke(Grant{RefreshToken: "unsupported"}); revoked || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("unsupported_token_type Revoke() = %v %v", revoked, err)
	}
}

func TestZendeskRevoke(t *testing.T) {
	var deleted, refreshed []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "DELETE /api/oauth/tokens/current":
			token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
			deleted = append(deleted, token)
			switch token {
			case "live", "renewed":
				writer.WriteHeader(http.StatusNoContent)
			case "failing":
				writer.WriteHeader(http.StatusBadGateway)
			default:
				writer.WriteHeader(http.StatusUnauthorized)
			}
		case "POST /token":
			body := map[string]string{}
			_ = json.NewDecoder(request.Body).Decode(&body)
			refreshed = append(refreshed, body["refresh_token"])
			if body["refresh_token"] != "refresh-1" || body["client_secret"] != "secret-value" {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":"invalid_grant","error_description":"The provided access grant is invalid, expired, or revoked"}`)
				return
			}
			_, _ = io.WriteString(writer, `{"access_token":"renewed","refresh_token":"refresh-2","expires_in":1800,"token_type":"bearer"}`)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	provider := Zendesk()
	flow := pointAt(provider, server)
	provider.OAuth2 = &flow
	for name, test := range map[string]struct {
		grant              Grant
		revoked            bool
		deleted, refreshed []string
		unavailable        bool
	}{
		"a live access token":                    {Grant{AccessToken: "live", RefreshToken: "refresh-1"}, true, []string{"live"}, nil, false},
		"an expired access token":                {Grant{AccessToken: "expired", RefreshToken: "refresh-1"}, true, []string{"expired", "renewed"}, []string{"refresh-1"}, false},
		"a refresh token alone":                  {Grant{RefreshToken: "refresh-1"}, true, []string{"renewed"}, []string{"refresh-1"}, false},
		"a refresh token Zendesk refuses":        {Grant{AccessToken: "expired", RefreshToken: "refresh-gone"}, true, []string{"expired"}, []string{"refresh-gone"}, false},
		"a refused token that nothing can renew": {Grant{AccessToken: "revoked"}, true, []string{"revoked"}, nil, false},
		"a failure deleting the token":           {Grant{AccessToken: "failing", RefreshToken: "refresh-1"}, false, []string{"failing"}, nil, true},
	} {
		deleted, refreshed = nil, nil
		revoked, err := testClient(server).Revoke(context.Background(), provider, RevokeRequest{ClientID: "client", ClientSecret: "secret-value", Grant: test.grant})
		if revoked != test.revoked || errors.Is(err, domain.ErrProviderUnavailable) != test.unavailable || (err != nil && !test.unavailable) ||
			!slices.Equal(deleted, test.deleted) || !slices.Equal(refreshed, test.refreshed) {
			t.Errorf("%s: Revoke() = %v %v, deleted %v, refreshed %v", name, revoked, err, deleted, refreshed)
		}
	}
}

func TestAsanaRevoke(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/revoke" || request.ParseForm() != nil {
			t.Errorf("revoke request = %s", request.URL.Path)
		}
		got = request.PostForm
		switch got.Get("token") {
		case "already-gone":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":"invalid_grant"}`)
		case "wrong-client":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":"invalid_client"}`)
		case "failure":
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, "secret-value")
		default:
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client, flow := testClient(server), pointAt(Asana(), server)
	flow.RevokeURL = server.URL + "/revoke" // Asana's revoke endpoint is not under APIURL.
	provider := Asana()
	provider.OAuth2 = &flow
	revoke := func(refreshToken string) (bool, error) {
		return client.Revoke(context.Background(), provider, RevokeRequest{
			ClientID: "client", ClientSecret: "secret-value", Grant: Grant{AccessToken: "access-1", RefreshToken: refreshToken},
		})
	}

	// Asana revokes by refresh token, with the app's client.
	if revoked, err := revoke("refresh-1"); err != nil || !revoked || got.Get("token") != "refresh-1" ||
		got.Get("client_id") != "client" || got.Get("client_secret") != "secret-value" {
		t.Fatalf("Revoke() = %v %v, form = %v", revoked, err, got)
	}
	if revoked, err := revoke("already-gone"); err != nil || !revoked {
		t.Fatalf("already gone Revoke() = %v %v", revoked, err)
	}
	if revoked, err := revoke("wrong-client"); revoked || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("invalid_client Revoke() = %v %v", revoked, err)
	}
	if revoked, err := revoke("failure"); revoked || !errors.Is(err, domain.ErrProviderUnavailable) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("failure Revoke() = %v %v", revoked, err)
	}
	if revoked, err := client.Revoke(context.Background(), provider, RevokeRequest{Grant: Grant{AccessToken: "access-only"}}); revoked || err != nil {
		t.Fatalf("no refresh token Revoke() = %v %v, want (false, nil)", revoked, err)
	}
}

func TestNotionRevoke(t *testing.T) {
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		user, password, basic := request.BasicAuth()
		got = map[string]string{}
		if request.URL.Path != "/revoke" || !basic || user != "client" || password != "secret-value" ||
			request.Header.Get("Notion-Version") != notionVersion || json.NewDecoder(request.Body).Decode(&got) != nil {
			t.Errorf("revoke request = %s %v", request.URL.Path, request.Header)
		}
		switch got["token"] {
		case "already-gone":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"object":"error","status":400,"code":"invalid_grant","message":"Token is invalid."}`)
		case "failure":
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, "secret-value")
		default:
			_, _ = io.WriteString(writer, `{"request_id":"r1"}`)
		}
	}))
	defer server.Close()

	client, flow := testClient(server), pointAt(Notion(), server)
	flow.RevokeURL = server.URL + "/revoke"
	provider := Notion()
	provider.OAuth2 = &flow
	revoke := func(accessToken string) (bool, error) {
		return client.Revoke(context.Background(), provider, RevokeRequest{ClientID: "client", ClientSecret: "secret-value", Grant: Grant{AccessToken: accessToken}})
	}
	if revoked, err := revoke("access-1"); err != nil || !revoked || got["token"] != "access-1" {
		t.Fatalf("Revoke() = %v %v, body %v", revoked, err, got)
	}
	if revoked, err := revoke("already-gone"); err != nil || !revoked {
		t.Fatalf("already gone Revoke() = %v %v", revoked, err)
	}
	if revoked, err := revoke("failure"); revoked || !errors.Is(err, domain.ErrProviderUnavailable) || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("failure Revoke() = %v %v", revoked, err)
	}
}

func TestBoxRevoke(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/revoke" || request.ParseForm() != nil {
			t.Errorf("revoke request = %s", request.URL.Path)
		}
		got = request.PostForm
		switch got.Get("token") {
		case "already-gone":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":"invalid_token","error_description":"The token is invalid"}`)
		case "wrong-client":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":"invalid_client"}`)
		}
	}))
	defer server.Close()

	client, flow := testClient(server), pointAt(Box(), server)
	flow.RevokeURL = server.URL + "/revoke"
	provider := Box()
	provider.OAuth2 = &flow
	revoke := func(grant Grant) (bool, error) {
		return client.Revoke(context.Background(), provider, RevokeRequest{ClientID: "client", ClientSecret: "secret-value", Grant: grant})
	}
	// The refresh token outlives the access token, so it is the one revoked.
	if revoked, err := revoke(Grant{AccessToken: "access-1", RefreshToken: "refresh-1"}); err != nil || !revoked ||
		got.Get("token") != "refresh-1" || got.Get("client_id") != "client" || got.Get("client_secret") != "secret-value" {
		t.Fatalf("Revoke() = %v %v, form %v", revoked, err, got)
	}
	if revoked, err := revoke(Grant{AccessToken: "access-only"}); err != nil || !revoked || got.Get("token") != "access-only" {
		t.Fatalf("access token Revoke() = %v %v, form %v", revoked, err, got)
	}
	if revoked, err := revoke(Grant{RefreshToken: "already-gone"}); err != nil || !revoked {
		t.Fatalf("already gone Revoke() = %v %v", revoked, err)
	}
	if revoked, err := revoke(Grant{RefreshToken: "wrong-client"}); revoked || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("invalid_client Revoke() = %v %v", revoked, err)
	}
}

func TestDropboxRevoke(t *testing.T) {
	var revokedWith []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			_ = request.ParseForm()
			if request.PostForm.Get("refresh_token") == "revoked" {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":"invalid_grant","error_description":"refresh token is invalid or revoked"}`)
				return
			}
			_, _ = io.WriteString(writer, `{"access_token":"fresh","expires_in":14400,"token_type":"bearer"}`)
		case "/api/auth/token/revoke":
			revokedWith = append(revokedWith, request.Header.Get("Authorization"))
			switch request.Header.Get("Authorization") {
			case "Bearer fresh":
			case "Bearer failing":
				writer.WriteHeader(http.StatusInternalServerError)
			default:
				writer.WriteHeader(http.StatusUnauthorized)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, flow := testClient(server), pointAt(Dropbox(), server)
	provider := Dropbox()
	provider.OAuth2 = &flow
	revoke := func(grant Grant) (bool, error) {
		revokedWith = nil
		return client.Revoke(context.Background(), provider, RevokeRequest{ClientID: "client", ClientSecret: "secret-value", Grant: grant})
	}
	// A stored access token may have expired, so the grant is refreshed and the
	// fresh token revokes it.
	if revoked, err := revoke(Grant{AccessToken: "stale", RefreshToken: "refresh"}); err != nil || !revoked || !slices.Equal(revokedWith, []string{"Bearer fresh"}) {
		t.Fatalf("Revoke() = %v %v, revoked with %v", revoked, err, revokedWith)
	}
	if revoked, err := revoke(Grant{AccessToken: "stale", RefreshToken: "revoked"}); err != nil || !revoked || len(revokedWith) != 0 {
		t.Fatalf("Revoke() of a refused grant = %v %v, revoked with %v", revoked, err, revokedWith)
	}
	if revoked, err := revoke(Grant{AccessToken: "legacy"}); err != nil || !revoked || !slices.Equal(revokedWith, []string{"Bearer legacy"}) {
		t.Fatalf("Revoke() of a token that no longer works = %v %v, revoked with %v", revoked, err, revokedWith)
	}
	if revoked, err := revoke(Grant{AccessToken: "failing"}); revoked || !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("failing Revoke() = %v %v", revoked, err)
	}
}
