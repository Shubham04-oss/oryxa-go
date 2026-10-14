package threads

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/usage"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// slowModel answers once release is closed, and gives up when its call's
// context ends, as a real model call does: a run in the middle of one.
type slowModel struct {
	calling chan struct{}
	release chan struct{}
}

func (m *slowModel) Name() string { return testModel }

func (m *slowModel) GenerateContent(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		select {
		case m.calling <- struct{}{}:
		default:
		}
		select {
		case <-m.release:
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
		yield(&model.LLMResponse{
			Content: genai.NewContentFromText("Done.", genai.RoleModel), TurnComplete: true, FinishReason: genai.FinishReasonStop,
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 1, TotalTokenCount: 11},
		}, nil)
	}
}

// drainHarness is a thread service whose runs go to a slowModel, tracked by a
// drain.
func drainHarness(t *testing.T) (*Service, *fakeRuns, *slowModel, *Drain) {
	t.Helper()
	llm := &slowModel{calling: make(chan struct{}, 8), release: make(chan struct{})}
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: usage.Counted(llm), Instruction: "You are Krude."})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	runs, drain := newFakeRuns(), NewDrain()
	service, err := New(Config{
		AppName: "krude", Agent: krude, Sessions: session.InMemoryService(), Artifacts: artifact.InMemoryService(),
		Runs: runs, Prices: prices, RunTimeout: time.Minute, Drain: drain,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, runs, llm, drain
}

// newThread makes member a thread to run in.
func newThread(t *testing.T, service *Service, member Member) string {
	t.Helper()
	created, err := service.Create(context.Background(), member)
	if err != nil {
		t.Fatal(err)
	}
	return created.ID
}

// going starts a run and executes it in the background, until its model call
// is under way. The items it streams arrive on the channel, which is closed
// once it ends.
func going(t *testing.T, service *Service, llm *slowModel) (*Run, <-chan []Item) {
	t.Helper()
	run, err := service.StartRun(context.Background(), asha, newThread(t, service, asha), RunInput{Message: "Take your time."})
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan []Item, 1)
	go func() {
		var items []Item
		run.Execute(context.Background(), func(item Item) error {
			items = append(items, item)
			return nil
		})
		ended <- items
	}()
	select {
	case <-llm.calling:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never called its model")
	}
	return run, ended
}

// TestADrainLetsARunFinish: once a drain begins, no new run starts, and a run
// already going finishes as it would have, completed; the drain returns once
// it has, having interrupted nothing.
func TestADrainLetsARunFinish(t *testing.T) {
	service, runs, llm, drain := drainHarness(t)
	run, ended := going(t, service, llm)
	if drain.Running() != 1 {
		t.Fatalf("running = %d, want the one run", drain.Running())
	}

	drained := make(chan int, 1)
	go func() { drained <- drain.Drain(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for !drain.Draining() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := service.StartRun(context.Background(), asha, newThread(t, service, asha), RunInput{Message: "Hello."}); !errors.Is(err, ErrDraining) {
		t.Fatalf("a new run while draining: %v, want ErrDraining", err)
	}
	select {
	case <-drained:
		t.Fatal("the drain returned while a run was going")
	case <-time.After(50 * time.Millisecond):
	}

	close(llm.release)
	items := <-ended
	if interrupted := <-drained; interrupted != 0 {
		t.Fatalf("the drain interrupted %d runs, want none", interrupted)
	}
	if last := items[len(items)-1]; last.Type != ItemRunCompleted {
		t.Fatalf("the run ended %+v, want completed", last)
	}
	if outcome := runs.finished[run.ID()]; outcome.Status != runstore.StatusCompleted {
		t.Fatalf("the run was recorded %q, want completed", outcome.Status)
	}
}

// TestADrainInterruptsWhatIsLeftAtItsDeadline: a run still going when the
// drain's time is up is stopped, and ends interrupted, not failed: the member
// is told plainly, the ledger says interrupted, and its event says so.
func TestADrainInterruptsWhatIsLeftAtItsDeadline(t *testing.T) {
	service, runs, llm, drain := drainHarness(t)
	run, ended := going(t, service, llm)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if interrupted := drain.Drain(ctx); interrupted != 1 {
		t.Fatalf("the drain interrupted %d runs, want the one", interrupted)
	}
	items := <-ended
	last := items[len(items)-1]
	if last.Type != ItemRunFailed || !last.Interrupted || !strings.Contains(last.Error, "Oryxa restarted") {
		t.Fatalf("the run ended %+v, want failed, interrupted, saying Oryxa restarted", last)
	}
	if outcome := runs.finished[run.ID()]; outcome.Status != runstore.StatusInterrupted {
		t.Fatalf("the run was recorded %q, want interrupted", outcome.Status)
	}
	var failed agentsv1.RunFailed
	if event := runs.events[run.ID()]; event.DetailType != agentsv1.RunFailedDetailType || json.Unmarshal(event.Data, &failed) != nil || !failed.Interrupted {
		t.Fatalf("the run's event = %s %s, want a failure marked interrupted", event.DetailType, event.Data)
	}
	if drain.Running() != 0 {
		t.Fatalf("running = %d after the drain", drain.Running())
	}
}

// TestARunExecutedOnceTheDrainBeganEndsInterrupted: a run started just before
// the drain began, but not yet executing, ends at once as interrupted, never
// calling its model, so whoever started it starts it again on the next
// process.
func TestARunExecutedOnceTheDrainBeganEndsInterrupted(t *testing.T) {
	service, runs, llm, drain := drainHarness(t)
	run, err := service.StartRun(context.Background(), asha, newThread(t, service, asha), RunInput{Message: "Hello."})
	if err != nil {
		t.Fatal(err)
	}
	if interrupted := drain.Drain(context.Background()); interrupted != 0 {
		t.Fatalf("a drain with nothing going interrupted %d", interrupted)
	}
	var items []Item
	run.Execute(context.Background(), func(item Item) error {
		items = append(items, item)
		return nil
	})
	if len(items) != 1 || items[0].Type != ItemRunFailed || !items[0].Interrupted {
		t.Fatalf("items = %+v, want one failure, interrupted", items)
	}
	select {
	case <-llm.calling:
		t.Fatal("the model was called")
	default:
	}
	if outcome := runs.finished[run.ID()]; outcome.Status != runstore.StatusInterrupted {
		t.Fatalf("the run was recorded %q, want interrupted", outcome.Status)
	}
}

// TestNoDrainTracksNothing: a thread service with no drain runs as ever, and a
// nil drain answers as one that never drains.
func TestNoDrainTracksNothing(t *testing.T) {
	var drain *Drain
	if drain.Draining() || drain.Running() != 0 || drain.Drain(context.Background()) != 0 {
		t.Fatal("a nil drain did something")
	}
	if !Interrupted(ErrDraining) || !Interrupted(ErrInterrupted) || Interrupted(context.Canceled) {
		t.Fatal("Interrupted does not tell a restart from anything else")
	}
}
