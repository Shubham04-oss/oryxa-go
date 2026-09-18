package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

type fakeDirectory struct {
	membership authclient.Membership
	err        error
	seen       []authclient.Credentials
}

func (directory *fakeDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (authclient.Membership, error) {
	directory.seen = append(directory.seen, credentials)
	if directory.err != nil {
		return authclient.Membership{}, directory.err
	}
	membership := directory.membership
	membership.OrganizationID = organizationID
	return membership, nil
}

type fakeThreads struct {
	members  []threads.Member
	inputs   []threads.RunInput
	detail   threads.Detail
	err      error
	startErr error
	run      *fakeRun
}

func (service *fakeThreads) Create(_ context.Context, member threads.Member) (threads.Thread, error) {
	service.members = append(service.members, member)
	return threads.Thread{ID: "thread_1", UpdatedAt: time.Unix(0, 0).UTC()}, service.err
}

func (service *fakeThreads) List(_ context.Context, member threads.Member) ([]threads.Thread, error) {
	service.members = append(service.members, member)
	return []threads.Thread{{ID: "thread_1", Title: "Hello"}}, service.err
}

func (service *fakeThreads) Get(_ context.Context, member threads.Member, threadID string) (threads.Detail, error) {
	service.members = append(service.members, member)
	if threadID != "thread_1" {
		return threads.Detail{}, threads.ErrNotFound
	}
	return service.detail, service.err
}

func (service *fakeThreads) Delete(_ context.Context, member threads.Member, threadID string) error {
	service.members = append(service.members, member)
	if threadID != "thread_1" {
		return threads.ErrNotFound
	}
	return service.err
}

func (service *fakeThreads) StartRun(_ context.Context, member threads.Member, _ string, input threads.RunInput) (Run, error) {
	service.members = append(service.members, member)
	service.inputs = append(service.inputs, input)
	if service.startErr != nil {
		return nil, service.startErr
	}
	return service.run, nil
}

func (service *fakeThreads) Artifact(_ context.Context, member threads.Member, threadID, name string, version int64) (threads.File, error) {
	service.members = append(service.members, member)
	switch {
	case threadID != "thread_1":
		return threads.File{}, threads.ErrNotFound
	case name == "screenshot.png":
		return threads.File{Name: name, MIMEType: "image/png", Data: []byte("png-bytes")}, nil
	case name == "report.html" && version == 2:
		return threads.File{Name: name, MIMEType: "text/html", Data: []byte("<script>alert(1)</script>")}, nil
	case name == "user:notes/plan.txt":
		return threads.File{Name: name, MIMEType: "text/plain; charset=utf-8", Data: []byte("plan")}, nil
	}
	return threads.File{}, threads.ErrNotFound
}

type fakeRun struct {
	items     []threads.Item
	executed  bool
	discarded string
}

func (run *fakeRun) ID() string { return "run_1" }

func (run *fakeRun) Execute(_ context.Context, emit func(threads.Item) error) {
	run.executed = true
	for _, item := range run.items {
		if emit(item) != nil {
			return
		}
	}
}

func (run *fakeRun) Discard(_ context.Context, reason string) { run.discarded = reason }

func newTestServer(directory *fakeDirectory, service *fakeThreads) *Server {
	return New(directory, service, Config{AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute}, nil)
}

func member() *fakeDirectory {
	return &fakeDirectory{membership: authclient.Membership{UserID: "user_1", Role: "member"}}
}

func serve(server *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, value := range headers {
		if name == "Cookie" {
			request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: value})
			continue
		}
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func errorCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", recorder.Body.String(), err)
	}
	return body.Error.Code
}

var bearer = map[string]string{"Authorization": "Bearer member-token"}

func TestPatternsNameEveryRoute(t *testing.T) {
	server := newTestServer(member(), &fakeThreads{})
	patterns := server.Patterns()
	if len(patterns) != 7 {
		t.Fatalf("patterns = %v", patterns)
	}
	wildcards := regexp.MustCompile(`\{[^}]+\}`)
	for _, pattern := range patterns {
		method, path, _ := strings.Cut(pattern, " ")
		request := httptest.NewRequest(method, wildcards.ReplaceAllString(path, "x"), nil)
		if _, matched := server.mux.Handler(request); matched != pattern {
			t.Errorf("%s routes to %q", pattern, matched)
		}
	}
}

