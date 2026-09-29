package surfaces

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
)

const (
	// maxAttempts is how many times a request's pass may fail in a row before
	// it is given up on. The next event for the request starts it over.
	maxAttempts = 6
	// firstRetry is the delay after the first failure; each failure after it
	// doubles the delay, up to maxRetryDelay. A notice is worth having in the
	// minutes an owner is likely to look, so the last try comes about five
	// minutes after the first.
	firstRetry    = 10 * time.Second
	maxRetryDelay = 5 * time.Minute
	// jobTimeout bounds one pass. It is less than any lease, so a pass is never
	// running under a lease that has run out.
	jobTimeout = 90 * time.Second
	// jobRetention is how long a finished job's row is kept, and linkRetention
	// how long a link is, counted from when it last changed. A request can be
	// answered and reviewed for days, and a message is updated only while its
	// request is not settled, so a month is well past both.
	jobRetention  = 7 * 24 * time.Hour
	linkRetention = 30 * 24 * time.Hour
	// expiredPerSweep bounds how many lapsed notices one sweep wakes.
	expiredPerSweep = 50
)

// retryDelay is how long to wait before the attempt after failure number
// failures.
func retryDelay(failures int) time.Duration {
	delay := firstRetry
	for range max(failures-1, 0) {
		delay *= 2
		if delay >= maxRetryDelay {
			return maxRetryDelay
		}
	}
	return delay
}

// retryAfterOf reads the delay an error asks for, if it asks for one: a rate
// limit says how long to leave Slack alone.
func retryAfterOf(err error) time.Duration {
	var hinted interface{ RetryAfter() time.Duration }
	if errors.As(err, &hinted) {
		return hinted.RetryAfter()
	}
	return 0
}

// jobQueue is the durable side of the worker. Store implements it.
type jobQueue interface {
	Wake(context.Context, Wake) error
	Claim(ctx context.Context, lease time.Duration, limit int) ([]Job, error)
	Done(context.Context, Job) error
	GiveUp(context.Context, Job, string) error
	Release(ctx context.Context, job Job, failed bool, maxAttempts int, retryAfter time.Duration, reason string) (bool, error)
	WakeExpired(ctx context.Context, now time.Time, limit int) (int, error)
	Purge(ctx context.Context, jobAge, linkAge time.Duration) error
}

// jobHandler does one request's pass. Service implements it.
type jobHandler interface {
	Handle(ctx context.Context, job Job) error
}

// WorkerConfig sizes the worker.
type WorkerConfig struct {
	// Interval is how often the worker looks for due jobs without being nudged,
	// and for notices that lapsed: the backstop for a job released to be retried
	// later, and for one another replica recorded.
	Interval time.Duration
	// Lease is how long a claimed job stays its claimer's. It must outlast one
	// pass.
	Lease time.Duration
	// Concurrency is how many requests are worked on at once.
	Concurrency int
}

func (config WorkerConfig) withDefaults() WorkerConfig {
	if config.Interval <= 0 {
		config.Interval = 15 * time.Second
	}
	if config.Lease <= jobTimeout {
		config.Lease = 2 * jobTimeout
	}
	config.Concurrency = max(config.Concurrency, 1)
	return config
}

// Worker turns the ledger's events into passes and runs them, the same pattern
// interactiondelivery and the router use. Recording an event never calls Slack,
// a model or another service.
type Worker struct {
	queue   jobQueue
	handler jobHandler
	config  WorkerConfig
	logger  *slog.Logger
	now     func() time.Time
	// timeout bounds one pass.
	timeout time.Duration
	// slots holds one place for each pass running, Concurrency at most.
	slots chan struct{}
	// nudges says a job may be due now. It holds one, so nudges that arrive
	// together make one pass.
	nudges  chan struct{}
	running sync.WaitGroup
}

func NewWorker(queue jobQueue, handler jobHandler, config WorkerConfig, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	config = config.withDefaults()
	return &Worker{
		queue: queue, handler: handler, config: config, logger: logger, now: time.Now, timeout: jobTimeout,
		slots: make(chan struct{}, config.Concurrency), nudges: make(chan struct{}, 1),
	}
}

// HandleEvent wakes the pass of the request an interaction event is about, and
// nudges the worker. It is what agents' consumer calls for the detail types
// Handles names; an event about a request that was not asked in Slack is
// nothing. The event is at least once and may come in any order: waking a
// request again only asks for another look at the ledger. An error is an event
// that cannot be read, or a database that did not answer; the queue delivers it
// again either way.
func (worker *Worker) HandleEvent(ctx context.Context, detailType, eventID string, data []byte) error {
	wake, ours, err := wakeFromEvent(detailType, eventID, data)
	if err != nil {
		return err
	}
	if !ours {
		return nil
	}
	if err := worker.queue.Wake(ctx, wake); err != nil {
		return fmt.Errorf("event %s: %w", eventID, err)
	}
	worker.Nudge()
	worker.logger.InfoContext(ctx, "a Slack request's surfaces were woken",
		"event_id", eventID, "request_id", wake.RequestID, "organization_id", wake.OrganizationID, "detail_type", detailType)
	return nil
}

