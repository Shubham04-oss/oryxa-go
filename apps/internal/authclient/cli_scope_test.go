package authclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/cliscope"
)

func TestCLISetupTokenCannotAuthorizeOrdinaryMemberAccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"organization":{"id":"org_1"},"membership":{"organization_id":"org_1","user_id":"owner-1","role":"owner"},"cli":true}`))
	}))
	defer server.Close()
	client := New(server.URL)
	credentials := Credentials{Authorization: "Bearer setup-token"}
	if _, err := client.Membership(context.Background(), "org_1", credentials); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ordinary member authorization = %v, want forbidden", err)
	}
	// Spoofed paths or HTTP headers do not confer the trusted route context.
	for _, pattern := range []string{
		"POST /v1/organizations/{organizationID}/agent-requests/{requestID}/decision",
		"POST /v1/organizations/{organizationID}/agent-requests/{requestID}/review",
		"GET /v1/organizations/{organizationID}/threads",
		"GET /v1/organizations/{organizationID}/files",
		"POST /v1/organizations/{organizationID}/apps/tools/{tool}/run",
		"POST /v1/organizations/{organizationID}/apps/read",
		"DELETE /v1/organizations/{organizationID}",
	} {
		cliscope.Wrap(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := client.Membership(r.Context(), "org_1", credentials); !errors.Is(err, domain.ErrForbidden) {
				t.Errorf("%s: %v, want forbidden", pattern, err)
			}
		})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/organizations/org_1/teams", nil))
	}
	// Only services involved in setup opt in. File/search/billing clients
	// refuse CLI tokens even if a caller inherits a setup handler context.
	cliscope.Wrap("GET /v1/organizations/{organizationID}/teams", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		member, err := client.Membership(r.Context(), "org_1", credentials)
		if true {
			if err != nil || member.UserID != "owner-1" {
				t.Errorf("setup authorization = %+v, %v", member, err)
			}
		} else if !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("unrelated service setup authorization = %v, want forbidden", err)
		}
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}
