package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
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

func TestMembershipDecodesAgent(t *testing.T) {
	for name, test := range map[string]struct {
		body      string
		wantAgent bool
	}{
		"agent token": {
			`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"member"},"agent":true}`,
			true,
		},
		"member's own token, agent absent": {
			`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"member"}}`,
			false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token-1"})
			if err != nil {
				t.Fatalf("Membership() error = %v", err)
			}
			if membership.Agent != test.wantAgent {
				t.Fatalf("membership.Agent = %v, want %v", membership.Agent, test.wantAgent)
			}
		})
	}
}

func TestMembershipWithoutCredentials(t *testing.T) {
	if _, err := New("http://auth.invalid").Membership(context.Background(), "org_1", Credentials{}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Membership() error = %v, want ErrUnauthorized", err)
	}
}
