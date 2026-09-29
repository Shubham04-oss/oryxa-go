package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

type fakeCLIIdentity struct {
	claims authn.IdentityClaims
}

func (fake fakeCLIIdentity) VerifyCLIIdentityToken(_ context.Context, token, nonce string) (authn.IdentityClaims, error) {
	if token != "identity" || nonce != "nonce" {
		return authn.IdentityClaims{}, domain.ErrUnauthorized
	}
	return fake.claims, nil
}

func TestCLISessionRequiresMatchingNativeTokens(t *testing.T) {
	called := false
	config := Config{
		CLIClientID: "cli-client", CLICallbackURL: "http://127.0.0.1:8765/callback",
		CognitoDomain: "https://cognito.example.com",
		CLIVerifier: fakeCLIIdentity{claims: authn.IdentityClaims{
			Subject: "user-1", Email: "person@example.com", Name: "Person",
		}},
		CLICompleteLogin: func(_ context.Context, identity application.FederatedIdentity) (domain.User, bool, error) {
			called = true
			if identity.Subject != "user-1" || identity.Email != "person@example.com" {
				t.Fatalf("registered identity = %+v", identity)
			}
			return domain.User{ID: "user-1"}, true, nil
		},
	}
	for _, test := range []struct {
		name     string
		claims   authn.Claims
		wantCode int
	}{
		{"web token", authn.Claims{Subject: "user-1", ClientID: "web-client"}, http.StatusUnauthorized},
		{"other subject", authn.Claims{Subject: "user-2", ClientID: "cli-client"}, http.StatusUnauthorized},
		{"matching tokens", authn.Claims{Subject: "user-1", ClientID: "cli-client"}, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			called = false
			server := New(fakeVerifier{claims: test.claims}, &fakeAuthFlow{}, &fakePasswordAuth{}, &fakeAuthService{}, config)
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/cli/session", strings.NewReader(`{"id_token":"identity","nonce":"nonce"}`))
			request.Header.Set("Authorization", "Bearer access")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.wantCode || called != (test.wantCode == http.StatusOK) {
				t.Fatalf("status %d, registered %t", response.Code, called)
			}
		})
	}
}

func TestCLISessionIsOffWithoutClient(t *testing.T) {
	server := New(fakeVerifier{}, &fakeAuthFlow{}, &fakePasswordAuth{}, &fakeAuthService{}, Config{})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/auth/cli/session", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestAuthConfigPublishesOnlyNativeClientMetadata(t *testing.T) {
	server := New(fakeVerifier{}, &fakeAuthFlow{}, &fakePasswordAuth{}, &fakeAuthService{}, Config{
		CognitoDomain: "https://cognito.example.com", CLIClientID: "cli-client",
		CLICallbackURL: "http://127.0.0.1:8765/callback",
		CLISignUpURL:   "https://app.example.com/auth/sign-up",
	})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/auth/config", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"cli_client_id":"cli-client"`) || !strings.Contains(response.Body.String(), `"cli_callback_url":"http://127.0.0.1:8765/callback"`) || !strings.Contains(response.Body.String(), `"cli_signup_url":"https://app.example.com/auth/sign-up"`) {
		t.Fatalf("config response = %d %s", response.Code, response.Body)
	}
}