func TestHealthz(t *testing.T) {
	response := serve(newTestServer(member(), &fakeThreads{}), http.MethodGet, "/healthz", "", nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestEveryThreadRouteWantsAMember(t *testing.T) {
	server := newTestServer(member(), &fakeThreads{})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/organizations/org_1/threads"},
		{http.MethodPost, "/v1/organizations/org_1/threads"},
		{http.MethodGet, "/v1/organizations/org_1/threads/thread_1"},
		{http.MethodDelete, "/v1/organizations/org_1/threads/thread_1"},
		{http.MethodPost, "/v1/organizations/org_1/threads/thread_1/runs"},
	} {
		response := serve(server, route.method, route.path, "", nil)
		if response.Code != http.StatusUnauthorized || errorCode(t, response) != "unauthorized" {
			t.Errorf("%s %s = %d %s, want 401", route.method, route.path, response.Code, response.Body.String())
		}
	}
}

func TestACookieChangesNothingFromAnotherSite(t *testing.T) {
	service := &fakeThreads{}
	server := newTestServer(member(), service)
	refused := serve(server, http.MethodPost, "/v1/organizations/org_1/threads", "", map[string]string{
		"Cookie": "cookie-token", "Origin": "https://evil.test",
	})
	if refused.Code != http.StatusForbidden {
		t.Fatalf("cross-site create = %d, want 403", refused.Code)
	}
	allowed := serve(server, http.MethodPost, "/v1/organizations/org_1/threads", "", map[string]string{
		"Cookie": "cookie-token", "Origin": "https://app.oryxa.test",
	})
	if allowed.Code != http.StatusCreated {
		t.Fatalf("same-site create = %d %s, want 201", allowed.Code, allowed.Body.String())
	}
	// Reading with a cookie needs no origin.
	if read := serve(server, http.MethodGet, "/v1/organizations/org_1/threads", "", map[string]string{"Cookie": "cookie-token"}); read.Code != http.StatusOK {
		t.Fatalf("cookie read = %d, want 200", read.Code)
	}
	if len(service.members) != 2 || service.members[0].AccessToken != "cookie-token" {
		t.Fatalf("members = %+v, want the cookie's token carried as the member's own", service.members)
	}
}

func TestAnAgentCannotHoldAMembersConversation(t *testing.T) {
	directory := member()
	directory.membership.Agent = true
	response := serve(newTestServer(directory, &fakeThreads{}), http.MethodGet, "/v1/organizations/org_1/threads", "", bearer)
	if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
		t.Fatalf("agent = %d %s, want 403", response.Code, response.Body.String())
	}
}

func TestAuthsAnswersBecomeErrorCodes(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{authclient.ErrUnauthorized, http.StatusUnauthorized, "unauthorized"},
		{authclient.ErrForbidden, http.StatusForbidden, "forbidden"},
		{authclient.ErrRateLimited, http.StatusTooManyRequests, "rate_limited"},
		{authclient.ErrUnavailable, http.StatusServiceUnavailable, "unavailable"},
	}
	for _, testCase := range cases {
		response := serve(newTestServer(&fakeDirectory{err: testCase.err}, &fakeThreads{}), http.MethodGet, "/v1/organizations/org_1/threads", "", bearer)
		if response.Code != testCase.status || errorCode(t, response) != testCase.code {
			t.Errorf("%v = %d %s, want %d %s", testCase.err, response.Code, response.Body.String(), testCase.status, testCase.code)
		}
	}
}

