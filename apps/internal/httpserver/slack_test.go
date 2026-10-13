package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/slackbot"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// fakeSlack answers what a test sets, and records what it was asked.
type fakeSlack struct {
	err error

	credentials  authclient.Credentials
	organization string
	returnTo     string
	channelIDs   []string
	callback     slackbot.InstallCallback
	completion   slackbot.InstallCompletion
	// linkCompletion is what CompleteLink answers.
	linkCompletion slackbot.LinkCompletion
	inboundID      string
	profileUsers   []string
	channelID      string
	post           slackbot.PostRequest
	update         slackbot.UpdateRequest
	calls          []string
}

func (f *fakeSlack) record(name string) { f.calls = append(f.calls, name) }

func (f *fakeSlack) Status(_ context.Context, credentials authclient.Credentials, organizationID string) (slackbot.StatusView, error) {
	f.record("status")
	f.credentials, f.organization = credentials, organizationID
	return slackbot.StatusView{Status: slackbot.StatusActive}, f.err
}

func (f *fakeSlack) Install(_ context.Context, credentials authclient.Credentials, organizationID, returnTo string) (slackbot.InstallView, error) {
	f.record("install")
	f.credentials, f.organization, f.returnTo = credentials, organizationID, returnTo
	return slackbot.InstallView{Status: slackbot.StatusNotInstalled, AddToSlackURL: "https://slack.com/oauth/v2/authorize?state=s"}, f.err
}

func (f *fakeSlack) Uninstall(_ context.Context, credentials authclient.Credentials, organizationID string) error {
	f.record("uninstall")
	f.credentials, f.organization = credentials, organizationID
	return f.err
}

func (f *fakeSlack) CompleteInstall(_ context.Context, callback slackbot.InstallCallback) (slackbot.InstallCompletion, error) {
	f.record("complete")
	f.callback = callback
	return f.completion, f.err
}

func (f *fakeSlack) LinkStatus(_ context.Context, credentials authclient.Credentials, organizationID string) (slackbot.LinkView, error) {
	f.record("link-status")
	f.credentials, f.organization = credentials, organizationID
	return slackbot.LinkView{Linked: true, TeamID: "T0ACME", SlackUserID: "U0ADA"}, f.err
}

func (f *fakeSlack) StartLink(_ context.Context, credentials authclient.Credentials, organizationID, returnTo string) (slackbot.LinkStart, error) {
	f.record("link-start")
	f.credentials, f.organization, f.returnTo = credentials, organizationID, returnTo
	return slackbot.LinkStart{SignInURL: "https://slack.com/openid/connect/authorize?state=s"}, f.err
}

func (f *fakeSlack) CompleteLink(_ context.Context, credentials authclient.Credentials, callback slackbot.InstallCallback) (slackbot.LinkCompletion, error) {
	f.record("link-complete")
	f.credentials, f.callback = credentials, callback
	return f.linkCompletion, f.err
}

func (f *fakeSlack) Unlink(_ context.Context, credentials authclient.Credentials, organizationID string) error {
	f.record("unlink")
	f.credentials, f.organization = credentials, organizationID
	return f.err
}

func (f *fakeSlack) Channels(_ context.Context, credentials authclient.Credentials, organizationID string) (slackbot.ChannelsView, error) {
	f.record("channels")
	f.credentials, f.organization = credentials, organizationID
	return slackbot.ChannelsView{Channels: []slackbot.AdminChannel{{ChannelID: "G0ALPHA", Name: "alpha", Markable: true}}}, f.err
}

func (f *fakeSlack) SetHighPriority(_ context.Context, credentials authclient.Credentials, organizationID string, channelIDs []string) (slackbot.ChannelsView, error) {
	f.record("set")
	f.credentials, f.organization, f.channelIDs = credentials, organizationID, channelIDs
	return slackbot.ChannelsView{Channels: []slackbot.AdminChannel{}}, f.err
}

func (f *fakeSlack) Inbound(_ context.Context, id string) (slackbot.InboundView, error) {
	f.record("inbound")
	f.inboundID = id
	return slackbot.InboundView{ID: id, OrganizationID: "org_1", Kind: slackbot.InboundMessage, ChannelID: "C0GENERAL", UserID: "U0ASKER"}, f.err
}

