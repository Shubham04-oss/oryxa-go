package interactiondelivery

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// record adds a delivery as the consumer's Record would, while a waker may
// be claiming from the same store.
func (store *fakeDeliveries) record(delivery Delivery) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pending = append(store.pending, delivery)
}

func (store *fakeDeliveries) handled() []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]string{}, store.done...)
}

// runWaker runs the waker as the service does, and returns what stops it:
// cancelling Run, and waiting for the runs it started.
func runWaker(t *testing.T, waker *Waker) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		waker.Run(ctx)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-stopped
		})
	}
	t.Cleanup(stop)
	return stop
}

// TestANudgedDeliveryStartsAtOnceWithoutThePoll: the consumer nudges the
// waker as it records a delivery, so the agent starts at once; the poll, an
// hour away here, is only the backstop.
func TestANudgedDeliveryStartsAtOnceWithoutThePoll(t *testing.T) {
	store := &fakeDeliveries{}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": decidable("pending")}}
	started := make(chan time.Time, 1)
	runs := &fakeAgentRuns{onExecute: func() {
		started <- time.Now()
		requests.setStatus("irq_1", "approved")
	}}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Hour, Lease: time.Minute, Standing: standsAlways{}}, nil)
	stop := runWaker(t, waker)

	recorded := time.Now()
	store.record(testDelivery("evt_1", KindTargetRequested, 1))
	waker.Nudge()
	select {
	case at := <-started:
		if waited := at.Sub(recorded); waited > 500*time.Millisecond {
			t.Fatalf("the run started %s after its delivery was recorded, want at once", waited)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a nudged delivery did not start, and the poll is an hour away")
	}
	stop()
	if done := store.handled(); len(done) != 1 || done[0] != "evt_1" {
		t.Fatalf("done=%v, want the delivery completed", done)
	}
}

// gatedRuns starts runs that each wait until the test lets them go, then
// record the decision their delivery asks for. It tells each delivery apart
// by its recipient agent, and keeps how many ran at once.
type gatedRuns struct {
	requests  *fakeRequests
	requestOf map[string]string
	started   chan string
	gates     map[string]chan struct{}
	mu        sync.Mutex
	running   int
	peak      int
}

func newGatedRuns(requests *fakeRequests, requestOf map[string]string) *gatedRuns {
	runs := &gatedRuns{requests: requests, requestOf: requestOf, started: make(chan string, len(requestOf)), gates: map[string]chan struct{}{}}
	for agentID := range requestOf {
		runs.gates[agentID] = make(chan struct{})
	}
	return runs
}

func (runs *gatedRuns) StartUnattendedRun(_ context.Context, _ threads.Member, agentID, threadID string, _ threads.RunInput, _ managedagents.UnattendedProfile) (managedagents.StartedRun, error) {
	if threadID == "" {
		threadID = "thread_" + agentID
	}
	return managedagents.StartedRun{ThreadID: threadID, Run: &gatedRun{runs: runs, agentID: agentID}}, nil
}

func (runs *gatedRuns) let(agentID string) { close(runs.gates[agentID]) }

// mostAtOnce is the most runs that went at the same time.
func (runs *gatedRuns) mostAtOnce() int {
	runs.mu.Lock()
	defer runs.mu.Unlock()
	return runs.peak
}

type gatedRun struct {
	runs    *gatedRuns
	agentID string
}

func (run *gatedRun) ID() string { return "run_" + run.agentID }

func (run *gatedRun) Execute(ctx context.Context, emit func(threads.Item) error) {
	runs := run.runs
	runs.mu.Lock()
	runs.running++
	runs.peak = max(runs.peak, runs.running)
	runs.mu.Unlock()
	runs.started <- run.agentID
	select {
	case <-runs.gates[run.agentID]:
		runs.requests.setStatus(runs.requestOf[run.agentID], interactions.StatusApproved)
	case <-ctx.Done():
	}
	runs.mu.Lock()
	runs.running--
	runs.mu.Unlock()
	_ = emit(threads.Item{Type: threads.ItemRunCompleted, RunID: run.ID()})
}

