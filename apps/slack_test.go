package apps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// signinOn is what Oryxa's own sign-in takes, with no toolkit switched on:
// the vault key the Slack surface seals its bot tokens with.
func signinOn() map[string]string {
	values := required()
	values["APPS_ALLOWED_ORIGINS"] = "https://app.oryxa.test"
	values["APPS_SIGNIN_KMS_KEY_ID"] = "alias/oryxa-apps-signin"
	values["APPS_SIGNIN_REDIRECT_URI"] = "https://app.oryxa.test/v1/apps/oauth/callback"
	values["APPS_SERVICE_CALLERS"] = `[{"service":"agents","key_id":"alias/oryxa-agents","purposes":["slack"]}]`
	return values
}

func slackSurfaceOn() map[string]string {
	values := signinOn()
	values["APPS_SLACK_SURFACE"] = "true"
	values["APPS_SLACK_APP"] = `{"client_id":"1234567890.0987654321","client_secret":"client-secret-0123456789abcdef","signing_secret":"signing-secret-0123456789abcdef"}`
	return values
}

var slackRoutes = []string{
	"POST /v1/apps/slack/events",
	"POST /v1/apps/slack/interactivity",
	"GET /v1/apps/slack/install/callback",
	"GET /v1/organizations/{organizationID}/apps/slack/install",
	"GET /v1/organizations/{organizationID}/apps/slack/status",
	"DELETE /v1/organizations/{organizationID}/apps/slack/install",
	"GET /v1/apps/slack/link/callback",
	"GET /v1/organizations/{organizationID}/apps/slack/link",
	"POST /v1/organizations/{organizationID}/apps/slack/link",
	"DELETE /v1/organizations/{organizationID}/apps/slack/link",
	"GET /v1/organizations/{organizationID}/apps/slack/channels",
	"PUT /v1/organizations/{organizationID}/apps/slack/channels",
	"GET /v1/internal/apps/slack/inbound/{id}",
	"POST /v1/internal/apps/slack/profiles",
	"GET /v1/internal/apps/slack/channels/{id}",
	"POST /v1/internal/apps/slack/post",
	"POST /v1/internal/apps/slack/update",
}

// With the surface off apps is exactly what it was: no Slack route, no
// Slack worker, and nothing about Slack read from the environment. On, the
// routes and the one worker that forgets what people typed are added, and
// nothing else changes.
func TestTheSlackSurfaceAddsItsRoutesAndItsWorkerOnlyWhenSwitchedOn(t *testing.T) {
	baseline, err := Mount(context.Background(), platform(signinOn()))
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range baseline.Routes {
		if strings.Contains(route, "slack") {
			t.Errorf("a Slack route with the surface off: %s", route)
		}
	}

	// Off, whatever Slack's variables say is not looked at.
	off := signinOn()
	off["APPS_SLACK_SURFACE"] = "false"
	off["APPS_SLACK_APP"] = "not even JSON"
	off["APPS_SLACK_API_BASE_URL"] = "https://slack.com/api"
	stillOff, err := Mount(context.Background(), platform(off))
	if err != nil {
		t.Fatalf("Mount() with the surface off and Slack's variables set = %v", err)
	}
	if !slices.Equal(stillOff.Routes, baseline.Routes) || len(stillOff.Workers) != len(baseline.Workers) {
		t.Fatal("the surface off changed what is mounted")
	}

	on, err := Mount(context.Background(), platform(slackSurfaceOn()))
	if err != nil {
		t.Fatal(err)
	}
	var added []string
	for _, route := range on.Routes {
		if !slices.Contains(baseline.Routes, route) {
			added = append(added, route)
		}
	}
	slices.Sort(added)
	want := slices.Clone(slackRoutes)
	slices.Sort(want)
	if !slices.Equal(added, want) {
		t.Fatalf("routes added:\n%v\nwant\n%v", added, want)
	}
	for _, route := range baseline.Routes {
		if !slices.Contains(on.Routes, route) {
			t.Errorf("the surface took %s away", route)
		}
	}
	if len(on.Workers) != len(baseline.Workers)+1 {
		t.Fatalf("workers = %d, want the baseline's %d and the surface's upkeep", len(on.Workers), len(baseline.Workers))
	}
}

// The routes that are mounted are the Slack ones, and the first thing each asks
// for is who is calling.
func TestTheMountedSlackRoutesAskWhoIsCalling(t *testing.T) {
	service, err := Mount(context.Background(), platform(slackSurfaceOn()))
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/v1/organizations/org_1/apps/slack/install", http.StatusUnauthorized},
		{http.MethodDelete, "/v1/organizations/org_1/apps/slack/install", http.StatusUnauthorized},
		{http.MethodGet, "/v1/organizations/org_1/apps/slack/channels", http.StatusUnauthorized},
		{http.MethodPut, "/v1/organizations/org_1/apps/slack/channels", http.StatusUnauthorized},
		{http.MethodGet, "/v1/internal/apps/slack/inbound/asi_1", http.StatusUnauthorized},
		{http.MethodPost, "/v1/internal/apps/slack/post", http.StatusUnauthorized},
		{http.MethodPost, "/v1/internal/apps/slack/update", http.StatusUnauthorized},
		{http.MethodPost, "/v1/internal/apps/slack/profiles", http.StatusUnauthorized},
		{http.MethodGet, "/v1/internal/apps/slack/channels/G0SECRET", http.StatusUnauthorized},
	} {
		request := httptest.NewRequest(probe.method, probe.path, strings.NewReader("{}"))
		recorder := httptest.NewRecorder()
		service.Handler.ServeHTTP(recorder, request)
		if recorder.Code != probe.status {
			t.Errorf("%s %s = %d, want %d: %s", probe.method, probe.path, recorder.Code, probe.status, recorder.Body)
		}
	}
	// Without the surface the same paths are not there.
	baseline, err := Mount(context.Background(), platform(signinOn()))
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	baseline.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/organizations/org_1/apps/slack/install", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("a Slack route without the surface = %d", recorder.Code)
	}
}

func TestTheSlackSurfaceRefusesToMountWithoutWhatItNeeds(t *testing.T) {
	for name, test := range map[string]struct {
		change func(map[string]string)
		names  string
	}{
		"no app": {func(values map[string]string) { delete(values, "APPS_SLACK_APP") }, "APPS_SLACK_APP"},
		"no vault key": {func(values map[string]string) {
			delete(values, "APPS_SIGNIN_KMS_KEY_ID")
			delete(values, "APPS_SIGNIN_REDIRECT_URI")
		}, "APPS_SIGNIN_KMS_KEY_ID"},
		"no callers": {func(values map[string]string) { delete(values, "APPS_SERVICE_CALLERS") }, "APPS_SERVICE_CALLERS"},
	} {
		values := slackSurfaceOn()
		test.change(values)
		if _, err := Mount(context.Background(), platform(values)); err == nil || !strings.Contains(err.Error(), test.names) {
			t.Errorf("%s: Mount() = %v, want it to name %s", name, err, test.names)
		}
	}
}