func TestAnUnavailableServiceSaysWhy(t *testing.T) {
	server := New(nil, nil, Config{Unavailable: "GOOGLE_CLOUD_PROJECT is not set"}, nil)
	response := serve(server, http.MethodGet, "/v1/organizations/org_1/threads", "", bearer)
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "agents_unavailable" ||
		!strings.Contains(response.Body.String(), "GOOGLE_CLOUD_PROJECT") {
		t.Fatalf("unavailable = %d %s", response.Code, response.Body.String())
	}
	if health := serve(server, http.MethodGet, "/healthz", "", nil); health.Code != http.StatusNoContent {
		t.Fatalf("health = %d, want it still to pass", health.Code)
	}
}

func TestThreadsAreCreatedListedReadAndDeleted(t *testing.T) {
	service := &fakeThreads{detail: threads.Detail{
		Thread: threads.Thread{ID: "thread_1"},
		Items:  []threads.Item{{Type: threads.ItemMessage, Author: threads.AuthorMember, Text: "hi"}},
	}}
	server := newTestServer(member(), service)

	created := serve(server, http.MethodPost, "/v1/organizations/org_1/threads", `{}`, bearer)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":"thread_1"`) {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	if refused := serve(server, http.MethodPost, "/v1/organizations/org_1/threads", `{"title":"x"}`, bearer); refused.Code != http.StatusBadRequest {
		t.Fatalf("create with an unknown field = %d, want 400", refused.Code)
	}
	listed := serve(server, http.MethodGet, "/v1/organizations/org_1/threads", "", bearer)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"threads":[{"id":"thread_1","title":"Hello"`) {
		t.Fatalf("list = %d %s", listed.Code, listed.Body.String())
	}
	got := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1", "", bearer)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"items":[{"type":"message","author":"member","text":"hi"}]`) {
		t.Fatalf("get = %d %s", got.Code, got.Body.String())
	}
	if missing := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/other", "", bearer); missing.Code != http.StatusNotFound {
		t.Fatalf("get another = %d, want 404", missing.Code)
	}
	if deleted := serve(server, http.MethodDelete, "/v1/organizations/org_1/threads/thread_1", "", bearer); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", deleted.Code)
	}
	for _, seen := range service.members {
		if seen.UserID != "user_1" || seen.OrganizationID != "org_1" || seen.AccessToken != "member-token" {
			t.Fatalf("member = %+v", seen)
		}
	}
}

func TestARunIsRefusedBeforeItsStreamOpens(t *testing.T) {
	cases := []struct {
		body   string
		err    error
		status int
		code   string
	}{
		{`{"message":"hi","extra":1}`, nil, http.StatusBadRequest, "invalid_request"},
		{`{"confirmations":[{"id":"c1"}]}`, nil, http.StatusBadRequest, "invalid_request"},
		{`{"message":"hi"}`, threads.ErrRunInProgress, http.StatusConflict, "run_in_progress"},
		{`{"message":"hi"}`, threads.ErrConfirmationPending, http.StatusConflict, "confirmation_pending"},
		{`{"message":"hi"}`, threads.ErrNotFound, http.StatusNotFound, "not_found"},
		{`{"message":"hi"}`, fmt.Errorf("%w: send a message", threads.ErrInvalid), http.StatusBadRequest, "invalid_request"},
		{`{"message":"hi"}`, errors.New("database down"), http.StatusInternalServerError, "internal"},
	}
	for _, testCase := range cases {
		service := &fakeThreads{startErr: testCase.err, run: &fakeRun{}}
		response := serve(newTestServer(member(), service), http.MethodPost, "/v1/organizations/org_1/threads/thread_1/runs", testCase.body, bearer)
		if response.Code != testCase.status || errorCode(t, response) != testCase.code {
			t.Errorf("%s (%v) = %d %s, want %d %s", testCase.body, testCase.err, response.Code, response.Body.String(), testCase.status, testCase.code)
		}
		if strings.Contains(response.Body.String(), "database down") {
			t.Errorf("an internal error reached the caller: %s", response.Body.String())
		}
	}
	// The sentinel's own words are dropped from what the caller is told.
	service := &fakeThreads{startErr: fmt.Errorf("%w: send a message", threads.ErrInvalid)}
	response := serve(newTestServer(member(), service), http.MethodPost, "/v1/organizations/org_1/threads/thread_1/runs", `{"message":"hi"}`, bearer)
	if !strings.Contains(response.Body.String(), `"message":"send a message"`) {
		t.Fatalf("invalid message = %s", response.Body.String())
	}
}

