package surfaces

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// memQueue is the store's queue in memory, with the same counters: a job is due
// while what was asked for outruns what a finished pass covered, and a job put
// back to wait is not due until the test says its delay is over.
type memQueue struct {
	mu       sync.Mutex
	jobs     map[string]*queued
	order    []string
	expired  []string
	released []release
	gaveUp   []string
	done     []Job
	purges   int
	wakes    []Wake
}

type queued struct {
	job     Job
	handled int64
	claimed bool
	waiting bool
}

type release struct {
	Job    Job
	Failed bool
	Delay  time.Duration
	Reason string
}

func newMemQueue() *memQueue { return &memQueue{jobs: map[string]*queued{}} }

func (q *memQueue) Wake(_ context.Context, wake Wake) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.wakes = append(q.wakes, wake)
	q.wakeLocked(wake)
	return nil
}

func (q *memQueue) wakeLocked(wake Wake) {
	entry, ok := q.jobs[wake.RequestID]
	if !ok {
		entry = &queued{job: Job{RequestID: wake.RequestID, OrganizationID: wake.OrganizationID}}
		q.jobs[wake.RequestID] = entry
		q.order = append(q.order, wake.RequestID)
	}
	entry.job.Wanted++
	entry.job.Attempts = 0
	entry.waiting = false
}

func (q *memQueue) Claim(_ context.Context, _ time.Duration, limit int) ([]Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var claimed []Job
	for _, id := range q.order {
		entry := q.jobs[id]
		if len(claimed) < limit && entry.handled < entry.job.Wanted && !entry.claimed && !entry.waiting {
			entry.claimed = true
			claimed = append(claimed, entry.job)
		}
	}
	return claimed, nil
}

func (q *memQueue) Done(_ context.Context, job Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.jobs[job.RequestID]
	entry.handled, entry.claimed, entry.job.Attempts = max(entry.handled, job.Wanted), false, 0
	q.done = append(q.done, job)
	return nil
}

func (q *memQueue) GiveUp(_ context.Context, job Job, _ string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.jobs[job.RequestID]
	entry.handled, entry.claimed = max(entry.handled, job.Wanted), false
	q.gaveUp = append(q.gaveUp, job.RequestID)
	return nil
}

func (q *memQueue) Release(_ context.Context, job Job, failed bool, maxAttempts int, retryAfter time.Duration, reason string) (bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.released = append(q.released, release{Job: job, Failed: failed, Delay: retryAfter, Reason: reason})
	entry := q.jobs[job.RequestID]
	entry.claimed = false
	if failed {
		entry.job.Attempts++
		if entry.job.Attempts >= maxAttempts {
			entry.handled, entry.job.Attempts = max(entry.handled, job.Wanted), 0
			q.gaveUp = append(q.gaveUp, job.RequestID)
			return true, nil
		}
		entry.waiting = true
	}
	return false, nil
}

func (q *memQueue) WakeExpired(context.Context, time.Time, int) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	woken := len(q.expired)
	for _, id := range q.expired {
		q.wakeLocked(Wake{RequestID: id, OrganizationID: "org_1"})
	}
	q.expired = nil
	return woken, nil
}

func (q *memQueue) Purge(context.Context, time.Duration, time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.purges++
	return nil
}

// due says the delay a released job was waiting for is over.
func (q *memQueue) due(requestID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs[requestID].waiting = false
}

func (q *memQueue) handledCount(requestID string) int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.jobs[requestID].handled
}

func (q *memQueue) releases() []release {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.released)
}

// handlerFunc is a jobHandler.
type handlerFunc func(ctx context.Context, job Job) error

func (f handlerFunc) Handle(ctx context.Context, job Job) error { return f(ctx, job) }

func slackEvent(t *testing.T) []byte { return changeEvent(t, nil) }

