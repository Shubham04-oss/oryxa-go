package slackrouter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeQueue is jobQueue in memory, with the semantics Store gives it in
// PostgreSQL: one job for each inbound id, leased when claimed, released
// with a delay, dropped once it has failed maxAttempts times.
type fakeQueue struct {
	mu   sync.Mutex
	now  time.Time
	jobs []*queuedJob

	enqueueErr error
	log        []string
	purged     []time.Duration
}

type queuedJob struct {
	Job
	handled, dropped bool
	availableAt      time.Time
	claimedUntil     time.Time
	reason           string
	lastDelay        time.Duration
}

func newFakeQueue() *fakeQueue { return &fakeQueue{now: time.Unix(1700000000, 0).UTC()} }

func (queue *fakeQueue) advance(by time.Duration) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.now = queue.now.Add(by)
}

func (queue *fakeQueue) find(inboundID string) *queuedJob {
	for _, job := range queue.jobs {
		if job.InboundID == inboundID {
			return job
		}
	}
	return nil
}

func (queue *fakeQueue) Enqueue(_ context.Context, job Job) (bool, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.enqueueErr != nil {
		return false, queue.enqueueErr
	}
	if err := validateJob(job); err != nil {
		return false, err
	}
	if queue.find(job.InboundID) != nil {
		return false, nil
	}
	queue.jobs = append(queue.jobs, &queuedJob{Job: job, availableAt: queue.now})
	return true, nil
}

func (queue *fakeQueue) Claim(_ context.Context, lease time.Duration, limit int) ([]Job, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	var claimed []Job
	for _, job := range queue.jobs {
		if len(claimed) == limit {
			break
		}
		if job.handled || job.availableAt.After(queue.now) || job.claimedUntil.After(queue.now) {
			continue
		}
		job.claimedUntil = queue.now.Add(lease)
		claimed = append(claimed, job.Job)
	}
	return claimed, nil
}

func (queue *fakeQueue) Done(_ context.Context, inboundID string) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.find(inboundID).handled = true
	queue.log = append(queue.log, "done:"+inboundID)
	return nil
}

func (queue *fakeQueue) Drop(_ context.Context, inboundID, reason string) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	job := queue.find(inboundID)
	job.handled, job.dropped, job.reason = true, true, reason
	queue.log = append(queue.log, "drop:"+inboundID)
	return nil
}

func (queue *fakeQueue) Release(_ context.Context, inboundID string, failed bool, maxAttempts int, retryAfter time.Duration, reason string) (bool, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	job := queue.find(inboundID)
	if failed {
		job.Attempts++
	}
	job.reason = reason
	if failed && job.Attempts >= maxAttempts {
		job.handled, job.dropped = true, true
		queue.log = append(queue.log, "gaveup:"+inboundID)
		return true, nil
	}
	job.claimedUntil, job.availableAt, job.lastDelay = time.Time{}, queue.now.Add(retryAfter), retryAfter
	queue.log = append(queue.log, fmt.Sprintf("release:%s:failed=%v", inboundID, failed))
	return false, nil
}

func (queue *fakeQueue) Purge(_ context.Context, age time.Duration) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.purged = append(queue.purged, age)
	return nil
}

func (queue *fakeQueue) state(inboundID string) queuedJob {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return *queue.find(inboundID)
}

// handlerFunc adapts a function to jobHandler.
type handlerFunc func(ctx context.Context, job Job) error

func (fn handlerFunc) Handle(ctx context.Context, job Job) error { return fn(ctx, job) }

func eventData(inboundID string) []byte {
	return []byte(fmt.Sprintf(`{"inbound_id":%q,"organization_id":%q,"team_id":%q,"received_at":"2026-09-29T10:00:00Z"}`, inboundID, testOrg, testTeam))
}

func TestRetryDelayDoublesUpToTheCap(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		0: 15 * time.Second, 1: 15 * time.Second, 2: 30 * time.Second, 3: time.Minute,
		4: 2 * time.Minute, 5: 4 * time.Minute, 6: 5 * time.Minute, 30: 5 * time.Minute,
	} {
		if got := retryDelay(failures); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", failures, got, want)
		}
	}
}

