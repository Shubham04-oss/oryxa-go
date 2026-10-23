package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

const organizationBody = `{"organization":{"id":"org_1","name":"Acme","slug":"acme"},"membership":{"organization_id":"org_1","user_id":"user-1","email":"asha@example.com","display_name":"Asha","role":"owner","joined_at":"2026-09-13T12:00:00Z"}}`

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
	if !reflect.DeepEqual(membership, want) {
		t.Fatalf("membership = %+v, want %+v", membership, want)
	}
}

// TestMembershipReadsWhetherTheCallerIsAnAgent: auth answers a token it
// minted for an agent with "agent": true, and a member's own session without
// the field at all. Files must carry the difference through, since an
// admin's agent may not read what only admins may.
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

// TestMembershipReadsASpecialistsFiles: the grant's files ride on the
// token's claims; absent means all (nil), an explicit empty list means none.
// An agent token without a role is a primary's.
func TestMembershipReadsASpecialistsFiles(t *testing.T) {
	prefix := `{"organization":{"id":"org_1","name":"Acme"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"admin"},"agent":true,"agent_claims":{"agent":"agt_a","role":"specialist"`
	for name, tc := range map[string]struct {
		body string
		want []string
		nil_ bool
	}{
		"named":         {prefix + `,"grant":{"apps":["*"],"files":["member","team_a"]}}}`, []string{"member", "team_a"}, false},
		"all":           {prefix + `,"grant":{"files":["*"]}}}`, []string{"*"}, false},
		"none":          {prefix + `,"grant":{"files":[]}}}`, []string{}, false},
		"field missing": {prefix + `,"grant":{"apps":["*"],"corpora":["cor_a"]}}}`, nil, true},
		"null":          {prefix + `,"grant":{"files":null}}}`, nil, true},
		"no grant":      {prefix + `}}`, nil, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(tc.body))
		}))
		membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token-1"})
		server.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := membership.AgentFiles
		if (got == nil) != tc.nil_ || !slices.Equal(got, tc.want) {
			t.Errorf("%s: files = %#v, want %#v", name, got, tc.want)
		}
		if membership.AgentRole != "specialist" {
			t.Errorf("%s: role = %q, want specialist", name, membership.AgentRole)
		}
	}
	for body, want := range map[string]string{
		`{"organization":{"id":"org_1","name":"Acme"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"admin"},"agent":true}`: "primary",
		organizationBody: "",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(body))
		}))
		membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer token-1"})
		server.Close()
		if err != nil || membership.AgentRole != want {
			t.Errorf("role = %q (%v), want %q", membership.AgentRole, err, want)
		}
	}
}
