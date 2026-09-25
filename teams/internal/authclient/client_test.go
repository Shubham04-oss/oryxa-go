package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

const organizationBody = `{"organization":{"id":"org_1","name":"Acme","slug":"acme"},"membership":{"organization_id":"org_1","user_id":"user-1","email":"asha@example.com","display_name":"Asha","role":"owner","joined_at":"2026-09-13T12:00:00Z"}}`

const membersBody = `{"members":[{"organization_id":"org_1","user_id":"user-2","email":"ben@example.com","display_name":"Ben","role":"member","joined_at":"2026-09-13T12:00:00Z"}]}`

func TestMembershipForwardsCredentials(t *testing.T) {
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
	want := domain.OrgMembership{OrganizationID: "org_1", OrganizationName: "Acme", UserID: "user-1", Email: "asha@example.com", DisplayName: "Asha", Role: domain.OrgRoleOwner}
	if membership != want {
		t.Fatalf("membership = %+v, want %+v", membership, want)
	}
}

// TestMembershipReadsWhetherTheCallerIsAnAgent: auth answers a token it
// minted for an agent with "agent": true, and a member's own session without
// the field at all. Teams must carry the difference through, since an
// admin's agent may not manage or see what only admins may.
func TestMembershipReadsWhetherTheCallerIsAnAgent(t *testing.T) {
	agentBody := `{"organization":{"id":"org_1","name":"Acme"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"admin"},"agent":true}`
	for body, wantAgent := range map[string]bool{agentBody: true, organizationBody: false} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(body))
		}))
		membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token-1"})
		server.Close()
		if err != nil {
			t.Fatalf("Membership() error = %v", err)
		}
		if membership.Agent != wantAgent {
			t.Fatalf("Agent = %v, want %v for %s", membership.Agent, wantAgent, body)
		}
	}
}

func TestMembersListsOrganizationMembers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/organizations/org_1/members" {
			t.Errorf("path = %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(membersBody))
	}))
	defer server.Close()

	members, err := New(server.URL).Members(context.Background(), "org_1", Credentials{AccessCookie: "cookie-1"})
	if err != nil {
		t.Fatalf("Members() error = %v", err)
	}
	if len(members) != 1 || members[0].UserID != "user-2" || members[0].Email != "ben@example.com" {
		t.Fatalf("members = %+v", members)
	}
}

func TestMembershipMapsAuthRefusals(t *testing.T) {
	tests := map[int]error{
		http.StatusUnauthorized: domain.ErrUnauthorized,
		http.StatusForbidden:    domain.ErrForbidden,
		http.StatusNotFound:     domain.ErrNotFound,
	}
	for status, want := range tests {
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

func TestMembershipWithoutCredentials(t *testing.T) {
	if _, err := New("http://auth.invalid").Membership(context.Background(), "org_1", Credentials{}); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Membership() error = %v, want ErrUnauthorized", err)
	}
}
