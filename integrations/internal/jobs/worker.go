package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Handler does a job's work. A returned error fails the attempt; a job that
// should not be retried can't be told apart yet (no handler needs it).
type Handler func(ctx context.Context, job *Job) error

// Worker claims jobs and runs them.
type Worker struct {
	queue    *Queue
	id       string
	handlers map[string]Handler
	logger   *slog.Logger
	idle     time.Duration
}

// NewWorker returns a worker named id (unique per worker: it owns leases).
// A claimed job whose kind has no handler fails its attempt.
func NewWorker(queue *Queue, id string, handlers map[string]Handler, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Worker{queue: queue, id: id, handlers: handlers, logger: logger, idle: 2 * time.Second}
}

// RunOne claims one job and runs it to its end. It reports whether there
// was one. The lease is extended every HeartbeatEvery while the handler
// runs, and the handler's context ends if the lease is lost.
func (w *Worker) RunOne(ctx context.Context) (bool, error) {
	job, err := w.queue.Claim(ctx, w.id)
	if err != nil || job == nil {
		return false, err
	}
	handler := w.handlers[job.Kind]
	var runErr error
	if handler == nil {
		runErr = fmt.Errorf("no handler for %s jobs", job.Kind)
	} else {
		runErr = w.run(ctx, job, handler)
	}
	// The result is recorded even if ctx has ended, so shutdown does not
	// leave the job running until its lease expires.
	done, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if runErr == nil {
		err = w.queue.Complete(done, job)
	} else {
		_, err = w.queue.Fail(done, job, runErr)
	}
	if errors.Is(err, ErrLeaseLost) {
		w.logger.Warn("integrations job lease lost", "job", job.ID, "kind", job.Kind)
		return true, nil
	}
	return true, err
}

func (w *Worker) run(ctx context.Context, job *Job, handler Handler) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lost := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(lost)
		t := time.NewTicker(HeartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := w.queue.Heartbeat(ctx, job); errors.Is(err, ErrLeaseLost) {
					cancel()
					return
				} else if err != nil {
					w.logger.Warn("integrations job heartbeat failed", "job", job.ID, "error", err)
				}
			}
		}
	}()
	// A panicking handler fails the attempt rather than the service.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panicked: %v", r)
		}
		close(stop)
		<-lost
	}()
	return handler(ctx, job)
}

// Run claims and runs jobs until ctx ends, sleeping briefly when the queue is empty.
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		ran, err := w.RunOne(ctx)
		if err != nil {
			w.logger.Error("integrations worker failed", "worker", w.id, "error", err)
		}
		if ran && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(w.idle):
		}
	}
}