func (*gatedRun) Discard(context.Context, string) {}

// deliveryTo is a new request's delivery to its own recipient agent.
func deliveryTo(eventID, requestID, agentID string) Delivery {
	delivery := testDelivery(eventID, KindTargetRequested, 1)
	delivery.RequestID, delivery.RecipientAgentID = requestID, agentID
	return delivery
}

// standsAlways reads every request made in a teamspace again as still
// standing, from any number of runs at once.
type standsAlways struct{}

func (standsAlways) Recheck(_ context.Context, request agents.Interaction) (agents.Interaction, interactions.Verdict, error) {
	return request, interactions.VerdictStands, nil
}

func pendingRequests(ids ...string) *fakeRequests {
	requests := &fakeRequests{requests: map[string]agents.Interaction{}}
	for _, id := range ids {
		request := decidable(interactions.StatusPending)
		request.ID = id
		requests.requests[id] = request
	}
	return requests
}

// waitStarted waits for the run of one agent to start, and fails when
// another starts instead or none starts at all.
func waitStarted(t *testing.T, runs *gatedRuns, agentID string, within time.Duration) {
	t.Helper()
	select {
	case started := <-runs.started:
		if started != agentID {
			t.Fatalf("%s's run started, want %s's", started, agentID)
		}
	case <-time.After(within):
		t.Fatalf("%s's run did not start within %s", agentID, within)
	}
}

// TestADeliveryRecordedWhileAnotherRunsStartsInAFreeSlot: a run that takes
// its time holds one slot, and a delivery recorded meanwhile starts at once
// in another, rather than after that run has ended.
func TestADeliveryRecordedWhileAnotherRunsStartsInAFreeSlot(t *testing.T) {
	requests := pendingRequests("irq_1", "irq_2")
	runs := newGatedRuns(requests, map[string]string{"agt_1": "irq_1", "agt_2": "irq_2"})
	store := &fakeDeliveries{}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Hour, Lease: time.Minute, Concurrency: 2, Standing: standsAlways{}}, nil)
	stop := runWaker(t, waker)

	store.record(deliveryTo("evt_1", "irq_1", "agt_1"))
	waker.Nudge()
	waitStarted(t, runs, "agt_1", time.Second)
	store.record(deliveryTo("evt_2", "irq_2", "agt_2"))
	waker.Nudge()
	waitStarted(t, runs, "agt_2", time.Second)

	runs.let("agt_1")
	runs.let("agt_2")
	stop()
	if done := store.handled(); len(done) != 2 {
		t.Fatalf("done=%v, want both deliveries completed", done)
	}
}

// TestWithEverySlotTakenADeliveryStartsAsOneFrees: the waker never runs more
// agents than its concurrency, and a delivery that finds every slot taken
// starts as soon as a run ends, not at the next poll.
func TestWithEverySlotTakenADeliveryStartsAsOneFrees(t *testing.T) {
	requests := pendingRequests("irq_1", "irq_2")
	runs := newGatedRuns(requests, map[string]string{"agt_1": "irq_1", "agt_2": "irq_2"})
	store := &fakeDeliveries{}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Hour, Lease: time.Minute, Concurrency: 1, Standing: standsAlways{}}, nil)
	stop := runWaker(t, waker)

	store.record(deliveryTo("evt_1", "irq_1", "agt_1"))
	waker.Nudge()
	waitStarted(t, runs, "agt_1", time.Second)
	store.record(deliveryTo("evt_2", "irq_2", "agt_2"))
	waker.Nudge()
	select {
	case started := <-runs.started:
		t.Fatalf("%s's run started while the only slot was taken", started)
	case <-time.After(200 * time.Millisecond):
	}

	runs.let("agt_1")
	waitStarted(t, runs, "agt_2", time.Second)
	runs.let("agt_2")
	stop()
	if most := runs.mostAtOnce(); most != 1 {
		t.Fatalf("%d runs went at once, want at most 1", most)
	}
	if done := store.handled(); len(done) != 2 {
		t.Fatalf("done=%v, want both deliveries completed", done)
	}
}
