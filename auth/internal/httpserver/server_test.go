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
	organization  domain.Organization
	membership    domain.Membership
	members       []domain.Membership
	received      []domain.ReceivedInvitation
	err           error
	signedOutUser string
	accepted      string
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

func (fake *fakeAuthService) ReceivedInvitations(_ context.Context, _ string) ([]domain.ReceivedInvitation, error) {
	return fake.received, fake.err
}

func (fake *fakeAuthService) AcceptReceivedInvitation(_ context.Context, actorID, invitationID string) (domain.Membership, error) {
	fake.accepted = actorID + " accepted " + invitationID
	return fake.membership, fake.err
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

func TestAgentLogoutDoesNotSignTheMemberOut(t *testing.T) {
	service := &fakeAuthService{}
	handler := New(
		fakeVerifier{claims: authn.Claims{Subject: "user-1", ClientID: "agent-client-id"}},
		&fakeAuthFlow{},
		&fakePasswordAuth{},
		service,
		Config{AllowedOrigins: []string{"https://app.example.com"}, AgentClientID: "agent-client-id"},
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer agent-token")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || service.signedOutUser != "" {
		t.Fatalf("logout with an agent token status=%d signed-out-user=%q, want no sign-out", response.Code, service.signedOutUser)
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

func TestReceivedInvitationsNameTheirOrganizations(t *testing.T) {
	service := &fakeAuthService{received: []domain.ReceivedInvitation{
		{ID: "inv-1", OrganizationID: "org-1", OrganizationName: "Acme", Role: domain.RoleMember},
	}}
	handler := testHandler(fakeVerifier{claims: authn.Claims{Subject: "user-1"}}, &fakeAuthFlow{}, service)
	request := httptest.NewRequest(http.MethodGet, "/v1/invitations", nil)
	request.AddCookie(&http.Cookie{Name: accessCookieName, Value: "token"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"invitations":[{"id":"inv-1"`) || !strings.Contains(body, `"organization_name":"Acme"`) {
		t.Fatalf("status = %d, body = %s; want the invitation to Acme", response.Code, body)
	}
}

func TestAcceptReceivedInvitation(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "joins the organization", status: http.StatusOK},
		{name: "someone else's invitation is not found", err: domain.ErrNotFound, status: http.StatusNotFound},
		{name: "a full organization needs payment", err: fmt.Errorf("accept: %w", domain.ErrLimitReached), status: http.StatusPaymentRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeAuthService{
				membership: domain.Membership{OrganizationID: "org-1", UserID: "user-1", Role: domain.RoleMember},
				err:        test.err,
			}
			handler := testHandler(fakeVerifier{claims: authn.Claims{Subject: "user-1"}}, &fakeAuthFlow{}, service)
			request := httptest.NewRequest(http.MethodPost, "/v1/invitations/inv-1/accept", nil)
			request.AddCookie(&http.Cookie{Name: accessCookieName, Value: "token"})
			request.Header.Set("Origin", "https://app.example.com")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.status || service.accepted != "user-1 accepted inv-1" {
				t.Fatalf("status = %d, accepted %q, body = %s; want %d for user-1 and inv-1", response.Code, service.accepted, response.Body, test.status)
			}
			if test.err == nil && !strings.Contains(response.Body.String(), `"organization_id":"org-1"`) {
				t.Fatalf("body = %s, want the new membership", response.Body)
			}
		})
	}
}