func (f *fakeSlack) Profiles(_ context.Context, organizationID string, userIDs []string) (slackbot.ProfilesView, error) {
	f.record("profiles")
	f.organization, f.profileUsers = organizationID, userIDs
	return slackbot.ProfilesView{Profiles: []slackbot.ProfileView{}, Missing: []string{}}, f.err
}

func (f *fakeSlack) Channel(_ context.Context, organizationID, channelID string) (slackbot.ChannelView, error) {
	f.record("channel")
	f.organization, f.channelID = organizationID, channelID
	return slackbot.ChannelView{ChannelID: channelID, IsPrivate: true}, f.err
}

func (f *fakeSlack) Post(_ context.Context, request slackbot.PostRequest) (slackbot.PostResult, error) {
	f.record("post")
	f.post = request
	return slackbot.PostResult{ChannelID: "C0GENERAL", TS: "1780000000.000100"}, f.err
}

func (f *fakeSlack) Update(_ context.Context, request slackbot.UpdateRequest) (slackbot.PostResult, error) {
	f.record("update")
	f.update = request
	return slackbot.PostResult{ChannelID: request.ChannelID, TS: request.TS}, f.err
}

// ingressStub is a hooks alias handler: it answers 200 and says what it was.
func ingressStub(name string, reached *[]string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		*reached = append(*reached, name+" "+request.Method)
		writer.WriteHeader(http.StatusOK)
	})
}

type slackHarness struct {
	server  *Server
	slack   *fakeSlack
	ingress []string
}

var slackServices = fakeServices{claims: map[string]serviceauth.Claims{
	"agents":   {Service: "agents", Purpose: SlackPurpose},
	"operator": {Service: "operator", Purpose: InternalPurpose},
}}

func newSlackHarness(t *testing.T, withServices bool) *slackHarness {
	t.Helper()
	h := &slackHarness{slack: &fakeSlack{}}
	config := Config{
		AllowedOrigins: []string{"https://app.oryxa.test"},
		Slack:          &Slack{Service: h.slack, Events: ingressStub("events", &h.ingress), Interactivity: ingressStub("interactivity", &h.ingress)},
	}
	if withServices {
		config.Services = slackServices
	}
	h.server = New(&fakeService{}, config, nil)
	return h
}

func (h *slackHarness) call(method, target, body string, headers ...string) *httptest.ResponseRecorder {
	return call(h.server, method, target, body, headers...)
}

func TestTheSlackRoutesAreServedOnlyWhereTheSurfaceIsOn(t *testing.T) {
	off := New(&fakeService{}, Config{Services: slackServices}, nil)
	for _, pattern := range off.Patterns() {
		if strings.Contains(pattern, "slack") {
			t.Errorf("a Slack route without the surface: %s", pattern)
		}
	}
	for _, target := range []string{slackbot.EventsPath, slackbot.InteractivityPath} {
		if recorder := call(off, http.MethodPost, target, "{}"); recorder.Code != http.StatusNotFound && recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s without the surface = %d", target, recorder.Code)
		}
	}

	on := newSlackHarness(t, true)
	patterns := strings.Join(on.server.Patterns(), "\n")
	for _, route := range []string{
		"POST /v1/apps/slack/events",
		"POST /v1/apps/slack/interactivity",
		"GET /v1/apps/slack/install/callback",
		"GET /v1/organizations/{organizationID}/apps/slack/install",
		"GET /v1/organizations/{organizationID}/apps/slack/status",
		"DELETE /v1/organizations/{organizationID}/apps/slack/install",
		"GET /v1/organizations/{organizationID}/apps/slack/channels",
		"PUT /v1/organizations/{organizationID}/apps/slack/channels",
		"GET /v1/internal/apps/slack/inbound/{id}",
		"POST /v1/internal/apps/slack/profiles",
		"GET /v1/internal/apps/slack/channels/{id}",
		"POST /v1/internal/apps/slack/post",
		"POST /v1/internal/apps/slack/update",
	} {
		if !strings.Contains(patterns, route+"\n") && !strings.HasSuffix(patterns, route) {
			t.Errorf("no route %s in:\n%s", route, patterns)
		}
	}

	// Without callers allowed to call apps there are no internal routes, the
	// rest is served.
	bare := newSlackHarness(t, false)
	for _, pattern := range bare.server.Patterns() {
		if strings.Contains(pattern, "/internal/apps/slack") {
			t.Errorf("an internal Slack route without callers: %s", pattern)
		}
	}
	if !slices.Contains(bare.server.Patterns(), "POST /v1/apps/slack/events") {
		t.Error("the ingress is not served without callers")
	}
	if recorder := bare.call(http.MethodPost, "/v1/internal/apps/slack/post", `{}`, serviceauth.Header, "agents"); recorder.Code != http.StatusNotFound {
		t.Errorf("an internal route without callers = %d", recorder.Code)
	}
}