func TestAnEventForASlackRequestWakesItsPassAndAnotherKindOfRequestIsNothing(t *testing.T) {
	queue := newMemQueue()
	worker := NewWorker(queue, handlerFunc(func(context.Context, Job) error { return nil }), WorkerConfig{}, nil)
	if err := worker.HandleEvent(context.Background(), DecidedDetailType, "evt_1", slackEvent(t)); err != nil {
		t.Fatal(err)
	}
	if len(queue.wakes) != 1 || queue.wakes[0] != (Wake{RequestID: "irq_1", OrganizationID: "org_1", EventID: "evt_1", AskerUserID: "user_asker", OwnerUserID: "user_raya"}) {
		t.Fatalf("wakes = %+v", queue.wakes)
	}
	if len(worker.nudges) != 1 {
		t.Fatal("the worker was not nudged")
	}
	agentEvent := changeEvent(t, func(c *agentsv1.InteractionChanged) { c.Origin, c.AskID = agentsv1.InteractionOriginAgent, "" })
	if err := worker.HandleEvent(context.Background(), RequestedDetailType, "evt_2", agentEvent); err != nil || len(queue.wakes) != 1 {
		t.Fatalf("an agent's request woke something: %+v, %v", queue.wakes, err)
	}
	if err := worker.HandleEvent(context.Background(), RequestedDetailType, "evt_3", []byte("nope")); err == nil {
		t.Fatal("an event that cannot be read was taken")
	}
}