func TestALeaseOutlastsAnAttempt(t *testing.T) {
	if got := (WorkerConfig{}).withDefaults(); got.Lease <= jobTimeout || got.Interval <= 0 || got.Concurrency < 1 {
		t.Fatalf("defaults = %+v", got)
	}
	if got := (WorkerConfig{Lease: time.Second}).withDefaults(); got.Lease <= jobTimeout {
		t.Fatalf("a lease of %v does not outlast an attempt", got.Lease)
	}
}

func TestSubscriptionsAndHandles(t *testing.T) {
	want := map[string][]string{"oryxa.apps": {"apps.slack.message.received.v1", "apps.slack.action.received.v1"}}
	if got := Subscriptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Subscriptions() = %v, want %v", got, want)
	}
	for detailType, handled := range map[string]bool{
		"apps.slack.message.received.v1": true, "apps.slack.action.received.v1": true,
		"apps.trigger.received.v1": false, "apps.slack.install.changed.v1": false, "": false,
	} {
		if Handles(detailType) != handled {
			t.Errorf("Handles(%q) = %v", detailType, !handled)
		}
	}
}

func TestHandleEventRecordsOneJobForOneInboundRow(t *testing.T) {
	queue := newFakeQueue()
	worker := NewWorker(queue, handlerFunc(func(context.Context, Job) error { return nil }), WorkerConfig{}, nil)

	if err := worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt_1", eventData("in_1")); err != nil {
		t.Fatal(err)
	}
	// The bus delivers at least once, and apps may nudge twice for one row.
	for _, again := range []struct{ detail, event string }{
		{MessageReceivedDetailType, "evt_1"}, {MessageReceivedDetailType, "evt_2"}, {ActionReceivedDetailType, "evt_3"},
	} {
		if err := worker.HandleEvent(context.Background(), again.detail, again.event, eventData("in_1")); err != nil {
			t.Fatal(err)
		}
	}
	if len(queue.jobs) != 1 {
		t.Fatalf("%d jobs for one inbound row", len(queue.jobs))
	}
	job := queue.jobs[0].Job
	if job.InboundID != "in_1" || job.Kind != KindMessage || job.OrganizationID != testOrg || job.TeamID != testTeam ||
		job.EventID != "evt_1" || !job.ReceivedAt.Equal(time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)) || job.Attempts != 0 || job.AskID != "" {
		t.Fatalf("job = %+v, want the first event's", job)
	}

	if err := worker.HandleEvent(context.Background(), ActionReceivedDetailType, "evt_4", eventData("in_2")); err != nil {
		t.Fatal(err)
	}
	if len(queue.jobs) != 2 || queue.jobs[1].Kind != KindAction {
		t.Fatalf("jobs = %+v", queue.jobs)
	}
}

func TestHandleEventNudgesOnlyForANewJob(t *testing.T) {
	worker := NewWorker(newFakeQueue(), handlerFunc(func(context.Context, Job) error { return nil }), WorkerConfig{}, nil)
	drain := func() bool {
		select {
		case <-worker.nudges:
			return true
		default:
			return false
		}
	}
	if err := worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt_1", eventData("in_1")); err != nil || !drain() {
		t.Fatalf("a new job did not nudge: %v", err)
	}
	if err := worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt_2", eventData("in_1")); err != nil || drain() {
		t.Fatalf("a repeated job nudged: %v", err)
	}
}

