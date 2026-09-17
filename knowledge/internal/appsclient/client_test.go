package appsclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

func TestConnectionRefusesACallerWithNothingToIdentifyThem(t *testing.T) {
	// No request is worth making: apps would only say the same thing back.
	if _, err := New("http://unused.test").Connection(context.Background(), "org_1", "apc_1", authclient.Credentials{}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Connection() error = %v, want ErrUnauthorized", err)
	}
}

func TestConnectionCarriesABearerToken(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request
		_, _ = writer.Write([]byte(`{
			"id": "apc_1", "toolkit": "github", "status": "active", "label": "github_red-castle",
			"member_id": "user-1", "composio_account_id": "ca_1"
		}`))
	}))
	defer server.Close()

	connection, err := New(server.URL).Connection(context.Background(), "org_1", "apc_1", authclient.Credentials{Authorization: "Bearer t"})
	if err != nil {
		t.Fatalf("Connection() error = %v", err)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer t" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer t")
	}
	if got := seen.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
	if !strings.Contains(seen.URL.Path, "/v1/organizations/org_1/apps/connections/apc_1") {
		t.Fatalf("path = %q, want the connection's own path", seen.URL.Path)
	}
	if connection != (domain.AppConnection{
		ID: "apc_1", Toolkit: "github", Status: "active", MemberID: "user-1",
		ComposioAccountID: "ca_1", Label: "github_red-castle",
	}) {
		t.Fatalf("Connection() = %+v", connection)
	}
}

func TestConnectionCarriesTheAccessCookieInstead(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request
		_, _ = writer.Write([]byte(`{"id": "apc_1", "toolkit": "github", "status": "active", "member_id": "user-1"}`))
	}))
	defer server.Close()

	if _, err := New(server.URL).Connection(context.Background(), "org_1", "apc_1", authclient.Credentials{AccessCookie: "c"}); err != nil {
		t.Fatalf("Connection() error = %v", err)
	}
	cookie, err := seen.Cookie(authclient.AccessCookieName)
	if err != nil || cookie.Value != "c" {
		t.Fatalf("cookie %s = %v, %v, want %q", authclient.AccessCookieName, cookie, err, "c")
	}
	if seen.Header.Get("Authorization") != "" {
		t.Fatal("a cookie-authenticated call must not also carry a bearer token")
	}
}

func TestConnectionNeverForwardsTheCredentialAnywhereElse(t *testing.T) {
	// The only place a caller's credentials may go is the request apps
	// itself answers; nothing about this client sends them on.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.URL.Query().Get("access_token"); got != "" {
			t.Fatalf("a credential must never travel in the query: got %q", got)
		}
		_, _ = writer.Write([]byte(`{"id": "apc_1", "toolkit": "github", "status": "active", "member_id": "user-1"}`))
	}))
	defer server.Close()

	if _, err := New(server.URL).Connection(context.Background(), "org_1", "apc_1", authclient.Credentials{Authorization: "Bearer secret"}); err != nil {
		t.Fatalf("Connection() error = %v", err)
	}
}

func TestConnectionRefusesAnAnswerThatIsNotJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("not json"))
	}))
	defer server.Close()

	if _, err := New(server.URL).Connection(context.Background(), "org_1", "apc_1", authclient.Credentials{Authorization: "Bearer t"}); err == nil {
		t.Fatal("Connection() error = nil, want a refusal")
	}
}

func TestAppsAnswersMapToReasonsACallerActsOn(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusUnauthorized:    domain.ErrUnauthorized,
		http.StatusForbidden:       domain.ErrNotFound,
		http.StatusNotFound:        domain.ErrNotFound,
		http.StatusTooManyRequests: domain.ErrRateLimited,
		http.StatusBadGateway:      domain.ErrProviderUnavailable,
		http.StatusBadRequest:      domain.ErrInvalid,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := New(server.URL).Connection(context.Background(), "org_1", "apc_1", authclient.Credentials{Authorization: "Bearer t"})
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("Connection() on %d error = %v, want %v", status, err, want)
		}
	}
}

func TestAppsNotAnsweringIsNotTheCallersFault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()

	if _, err := New(address).Connection(context.Background(), "org_1", "apc_1", authclient.Credentials{Authorization: "Bearer t"}); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Connection() error = %v, want ErrProviderUnavailable", err)
	}
}
