package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

// fakeAgentGrants is a hand-rolled AgentGrantsFlow: embedding the nil
// interface means any method a test does not stub panics if the handler
// unexpectedly calls it, the same trick the other fakes in this package use.
type fakeAgentGrants struct {
	AgentGrantsFlow

	statusGrant domain.AgentGrant
	statusErr   error

	authorizeURL   string
	authorizeState string
	authorizeErr   error
	gotMemberID    string
	gotReturnTo    string

	completeReturnTo string
	completeErr      error
	gotState         string
	gotStateCookie   string
	gotCode          string

	revokeErr   error
	revokedUser string

	mintAccessToken string
	mintExpiresIn   int
	mintErr         error
	mintUserID      string
}

func (fake *fakeAgentGrants) Status(_ context.Context, _ string) (domain.AgentGrant, error) {
	return fake.statusGrant, fake.statusErr
}

func (fake *fakeAgentGrants) Authorize(_ context.Context, memberID, returnTo string) (string, string, error) {
	fake.gotMemberID, fake.gotReturnTo = memberID, returnTo
	return fake.authorizeURL, fake.authorizeState, fake.authorizeErr
}

func (fake *fakeAgentGrants) Complete(_ context.Context, state, stateCookie, code string) (string, error) {
	fake.gotState, fake.gotStateCookie, fake.gotCode = state, stateCookie, code
	return fake.completeReturnTo, fake.completeErr
}

func (fake *fakeAgentGrants) Revoke(_ context.Context, userID string) error {
	fake.revokedUser = userID
	return fake.revokeErr
}

func (fake *fakeAgentGrants) MintToken(_ context.Context, userID string) (string, int, error) {
	fake.mintUserID = userID
	return fake.mintAccessToken, fake.mintExpiresIn, fake.mintErr
}

// The rest of authService's methods, so a request that reaches the handler
// for any route never panics on an unimplemented fake method, whichever
// token (member or agent) it carries.

func (fake *fakeAuthService) CreateOrganization(_ context.Context, _ string, _ application.CreateOrganizationInput) (domain.Organization, error) {
	return fake.organization, fake.err
}

func (fake *fakeAuthService) UpdateOrganization(_ context.Context, _, _ string, _ application.UpdateOrganizationInput) (domain.Organization, error) {
	return fake.organization, fake.err
}

func (fake *fakeAuthService) DeleteOrganization(_ context.Context, _, _ string) error {
	return fake.err
}

func (fake *fakeAuthService) Organizations(_ context.Context, _ string) ([]domain.Organization, error) {
	return fake.organizations, fake.err
}

func (fake *fakeAuthService) Organization(_ context.Context, _, _ string) (domain.Organization, domain.Membership, error) {
	return fake.organization, fake.membership, fake.err
}

func (fake *fakeAuthService) Members(_ context.Context, _, _ string) ([]domain.Membership, error) {
	return fake.members, fake.err
}

func (fake *fakeAuthService) Invitations(_ context.Context, _, _ string) ([]domain.Invitation, error) {
	return nil, fake.err
}

func (fake *fakeAuthService) RevokeInvitation(_ context.Context, _, _, _ string) error {
	return fake.err
}

func (fake *fakeAuthService) AcceptInvitation(_ context.Context, _, _ string) (domain.Membership, error) {
	return fake.membership, fake.err
}

func (fake *fakeAuthService) ChangeMemberRole(_ context.Context, _, _, _ string, _ domain.Role) (domain.Membership, error) {
	return fake.membership, fake.err
}

func (fake *fakeAuthService) RemoveMember(_ context.Context, _, _, _ string) error {
	return fake.err
}

const agentClientID = "agent-client-id"

func agentGrantHandler(agentGrants *fakeAgentGrants) http.Handler {
	return New(
		fakeVerifier{claims: authn.Claims{Subject: "member-1", ClientID: "web-client-id"}},
		&fakeAuthFlow{},
		&fakePasswordAuth{},
		&fakeAuthService{},
		Config{
			AllowedOrigins: []string{"https://app.example.com"},
			CookieSecure:   true,
			AgentClientID:  agentClientID,
			AgentGrants:    agentGrants,
		},
	)
}

func TestAgentGrantStatusReportsGranted(t *testing.T) {
	createdAt := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	handler := agentGrantHandler(&fakeAgentGrants{statusGrant: domain.AgentGrant{UserID: "member-1", CreatedAt: createdAt}})
	request := httptest.NewRequest(http.MethodGet, "/v1/agent-grant", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"granted":true`) || !strings.Contains(response.Body.String(), "2026-09-16") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
}

func TestAgentGrantStatusReportsUngrantedWithoutCreatedAt(t *testing.T) {
	handler := agentGrantHandler(&fakeAgentGrants{statusErr: domain.ErrNotFound})
	request := httptest.NewRequest(http.MethodGet, "/v1/agent-grant", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"granted":false`) || strings.Contains(response.Body.String(), "created_at") {
		t.Fatalf("status = %d, body = %s, want granted:false with no created_at", response.Code, response.Body)
	}
}