func TestHandleEventRefusesWhatItCannotUse(t *testing.T) {
	queue := newFakeQueue()
	worker := NewWorker(queue, handlerFunc(func(context.Context, Job) error { return nil }), WorkerConfig{}, nil)
	for name, test := range map[string]struct {
		detail string
		data   string
	}{
		"another detail type": {"apps.slack.install.changed.v1", string(eventData("in_1"))},
		"not JSON":            {MessageReceivedDetailType, `nope`},
		"no inbound id":       {MessageReceivedDetailType, `{"organization_id":"o","team_id":"t"}`},
		"no organization":     {MessageReceivedDetailType, `{"inbound_id":"i","team_id":"t"}`},
		"no team":             {MessageReceivedDetailType, `{"inbound_id":"i","organization_id":"o"}`},
		"blank ids":           {ActionReceivedDetailType, `{"inbound_id":" ","organization_id":"o","team_id":"t"}`},
	} {
		if err := worker.HandleEvent(context.Background(), test.detail, "evt", []byte(test.data)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(queue.jobs) != 0 {
		t.Fatalf("recorded %d jobs from events it refused", len(queue.jobs))
	}

	// A database that does not answer is an error for the queue to deliver
	// the event again after.
	queue.enqueueErr = errors.New("database down")
	if err := worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt", eventData("in_1")); err == nil || !strings.Contains(err.Error(), "evt") {
		t.Fatalf("err = %v", err)
	}
}

func TestAReceivedEventWithoutATimeIsTimedByTheWorker(t *testing.T) {
	queue := newFakeQueue()
	worker := NewWorker(queue, handlerFunc(func(context.Context, Job) error { return nil }), WorkerConfig{}, nil)
	worker.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	data := `{"inbound_id":"in_1","organization_id":"o","team_id":"t"}`
	if err := worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt", []byte(data)); err != nil {
		t.Fatal(err)
	}
	if got := queue.jobs[0].ReceivedAt; !got.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("received at %v", got)
	}
}

