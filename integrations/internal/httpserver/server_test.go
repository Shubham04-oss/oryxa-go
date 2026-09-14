package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
)

type testAuthenticator struct {
	memberID string
	err      error
}

func (a testAuthenticator) Authenticate(*http.Request) (string, error) {
	return a.memberID, a.err
}

type testCatalog struct {
	organizationID string
	memberID       string
	items          []domain.MemberIntegration
	err            error
}

func TestHealthDoesNotRequireRuntimeDependencies(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	NewAPI(Dependencies{}).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
}

func (c *testCatalog) MemberIntegrations(_ context.Context, organizationID, memberID string) ([]domain.MemberIntegration, error) {
	c.organizationID = organizationID
	c.memberID = memberID
	return c.items, c.err
}

func TestMemberCatalogShowsAvailableAndConnectedWithoutCredentialFields(t *testing.T) {
	t.Parallel()
	catalog := &testCatalog{items: []domain.MemberIntegration{
		{
			ProfileID: "app-available", Provider: "slack",
			PieceName: "@activepieces/piece-slack", PieceVersion: "0.17.10",
			Mode: domain.OAuthAppModeBYOC, Scopes: []string{"channels:history"},
			State: domain.MemberIntegrationAvailable,
		},
		{
			ProfileID: "app-connected", Provider: "slack",
			PieceName: "@activepieces/piece-slack", PieceVersion: "0.17.10",
			Mode: domain.OAuthAppModeBYOC, Scopes: []string{"channels:history"},
			State: domain.MemberIntegrationConnected,
			Connection: &domain.MemberConnection{
				ID: "connection-1", Identity: domain.ProviderIdentity{SubjectID: "U123", TenantID: "T123", TenantName: "Oryxa"},
				ConnectedAt: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC),
			},
		},
	}}
	server := New(testAuthenticator{memberID: "member-1"}, catalog)
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org-1/integrations", nil)
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if catalog.organizationID != "org-1" || catalog.memberID != "member-1" {
		t.Fatalf("catalog identity = %s/%s", catalog.organizationID, catalog.memberID)
	}
	var body struct {
		Items []domain.MemberIntegration `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Items[0].State != domain.MemberIntegrationAvailable || body.Items[0].Connection != nil {
		t.Fatalf("available item = %+v", body.Items)
	}
	if body.Items[1].State != domain.MemberIntegrationConnected || body.Items[1].Connection == nil || body.Items[1].Connection.ID != "connection-1" {
		t.Fatalf("connected item = %+v", body.Items)
	}
	for _, forbidden := range []string{"client_id", "client_secret", "encrypted", "ciphertext", "access_token", "refresh_token"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("member response contains %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestMemberCatalogRequiresAuthentication(t *testing.T) {
	t.Parallel()
	catalog := &testCatalog{}
	server := New(testAuthenticator{err: ErrUnauthenticated}, catalog)
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org-1/integrations", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || catalog.organizationID != "" {
		t.Fatalf("status/catalog = %d/%q", response.Code, catalog.organizationID)
	}
}

func TestMemberCatalogHidesAuthorizationFailureDetails(t *testing.T) {
	t.Parallel()
	catalog := &testCatalog{err: errors.Join(oauthflow.ErrForbidden, errors.New("database or membership detail"))}
	server := New(testAuthenticator{memberID: "member-1"}, catalog)
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org-1/integrations", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "database") {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestMemberCatalogReturnsGenericInternalError(t *testing.T) {
	t.Parallel()
	catalog := &testCatalog{err: errors.New("database detail")}
	server := New(testAuthenticator{memberID: "member-1"}, catalog)
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/org-1/integrations", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "database") {
		t.Fatalf("status/body = %d/%s", response.Code, response.Body.String())
	}
}

func TestActionErrorsIncludeStableMessagesWithoutInternalDetails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
		hidden  string
	}{
		{
			name:    "validation",
			err:     fmt.Errorf("%w: %s", actionflow.ErrInvalidInput, "invalid GitHub create-gist properties"),
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: "invalid GitHub create-gist properties",
		},
		{
			name:    "scope",
			err:     fmt.Errorf("%w: %s", actionflow.ErrRequiredScope, "gist"),
			status:  http.StatusUnprocessableEntity,
			code:    "scope_required",
			message: "connection requires additional authorization: gist",
		},
		{
			name:    "runner",
			err:     actionflow.ExecutionFailed{Code: "file_too_large", Message: "file exceeds the size limit"},
			status:  http.StatusRequestEntityTooLarge,
			code:    "file_too_large",
			message: "file exceeds the size limit",
		},
		{
			name:    "opaque execution",
			err:     fmt.Errorf("%w: %s", actionflow.ErrExecutionFailed, "provider timeout"),
			status:  http.StatusBadGateway,
			code:    "provider_execution_failed",
			message: "provider action could not be completed",
			hidden:  "provider timeout",
		},
		{
			name:    "joined database detail",
			err:     errors.Join(fmt.Errorf("%w: %s", actionflow.ErrInvalidInput, "invalid GitHub create-gist properties"), errors.New("database detail")),
			status:  http.StatusBadRequest,
			code:    "invalid_request",
			message: "invalid GitHub create-gist properties",
			hidden:  "database",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := NewAPI(Dependencies{
				Authenticator: testAuthenticator{memberID: "member-1"},
				Actions:       failingActions{err: test.err},
			})
			response := perform(server, http.MethodPost, "/v1/organizations/org-1/connections/con-1/actions/create-gist",
				`{"props":{"public":false,"filename":"note.txt","content":"hello"}}`,
				map[string]string{"Idempotency-Key": "request-123"})
			if response.Code != test.status {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != test.code || body.Error.Message != test.message {
				t.Fatalf("error = %+v", body.Error)
			}
			if test.hidden != "" && strings.Contains(response.Body.String(), test.hidden) {
				t.Fatalf("body leaked %q: %s", test.hidden, response.Body.String())
			}
		})
	}
}

type failingActions struct{ err error }

func (a failingActions) Run(context.Context, actionflow.RunRequest) (actionflow.RunResult, error) {
	return actionflow.RunResult{}, a.err
}