// Nudge says a job may be due now. It never waits.
func (worker *Worker) Nudge() {
	select {
	case worker.nudges <- struct{}{}:
	default:
	}
}

// Run does due passes until ctx ends: at once when nudged, and every Interval in
// any case. It returns once every pass it started has ended.
func (worker *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(worker.config.Interval)
	defer ticker.Stop()
	defer worker.running.Wait()
	// Jobs recorded before this start, or by a replica that has since gone, are
	// due now, not after the first tick.
	worker.Nudge()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			worker.tidy(ctx)
		case <-worker.nudges:
		}
		if err := worker.claimDue(ctx); err != nil && ctx.Err() == nil {
			worker.logger.ErrorContext(ctx, "claim surfaces jobs", "error", err)
		}
	}
}

// WakeDue is one of Run's passes that waits: it starts every due job there is
// room for and returns once they have all ended.
func (worker *Worker) WakeDue(ctx context.Context) error {
	worker.tidy(ctx)
	err := worker.claimDue(ctx)
	worker.running.Wait()
	return err
}

// tidy is what a pass does on the poll: it wakes the notices of requests that
// lapsed, which no event announces, and forgets what is old.
func (worker *Worker) tidy(ctx context.Context) {
	woken, err := worker.queue.WakeExpired(ctx, worker.now().UTC(), expiredPerSweep)
	switch {
	case err != nil && ctx.Err() == nil:
		worker.logger.WarnContext(ctx, "wake the notices of lapsed requests", "error", err)
	case woken > 0:
		worker.logger.InfoContext(ctx, "woke the notices of lapsed requests", "notices", woken)
		worker.Nudge()
	}
	if err := worker.queue.Purge(ctx, jobRetention, linkRetention); err != nil && ctx.Err() == nil {
		worker.logger.WarnContext(ctx, "purge old surfaces rows", "error", err)
	}
}

// claimDue claims due jobs while a slot is free and starts each without waiting
// for it. A job is claimed only once a slot holds a place for it, so none sits
// leased while it waits; one that ends nudges the worker, so what is due by
// then starts next.
func (worker *Worker) claimDue(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case worker.slots <- struct{}{}:
		default:
			return nil
		}
		claimed, err := worker.queue.Claim(ctx, worker.config.Lease, 1)
		if err != nil || len(claimed) == 0 {
			<-worker.slots
			return err
		}
		job := claimed[0]
		worker.running.Go(func() {
			defer worker.Nudge()
			defer func() { <-worker.slots }()
			worker.process(ctx, job)
		})
	}
}

// process runs one request's pass and settles it: done, given up on because it
// could never be done, or released to be tried again.
func (worker *Worker) process(ctx context.Context, job Job) {
	logger := worker.logger.With("request_id", job.RequestID, "organization_id", job.OrganizationID)
	attempt, cancel := context.WithTimeout(ctx, worker.timeout)
	err := worker.handle(attempt, job, logger)
	cancel()
	// Settling must survive a shutdown that interrupted the attempt.
	settle := context.WithoutCancel(ctx)
	switch {
	case err == nil:
		if err := worker.queue.Done(settle, job); err != nil {
			logger.ErrorContext(ctx, "finish surfaces job", "error", err)
			return
		}
		logger.InfoContext(ctx, "surfaces pass done", "attempts", job.Attempts+1)
	case slackrouter.IsPermanent(err):
		logger.WarnContext(ctx, "gave up on a request's surfaces: it cannot be done", "reason", err)
		if err := worker.queue.GiveUp(settle, job, err.Error()); err != nil {
			logger.ErrorContext(ctx, "give up surfaces job", "error", err)
		}
	case ctx.Err() != nil:
		// The service is stopping. The pass did not fail; it waits for the next
		// one to start.
		if _, err := worker.queue.Release(settle, job, false, maxAttempts, 0, "interrupted by shutdown"); err != nil {
			logger.ErrorContext(ctx, "release interrupted surfaces job", "error", err)
		}
	default:
		delay := min(max(retryDelay(job.Attempts+1), retryAfterOf(err)), maxRetryDelay)
		gaveUp, releaseErr := worker.queue.Release(settle, job, true, maxAttempts, delay, err.Error())
		switch {
		case releaseErr != nil:
			logger.ErrorContext(ctx, "release failed surfaces job", "error", releaseErr, "job_error", err)
		case gaveUp:
			logger.ErrorContext(ctx, "gave up on a request's surfaces after failing too often", "attempts", job.Attempts+1, "error", err)
		default:
			logger.WarnContext(ctx, "a request's surfaces failed and will be tried again",
				"attempts", job.Attempts+1, "retry_in", delay.String(), "error", err)
		}
	}
}

// handle runs the handler, and turns a panic in it into a failure of the pass.
// One request that trips a bug must not take the service down with it, and come
// back after every restart to do it again: it is counted as any other failure
// is, tried a few more times, and then given up on.
func (worker *Worker) handle(ctx context.Context, job Job, logger *slog.Logger) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.ErrorContext(ctx, "a surfaces pass panicked", "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
			err = fmt.Errorf("surfaces: the pass panicked: %v", recovered)
		}
	}()
	return worker.handler.Handle(ctx, job)
}

var _ jobQueue = (*Store)(nil)