func TestAgentGrantRoutesAnswer404WhenDisabled(t *testing.T) {
	handler := New(
		fakeVerifier{claims: authn.Claims{Subject: "member-1", ClientID: "web-client-id"}},
		&fakeAuthFlow{}, &fakePasswordAuth{}, &fakeAuthService{},
		Config{AllowedOrigins: []string{"https://app.example.com"}},
	)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/agent-grant"},
		{http.MethodPost, "/v1/agent-grant/authorize"},
		{http.MethodGet, "/v1/agent-grant/callback"},
		{http.MethodDelete, "/v1/agent-grant"},
		{http.MethodPost, "/v1/internal/agent-tokens"},
	} {
		request := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404 when agent grants are disabled", route.method, route.path, response.Code)
		}
	}
}

func TestAgentGrantAuthorizeBuildsURLAndSetsStateCookie(t *testing.T) {
	agentGrants := &fakeAgentGrants{authorizeURL: "https://identity.example.com/oauth2/authorize?state=abc", authorizeState: "member-1.random"}
	handler := agentGrantHandler(agentGrants)
	request := httptest.NewRequest(http.MethodPost, "/v1/agent-grant/authorize", strings.NewReader(`{"return_to":"https://app.example.com"}`))
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), agentGrants.authorizeURL) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	if agentGrants.gotMemberID != "member-1" || agentGrants.gotReturnTo != "https://app.example.com" {
		t.Fatalf("Authorize() called with memberID=%q returnTo=%q", agentGrants.gotMemberID, agentGrants.gotReturnTo)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != agentGrantStateCookieName || cookies[0].Value != "member-1.random" || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("unexpected state cookie: %+v", cookies)
	}
}

func TestAgentGrantCallbackStoresGrantAndRedirects(t *testing.T) {
	agentGrants := &fakeAgentGrants{completeReturnTo: "https://app.example.com/agents"}
	handler := agentGrantHandler(agentGrants)
	request := httptest.NewRequest(http.MethodGet, "/v1/agent-grant/callback?state=member-1.random&code=code-1", nil)
	request.AddCookie(&http.Cookie{Name: agentGrantStateCookieName, Value: "member-1.random"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	if location := response.Header().Get("Location"); location != "https://app.example.com/agents" {
		t.Fatalf("location = %q", location)
	}
	if agentGrants.gotState != "member-1.random" || agentGrants.gotStateCookie != "member-1.random" || agentGrants.gotCode != "code-1" {
		t.Fatalf("Complete() called with state=%q cookie=%q code=%q", agentGrants.gotState, agentGrants.gotStateCookie, agentGrants.gotCode)
	}
}

func TestAgentGrantCallbackRefusesAndStoresNothingOnFailure(t *testing.T) {
	for name, request := range map[string]func() *http.Request{
		"missing state cookie": func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/v1/agent-grant/callback?state=member-1.random&code=code-1", nil)
		},
		"identity provider error": func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/v1/agent-grant/callback?error=access_denied", nil)
			r.AddCookie(&http.Cookie{Name: agentGrantStateCookieName, Value: "member-1.random"})
			return r
		},
	} {
		t.Run(name, func(t *testing.T) {
			agentGrants := &fakeAgentGrants{completeErr: domain.ErrUnauthorized}
			handler := agentGrantHandler(agentGrants)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request())
			if response.Code == http.StatusSeeOther {
				t.Fatalf("callback redirected despite failure: status = %d", response.Code)
			}
		})
	}

	// A refusal from the flow itself (another member's code, a bad state, or
	// a disallowed return URL) must not redirect anywhere either.
	agentGrants := &fakeAgentGrants{completeErr: domain.ErrUnauthorized}
	handler := agentGrantHandler(agentGrants)
	request := httptest.NewRequest(http.MethodGet, "/v1/agent-grant/callback?state=member-1.random&code=code-1", nil)
	request.AddCookie(&http.Cookie{Name: agentGrantStateCookieName, Value: "member-1.random"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when the flow refuses", response.Code)
	}
}

