package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

type fakeServiceVerifier struct {
	claims serviceauth.Claims
	err    error
}

func (fake fakeServiceVerifier) Verify(_ context.Context, _ string) (serviceauth.Claims, error) {
	return fake.claims, fake.err
}

// agentGrants is typed as the AgentGrantsFlow interface, not *fakeAgentGrants:
// passing a literal nil must produce a true nil interface, matching how main.go
// leaves Config.AgentGrants nil when agent grants are disabled. A nil
// *fakeAgentGrants wrapped in the interface would be non-nil and panic instead.
func internalHandler(agentGrants AgentGrantsFlow, services ServiceVerifier) http.Handler {
	return New(
		fakeVerifier{},
		&fakeAuthFlow{},
		&fakePasswordAuth{},
		&fakeAuthService{},
		Config{
			AllowedOrigins: []string{"https://app.example.com"},
			AgentGrants:    agentGrants,
			Services:       services,
		},
	)
}

func mintRequest(body string, serviceToken string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/internal/agent-tokens", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if serviceToken != "" {
		request.Header.Set(serviceauth.Header, serviceToken)
	}
	return request
}

func TestMintAgentTokenSucceeds(t *testing.T) {
	agentGrants := &fakeAgentGrants{mintAccessToken: "access-token-1", mintExpiresIn: 3600}
	services := fakeServiceVerifier{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}}
	handler := internalHandler(agentGrants, services)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{"user_id":"member-1"}`, "svc-token"))

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"access_token":"access-token-1"`) || !strings.Contains(response.Body.String(), `"expires_in":3600`) {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	if agentGrants.mintUserID != "member-1" {
		t.Fatalf("mint user = %q, want member-1", agentGrants.mintUserID)
	}
}

func TestMintAgentTokenAnswersNotFoundWithoutGrant(t *testing.T) {
	agentGrants := &fakeAgentGrants{mintErr: domain.ErrNotFound}
	services := fakeServiceVerifier{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}}
	handler := internalHandler(agentGrants, services)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{"user_id":"member-1"}`, "svc-token"))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestMintAgentTokenAnswersConflictAndDeletesWhenCognitoRefuses(t *testing.T) {
	agentGrants := &fakeAgentGrants{mintErr: domain.ErrConflict}
	services := fakeServiceVerifier{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}}
	handler := internalHandler(agentGrants, services)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{"user_id":"member-1"}`, "svc-token"))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
}

func TestMintAgentTokenAnswersServiceUnavailableWhenCognitoIsDown(t *testing.T) {
	agentGrants := &fakeAgentGrants{mintErr: domain.ErrUnavailable}
	services := fakeServiceVerifier{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}}
	handler := internalHandler(agentGrants, services)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{"user_id":"member-1"}`, "svc-token"))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestMintAgentTokenRequiresServiceToken(t *testing.T) {
	handler := internalHandler(&fakeAgentGrants{}, fakeServiceVerifier{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{"user_id":"member-1"}`, ""))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMintAgentTokenRejectsWithoutConfiguredCallers(t *testing.T) {
	// Services is nil: AUTH_SERVICE_CALLERS was not set, so no service can be
	// verified even though agent grants are otherwise enabled.
	handler := internalHandler(&fakeAgentGrants{}, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{"user_id":"member-1"}`, "svc-token"))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMintAgentTokenRejectsInvalidServiceToken(t *testing.T) {
	handler := internalHandler(&fakeAgentGrants{}, fakeServiceVerifier{err: serviceauth.ErrInvalidToken})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{"user_id":"member-1"}`, "svc-token"))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMintAgentTokenRejectsDisallowedPurposeFromVerifier(t *testing.T) {
	handler := internalHandler(&fakeAgentGrants{}, fakeServiceVerifier{err: serviceauth.ErrPurposeNotAllowed})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{"user_id":"member-1"}`, "svc-token"))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestMintAgentTokenRejectsWrongPurposeEvenIfCallerAllowsIt(t *testing.T) {
	// The service token verifies fine (its caller is allowed to use purpose
	// "cleanup" in general), but this route only ever accepts "agent".
	agentGrants := &fakeAgentGrants{mintAccessToken: "should-not-be-returned", mintExpiresIn: 3600}
	services := fakeServiceVerifier{claims: serviceauth.Claims{Service: "agents", Purpose: "cleanup"}}
	handler := internalHandler(agentGrants, services)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{"user_id":"member-1"}`, "svc-token"))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if agentGrants.mintUserID != "" {
		t.Fatal("MintToken() was called despite the wrong purpose")
	}
}

func TestMintAgentTokenAnswers404WhenGrantsAreDisabled(t *testing.T) {
	handler := internalHandler(nil, fakeServiceVerifier{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{"user_id":"member-1"}`, "svc-token"))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestMintAgentTokenRequiresUserID(t *testing.T) {
	services := fakeServiceVerifier{claims: serviceauth.Claims{Service: "agents", Purpose: "agent"}}
	handler := internalHandler(&fakeAgentGrants{}, services)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mintRequest(`{}`, "svc-token"))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
