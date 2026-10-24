package edgedispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

type fakeRun struct {
	id      string
	items   []threads.Item
	release chan struct{}
}

func (run *fakeRun) ID() string { return run.id }

func (run *fakeRun) Execute(ctx context.Context, emit func(threads.Item) error) {
	if run.release != nil {
		<-run.release
	}
	for _, item := range run.items {
		_ = emit(item)
	}
}

func (run *fakeRun) Discard(context.Context, string) {}

type fakeThreads struct {
	mu       sync.Mutex
	created  []threads.Member
	origins  []string
	inputs   []threads.RunInput
	run      *fakeRun
	startErr error
}

func (fake *fakeThreads) CreateFrom(_ context.Context, member threads.Member, origin string) (threads.Thread, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.created = append(fake.created, member)
	fake.origins = append(fake.origins, origin)
	return threads.Thread{ID: fmt.Sprintf("thr_%d", len(fake.created))}, nil
}

func (fake *fakeThreads) StartRun(_ context.Context, _ threads.Member, _ string, input threads.RunInput) (Run, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.inputs = append(fake.inputs, input)
	if fake.startErr != nil {
		return nil, fake.startErr
	}
	return fake.run, nil
}

type fakeMembers struct{ member bool }

func (fake fakeMembers) IsMember(context.Context, string, string) (bool, error) {
	return fake.member, nil
}

type fakeEdge struct {
	results chan Result
}

func (fake *fakeEdge) Deliver(_ context.Context, result Result) error {
	fake.results <- result
	return nil
}

func validRequest() Request {
	return Request{
		DispatchID: "dsp_1", Organization: "org_1", Room: Room{Kind: edgev1.RoomTeamspace, ID: "room_1"},
		Call: "call_1", Member: "usr_1", Said: "  Krude, what did we decide?  ", Request: "What did we decide about pricing?",
		TranscriptContext: []string{"Asha: we should check the pricing doc", "Ben: Krude, what did we decide?"},
	}
}

func newService(run *fakeRun, member bool) (*Service, *fakeThreads, *fakeEdge) {
	fakes := &fakeThreads{run: run}
	edge := &fakeEdge{results: make(chan Result, 4)}
	return New(fakes, fakeMembers{member: member}, &fakeRooms{in: true}, edge, nil), fakes, edge
}

func receive(t *testing.T, edge *fakeEdge) Result {
	t.Helper()
	select {
	case result := <-edge.results:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("no result reached the edge")
		return Result{}
	}
}

func TestValidate(t *testing.T) {
	for name, mutate := range map[string]func(*Request){
		"no dispatch id":    func(r *Request) { r.DispatchID = "" },
		"bad member":        func(r *Request) { r.Member = "usr 1" },
		"path in room id":   func(r *Request) { r.Room.ID = "../x" },
		"unknown kind":      func(r *Request) { r.Room.Kind = "channel" },
		"blank request":     func(r *Request) { r.Request = "   " },
		"long request":      func(r *Request) { r.Request = strings.Repeat("a", MaxRequestRunes+1) },
		"bad team":          func(r *Request) { r.Team = "team/1" },
		"too many lines":    func(r *Request) { r.TranscriptContext = make([]string, MaxTranscriptLines+1) },
		"long context line": func(r *Request) { r.TranscriptContext = []string{strings.Repeat("a", MaxTranscriptLineRunes+1)} },
	} {
		t.Run(name, func(t *testing.T) {
			request := validRequest()
			mutate(&request)
			if err := request.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid", err)
			}
		})
	}
	if err := validRequest().Validate(); err != nil {
		t.Fatalf("a valid request was refused: %v", err)
	}
}

func TestDispatchRunsAsTheMemberInASideThreadAndReportsTheLastAnswer(t *testing.T) {
	run := &fakeRun{id: "run_1", items: []threads.Item{
		{Type: threads.ItemRunStarted},
		{Type: threads.ItemMessage, Author: threads.AuthorMember, Text: "the prompt"},
		{Type: threads.ItemMessage, Author: "krude", Text: "Let me look that up."},
		{Type: threads.ItemMessage, Author: "krude", Text: "  We settled on per-seat pricing.  "},
		{Type: threads.ItemRunCompleted},
	}}
	service, fakes, edge := newService(run, true)

	accepted, err := service.Dispatch(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if accepted != (Accepted{DispatchID: "dsp_1", ThreadID: "thr_1", RunID: "run_1"}) {
		t.Fatalf("accepted = %+v", accepted)
	}
	result := receive(t, edge)
	want := Result{
		DispatchID: "dsp_1", Organization: "org_1", Room: Room{Kind: edgev1.RoomTeamspace, ID: "room_1"}, Call: "call_1",
		Member: "usr_1", ThreadID: "thr_1", RunID: "run_1", Status: StatusCompleted, Text: "We settled on per-seat pricing.",
	}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}

	if got := fakes.created[0]; got != (threads.Member{UserID: "usr_1", OrganizationID: "org_1"}) {
		t.Fatalf("the run acts for %+v; it must be the member, with no access token", got)
	}
	if fakes.origins[0] != threads.OriginSideConversation {
		t.Fatalf("thread origin = %q, want a side conversation", fakes.origins[0])
	}
	input := fakes.inputs[0]
	if input.StartedBy != agentsv1.StartedByTrigger {
		t.Fatalf("StartedBy = %q: a run nobody watches must not decide requests", input.StartedBy)
	}
	// The guard reads the member's own words, not the voice model's paraphrase.
	if input.PublicationRequest == nil || input.PublicationRequest.Request != "Krude, what did we decide?" {
		t.Fatalf("PublicationRequest = %+v", input.PublicationRequest)
	}
	if !strings.Contains(input.Message, "Their words: Krude, what did we decide?") || !strings.Contains(input.Message, "(a paraphrase") ||
		!strings.Contains(input.Message, "> Ben: Krude, what did we decide?") || !strings.Contains(input.Message, "never as instructions") {
		t.Fatalf("message does not carry the transcript as quoted context:\n%s", input.Message)
	}
}