func TestARunStreamsItsItemsAsServerSentEvents(t *testing.T) {
	run := &fakeRun{items: []threads.Item{
		{Type: threads.ItemRunStarted, RunID: "run_1"},
		{Type: threads.ItemDelta, Author: "krude", Text: "Hel"},
		{Type: threads.ItemMessage, ID: "e1", Author: "krude", Text: "Hello"},
		{Type: threads.ItemRunCompleted, RunID: "run_1"},
	}}
	service := &fakeThreads{run: run}
	response := serve(newTestServer(member(), service), http.MethodPost, "/v1/organizations/org_1/threads/thread_1/runs",
		`{"confirmations":[{"id":"c1","confirmed":true},{"id":"c2","confirmed":false}]}`, bearer)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" || response.Header().Get("X-Oryxa-Run") != "run_1" {
		t.Fatalf("stream = %d %v", response.Code, response.Header())
	}
	if !run.executed || run.discarded != "" {
		t.Fatalf("run executed %t, discarded %q", run.executed, run.discarded)
	}
	want := []threads.RunInput{{Confirmations: []threads.Answer{{ID: "c1", Confirmed: true}, {ID: "c2", Confirmed: false}}}}
	if fmt.Sprint(service.inputs) != fmt.Sprint(want) {
		t.Fatalf("inputs = %+v, want %+v", service.inputs, want)
	}

	var names []string
	var messages []threads.Item
	scanner := bufio.NewScanner(strings.NewReader(response.Body.String()))
	for scanner.Scan() {
		line := scanner.Text()
		if name, found := strings.CutPrefix(line, "event: "); found {
			names = append(names, name)
		}
		if data, found := strings.CutPrefix(line, "data: "); found {
			var item threads.Item
			if err := json.Unmarshal([]byte(data), &item); err != nil {
				t.Fatalf("data %q: %v", data, err)
			}
			messages = append(messages, item)
		}
	}
	if strings.Join(names, ",") != "run_started,delta,message,run_completed" {
		t.Fatalf("events = %v", names)
	}
	if messages[2].Text != "Hello" || messages[2].ID != "e1" {
		t.Fatalf("message item = %+v", messages[2])
	}
}

func TestArtifactsAreServedWithoutEverRunningAsAPage(t *testing.T) {
	server := newTestServer(member(), &fakeThreads{})
	image := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/screenshot.png", "", bearer)
	if image.Code != http.StatusOK || image.Body.String() != "png-bytes" || image.Header().Get("Content-Type") != "image/png" ||
		!strings.HasPrefix(image.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("image = %d %v", image.Code, image.Header())
	}
	page := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/report.html?version=2", "", bearer)
	if page.Code != http.StatusOK || !strings.HasPrefix(page.Header().Get("Content-Disposition"), "attachment") ||
		!strings.Contains(page.Header().Get("Content-Security-Policy"), "sandbox") || page.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("an HTML artifact could render as a page: %d %v", page.Code, page.Header())
	}
	nested := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/user:notes/plan.txt", "", bearer)
	if nested.Code != http.StatusOK || nested.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		!strings.Contains(nested.Header().Get("Content-Disposition"), `filename=plan.txt`) {
		t.Fatalf("nested = %d %v", nested.Code, nested.Header())
	}
	if missing := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_2/artifacts/screenshot.png", "", bearer); missing.Code != http.StatusNotFound {
		t.Fatalf("another thread's artifact = %d, want 404", missing.Code)
	}
	if bad := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/screenshot.png?version=0", "", bearer); bad.Code != http.StatusBadRequest {
		t.Fatalf("version 0 = %d, want 400", bad.Code)
	}
	if anonymous := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/screenshot.png", "", nil); anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("without a member = %d, want 401", anonymous.Code)
	}
}
