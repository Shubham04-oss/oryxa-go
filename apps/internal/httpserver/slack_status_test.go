package httpserver

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

func TestHomeSlackStatusForwardsMemberCredentialsAndReturnsOnlyMetadata(t *testing.T) {
	h := newSlackHarness(t, true)
	response := h.call(http.MethodGet, "/v1/organizations/org_1/apps/slack/status", "", "Cookie", "oryxa_access=member")
	body := decodeBody(t, response)
	if response.Code != http.StatusOK || len(body) != 2 || body["status"] != "active" || body["can_manage"] != false {
		t.Fatalf("status=%d %s", response.Code, response.Body)
	}
	if !slices.Equal(h.slack.calls, []string{"status"}) || h.slack.organization != "org_1" || h.slack.credentials.AccessCookie != "member" || h.slack.credentials.Authorization != "" {
		t.Fatalf("calls=%v org=%s credentials=%+v", h.slack.calls, h.slack.organization, h.slack.credentials)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("member Slack status was cacheable")
	}
	if response := h.call(http.MethodGet, "/v1/organizations/org_1/apps/slack/status", ""); response.Code != http.StatusUnauthorized || len(h.slack.calls) != 1 {
		t.Fatalf("anonymous status=%d calls=%v", response.Code, h.slack.calls)
	}
}

func TestHomeSlackStatusDoesNotTurnFailedReadsIntoNoInstall(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{domain.ErrForbidden, http.StatusForbidden},
		{domain.ErrUnavailable, http.StatusServiceUnavailable},
		{errors.New("database unavailable"), http.StatusInternalServerError},
	} {
		h := newSlackHarness(t, true)
		h.slack.err = test.err
		response := h.call(http.MethodGet, "/v1/organizations/org_1/apps/slack/status", "", "Authorization", "Bearer member")
		if response.Code != test.status || decodeBody(t, response)["status"] != nil {
			t.Fatalf("%v=%d %s", test.err, response.Code, response.Body)
		}
	}
}
