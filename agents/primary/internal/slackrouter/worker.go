package slackrouter

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	// maxAttempts is how many times running a job may fail before it is
	// given up on. A mention is answered in seconds or not at all in any way
	// that matters, so the last try comes a few minutes after the first.
	maxAttempts = 5
	// firstRetry is the delay after the first failure; each failure after it
	// doubles the delay, up to maxRetryDelay.
	firstRetry    = 15 * time.Second
	maxRetryDelay = 5 * time.Minute
	// jobTimeout bounds one attempt. It is less than any lease, so a job is
	// never running under a lease that has run out.
	jobTimeout = 90 * time.Second
	// handledRetention is how long a handled job's row is kept: apps purges
	// its inbound row after seven days, so past that the same one cannot come
	// back.
	handledRetention = 7 * 24 * time.Hour
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

// jobQueue is the durable side of the worker. Store implements it.
type jobQueue interface {
	Enqueue(context.Context, Job) (bool, error)
	Claim(ctx context.Context, lease time.Duration, limit int) ([]Job, error)
	Done(ctx context.Context, inboundID string) error
	Drop(ctx context.Context, inboundID, reason string) error
	Release(ctx context.Context, inboundID string, failed bool, maxAttempts int, retryAfter time.Duration, reason string) (bool, error)
	Purge(ctx context.Context, age time.Duration) error
}

// jobHandler routes one job. Router implements it.
type jobHandler interface {
	Handle(ctx context.Context, job Job) error
}

// WorkerConfig sizes the worker.
type WorkerConfig struct {
	// Interval is how often the worker looks for due jobs without being
	// nudged: the backstop for a job released to be retried later, and for
	// one another replica recorded.
	Interval time.Duration
	// Lease is how long a claimed job stays its claimer's. It must outlast
	// one attempt.
	Lease time.Duration
	// Concurrency is how many jobs run at once.
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

// Worker turns the events apps publishes into jobs and runs them: the same
// pattern interactiondelivery uses. Recording a job never calls Slack, a
// model or another service; the events are ids only.
type Worker struct {
	queue   jobQueue
	handler jobHandler
	config  WorkerConfig
	logger  *slog.Logger
	now     func() time.Time
	// timeout bounds one attempt.
	timeout time.Duration
	// slots holds one place for each job running, Concurrency at most.
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

// HandleEvent records the job a Slack inbound event asks for, and nudges the
// worker when it is new. It is what agents' consumer calls for the detail
// types Handles names. The event is at least once: recording the same inbound
// row again does nothing, whichever event it came in, and an error here is
// only a database that did not answer, for the queue to deliver again.
func (worker *Worker) HandleEvent(ctx context.Context, detailType, eventID string, data []byte) error {
	job, err := jobFromEvent(detailType, eventID, data, worker.now().UTC())
	if err != nil {
		return err
	}
	added, err := worker.queue.Enqueue(ctx, job)
	if err != nil {
		return fmt.Errorf("event %s: %w", eventID, err)
	}
	if added {
		worker.Nudge()
	}
	worker.logger.InfoContext(ctx, "slack inbound recorded",
		"event_id", eventID, "inbound_id", job.InboundID, "organization_id", job.OrganizationID,
		"kind", job.Kind, "new", added)
	return nil
}

// Nudge says a job may be due now. It never waits.
func (worker *Worker) Nudge() {
	select {
	case worker.nudges <- struct{}{}:
	default:
	}
}

// Run routes due jobs until ctx ends: at once when nudged, and every Interval
// in any case. It returns once every job it started has ended.
func (worker *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(worker.config.Interval)
	defer ticker.Stop()
	defer worker.running.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			worker.tidy(ctx)
		case <-worker.nudges:
		}
		if err := worker.claimDue(ctx); err != nil && ctx.Err() == nil {
			worker.logger.ErrorContext(ctx, "claim Slack jobs", "error", err)
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

func (worker *Worker) tidy(ctx context.Context) {
	if err := worker.queue.Purge(ctx, handledRetention); err != nil && ctx.Err() == nil {
		worker.logger.WarnContext(ctx, "purge handled Slack jobs", "error", err)
	}
}

// claimDue claims due jobs while a slot is free and starts each without
// waiting for it. A job is claimed only once a slot holds a place for it, so
// none sits leased while it waits; one that ends nudges the worker, so what
// is due by then starts next.
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

// process runs one job and settles it: done, dropped because it could never
// be routed, or released to be tried again.
func (worker *Worker) process(ctx context.Context, job Job) {
	logger := worker.logger.With("inbound_id", job.InboundID, "organization_id", job.OrganizationID, "kind", job.Kind)
	attempt, cancel := context.WithTimeout(ctx, worker.timeout)
	err := worker.handler.Handle(attempt, job)
	cancel()
	// Settling must survive a shutdown that interrupted the attempt.
	settle := context.WithoutCancel(ctx)
	switch {
	case err == nil:
		if err := worker.queue.Done(settle, job.InboundID); err != nil {
			logger.ErrorContext(ctx, "finish Slack job", "error", err)
		}
	case IsPermanent(err):
		logger.WarnContext(ctx, "dropped a Slack job that cannot be routed", "reason", err)
		if err := worker.queue.Drop(settle, job.InboundID, err.Error()); err != nil {
			logger.ErrorContext(ctx, "drop Slack job", "error", err)
		}
	case ctx.Err() != nil:
		// The service is stopping. The job did not fail; it waits for the
		// next one to start.
		if _, err := worker.queue.Release(settle, job.InboundID, false, maxAttempts, 0, "interrupted by shutdown"); err != nil {
			logger.ErrorContext(ctx, "release interrupted Slack job", "error", err)
		}
	default:
		delay := min(max(retryDelay(job.Attempts+1), retryAfterOf(err)), maxRetryDelay)
		dropped, releaseErr := worker.queue.Release(settle, job.InboundID, true, maxAttempts, delay, err.Error())
		switch {
		case releaseErr != nil:
			logger.ErrorContext(ctx, "release failed Slack job", "error", releaseErr, "job_error", err)
		case dropped:
			logger.ErrorContext(ctx, "gave up on a Slack job", "attempts", job.Attempts+1, "error", err)
		default:
			logger.WarnContext(ctx, "a Slack job failed and will be tried again",
				"attempts", job.Attempts+1, "retry_in", delay.String(), "error", err)
		}
	}
}
