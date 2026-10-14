package authclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMembershipNamesTheAgent: auth answers an agent token with the agent it
// was minted for, and an older agent token with none, which is the primary.
func TestMembershipNamesTheAgent(t *testing.T) {
	for body, want := range map[string][3]string{
		`{"membership":{"user_id":"user-1","role":"member"},"agent":true,"agent_claims":{"agent":"agt_writer","role":"specialist"}}`: {"true", "agt_writer", "specialist"},
		`{"membership":{"user_id":"user-1","role":"member"},"agent":true}`:                                                           {"true", "", "primary"},
		`{"membership":{"user_id":"user-1","role":"member"}}`:                                                                        {"false", "", ""},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"})
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		got := [3]string{map[bool]string{true: "true", false: "false"}[membership.Agent], membership.AgentID, membership.AgentRole}
		if got != want {
			t.Fatalf("%s: got %v, want %v", body, got, want)
		}
	}
}

// TestMembershipCarriesTheSpecialistsGrant: a specialist's grant reaches
// apps as auth answers it, and one apps cannot read refuses the call rather
// than leaving the specialist ungranted.
func TestMembershipCarriesTheSpecialistsGrant(t *testing.T) {
	for body, wantErr := range map[string]bool{
		`{"membership":{"user_id":"user-1","role":"member"},"agent":true,"agent_claims":{"agent":"agt_writer","role":"specialist","grant":{"apps":["gmail"],"write_tools":["GMAIL_SEND_EMAIL"]}}}`: false,
		`{"membership":{"user_id":"user-1","role":"member"},"agent":true,"agent_claims":{"agent":"agt_writer","role":"specialist","grant":{"apps":"gmail"}}}`:                                      true,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		membership, err := New(server.URL).Membership(context.Background(), "org_1", Credentials{Authorization: "Bearer t"})
		server.Close()
		if wantErr {
			if err == nil {
				t.Fatalf("%s: answered %+v, want an error", body, membership)
			}
			continue
		}
		if err != nil || membership.AgentGrant == nil || membership.AgentGrant.Apps[0] != "gmail" || membership.AgentGrant.WriteTools[0] != "GMAIL_SEND_EMAIL" {
			t.Fatalf("%s: got %+v, %v", body, membership.AgentGrant, err)
		}
	}
}