func TestTheIngressIsServedAtSlacksTwoURLsForPOSTOnly(t *testing.T) {
	h := newSlackHarness(t, true)
	if recorder := h.call(http.MethodPost, slackbot.EventsPath, "{}"); recorder.Code != http.StatusOK {
		t.Fatalf("events = %d", recorder.Code)
	}
	if recorder := h.call(http.MethodPost, slackbot.InteractivityPath, "payload=x"); recorder.Code != http.StatusOK {
		t.Fatalf("interactivity = %d", recorder.Code)
	}
	if want := []string{"events POST", "interactivity POST"}; !slices.Equal(h.ingress, want) {
		t.Fatalf("reached = %v, want %v", h.ingress, want)
	}
	// Nothing about a delivery is judged here: verification is the alias's.
	if recorder := h.call(http.MethodGet, slackbot.EventsPath, ""); recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("a GET = %d", recorder.Code)
	}
}

// What a service may do with the internal routes is what its token's purpose
// says, and the deployment-endpoint routes and Slack's take different ones.
func TestTheInternalSlackRoutesTakeOnlyAServiceTokenForThePurposeSlack(t *testing.T) {
	h := newSlackHarness(t, true)
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/internal/apps/slack/inbound/asi_abc", ""},
		{http.MethodPost, "/v1/internal/apps/slack/profiles", `{"organization_id":"org_1","user_ids":["U0RAYA"]}`},
		{http.MethodGet, "/v1/internal/apps/slack/channels/G0SECRET?organization_id=org_1", ""},
		{http.MethodPost, "/v1/internal/apps/slack/post", `{"organization_id":"org_1","idempotency_key":"key-0001","target":{"user_id":"U0RAYA"},"template":"status","params":{"state":"held"}}`},
		{http.MethodPost, "/v1/internal/apps/slack/update", `{"organization_id":"org_1","channel_id":"D0RAYA","ts":"1780000000.000100","template":"notice_settled","params":{}}`},
	}
	for _, route := range routes {
		for name, test := range map[string]struct {
			header []string
			status int
		}{
			"no token":          {status: http.StatusUnauthorized},
			"a member's bearer": {header: []string{"Authorization", "Bearer admin"}, status: http.StatusUnauthorized},
			"a forged token":    {header: []string{serviceauth.Header, "forged"}, status: http.StatusUnauthorized},
			"the purpose hooks": {header: []string{serviceauth.Header, "operator"}, status: http.StatusForbidden},
			"a purpose refused": {header: []string{serviceauth.Header, "not-allowed"}, status: http.StatusForbidden},
			"KMS down":          {header: []string{serviceauth.Header, "kms-down"}, status: http.StatusServiceUnavailable},
			"the purpose slack": {header: []string{serviceauth.Header, "agents"}, status: http.StatusOK},
		} {
			before := len(h.slack.calls)
			recorder := h.call(route.method, route.path, route.body, test.header...)
			if recorder.Code != test.status {
				t.Errorf("%s %s with %s = %d, want %d: %s", route.method, route.path, name, recorder.Code, test.status, recorder.Body)
			}
			if reached := len(h.slack.calls) > before; reached != (test.status == http.StatusOK) {
				t.Errorf("%s %s with %s: the service was reached = %v", route.method, route.path, name, reached)
			}
		}
	}
	// And the other way: a token for Slack does not open the deployment endpoint's routes.
	if recorder := h.call(http.MethodGet, "/v1/internal/apps/hooks/notion", "", serviceauth.Header, "agents"); recorder.Code != http.StatusForbidden && recorder.Code != http.StatusNotFound {
		t.Fatalf("a Slack token on a hooks route = %d", recorder.Code)
	}
}