func TestAgentGrantDeleteRevokesAndAnswersNoContent(t *testing.T) {
	agentGrants := &fakeAgentGrants{}
	handler := agentGrantHandler(agentGrants)
	request := httptest.NewRequest(http.MethodDelete, "/v1/agent-grant", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if agentGrants.revokedUser != "member-1" {
		t.Fatalf("revoked user = %q, want member-1", agentGrants.revokedUser)
	}
}

func TestAgentGrantDeleteWithNoGrantIsStillNoContent(t *testing.T) {
	// Revoke itself is idempotent (see agentgrant.Manager), so the fake
	// simply returns no error here; the route must not turn that into
	// anything but 204.
	handler := agentGrantHandler(&fakeAgentGrants{})
	request := httptest.NewRequest(http.MethodDelete, "/v1/agent-grant", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestOrganizationResponseCarriesAgentOnlyForAgentTokens(t *testing.T) {
	organization := domain.Organization{ID: "org-1", Name: "Org", Slug: "org"}
	membership := domain.Membership{OrganizationID: "org-1", UserID: "member-1", Role: domain.RoleMember}

	memberHandler := New(
		fakeVerifier{claims: authn.Claims{Subject: "member-1", ClientID: "web-client-id"}},
		&fakeAuthFlow{}, &fakePasswordAuth{},
		&fakeAuthService{organization: organization, membership: membership},
		Config{AllowedOrigins: []string{"https://app.example.com"}, AgentClientID: agentClientID},
	)
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org-1", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	memberHandler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"agent"`) {
		t.Fatalf("member response = %d %s, want 200 without an agent field", response.Code, response.Body)
	}

	agentHandler := New(
		fakeVerifier{claims: authn.Claims{Subject: "member-1", ClientID: agentClientID}},
		&fakeAuthFlow{}, &fakePasswordAuth{},
		&fakeAuthService{organization: organization, membership: membership},
		Config{AllowedOrigins: []string{"https://app.example.com"}, AgentClientID: agentClientID},
	)
	request = httptest.NewRequest(http.MethodGet, "/v1/organizations/org-1", nil)
	request.Header.Set("Authorization", "Bearer token")
	response = httptest.NewRecorder()
	agentHandler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"agent":true`) {
		t.Fatalf("agent response = %d %s, want 200 with \"agent\":true", response.Code, response.Body)
	}
}

// TestAgentTokenRestrictedToAllowlist is a table over every authenticated
// route: an agent token must reach only the four allowlisted ones (the
// member-facing agent grant routes included in the restricted set, since
// only a member's own token may manage a grant) and must be refused
// everywhere else with 403 {"error":"agents cannot do this"}. A member's own
// token must never see that refusal, anywhere.
func TestAgentTokenRestrictedToAllowlist(t *testing.T) {
	routes := []struct {
		method  string
		path    string
		allowed bool
	}{
		{http.MethodGet, "/v1/auth/me", true},
		{http.MethodGet, "/v1/organizations", true},
		{http.MethodPost, "/v1/organizations", false},
		{http.MethodGet, "/v1/organizations/org-1", true},
		{http.MethodPatch, "/v1/organizations/org-1", false},
		{http.MethodDelete, "/v1/organizations/org-1", false},
		{http.MethodGet, "/v1/organizations/org-1/members", true},
		{http.MethodPost, "/v1/organizations/org-1/invitations", false},
		{http.MethodGet, "/v1/organizations/org-1/invitations", false},
		{http.MethodDelete, "/v1/organizations/org-1/invitations/inv-1", false},
		{http.MethodPost, "/v1/invitations/accept", false},
		{http.MethodPatch, "/v1/organizations/org-1/members/user-1", false},
		{http.MethodDelete, "/v1/organizations/org-1/members/user-1", false},
		{http.MethodGet, "/v1/agent-grant", false},
		{http.MethodPost, "/v1/agent-grant/authorize", false},
		{http.MethodDelete, "/v1/agent-grant", false},
	}

	newRequest := func(method, path string) *http.Request {
		var request *http.Request
		if method == http.MethodGet {
			request = httptest.NewRequest(method, path, nil)
		} else {
			request = httptest.NewRequest(method, path, strings.NewReader("{}"))
			request.Header.Set("Content-Type", "application/json")
		}
		request.Header.Set("Authorization", "Bearer token")
		return request
	}

	for _, route := range routes {
		t.Run("agent "+route.method+" "+route.path, func(t *testing.T) {
			handler := New(
				fakeVerifier{claims: authn.Claims{Subject: "member-1", ClientID: agentClientID}},
				&fakeAuthFlow{}, &fakePasswordAuth{}, &fakeAuthService{},
				Config{AllowedOrigins: []string{"https://app.example.com"}, AgentClientID: agentClientID, AgentGrants: &fakeAgentGrants{}},
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, newRequest(route.method, route.path))

			if route.allowed {
				if response.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s, want 200 on an allowlisted route", response.Code, response.Body)
				}
			} else if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "agents cannot do this") {
				t.Fatalf("status = %d, body = %s, want 403 agents cannot do this", response.Code, response.Body)
			}
		})

		t.Run("member "+route.method+" "+route.path, func(t *testing.T) {
			handler := New(
				fakeVerifier{claims: authn.Claims{Subject: "member-1", ClientID: "web-client-id"}},
				&fakeAuthFlow{}, &fakePasswordAuth{}, &fakeAuthService{},
				Config{AllowedOrigins: []string{"https://app.example.com"}, AgentClientID: agentClientID, AgentGrants: &fakeAgentGrants{}},
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, newRequest(route.method, route.path))

			if response.Code == http.StatusForbidden && strings.Contains(response.Body.String(), "agents cannot do this") {
				t.Fatalf("a member's own token was refused as an agent: status = %d, body = %s", response.Code, response.Body)
			}
		})
	}
}