func TestTheSameEventDeliveredTwiceSaysEverythingOnce(t *testing.T) {
	h := newHarness(t)
	queue := newMemQueue()
	worker := NewWorker(queue, h.service, WorkerConfig{}, nil)
	h.ledger.put(pendingRequest())
	ctx := context.Background()
	event := slackEvent(t)

	for range 2 {
		if err := worker.HandleEvent(ctx, RequestedDetailType, "evt_1", event); err != nil {
			t.Fatal(err)
		}
		if err := worker.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := templates(h.api.sent()); fmt.Sprint(got) != "[owner_notice]" || h.api.postRequests() != 1 {
		t.Fatalf("posted %v after %d post requests, want the one notice said once", got, h.api.postRequests())
	}

	// The owner decides, and the event that says so is delivered three times.
	h.ledger.put(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack))
	for range 3 {
		if err := worker.HandleEvent(ctx, DecidedDetailType, "evt_2", event); err != nil {
			t.Fatal(err)
		}
		if err := worker.WakeDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if h.api.updateRequests() != 1 || h.api.postRequests() != 1 {
		t.Fatalf("%d updates and %d posts, want the notice replaced once", h.api.updateRequests(), h.api.postRequests())
	}
	if queue.handledCount("irq_1") != queue.jobs["irq_1"].job.Wanted {
		t.Fatal("the job was left due")
	}
}

func TestEventsThatComeInDuringAPassLeaveTheRequestDueForAnother(t *testing.T) {
	queue := newMemQueue()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	worker := NewWorker(queue, handlerFunc(func(_ context.Context, _ Job) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	}), WorkerConfig{Interval: time.Hour}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { worker.Run(ctx); close(stopped) }()

	if err := worker.HandleEvent(ctx, RequestedDetailType, "evt_1", slackEvent(t)); err != nil {
		t.Fatal(err)
	}
	<-started
	// The decision comes in while the pass for the first event runs.
	if err := worker.HandleEvent(ctx, DecidedDetailType, "evt_2", slackEvent(t)); err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline := time.After(5 * time.Second)
	for queue.handledCount("irq_1") < 2 {
		select {
		case <-deadline:
			t.Fatalf("the second pass never ran: handled %d after %d calls", queue.handledCount("irq_1"), calls.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-stopped
	if calls.Load() != 2 {
		t.Fatalf("the handler ran %d times, want a pass for each event that was not covered", calls.Load())
	}
}

func TestAPassIsSettledByWhatItSaid(t *testing.T) {
	ctx := context.Background()
	job := Job{RequestID: "irq_1", OrganizationID: "org_1", Wanted: 3}

	t.Run("nothing wrong", func(t *testing.T) {
		queue := newMemQueue()
		queue.jobs["irq_1"] = &queued{job: job, claimed: true}
		NewWorker(queue, handlerFunc(func(context.Context, Job) error { return nil }), WorkerConfig{}, nil).process(ctx, job)
		if len(queue.done) != 1 || queue.done[0].Wanted != 3 || len(queue.released) != 0 || len(queue.gaveUp) != 0 {
			t.Fatalf("done %+v released %+v gaveUp %v", queue.done, queue.released, queue.gaveUp)
		}
	})

	t.Run("a request that can never be done", func(t *testing.T) {
		queue := newMemQueue()
		queue.jobs["irq_1"] = &queued{job: job, claimed: true}
		NewWorker(queue, handlerFunc(func(context.Context, Job) error {
			return slackrouter.Permanent(errors.New("the request is gone"))
		}), WorkerConfig{}, nil).process(ctx, job)
		if len(queue.gaveUp) != 1 || len(queue.done) != 0 || len(queue.released) != 0 {
			t.Fatalf("done %+v released %+v gaveUp %v", queue.done, queue.released, queue.gaveUp)
		}
	})

	t.Run("a failure is backed off from, doubling, and given up on at the limit", func(t *testing.T) {
		queue := newMemQueue()
		for range 3 {
			if err := queue.Wake(ctx, Wake{RequestID: "irq_1", OrganizationID: "org_1"}); err != nil {
				t.Fatal(err)
			}
		}
		worker := NewWorker(queue, handlerFunc(func(context.Context, Job) error { return errors.New("slack is down") }), WorkerConfig{}, nil)
		var delays []time.Duration
		for range maxAttempts {
			queue.due("irq_1")
			claimed, _ := queue.Claim(ctx, time.Minute, 1)
			if len(claimed) != 1 {
				t.Fatalf("the job was not due: %+v", queue.jobs["irq_1"])
			}
			worker.process(ctx, claimed[0])
			delays = append(delays, queue.releases()[len(queue.releases())-1].Delay)
		}
		want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second, 300 * time.Second}
		if !slices.Equal(delays, want) {
			t.Fatalf("delays = %v, want %v", delays, want)
		}
		for _, released := range queue.releases() {
			if !released.Failed {
				t.Fatalf("a failure was released as not one: %+v", released)
			}
		}
		if len(queue.gaveUp) != 1 {
			t.Fatalf("gave up %v, want the job given up on at its %dth failure", queue.gaveUp, maxAttempts)
		}
		if queue.handledCount("irq_1") < 3 {
			t.Fatal("a job given up on was left due")
		}
		if again, _ := queue.Claim(ctx, time.Minute, 1); len(again) != 0 {
			t.Fatalf("a job given up on was claimed again: %+v", again)
		}
	})

	t.Run("a rate limit's own word is waited when it is longer than the backoff, up to the cap", func(t *testing.T) {
		for _, test := range []struct {
			ask  time.Duration
			want time.Duration
		}{{0, 10 * time.Second}, {3 * time.Second, 10 * time.Second}, {45 * time.Second, 45 * time.Second}, {time.Hour, maxRetryDelay}} {
			queue := newMemQueue()
			queue.jobs["irq_1"] = &queued{job: job, claimed: true}
			NewWorker(queue, handlerFunc(func(context.Context, Job) error {
				return fmt.Errorf("post the notice: %w", errors.Join(&hinted{after: test.ask}))
			}), WorkerConfig{}, nil).process(ctx, job)
			if got := queue.releases(); len(got) != 1 || got[0].Delay != test.want {
				t.Fatalf("asked for %v: released %+v, want a delay of %v", test.ask, got, test.want)
			}
		}
	})

	t.Run("a shutdown is not a failure", func(t *testing.T) {
		queue := newMemQueue()
		queue.jobs["irq_1"] = &queued{job: job, claimed: true}
		stopping, stop := context.WithCancel(ctx)
		stop()
		NewWorker(queue, handlerFunc(func(ctx context.Context, _ Job) error { return ctx.Err() }), WorkerConfig{}, nil).process(stopping, job)
		if got := queue.releases(); len(got) != 1 || got[0].Failed || got[0].Delay != 0 || len(queue.gaveUp) != 0 {
			t.Fatalf("released %+v, gave up %v, want an interrupted job put back uncounted", got, queue.gaveUp)
		}
	})

	t.Run("a panic is a failure and not the end of the worker", func(t *testing.T) {
		queue := newMemQueue()
		queue.jobs["irq_1"] = &queued{job: job, claimed: true}
		NewWorker(queue, handlerFunc(func(context.Context, Job) error { panic("a bug") }), WorkerConfig{}, nil).process(ctx, job)
		if got := queue.releases(); len(got) != 1 || !got[0].Failed || got[0].Reason == "" {
			t.Fatalf("released %+v, want the panic counted as a failure", got)
		}
	})
}

// hinted is an error that asks for a wait, as a rate limit does.
type hinted struct{ after time.Duration }

func (e *hinted) Error() string             { return "rate limited" }
func (e *hinted) RetryAfter() time.Duration { return e.after }

func TestRetryDelayDoublesFromTheFirstFailureUpToTheCap(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		0: 10 * time.Second, 1: 10 * time.Second, 2: 20 * time.Second, 3: 40 * time.Second, 4: 80 * time.Second,
		5: 160 * time.Second, 6: 300 * time.Second, 40: 300 * time.Second,
	} {
		if got := retryDelay(failures); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", failures, got, want)
		}
	}
}

func TestOnlyASlotsWorthOfRequestsAreWorkedOnAtOnce(t *testing.T) {
	queue := newMemQueue()
	var running, peak atomic.Int32
	gate := make(chan struct{})
	worker := NewWorker(queue, handlerFunc(func(context.Context, Job) error {
		now := running.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		<-gate
		running.Add(-1)
		return nil
	}), WorkerConfig{Concurrency: 2, Interval: time.Hour}, nil)
	for i := range 5 {
		if err := queue.Wake(context.Background(), Wake{RequestID: fmt.Sprintf("irq_%d", i), OrganizationID: "org_1"}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { worker.Run(ctx); close(stopped) }()
	deadline := time.After(5 * time.Second)
	for running.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("two passes never ran at once")
		case <-time.After(2 * time.Millisecond):
		}
	}
	time.Sleep(50 * time.Millisecond)
	if peak.Load() != 2 {
		t.Fatalf("%d passes ran at once, want 2", peak.Load())
	}
	// Each that ends makes room for the next, until all five are done.
	close(gate)
	for done := 0; done < 5; {
		select {
		case <-deadline:
			t.Fatalf("only %d of 5 requests were worked on", done)
		case <-time.After(5 * time.Millisecond):
		}
		queue.mu.Lock()
		done = len(queue.done)
		queue.mu.Unlock()
	}
	cancel()
	<-stopped
	if peak.Load() != 2 {
		t.Fatalf("peak = %d", peak.Load())
	}
}

func TestTheSweepWakesNoticesWhoseRequestsLapsedAndPurgesWhatIsOld(t *testing.T) {
	queue := newMemQueue()
	queue.expired = []string{"irq_lapsed"}
	var handled []string
	worker := NewWorker(queue, handlerFunc(func(_ context.Context, job Job) error {
		handled = append(handled, job.RequestID)
		return nil
	}), WorkerConfig{}, nil)
	if err := worker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(handled) != "[irq_lapsed]" || queue.purges != 1 {
		t.Fatalf("handled %v, purged %d times", handled, queue.purges)
	}
}

func TestTheWorkerStopsWhenItsContextEndsAndPutsBackWhatItDidNotFinish(t *testing.T) {
	queue := newMemQueue()
	started := make(chan struct{})
	worker := NewWorker(queue, handlerFunc(func(ctx context.Context, _ Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}), WorkerConfig{Interval: time.Hour}, nil)
	if err := queue.Wake(context.Background(), Wake{RequestID: "irq_1", OrganizationID: "org_1"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { worker.Run(ctx); close(stopped) }()
	<-started
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return once its context ended")
	}
	if got := queue.releases(); len(got) != 1 || got[0].Failed {
		t.Fatalf("released %+v, want the interrupted pass put back without counting", got)
	}
}

func TestAWorkerIsSizedWithDefaultsThatOutlastAPass(t *testing.T) {
	config := WorkerConfig{}.withDefaults()
	if config.Interval != 15*time.Second || config.Lease <= jobTimeout || config.Concurrency != 1 {
		t.Fatalf("defaults = %+v", config)
	}
	if config := (WorkerConfig{Lease: time.Second, Concurrency: 4}).withDefaults(); config.Lease <= jobTimeout || config.Concurrency != 4 {
		t.Fatalf("a lease shorter than a pass was kept: %+v", config)
	}
}

func TestNewBuildsTheWorkerTheConsumerAndTheHostDrive(t *testing.T) {
	h := newHarness(t)
	worker, err := New(NewStore(nil), Deps{Requests: h.ledger, Asks: h.ledger, Tickets: h.ledger, Slack: h.client, Users: h.users}, WorkerConfig{Concurrency: 3})
	if err != nil || worker == nil {
		t.Fatalf("New() = %v, %v", worker, err)
	}
	if cap(worker.slots) != 3 {
		t.Fatalf("slots = %d", cap(worker.slots))
	}
	if !Handles(RequestedDetailType) {
		t.Fatal("the consumer would not hand the worker an event it handles")
	}
}