func TestTheInternalRoutesPassWhatTheyWereGivenToTheService(t *testing.T) {
	h := newSlackHarness(t, true)
	token := []string{serviceauth.Header, "agents"}

	recorder := h.call(http.MethodGet, "/v1/internal/apps/slack/inbound/asi_abc", "", token...)
	view := decodeBody(t, recorder)
	if recorder.Code != http.StatusOK || h.slack.inboundID != "asi_abc" || view["id"] != "asi_abc" || view["kind"] != "message" || view["organization_id"] != "org_1" {
		t.Fatalf("inbound = %d %s", recorder.Code, recorder.Body)
	}
	if recorder := h.call(http.MethodPost, "/v1/internal/apps/slack/profiles", `{"organization_id":"org_1","user_ids":["U0RAYA","U0MAYA"]}`, token...); recorder.Code != http.StatusOK ||
		h.slack.organization != "org_1" || !slices.Equal(h.slack.profileUsers, []string{"U0RAYA", "U0MAYA"}) {
		t.Fatalf("profiles = %d %s", recorder.Code, recorder.Body)
	}
	recorder = h.call(http.MethodGet, "/v1/internal/apps/slack/channels/G0SECRET?organization_id=org_9", "", token...)
	if channel := decodeBody(t, recorder); recorder.Code != http.StatusOK || h.slack.channelID != "G0SECRET" || h.slack.organization != "org_9" || channel["is_private"] != true {
		t.Fatalf("channel = %d %s", recorder.Code, recorder.Body)
	}

	body := `{"organization_id":"org_1","idempotency_key":"key-ask-1","target":{"channel_id":"C0GENERAL","thread_ts":"1780000000.000100"},` +
		`"template":"answer","params":{"text":"The launch is on the 14th.","owner_name":"Raya","mode":"summary"}}`
	recorder = h.call(http.MethodPost, "/v1/internal/apps/slack/post", body, token...)
	result := decodeBody(t, recorder)
	if recorder.Code != http.StatusOK || result["channel_id"] != "C0GENERAL" || result["ts"] != "1780000000.000100" {
		t.Fatalf("post = %d %s", recorder.Code, recorder.Body)
	}
	post := h.slack.post
	if post.OrganizationID != "org_1" || post.IdempotencyKey != "key-ask-1" || post.Template != "answer" ||
		post.Target != (slackbot.Target{ChannelID: "C0GENERAL", ThreadTS: "1780000000.000100"}) ||
		!strings.Contains(string(post.Params), `"owner_name":"Raya"`) {
		t.Fatalf("post = %+v", post)
	}
	if recorder := h.call(http.MethodPost, "/v1/internal/apps/slack/update",
		`{"organization_id":"org_1","channel_id":"D0RAYA","ts":"1780000000.000100","template":"notice_settled","params":{"decision":"denied"}}`, token...); recorder.Code != http.StatusOK ||
		h.slack.update.ChannelID != "D0RAYA" || h.slack.update.Template != "notice_settled" {
		t.Fatalf("update = %d %s", recorder.Code, recorder.Body)
	}

	// A body with a field the request does not have is refused before the service is asked.
	before := len(h.slack.calls)
	if recorder := h.call(http.MethodPost, "/v1/internal/apps/slack/post", `{"organization_id":"org_1","blocks":[]}`, token...); recorder.Code != http.StatusBadRequest || errorCode(t, recorder) != "invalid_request" {
		t.Fatalf("an unknown field = %d %s", recorder.Code, recorder.Body)
	}
	if len(h.slack.calls) != before {
		t.Fatal("the service was asked for a request that was refused")
	}
}

