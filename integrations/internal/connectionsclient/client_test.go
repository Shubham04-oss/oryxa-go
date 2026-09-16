package connectionsclient

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// fakeSigner records the audience and purpose it was asked to sign for and
// answers "svc-token", or err when it is set.
type fakeSigner struct {
	audience string
	purpose  string
	err      error
}

func (signer *fakeSigner) Token(_ context.Context, audience, purpose string) (string, error) {
	signer.audience, signer.purpose = audience, purpose
	if signer.err != nil {
		return "", signer.err
	}
	return "svc-token", nil
}

var testConnection = domain.Connection{
	ID: "con_1", OrganizationID: "org_1", AppID: "app_1", Provider: "github", CredentialType: "oauth2", Scope: "member",
	ProviderAccount: domain.ProviderAccount{ID: "42", Name: "octocat"}, Status: "active",
}

func TestConnection(t *testing.T) {
	signer := &fakeSigner{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/internal/connections/con_1" ||
			request.URL.Query().Get("organization_id") != "org_1" || request.Header.Get(serviceauth.Header) != "svc-token" ||
			request.Header.Get("Authorization") != "Bearer member-token" {
			t.Errorf("unexpected connection request %s %s %v", request.Method, request.URL, request.Header)
		}
		_ = json.NewEncoder(writer).Encode(testConnection)
	}))
	defer server.Close()

	connection, err := New(server.URL, signer).Connection(context.Background(), "org_1", "con_1", "member-token")
	if err != nil {
		t.Fatalf("Connection() error = %v", err)
	}
	if connection != testConnection {
		t.Fatalf("Connection() = %+v, want %+v", connection, testConnection)
	}
	if signer.audience != Audience || signer.purpose != string(domain.PurposeTrigger) {
		t.Fatalf("signer got audience = %q, purpose = %q", signer.audience, signer.purpose)
	}
}

func TestConnectionRejectsAnswerForAnotherConnectionOrOrganization(t *testing.T) {
	wrongID := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(domain.Connection{ID: "con_2", OrganizationID: "org_1"})
	}))
	defer wrongID.Close()
	if _, err := New(wrongID.URL, &fakeSigner{}).Connection(context.Background(), "org_1", "con_1", "member-token"); err == nil {
		t.Fatal("Connection() accepted an answer for a different connection")
	}

	wrongOrg := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(domain.Connection{ID: "con_1", OrganizationID: "org_2"})
	}))
	defer wrongOrg.Close()
	if _, err := New(wrongOrg.URL, &fakeSigner{}).Connection(context.Background(), "org_1", "con_1", "member-token"); err == nil {
		t.Fatal("Connection() accepted an answer for a different organization")
	}
}

func TestTokenWithAccessToken(t *testing.T) {
	signer := &fakeSigner{}
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/internal/connections/con_1/token" ||
			request.Header.Get("Authorization") != "Bearer member-token" {
			t.Errorf("unexpected token request %s %s %v", request.Method, request.URL.Path, request.Header)
		}
		_ = json.NewDecoder(request.Body).Decode(&received)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"access_token": "token-xyz",
			"placement":    map[string]string{"in": "header", "name": "Authorization", "prefix": "Bearer "},
			"domain":       "github.com",
			"metadata":     map[string]string{"account": "42"},
		})
	}))
	defer server.Close()

	credential, err := New(server.URL, signer).Token(context.Background(), TokenRequest{
		OrganizationID: "org_1", ConnectionID: "con_1", Purpose: domain.PurposeTrigger, AccessToken: "member-token",
	})
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	want := domain.Credential{
		AccessToken: "token-xyz", In: "header", Name: "Authorization", Prefix: "Bearer ",
		Domain: "github.com", Metadata: map[string]string{"account": "42"},
	}
	if credential.AccessToken != want.AccessToken || credential.In != want.In || credential.Name != want.Name ||
		credential.Prefix != want.Prefix || credential.Domain != want.Domain || !maps.Equal(credential.Metadata, want.Metadata) {
		t.Fatalf("Token() = %+v, want %+v", credential, want)
	}
	if received["organization_id"] != "org_1" || received["purpose"] != "trigger" || received["acting_member_id"] != nil {
		t.Fatalf("token request body = %v", received)
	}
	if signer.purpose != "trigger" {
		t.Fatalf("signer purpose = %q, want %q", signer.purpose, "trigger")
	}
}

func TestTokenWithoutAccessToken(t *testing.T) {
	var received map[string]any
	var authorizationSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorizationSeen = request.Header.Get("Authorization") != ""
		_ = json.NewDecoder(request.Body).Decode(&received)
		_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "token-xyz"})
	}))
	defer server.Close()

	if _, err := New(server.URL, &fakeSigner{}).Token(context.Background(), TokenRequest{
		OrganizationID: "org_1", ConnectionID: "con_1", Purpose: domain.PurposeTrigger, ActingMemberID: "user-1",
	}); err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if authorizationSeen {
		t.Fatal("Token() without an access token sent an Authorization header")
	}
	if received["acting_member_id"] != "user-1" || received["organization_id"] != "org_1" {
		t.Fatalf("token request body = %v", received)
	}
}

