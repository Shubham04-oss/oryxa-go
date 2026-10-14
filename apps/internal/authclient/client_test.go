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