func TestTheSurfacesFailuresAreAnsweredWithTheCodesCallersActOn(t *testing.T) {
	h := newSlackHarness(t, true)
	token := []string{serviceauth.Header, "agents"}
	post := func() *httptest.ResponseRecorder {
		return h.call(http.MethodPost, "/v1/internal/apps/slack/post", `{"organization_id":"org_1","idempotency_key":"key-0001","template":"status"}`, token...)
	}

	for name, test := range map[string]struct {
		err    error
		status int
		code   string
		retry  string
	}{
		"Slack rate limiting":       {&slackbot.RateLimitedError{Method: "chat.postMessage", RetryAfter: 7 * time.Second}, http.StatusTooManyRequests, "rate_limited", "7"},
		"the organization's budget": {&slackbot.RateLimitedError{RetryAfter: 400 * time.Millisecond}, http.StatusTooManyRequests, "rate_limited", "1"},
		"a thread never seen":       {slackbot.ErrThreadUnseen, http.StatusForbidden, "forbidden", ""},
		"no install":                {fmt.Errorf("not installed: %w", domain.ErrNotConnected), http.StatusConflict, "not_connected", ""},
		"a key reused":              {fmt.Errorf("busy: %w", domain.ErrConflict), http.StatusConflict, "conflict", ""},
		"params that do not fit":    {fmt.Errorf("status: %w", domain.ErrInvalid), http.StatusBadRequest, "invalid_request", ""},
		"a conversation unreadable": {slackbot.ErrChannelUnreadable, http.StatusUnprocessableEntity, "channel_unreadable", ""},
		"Slack refusing":            {&slackbot.Error{Method: "chat.postMessage", Code: "channel_not_found"}, http.StatusBadGateway, "slack_error", ""},
		"Slack unreachable":         {fmt.Errorf("%w: slack did not answer", domain.ErrUnavailable), http.StatusServiceUnavailable, "unavailable", ""},
		"anything else":             {errors.New("boom xoxb-secret-value"), http.StatusInternalServerError, "internal", ""},
	} {
		h.slack.err = test.err
		recorder := post()
		if recorder.Code != test.status || errorCode(t, recorder) != test.code || recorder.Header().Get("Retry-After") != test.retry {
			t.Errorf("%s = %d %s Retry-After %q, want %d %s %q", name, recorder.Code, recorder.Body, recorder.Header().Get("Retry-After"), test.status, test.code, test.retry)
		}
		// What a failure says is never what went wrong inside.
		if strings.Contains(recorder.Body.String(), "xoxb-secret-value") {
			t.Errorf("%s: an internal error was said: %s", name, recorder.Body)
		}
	}
	h.slack.err = &slackbot.Error{Method: "chat.postMessage", Code: "channel_not_found"}
	if recorder := post(); !strings.Contains(recorder.Body.String(), "channel_not_found") {
		t.Errorf("Slack's own code is not said: %s", recorder.Body)
	}
}