func TestTokenWithoutAnAccessTokenInTheAnswerIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{})
	}))
	defer server.Close()
	if _, err := New(server.URL, &fakeSigner{}).Token(context.Background(), TokenRequest{
		OrganizationID: "org_1", ConnectionID: "con_1", Purpose: domain.PurposeTrigger, AccessToken: "member-token",
	}); err == nil {
		t.Fatal("Token() with no access_token in the answer should be an error")
	}
}

func TestAddHold(t *testing.T) {
	signer := &fakeSigner{}
	var received map[string]any
	var authorizationSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/internal/connections/con_1/holds" {
			t.Errorf("unexpected add hold request %s %s", request.Method, request.URL.Path)
		}
		authorizationSeen = request.Header.Get("Authorization") != ""
		_ = json.NewDecoder(request.Body).Decode(&received)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := New(server.URL, signer).AddHold(context.Background(), "org_1", "con_1"); err != nil {
		t.Fatalf("AddHold() error = %v", err)
	}
	if authorizationSeen {
		t.Fatal("AddHold() sent an Authorization header")
	}
	if received["organization_id"] != "org_1" || received["holder"] != domain.Holder {
		t.Fatalf("AddHold() body = %v", received)
	}
	if signer.purpose != string(domain.PurposeCleanup) {
		t.Fatalf("signer purpose = %q, want %q", signer.purpose, domain.PurposeCleanup)
	}
}

func TestReleaseHold(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath, gotQuery = request.URL.Path, request.URL.RawQuery
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := New(server.URL, &fakeSigner{}).ReleaseHold(context.Background(), "org_1", "con_1"); err != nil {
		t.Fatalf("ReleaseHold() error = %v", err)
	}
	wantPath := "/v1/internal/connections/con_1/holds/" + url.PathEscape(domain.Holder)
	if gotPath != wantPath || gotQuery != "organization_id=org_1" {
		t.Fatalf("ReleaseHold() path = %q, query = %q, want path %q", gotPath, gotQuery, wantPath)
	}
}

func TestApp(t *testing.T) {
	var serviceTokenSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/organizations/org_1/apps" ||
			request.Header.Get("Authorization") != "Bearer member-token" {
			t.Errorf("unexpected app request %s %s %v", request.Method, request.URL.Path, request.Header)
		}
		serviceTokenSeen = request.Header.Get(serviceauth.Header) != ""
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"apps": []App{{ID: "app_1", Provider: "github", Status: "active"}, {ID: "app_2", Provider: "slack", Status: "active"}},
		})
	}))
	defer server.Close()

	app, err := New(server.URL, &fakeSigner{}).App(context.Background(), "org_1", "app_2", "member-token")
	if err != nil || app != (App{ID: "app_2", Provider: "slack", Status: "active"}) {
		t.Fatalf("App() = %+v, %v", app, err)
	}
	if serviceTokenSeen {
		t.Fatal("App() sent a service token header")
	}
}

func TestAppNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"apps": []App{{ID: "app_1"}}})
	}))
	defer server.Close()
	if _, err := New(server.URL, &fakeSigner{}).App(context.Background(), "org_1", "app_9", "member-token"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("App() error = %v, want ErrNotFound", err)
	}
}

func TestErrorMapping(t *testing.T) {
	for _, test := range []struct {
		status int
		code   string
		want   error
	}{
		{http.StatusConflict, "needs_reconnect", domain.ErrNeedsReconnect},
		{http.StatusConflict, "app_disabled", domain.ErrForbidden},
		{http.StatusUnprocessableEntity, "credential_rejected", domain.ErrCredentialRejected},
		{http.StatusUnprocessableEntity, "scope_required", domain.ErrForbidden},
		{http.StatusBadRequest, "invalid_request", domain.ErrInvalid},
		{http.StatusUnauthorized, "", domain.ErrUnauthorized},
		{http.StatusForbidden, "", domain.ErrForbidden},
		{http.StatusNotFound, "", domain.ErrNotFound},
		{http.StatusConflict, "conflict", domain.ErrConflict},
		{http.StatusServiceUnavailable, "", domain.ErrProviderUnavailable},
		{http.StatusTooManyRequests, "", domain.ErrProviderUnavailable},
		{http.StatusInternalServerError, "", domain.ErrProviderUnavailable},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(test.status)
			if test.code != "" {
				_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]string{"code": test.code}})
			}
		}))
		_, err := New(server.URL, &fakeSigner{}).App(context.Background(), "org_1", "app_1", "member-token")
		server.Close()
		if !errors.Is(err, test.want) {
			t.Fatalf("status %d code %q: error = %v, want %v", test.status, test.code, err, test.want)
		}
	}
}

func TestSignerErrorIsReturned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request should not reach connections when signing fails")
	}))
	defer server.Close()
	signerErr := errors.New("kms unavailable")
	if _, err := New(server.URL, &fakeSigner{err: signerErr}).Connection(context.Background(), "org_1", "con_1", "member-token"); !errors.Is(err, signerErr) {
		t.Fatalf("Connection() error = %v, want wrapping %v", err, signerErr)
	}
}

func TestClosedServerIsProviderUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	if _, err := New(server.URL, &fakeSigner{}).App(context.Background(), "org_1", "app_1", "member-token"); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("closed server error = %v, want ErrProviderUnavailable", err)
	}
}
