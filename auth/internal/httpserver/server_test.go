package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/passwordflow"
)

type fakeVerifier struct {
	claims authn.Claims
	err    error
}

func (fake fakeVerifier) Verify(_ context.Context, _ string) (authn.Claims, error) {
	return fake.claims, fake.err
}

type fakeAuthFlow struct {
	authFlow
	state  string
	target string
	err    error
}

type fakePasswordAuth struct {
	passwordAuth
	session passwordflow.Session
	err     error
}

func (fake *fakePasswordAuth) Login(context.Context, string, string) (passwordflow.Session, error) {
	return fake.session, fake.err
}

func (fake *fakeAuthFlow) Begin(_ context.Context, _ string) (string, string, error) {
	return fake.state, fake.target, fake.err
}

func (fake *fakeAuthFlow) Revoke(context.Context, string) error {
	return fake.err
}

type fakeAuthService struct {
	authService
	user          domain.User
	organizations []domain.Organization
	err           error
	signedOutUser string
}

func (fake *fakeAuthService) CurrentUser(_ context.Context, _ string) (domain.User, []domain.Organization, error) {
	return fake.user, fake.organizations, fake.err
}

func (fake *fakeAuthService) SignOut(_ context.Context, userID string) error {
	fake.signedOutUser = userID
	return fake.err
}

func (fake *fakeAuthService) InviteMember(_ context.Context, _, _ string, _ application.InviteMemberInput) (domain.Invitation, string, error) {
	return domain.Invitation{}, "", fake.err
}

func testHandler(verifier fakeVerifier, flow *fakeAuthFlow, service *fakeAuthService) http.Handler {
	return New(verifier, flow, &fakePasswordAuth{}, service, Config{
		AllowedOrigins:  []string{"https://app.example.com"},
		CookieSecure:    true,
		CognitoDomain:   "https://example.auth.ap-south-1.amazoncognito.com",
		CognitoClientID: "client-id",
	})
}

func TestMeRejectsMissingAccessToken(t *testing.T) {
	handler := testHandler(fakeVerifier{}, &fakeAuthFlow{}, &fakeAuthService{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMeReturnsPersistedUser(t *testing.T) {
	handler := testHandler(
		fakeVerifier{claims: authn.Claims{Subject: "user-1"}},
		&fakeAuthFlow{},
		&fakeAuthService{user: domain.User{ID: "user-1", Email: "person@example.com"}},
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestInvitationBeyondMemberLimitNeedsPayment(t *testing.T) {
	handler := testHandler(
		fakeVerifier{claims: authn.Claims{Subject: "user-1"}},
		&fakeAuthFlow{},
		&fakeAuthService{err: fmt.Errorf("create invitation: %w", domain.ErrLimitReached)},
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/org-1/invitations", strings.NewReader(`{"email":"new@example.com","role":"member"}`))
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusPaymentRequired || !strings.Contains(response.Body.String(), "member limit") {
		t.Fatalf("status = %d, body = %s; want 402 naming the member limit", response.Code, response.Body)
	}
}

func TestMeRejectsInvalidAccessToken(t *testing.T) {
	handler := testHandler(fakeVerifier{err: errors.New("bad token")}, &fakeAuthFlow{}, &fakeAuthService{})
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestLoginSetsStateCookieAndRedirects(t *testing.T) {
	handler := testHandler(fakeVerifier{}, &fakeAuthFlow{
		state:  "state-value",
		target: "https://identity.example.com/authorize",
	}, &fakeAuthService{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/auth/login", nil))

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	if location := response.Header().Get("Location"); location != "https://identity.example.com/authorize" {
		t.Fatalf("location = %q", location)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != stateCookieName || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("unexpected state cookie: %+v", cookies)
	}
}

func TestCookieMutationRequiresAllowedOrigin(t *testing.T) {
	handler := testHandler(fakeVerifier{claims: authn.Claims{Subject: "user-1"}}, &fakeAuthFlow{}, &fakeAuthService{})
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations", nil)
	request.AddCookie(&http.Cookie{Name: accessCookieName, Value: "token"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestBearerLogoutDoesNotRequireBrowserOrigin(t *testing.T) {
	service := &fakeAuthService{}
	handler := testHandler(
		fakeVerifier{claims: authn.Claims{Subject: "user-1"}},
		&fakeAuthFlow{},
		service,
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || service.signedOutUser != "user-1" {
		t.Fatalf("logout status=%d signed-out-user=%q", response.Code, service.signedOutUser)
	}
}

func TestPasswordLoginSetsSessionCookies(t *testing.T) {
	passwords := &fakePasswordAuth{session: passwordflow.Session{
		AccessToken: "access-token", RefreshToken: "refresh-token", ExpiresIn: 3600,
		User: domain.User{ID: "user-1", Email: "person@example.com"},
	}}
	handler := New(
		fakeVerifier{},
		&fakeAuthFlow{},
		passwords,
		&fakeAuthService{},
		Config{AllowedOrigins: []string{"https://app.example.com"}, CookieSecure: true},
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/password/login", strings.NewReader(`{"email":"person@example.com","password":"Correct-Horse-7!"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.example.com")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != accessCookieName || cookies[1].Name != refreshCookieName {
		t.Fatalf("unexpected session cookies: %+v", cookies)
	}
	if strings.Contains(response.Body.String(), "Correct-Horse") {
		t.Fatal("password leaked into response")
	}
}