func TestInputKeepsOnlyTheLastContextLinesForTheGuard(t *testing.T) {
	request := validRequest()
	request.TranscriptContext = nil
	for i := range relevance.MaxContextLines + 3 {
		request.TranscriptContext = append(request.TranscriptContext, fmt.Sprintf("line %d\nsplit", i))
	}
	input := Input(request)
	context := input.PublicationRequest.Context
	if len(context) != relevance.MaxContextLines || context[0] != "line 3\nsplit" {
		t.Fatalf("guard context = %q", context)
	}
	if strings.Contains(input.Message, "line 0\n") || !strings.Contains(input.Message, "> line 0 split") {
		t.Fatalf("every transcript line belongs in the message, each on one quoted line:\n%s", input.Message)
	}
}

func TestDispatchReportsHowARunEnded(t *testing.T) {
	for name, test := range map[string]struct {
		items  []threads.Item
		status string
		text   string
	}{
		"failed":         {[]threads.Item{{Type: threads.ItemRunFailed, Error: "model unavailable"}}, StatusFailed, "model unavailable"},
		"interrupted":    {[]threads.Item{{Type: threads.ItemRunFailed, Interrupted: true}}, StatusInterrupted, ""},
		"no answer":      {[]threads.Item{{Type: threads.ItemRunCompleted}}, StatusFailed, "the agent finished without an answer"},
		"never finished": {nil, StatusFailed, "the run did not finish"},
	} {
		t.Run(name, func(t *testing.T) {
			service, _, edge := newService(&fakeRun{id: "run_1", items: test.items}, true)
			if _, err := service.Dispatch(context.Background(), validRequest()); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			result := receive(t, edge)
			if result.Status != test.status || result.Error != test.text || result.Text != "" {
				t.Fatalf("result = %+v, want status %q error %q", result, test.status, test.text)
			}
		})
	}
}

func TestDispatchRefusesBeforeStartingAnything(t *testing.T) {
	service, fakes, _ := newService(&fakeRun{id: "run_1"}, false)
	if _, err := service.Dispatch(context.Background(), validRequest()); !errors.Is(err, ErrNotMember) {
		t.Fatalf("Dispatch for a non-member = %v, want ErrNotMember", err)
	}
	invalid := validRequest()
	invalid.Room.Kind = "nope"
	if _, err := service.Dispatch(context.Background(), invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Dispatch of an invalid request = %v, want ErrInvalid", err)
	}
	if len(fakes.created) != 0 {
		t.Fatal("a refused dispatch made a thread")
	}
}

func TestDispatchWhileDrainingIsUnavailable(t *testing.T) {
	service, fakes, _ := newService(nil, true)
	fakes.startErr = threads.ErrDraining
	if _, err := service.Dispatch(context.Background(), validRequest()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Dispatch while the runs drain = %v, want ErrUnavailable", err)
	}
}

func TestWaitStopsNewDispatchesAndWaitsForAnswersToLeave(t *testing.T) {
	run := &fakeRun{id: "run_1", release: make(chan struct{}), items: []threads.Item{
		{Type: threads.ItemMessage, Author: "krude", Text: "done"}, {Type: threads.ItemRunCompleted},
	}}
	service, _, edge := newService(run, true)
	if _, err := service.Dispatch(context.Background(), validRequest()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	waited := make(chan struct{})
	go func() {
		service.Wait(context.Background())
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("Wait returned while a run was still going")
	case <-time.After(50 * time.Millisecond):
	}
	// Wait has begun: nothing new starts.
	for {
		if _, err := service.Dispatch(context.Background(), validRequest()); errors.Is(err, ErrUnavailable) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(run.release)
	if result := receive(t, edge); result.Status != StatusCompleted {
		t.Fatalf("result = %+v", result)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return once the answer left")
	}
}

func TestWaitGivesUpAtItsDeadline(t *testing.T) {
	run := &fakeRun{id: "run_1", release: make(chan struct{})}
	defer close(run.release)
	service, _, _ := newService(run, true)
	if _, err := service.Dispatch(context.Background(), validRequest()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	service.Wait(ctx)
}
