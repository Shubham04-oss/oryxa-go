package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/authclient"
)

func TestCLISetupTokenCannotReadThreadsOrMakeOwnerDecisions(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"owner-1","role":"owner"},"cli":true}`))
	}))
	defer auth.Close()
	interactions := &fakeInteractions{}
	threads := &fakeThreads{}
	server := New(authclient.New(auth.URL), threads, Config{Interactions: interactions, Teamspaces: &fakeTeamspaces{}}, nil)
	for _, route := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/v1/organizations/org_1/threads", http.StatusForbidden},
		{http.MethodPost, "/v1/organizations/org_1/agent-requests/irq_1/decision", http.StatusForbidden},
		{http.MethodPost, "/v1/organizations/org_1/agent-requests/irq_1/review", http.StatusForbidden},
		{http.MethodGet, "/v1/organizations/org_1/teamspaces/space_1/messages", http.StatusForbidden},
		{http.MethodGet, "/v1/organizations/org_1/organization-agent", http.StatusOK},
	} {
		request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer setup-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != route.want {
			t.Errorf("%s %s = %d %s, want %d", route.method, route.path, response.Code, response.Body, route.want)
		}
	}
	if len(threads.members) != 0 || interactions.ownerDecisions != 0 || interactions.reviews != 0 {
		t.Fatal("CLI reached member data or owner decision code")
	}
}