func TestAdminRoutesTakeAMembersSessionAndPassItOn(t *testing.T) {
	h := newSlackHarness(t, true)

	recorder := h.call(http.MethodGet, "/v1/organizations/org_1/apps/slack/install?return_to="+url.QueryEscape("https://app.oryxa.test/settings"), "",
		"Authorization", "Bearer admin")
	view := decodeBody(t, recorder)
	if recorder.Code != http.StatusOK || h.slack.credentials.Authorization != "Bearer admin" || h.slack.organization != "org_1" ||
		h.slack.returnTo != "https://app.oryxa.test/settings" || view["status"] != "not_installed" || view["add_to_slack_url"] == "" {
		t.Fatalf("install = %d %s", recorder.Code, recorder.Body)
	}
	if recorder := h.call(http.MethodGet, "/v1/organizations/org_1/apps/slack/channels", "", "Cookie", authclient.AccessCookieName+"=session"); recorder.Code != http.StatusOK ||
		h.slack.credentials.AccessCookie != "session" {
		t.Fatalf("channels = %d %s", recorder.Code, recorder.Body)
	}
	body := `{"high_priority":["G0ALPHA","G0DELTA"]}`
	if recorder := h.call(http.MethodPut, "/v1/organizations/org_1/apps/slack/channels", body, "Authorization", "Bearer admin"); recorder.Code != http.StatusOK ||
		!slices.Equal(h.slack.channelIDs, []string{"G0ALPHA", "G0DELTA"}) {
		t.Fatalf("set = %d %s", recorder.Code, recorder.Body)
	}
	if recorder := h.call(http.MethodDelete, "/v1/organizations/org_1/apps/slack/install", "", "Authorization", "Bearer admin"); recorder.Code != http.StatusNoContent {
		t.Fatalf("uninstall = %d %s", recorder.Code, recorder.Body)
	}

	// Nobody is let in without a credential, and a cookie only mutates from our origins.
	before := len(h.slack.calls)
	for _, test := range []struct {
		method, path, body string
		headers            []string
		status             int
	}{
		{http.MethodGet, "/v1/organizations/org_1/apps/slack/install", "", nil, http.StatusUnauthorized},
		{http.MethodGet, "/v1/organizations/org_1/apps/slack/channels", "", nil, http.StatusUnauthorized},
		{http.MethodPut, "/v1/organizations/org_1/apps/slack/channels", body, nil, http.StatusUnauthorized},
		{http.MethodDelete, "/v1/organizations/org_1/apps/slack/install", "", nil, http.StatusUnauthorized},
		{http.MethodPut, "/v1/organizations/org_1/apps/slack/channels", body, []string{"Cookie", authclient.AccessCookieName + "=s"}, http.StatusForbidden},
		{http.MethodDelete, "/v1/organizations/org_1/apps/slack/install", "", []string{"Cookie", authclient.AccessCookieName + "=s", "Origin", "https://evil.example"}, http.StatusForbidden},
		{http.MethodPut, "/v1/organizations/org_1/apps/slack/channels", `{"high_priority":["G0A"],"extra":true}`, []string{"Authorization", "Bearer admin"}, http.StatusBadRequest},
		// Leaving the list out is not a request to unmark every channel.
		{http.MethodPut, "/v1/organizations/org_1/apps/slack/channels", `{}`, []string{"Authorization", "Bearer admin"}, http.StatusBadRequest},
		{http.MethodPut, "/v1/organizations/org_1/apps/slack/channels", `{"high_priority":null}`, []string{"Authorization", "Bearer admin"}, http.StatusBadRequest},
	} {
		if recorder := h.call(test.method, test.path, test.body, test.headers...); recorder.Code != test.status {
			t.Errorf("%s %s %v = %d, want %d", test.method, test.path, test.headers, recorder.Code, test.status)
		}
	}
	if len(h.slack.calls) != before {
		t.Fatal("the service was asked on behalf of someone not let in")
	}
	// An empty list is a request to unmark every channel, and is honoured.
	if recorder := h.call(http.MethodPut, "/v1/organizations/org_1/apps/slack/channels", `{"high_priority":[]}`, "Authorization", "Bearer admin"); recorder.Code != http.StatusOK ||
		h.slack.channelIDs == nil || len(h.slack.channelIDs) != 0 {
		t.Fatalf("an empty list = %d %s (channels %v)", recorder.Code, recorder.Body, h.slack.channelIDs)
	}
	// A cookie from one of our own origins may.
	if recorder := h.call(http.MethodDelete, "/v1/organizations/org_1/apps/slack/install", "",
		"Cookie", authclient.AccessCookieName+"=s", "Origin", "https://app.oryxa.test"); recorder.Code != http.StatusNoContent {
		t.Fatalf("a cookie from our origin = %d", recorder.Code)
	}

	// What the service refuses is answered as the API answers it.
	for name, test := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"a member":         {domain.ErrForbidden, http.StatusForbidden, "forbidden"},
		"no such install":  {domain.ErrNotFound, http.StatusNotFound, "not_found"},
		"a page not ours":  {fmt.Errorf("return_to: %w", domain.ErrInvalid), http.StatusBadRequest, "invalid_request"},
		"auth unavailable": {domain.ErrUnavailable, http.StatusServiceUnavailable, "unavailable"},
	} {
		h.slack.err = test.err
		recorder := h.call(http.MethodGet, "/v1/organizations/org_1/apps/slack/install", "", "Authorization", "Bearer admin")
		if recorder.Code != test.status || errorCode(t, recorder) != test.code {
			t.Errorf("%s = %d %s", name, recorder.Code, recorder.Body)
		}
	}
}