// process runs every due job once and waits for them.
func wake(t *testing.T, worker *Worker) {
	t.Helper()
	if err := worker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAJobThatSucceedsIsDone(t *testing.T) {
	queue := newFakeQueue()
	var seen []Job
	worker := NewWorker(queue, handlerFunc(func(_ context.Context, job Job) error { seen = append(seen, job); return nil }), WorkerConfig{}, nil)
	_ = worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt_1", eventData("in_1"))
	wake(t, worker)
	wake(t, worker)
	if len(seen) != 1 || seen[0].InboundID != "in_1" || !queue.state("in_1").handled || queue.state("in_1").dropped {
		t.Fatalf("seen %+v, state %+v", seen, queue.state("in_1"))
	}
	if len(queue.purged) != 2 || queue.purged[0] != handledRetention {
		t.Fatalf("purged with %v", queue.purged)
	}
}

func TestAJobThatCannotBeRoutedIsDroppedNotRetried(t *testing.T) {
	queue := newFakeQueue()
	var calls int
	worker := NewWorker(queue, handlerFunc(func(context.Context, Job) error {
		calls++
		return Permanent(errors.New("the inbound row is gone"))
	}), WorkerConfig{}, nil)
	_ = worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt_1", eventData("in_1"))
	wake(t, worker)
	queue.advance(time.Hour)
	wake(t, worker)
	state := queue.state("in_1")
	if calls != 1 || !state.dropped || !strings.Contains(state.reason, "inbound row is gone") || state.Attempts != 0 {
		t.Fatalf("calls %d, state %+v", calls, state)
	}
}

func TestAFailedJobIsRetriedWithAGrowingDelayThenGivenUp(t *testing.T) {
	queue := newFakeQueue()
	var calls int
	var attempts []int
	worker := NewWorker(queue, handlerFunc(func(_ context.Context, job Job) error {
		calls++
		attempts = append(attempts, job.Attempts)
		return errors.New("apps timed out")
	}), WorkerConfig{}, nil)
	_ = worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt_1", eventData("in_1"))

	for attempt := 1; attempt < maxAttempts; attempt++ {
		wake(t, worker)
		state := queue.state("in_1")
		if state.handled || state.Attempts != attempt || state.lastDelay != retryDelay(attempt) {
			t.Fatalf("after failure %d: %+v, want a retry in %v", attempt, state, retryDelay(attempt))
		}
		// Not due until its delay has passed.
		wake(t, worker)
		if calls != attempt {
			t.Fatalf("ran again before its delay: %d calls after failure %d", calls, attempt)
		}
		queue.advance(retryDelay(attempt))
	}
	wake(t, worker)
	state := queue.state("in_1")
	if calls != maxAttempts || !state.dropped || !state.handled || !strings.Contains(state.reason, "apps timed out") {
		t.Fatalf("after %d calls: %+v, want it given up on", calls, state)
	}
	if want := []int{0, 1, 2, 3, 4}; !reflect.DeepEqual(attempts, want) {
		t.Fatalf("handler saw attempts %v, want %v", attempts, want)
	}
	queue.advance(time.Hour)
	wake(t, worker)
	if calls != maxAttempts {
		t.Fatal("a job that was given up on ran again")
	}
}

type rateLimited struct{ after time.Duration }

func (err rateLimited) Error() string             { return "apps answered 429" }
func (err rateLimited) RetryAfter() time.Duration { return err.after }

func TestARateLimitLengthensTheDelayButNotBeyondTheCap(t *testing.T) {
	for name, test := range map[string]struct {
		after time.Duration
		want  time.Duration
	}{
		"longer than the backoff":  {2 * time.Minute, 2 * time.Minute},
		"shorter than the backoff": {time.Second, firstRetry},
		"beyond the cap":           {time.Hour, maxRetryDelay},
		"none":                     {0, firstRetry},
	} {
		t.Run(name, func(t *testing.T) {
			queue := newFakeQueue()
			worker := NewWorker(queue, handlerFunc(func(context.Context, Job) error {
				return fmt.Errorf("post the refusal: %w", rateLimited{after: test.after})
			}), WorkerConfig{}, nil)
			_ = worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt_1", eventData("in_1"))
			wake(t, worker)
			if got := queue.state("in_1").lastDelay; got != test.want {
				t.Fatalf("delay = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAJobInterruptedByShutdownIsNotAFailure(t *testing.T) {
	queue := newFakeQueue()
	ctx, cancel := context.WithCancel(context.Background())
	worker := NewWorker(queue, handlerFunc(func(ctx context.Context, job Job) error {
		cancel()
		return ctx.Err()
	}), WorkerConfig{}, nil)
	_ = worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt_1", eventData("in_1"))
	_ = worker.WakeDue(ctx)
	state := queue.state("in_1")
	if state.handled || state.Attempts != 0 || len(queue.log) != 1 || queue.log[0] != "release:in_1:failed=false" {
		t.Fatalf("state %+v, log %v: a shutdown counted against the job", state, queue.log)
	}
}

func TestAnAttemptThatRunsTooLongFailsAndIsRetried(t *testing.T) {
	queue := newFakeQueue()
	worker := NewWorker(queue, handlerFunc(func(ctx context.Context, job Job) error {
		<-ctx.Done()
		return ctx.Err()
	}), WorkerConfig{}, nil)
	worker.timeout = 20 * time.Millisecond
	_ = worker.HandleEvent(context.Background(), MessageReceivedDetailType, "evt_1", eventData("in_1"))
	wake(t, worker)
	state := queue.state("in_1")
	if state.handled || state.Attempts != 1 || !strings.Contains(state.reason, "deadline") {
		t.Fatalf("state %+v: a timed-out attempt is a failure that is retried", state)
	}
}

func TestRunRoutesANudgedJobAtOnce(t *testing.T) {
	queue := newFakeQueue()
	done := make(chan Job, 1)
	worker := NewWorker(queue, handlerFunc(func(_ context.Context, job Job) error { done <- job; return nil }),
		WorkerConfig{Interval: time.Hour}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); worker.Run(ctx) }()
	defer func() { cancel(); <-stopped }()

	if err := worker.HandleEvent(ctx, MessageReceivedDetailType, "evt_1", eventData("in_1")); err != nil {
		t.Fatal(err)
	}
	select {
	case job := <-done:
		if job.InboundID != "in_1" {
			t.Fatalf("job = %+v", job)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a nudged job waited for the poll, an hour away")
	}
}

func TestRunPollsForAJobReleasedForLater(t *testing.T) {
	queue := newFakeQueue()
	var calls atomic.Int32
	seen := make(chan struct{}, 4)
	worker := NewWorker(queue, handlerFunc(func(context.Context, Job) error {
		seen <- struct{}{}
		if calls.Add(1) == 1 {
			return errors.New("try again")
		}
		return nil
	}), WorkerConfig{Interval: 10 * time.Millisecond}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); worker.Run(ctx) }()
	defer func() { cancel(); <-stopped }()

	_ = worker.HandleEvent(ctx, MessageReceivedDetailType, "evt_1", eventData("in_1"))
	<-seen
	// Once the first attempt has failed and been released for later, the delay
	// passes. Nothing nudges: the poll finds it.
	for queue.state("in_1").Attempts != 1 {
		time.Sleep(time.Millisecond)
	}
	queue.advance(time.Minute)
	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("a released job was never polled for")
	}
}

func TestOnlyConcurrencyJobsRunAtOnce(t *testing.T) {
	queue := newFakeQueue()
	var running, peak atomic.Int32
	release := make(chan struct{})
	worker := NewWorker(queue, handlerFunc(func(context.Context, Job) error {
		now := running.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		<-release
		running.Add(-1)
		return nil
	}), WorkerConfig{Concurrency: 2}, nil)
	for index := range 5 {
		_ = worker.HandleEvent(context.Background(), MessageReceivedDetailType, fmt.Sprintf("evt_%d", index), eventData(fmt.Sprintf("in_%d", index)))
	}

	finished := make(chan error, 1)
	go func() { finished <- worker.WakeDue(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for running.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // room for a third to start, were it going to
	if got := running.Load(); got != 2 {
		t.Fatalf("%d jobs running with room for 2", got)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency %d", peak.Load())
	}
	// A pass starts what there is room for and returns when those have ended,
	// so the rest take the next passes, two at a time.
	for pass := 0; pass < 3; pass++ {
		wake(t, worker)
	}
	for index := range 5 {
		if !queue.state(fmt.Sprintf("in_%d", index)).handled {
			t.Fatalf("job %d never ran", index)
		}
	}
}

// TestFromEventToAskThroughTheWorkerAndTheRouter runs the whole path over
// fakes: a bus event, a job, a routed mention, one ask, however many times the
// event comes, and a mention that failed once and was tried again.
func TestFromEventToAskThroughTheWorkerAndTheRouter(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	job := h.mention(toBot("ask <@URAYA> about the roadmap"))

	queue := newFakeQueue()
	worker := NewWorker(queue, h.router, WorkerConfig{}, nil)
	deliver := func(eventID string) {
		data := fmt.Sprintf(`{"inbound_id":%q,"organization_id":%q,"team_id":%q}`, job.InboundID, testOrg, testTeam)
		if err := worker.HandleEvent(context.Background(), MessageReceivedDetailType, eventID, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}

	h.slack.profilesErr = errors.New("apps timed out")
	deliver("evt_1")
	wake(t, worker)
	if len(h.asks.created) != 0 || queue.state(job.InboundID).Attempts != 1 {
		t.Fatalf("after the failure: %d asks, %+v", len(h.asks.created), queue.state(job.InboundID))
	}

	h.slack.profilesErr = nil
	deliver("evt_2") // the bus delivers again meanwhile
	queue.advance(retryDelay(1))
	wake(t, worker)
	wake(t, worker)
	deliver("evt_3") // and once more after it is done
	wake(t, worker)

	if len(h.asks.created) != 1 || h.asks.createCalls != 1 {
		t.Fatalf("%d asks in %d calls, want one", len(h.asks.created), h.asks.createCalls)
	}
	if state := queue.state(job.InboundID); !state.handled || state.dropped {
		t.Fatalf("state = %+v", state)
	}
	if posts := h.slack.sent(); len(posts) != 1 || posts[0].Template != TemplateAskAck {
		t.Fatalf("posts = %+v, want the one acknowledgement", posts)
	}
}
