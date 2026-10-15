package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

type fakeRunStopper struct {
	going          bool
	org, user, run string
}

func (stopper *fakeRunStopper) Stop(organizationID, userID, runID string) bool {
	stopper.org, stopper.user, stopper.run = organizationID, userID, runID
	return stopper.going
}

func TestStopRunStopsTheMembersRun(t *testing.T) {
	stopper := &fakeRunStopper{going: true}
	server := New(member(), &fakeThreads{}, Config{AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute, Runs: stopper}, nil)
	recorder := serve(server, http.MethodPost, "/v1/organizations/org_1/runs/run_9/stop", "", bearer)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if stopper.org != "org_1" || stopper.user != "user_1" || stopper.run != "run_9" {
		t.Fatalf("stopped %q %q %q", stopper.org, stopper.user, stopper.run)
	}
}

func TestStopRunAnswers404WhenNoneOfTheirsIsGoing(t *testing.T) {
	for name, stopper := range map[string]RunStopper{"not going": &fakeRunStopper{}, "no stopper": nil} {
		server := New(member(), &fakeThreads{}, Config{AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute, Runs: stopper}, nil)
		if recorder := serve(server, http.MethodPost, "/v1/organizations/org_1/runs/run_9/stop", "", bearer); recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d", name, recorder.Code)
		}
	}
}

// outlivingRun records whether its context ended while it emitted every item.
type outlivingRun struct {
	emitted  int
	canceled bool
	errs     int
}

func (run *outlivingRun) ID() string { return "run_1" }

func (run *outlivingRun) Execute(ctx context.Context, emit func(threads.Item) error) {
	for range 3 {
		if emit(threads.Item{Type: threads.ItemRunStarted}) != nil {
			run.errs++
		}
		run.emitted++
	}
	run.canceled = ctx.Err() != nil
}

func (run *outlivingRun) Discard(context.Context, string) {}

// failingWriter is a response whose member stopped listening after the headers.
type failingWriter struct{ *httptest.ResponseRecorder }

func (writer failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestARunOutlivesItsStream(t *testing.T) {
	server := New(member(), &fakeThreads{}, Config{RunTimeout: time.Minute}, nil)
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	requestContext, cancel := context.WithCancel(request.Context())
	cancel() // the member's connection is already gone
	run := &outlivingRun{}
	server.streamRun(failingWriter{httptest.NewRecorder()}, request.WithContext(requestContext), run)
	if run.emitted != 3 || run.errs != 0 {
		t.Fatalf("emitted %d with %d errors; a dropped stream must not stop the run", run.emitted, run.errs)
	}
	if run.canceled {
		t.Fatal("the run's context ended with the request's")
	}
}
