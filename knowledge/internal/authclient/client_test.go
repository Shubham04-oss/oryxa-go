package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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
		_, _ = writer.Write([]byte(`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"member"}}`))
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
		_, _ = writer.Write([]byte(`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"member"}}`))
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
		_, _ = writer.Write([]byte(`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"admin"},"agent":true}`))
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

func TestMembershipRefusesAnAnswerForAnotherOrganization(t *testing.T) {
	for _, answer := range []string{
		`{"organization":{"id":"org_2"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"admin"}}`,
		`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_2","user_id":"user-1","role":"admin"}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(answer))
		}))
		_, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"})
		server.Close()
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("Membership() for a foreign organization = %v, want ErrForbidden", err)
		}
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

// TestMembershipReadsASpecialistsKnowledge: the grant's knowledge sources
// ride on the token's claims; absent means all (nil), an explicit empty list
// means none. An agent token without a role is a primary's.
func TestMembershipReadsASpecialistsKnowledge(t *testing.T) {
	prefix := `{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"admin"},"agent":true,"agent_claims":{"agent":"agt_a","role":"specialist"`
	for name, tc := range map[string]struct {
		body string
		want []string
		nil_ bool
	}{
		"named":         {prefix + `,"grant":{"apps":["*"],"knowledge":["src_a","src_b"]}}}`, []string{"src_a", "src_b"}, false},
		"all":           {prefix + `,"grant":{"knowledge":["*"]}}}`, []string{"*"}, false},
		"none":          {prefix + `,"grant":{"knowledge":[]}}}`, []string{}, false},
		"field missing": {prefix + `,"grant":{"apps":["*"],"files":["member"]}}}`, nil, true},
		"null":          {prefix + `,"grant":{"knowledge":null}}}`, nil, true},
		"no grant":      {prefix + `}}`, nil, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(tc.body))
		}))
		membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"})
		server.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := membership.AgentKnowledge
		if (got == nil) != tc.nil_ || !slices.Equal(got, tc.want) {
			t.Errorf("%s: knowledge = %#v, want %#v", name, got, tc.want)
		}
		if !membership.Agent || membership.AgentID != "agt_a" || membership.AgentRole != "specialist" {
			t.Errorf("%s: agent %v %q, role %q, want specialist agt_a", name, membership.Agent, membership.AgentID, membership.AgentRole)
		}
	}
	for body, want := range map[string]string{
		`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"admin"},"agent":true}`: "primary",
		`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"user-1","role":"admin"}}`:              "",
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(body))
		}))
		membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"})
		server.Close()
		if err != nil || membership.AgentRole != want {
			t.Errorf("role = %q (%v), want %q", membership.AgentRole, err, want)
		}
	}
}
