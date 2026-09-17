package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

func TestMembershipRefusesACallerWithNothingToIdentifyThem(t *testing.T) {
	// No request is worth making: auth would only say the same thing back.
	if _, err := New("http://unused.test").Membership(context.Background(), "org_1", Credentials{}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Membership() error = %v, want ErrUnauthorized", err)
	}
}

func TestMembershipCarriesABearerToken(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request
		_, _ = writer.Write([]byte(`{"membership":{"user_id":"user-1","role":"member"}}`))
	}))
	defer server.Close()

	membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"})
	if err != nil {
		t.Fatalf("Membership() error = %v", err)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer t" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer t")
	}
	if got := seen.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
	if !strings.Contains(seen.URL.Path, "/v1/organizations/org_1") {
		t.Fatalf("path = %q, want the organization", seen.URL.Path)
	}
	if membership.OrganizationID != "org_1" || membership.UserID != "user-1" || membership.Role != domain.OrgRoleMember {
		t.Fatalf("Membership() = %+v", membership)
	}
}

func TestMembershipCarriesTheAccessCookieInstead(t *testing.T) {
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request
		_, _ = writer.Write([]byte(`{"membership":{"user_id":"user-1","role":"member"}}`))
	}))
	defer server.Close()

	if _, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{AccessCookie: "c"}); err != nil {
		t.Fatalf("Membership() error = %v", err)
	}
	cookie, err := seen.Cookie(AccessCookieName)
	if err != nil || cookie.Value != "c" {
		t.Fatalf("cookie %s = %v, %v, want %q", AccessCookieName, cookie, err, "c")
	}
	if seen.Header.Get("Authorization") != "" {
		t.Fatal("a cookie-authenticated call must not also carry a bearer token")
	}
}

func TestMembershipReadsWhetherTheCallerIsAnAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"membership":{"user_id":"user-1","role":"admin"},"agent":true}`))
	}))
	defer server.Close()

	membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"})
	if err != nil {
		t.Fatalf("Membership() error = %v", err)
	}
	if !membership.Agent || membership.Role != domain.OrgRoleAdmin {
		t.Fatalf("Membership() = %+v", membership)
	}
}

func TestMembershipRefusesAnAnswerWithNoMember(t *testing.T) {
	// Answering forbidden tells the caller nothing an empty membership did
	// not already say.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()

	if _, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Membership() error = %v, want ErrForbidden", err)
	}
}

func TestMembershipRefusesAnAnswerThatIsNotJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("not json"))
	}))
	defer server.Close()

	if _, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"}); err == nil {
		t.Fatal("Membership() error = nil, want a refusal")
	}
}

func TestAuthAnswersMapToReasonsACallerActsOn(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusUnauthorized:        domain.ErrUnauthorized,
		http.StatusForbidden:           domain.ErrForbidden,
		http.StatusNotFound:            domain.ErrForbidden,
		http.StatusTooManyRequests:     domain.ErrRateLimited,
		http.StatusBadGateway:          domain.ErrProviderUnavailable,
		http.StatusUnprocessableEntity: domain.ErrInvalid,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"})
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("Membership() on %d error = %v, want %v", status, err, want)
		}
	}
}

func TestAuthNotAnsweringIsNotTheCallersFault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()

	if _, err := New(address).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"}); !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Membership() error = %v, want ErrProviderUnavailable", err)
	}
}