func TestTheInstallCallbackSendsTheBrowserBackOrAnswersJSON(t *testing.T) {
	h := newSlackHarness(t, true)

	// Finished, with no page to go back to: JSON.
	h.slack.completion = slackbot.InstallCompletion{OrganizationID: "org_1", TeamID: "T0ACME01", TeamName: "Acme"}
	recorder := h.call(http.MethodGet, "/v1/apps/slack/install/callback?state=s&code=c", "")
	body := decodeBody(t, recorder)
	if recorder.Code != http.StatusOK || body["installed"] != true || body["organization_id"] != "org_1" ||
		h.slack.callback != (slackbot.InstallCallback{State: "s", Code: "c"}) {
		t.Fatalf("callback = %d %s", recorder.Code, recorder.Body)
	}
	if recorder.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("the code and state may follow the browser")
	}

	// Finished, with a page: back to it, saying so, and keeping what its query had.
	h.slack.completion.ReturnTo = "https://app.oryxa.test/settings?tab=slack"
	recorder = h.call(http.MethodGet, "/v1/apps/slack/install/callback?state=s&code=c", "")
	location, _ := url.Parse(recorder.Header().Get("Location"))
	if recorder.Code != http.StatusSeeOther || location.Host != "app.oryxa.test" || location.Query().Get("slack") != "connected" || location.Query().Get("tab") != "slack" {
		t.Fatalf("callback = %d %s", recorder.Code, recorder.Header().Get("Location"))
	}

	// A refusal names its reason where the page can read it.
	h.slack.err = &slackbot.InstallFailure{Reason: slackbot.FailureTeamBound, ReturnTo: "https://app.oryxa.test/settings"}
	recorder = h.call(http.MethodGet, "/v1/apps/slack/install/callback?state=s&code=c", "")
	location, _ = url.Parse(recorder.Header().Get("Location"))
	if recorder.Code != http.StatusSeeOther || location.Query().Get("slack") != "error" || location.Query().Get("slack_error") != "team_bound" {
		t.Fatalf("a refusal = %d %s", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestTheInstallCallbackAnswersRefusalsWithTheAPIsCodes(t *testing.T) {
	h := newSlackHarness(t, true)
	for name, test := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"a workspace bound to another organization": {slackbot.ErrTeamBound, http.StatusConflict, "conflict"},
		"an organization with another workspace":    {slackbot.ErrOrganizationBound, http.StatusConflict, "conflict"},
		"a state used up":                           {fmt.Errorf("used: %w", domain.ErrInvalid), http.StatusBadRequest, "invalid_request"},
		"an install that is not served":             {fmt.Errorf("rotation: %w", domain.ErrNotConnectable), http.StatusUnprocessableEntity, "not_connectable"},
		"Slack unreachable":                         {fmt.Errorf("%w: down", domain.ErrUnavailable), http.StatusServiceUnavailable, "unavailable"},
	} {
		h.slack.err = test.err
		// With no page to go back to, the answer is the error itself.
		if recorder := h.call(http.MethodGet, "/v1/apps/slack/install/callback?state=s&code=c", ""); recorder.Code != test.status || errorCode(t, recorder) != test.code {
			t.Errorf("%s = %d %s", name, recorder.Code, recorder.Body)
		}
	}
	// A refusal that has a page to go to is sent there and not answered as an error.
	h.slack.err = &slackbot.InstallFailure{Reason: slackbot.FailureDenied, ReturnTo: "https://app.oryxa.test/settings"}
	if recorder := h.call(http.MethodGet, "/v1/apps/slack/install/callback?state=s&error=access_denied", ""); recorder.Code != http.StatusSeeOther {
		t.Errorf("a declined install = %d", recorder.Code)
	}
	if h.slack.callback.Error != "access_denied" {
		t.Errorf("callback = %+v", h.slack.callback)
	}
}

func TestTheInstallCallbackIsRateLimitedPerAddress(t *testing.T) {
	h := newSlackHarness(t, true)
	h.slack.err = fmt.Errorf("used: %w", domain.ErrInvalid)
	limited := 0
	for range application.CallbackBudget + 5 {
		request := httptest.NewRequest(http.MethodGet, "/v1/apps/slack/install/callback?state=s&code=c", nil)
		request.RemoteAddr = "203.0.113.9:4000"
		recorder := httptest.NewRecorder()
		h.server.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("a caller that hammers the callback is never limited")
	}
	// Another address is not.
	request := httptest.NewRequest(http.MethodGet, "/v1/apps/slack/install/callback?state=s&code=c", nil)
	request.RemoteAddr = "203.0.113.77:4000"
	recorder := httptest.NewRecorder()
	h.server.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusTooManyRequests {
		t.Fatal("another address was limited")
	}
}
