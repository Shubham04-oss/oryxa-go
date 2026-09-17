package oauthflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

type fakeLoginService struct {
	flow     domain.LoginFlow
	identity application.FederatedIdentity
}

func (fake *fakeLoginService) SaveLoginFlow(_ context.Context, flow domain.LoginFlow) error {
	fake.flow = flow
	return nil
}

func (fake *fakeLoginService) ConsumeLoginFlow(_ context.Context, _ string) (domain.LoginFlow, error) {
	return fake.flow, nil
}

func (fake *fakeLoginService) CompleteLogin(_ context.Context, identity application.FederatedIdentity) (domain.User, bool, error) {
	fake.identity = identity
	return domain.User{ID: identity.Subject, Email: identity.Email}, true, nil
}

type fakeIdentityVerifier struct {
	claims        authn.IdentityClaims
	expectedNonce string
}

func (fake *fakeIdentityVerifier) VerifyIdentityToken(_ context.Context, _ string, nonce string) (authn.IdentityClaims, error) {
	fake.expectedNonce = nonce
	return fake.claims, nil
}

func TestBeginStoresPKCEFlowAndBuildsGoogleAuthorizationURL(t *testing.T) {
	service := &fakeLoginService{}
	manager, err := New(Config{
		CognitoDomain:   "https://identity.example.com",
		ClientID:        "client-id",
		CallbackURL:     "https://auth.example.com/v1/auth/callback",
		AllowedReturnTo: []string{"https://app.example.com"},
	}, service, &fakeIdentityVerifier{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	state, target, err := manager.Begin(context.Background(), "https://app.example.com")
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	query := parsed.Query()
	if state == "" || service.flow.StateHash == "" || service.flow.CodeVerifier == "" || service.flow.Nonce == "" {
		t.Fatalf("incomplete login flow: %+v", service.flow)
	}
	if query.Get("state") != state || query.Get("code_challenge_method") != "S256" || query.Get("identity_provider") != "Google" {
		t.Fatalf("unexpected authorization query: %v", query)
	}
}

func TestCompleteExchangesCodeAndRegistersVerifiedIdentity(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if request.Form.Get("code_verifier") != "verifier" {
			t.Fatalf("code verifier = %q", request.Form.Get("code_verifier"))
		}
		_ = json.NewEncoder(writer).Encode(tokenResponse{
			AccessToken:  "access-token",
			IDToken:      "identity-token",
			RefreshToken: "refresh-token",
			ExpiresIn:    3600,
		})
	}))
	defer tokenServer.Close()

	service := &fakeLoginService{flow: domain.LoginFlow{
		CodeVerifier: "verifier",
		Nonce:        "nonce",
		ReturnTo:     "https://app.example.com",
	}}
	verifier := &fakeIdentityVerifier{claims: authn.IdentityClaims{
		Subject:       "user-1",
		Email:         "person@example.com",
		EmailVerified: true,
		Name:          "Person",
	}}
	manager, err := New(Config{
		CognitoDomain:   tokenServer.URL,
		ClientID:        "client-id",
		CallbackURL:     "https://auth.example.com/v1/auth/callback",
		AllowedReturnTo: []string{"https://app.example.com"},
	}, service, verifier)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	session, err := manager.Complete(context.Background(), "state", "state", "code")
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if session.User.ID != "user-1" || session.AccessToken != "access-token" || verifier.expectedNonce != "nonce" {
		t.Fatalf("unexpected session: %+v", session)
	}
}

func TestBeginRejectsUnlistedReturnURL(t *testing.T) {
	manager, err := New(Config{
		CognitoDomain:   "https://identity.example.com",
		ClientID:        "client-id",
		CallbackURL:     "https://auth.example.com/v1/auth/callback",
		AllowedReturnTo: []string{"https://app.example.com"},
	}, &fakeLoginService{}, &fakeIdentityVerifier{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, _, err = manager.Begin(context.Background(), "https://attacker.example.com")
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Begin() error = %v, want invalid input", err)
	}
}

func TestCompleteRejectsMismatchedStateBeforeExchange(t *testing.T) {
	manager, err := New(Config{
		CognitoDomain:   "https://identity.example.com",
		ClientID:        "client-id",
		CallbackURL:     "https://auth.example.com/v1/auth/callback",
		AllowedReturnTo: []string{"https://app.example.com"},
	}, &fakeLoginService{}, &fakeIdentityVerifier{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = manager.Complete(context.Background(), "request-state", "cookie-state", "code")
	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Complete() error = %v, want unauthorized", err)
	}
}

func TestBeginReturnsOnlyToPagesOnAllowedOrigins(t *testing.T) {
	manager, err := New(Config{
		CognitoDomain:   "https://identity.example.com",
		ClientID:        "client-id",
		CallbackURL:     "https://auth.example.com/v1/auth/callback",
		AllowedReturnTo: []string{"https://app.example.com", "https://admin.example.com/console/"},
	}, &fakeLoginService{}, &fakeIdentityVerifier{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	allowed := []string{
		"https://app.example.com",
		"https://app.example.com/",
		"https://app.example.com/app",
		"https://app.example.com/invitations/accept?token=abc",
		"HTTPS://App.Example.com/app",
		"https://admin.example.com/console",
		"https://admin.example.com/console/members",
	}
	for _, target := range allowed {
		if _, _, err := manager.Begin(context.Background(), target); err != nil {
			t.Errorf("Begin(%q) error = %v, want allowed", target, err)
		}
	}

	refused := []string{
		"https://attacker.example.com/app",
		"http://app.example.com/app",
		"https://app.example.com:8443/app",
		"https://app.example.com.attacker.example/app",
		"https://app.example.com@attacker.example/app",
		"//app.example.com/app",
		"/app",
		"javascript:alert(1)",
		"https://admin.example.com/consoles",
		"https://admin.example.com/",
		"https://admin.example.com/console/../billing",
		"https://admin.example.com/console/%2e%2e/billing",
		"https://admin.example.com/console%2F..%2Fbilling",
	}
	for _, target := range refused {
		if _, _, err := manager.Begin(context.Background(), target); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("Begin(%q) error = %v, want invalid input", target, err)
		}
	}
}

func TestNewRejectsRelativeReturnURL(t *testing.T) {
	_, err := New(Config{
		CognitoDomain:   "https://identity.example.com",
		ClientID:        "client-id",
		CallbackURL:     "https://auth.example.com/v1/auth/callback",
		AllowedReturnTo: []string{"/app"},
	}, &fakeLoginService{}, &fakeIdentityVerifier{})
	if err == nil {
		t.Fatal("New() accepted a relative return URL")
	}
}
