package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

const organizationBody = `{"organization":{"id":"org_1","name":"Acme","slug":"acme"},"membership":{"organization_id":"org_1","user_id":"user-1","email":"asha@example.com","display_name":"Asha","role":"owner","joined_at":"2026-09-13T12:00:00Z"}}`

func TestMembershipForwardsBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/organizations/org_1" || request.Header.Get("Authorization") != "Bearer token-1" {
			t.Errorf("unexpected auth request %s %s %v", request.Method, request.URL.Path, request.Header)
		}
		_, _ = writer.Write([]byte(organizationBody))
	}))
	defer server.Close()

	membership, err := New(server.URL+"/").Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token-1"})
	if err != nil {
		t.Fatalf("Membership() error = %v", err)
	}
	if want := (domain.OrgMembership{OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleOwner}); membership != want {
		t.Fatalf("membership = %+v, want %+v", membership, want)
	}
}

func TestMembershipForwardsAccessCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie(AccessCookieName)
		if err != nil || cookie.Value != "cookie-1" || request.Header.Get("Authorization") != "" {
			t.Errorf("unexpected credentials %v %v", request.Header, err)
		}
		_, _ = writer.Write([]byte(organizationBody))
	}))
	defer server.Close()
	if _, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{AccessCookie: "cookie-1"}); err != nil {
		t.Fatalf("Membership() error = %v", err)
	}
}

func TestMembershipMapsAuthRefusals(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusUnauthorized: domain.ErrUnauthorized,
		http.StatusForbidden:    domain.ErrForbidden,
		http.StatusNotFound:     domain.ErrNotFound,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
		}))
		_, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token"})
		server.Close()
		if !errors.Is(err, want) {
			t.Fatalf("status %d: error = %v, want %v", status, err, want)
		}
	}
}

func TestMembershipRejectsAnswersForAnotherOrganization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(organizationBody))
	}))
	defer server.Close()
	if _, err := New(server.URL).Membership(context.Background(), "org_2", Credentials{Authorization: "Bearer token"}); err == nil {
		t.Fatal("Membership() accepted an answer for another organization")
	}
}

func TestMembershipWithoutCredentials(t *testing.T) {
	if _, err := New("http://auth.invalid").Membership(context.Background(), "org_1", Credentials{}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Membership() error = %v, want ErrUnauthorized", err)
	}
}

func TestCredentialsAccessToken(t *testing.T) {
	for _, test := range []struct {
		name        string
		credentials Credentials
		want        string
	}{
		{"bearer", Credentials{Authorization: "Bearer abc"}, "abc"},
		{"lowercase bearer", Credentials{Authorization: "bearer abc"}, "abc"},
		{"basic is not a bearer token", Credentials{Authorization: "Basic xyz"}, ""},
		{"cookie only", Credentials{AccessCookie: "cookie-1"}, "cookie-1"},
		{"authorization is preferred over a cookie", Credentials{Authorization: "Bearer abc", AccessCookie: "cookie-1"}, "abc"},
		{"a non-bearer authorization is preferred over a cookie too", Credentials{Authorization: "Basic xyz", AccessCookie: "cookie-1"}, ""},
	} {
		if got := test.credentials.AccessToken(); got != test.want {
			t.Errorf("%s: Credentials{%+v}.AccessToken() = %q, want %q", test.name, test.credentials, got, test.want)
		}
	}
}

func TestMembershipSaysWhenTheCallerIsAnAgent(t *testing.T) {
	for _, test := range []struct {
		name  string
		agent string
		want  bool
	}{
		{name: "an agent token", agent: `,"agent":true`, want: true},
		{name: "the member's own token", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"member"}` + test.agent + `}`))
			}))
			defer server.Close()
			membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token"})
			if err != nil || membership.Agent != test.want {
				t.Fatalf("Membership() = %+v, %v, want Agent %v", membership, err, test.want)
			}
		})
	}
}
