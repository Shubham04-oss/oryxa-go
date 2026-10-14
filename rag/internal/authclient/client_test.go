package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

const organizationBody = `{"organization":{"id":"org_1","name":"Acme"},"membership":{"organization_id":"org_1","user_id":"user-1","email":"asha@example.com","display_name":"Asha","role":"owner"}}`

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
// the field at all. Rag must carry the difference through, since an agent
// acting for an admin may not read what only admins may.
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

// TestMembershipNamesTheAgent: auth answers an agent token with the agent it
// was minted for; an older agent token names none, and is the primary.
func TestMembershipNamesTheAgent(t *testing.T) {
	named := `{"organization":{"id":"org_1","name":"Acme"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"admin"},"agent":true,"agent_claims":{"agent":"agt_writer","role":"specialist"}}`
	legacy := `{"organization":{"id":"org_1","name":"Acme"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"admin"},"agent":true}`
	for body, want := range map[string][2]string{named: {"agt_writer", "specialist"}, legacy: {"", "primary"}, organizationBody: {"", ""}} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(body))
		}))
		membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token-1"})
		server.Close()
		if err != nil {
			t.Fatalf("Membership() error = %v", err)
		}
		if got := [2]string{membership.AgentID, membership.AgentRole}; got != want {
			t.Fatalf("agent = %v, want %v for %s", got, want, body)
		}
	}
}

func TestMembershipFallsBackToTheAccessCookie(t *testing.T) {
	// A bearer token is not sent by a browser on its own, so the cookie is
	// what a browser session actually carries.
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request
		_, _ = writer.Write([]byte(organizationBody))
	}))
	defer server.Close()

	if _, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{AccessCookie: "session-1"}); err != nil {
		t.Fatalf("Membership() error = %v", err)
	}
	if seen.Header.Get("Authorization") != "" {
		t.Fatalf("Authorization = %q, want none when only a cookie was given", seen.Header.Get("Authorization"))
	}
	cookie, err := seen.Cookie(AccessCookieName)
	if err != nil || cookie.Value != "session-1" {
		t.Fatalf("cookie = %v, %v, want session-1", cookie, err)
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

func TestMembershipRejectsAMismatchedOrEmptyAnswer(t *testing.T) {
	tests := map[string]string{
		"a different organization than was asked about": `{"organization":{"id":"org_2","name":"Other"},"membership":{"organization_id":"org_2","user_id":"user-1"}}`,
		"a membership for a different organization":     `{"organization":{"id":"org_1","name":"Acme"},"membership":{"organization_id":"org_2","user_id":"user-1"}}`,
		"no member at all": `{"organization":{"id":"org_1","name":"Acme"},"membership":{"organization_id":"org_1","user_id":""}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(body))
			}))
			defer server.Close()
			if _, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token"}); err == nil {
				t.Fatal("Membership() error = nil, want a refusal")
			}
		})
	}
}
