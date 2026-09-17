package application

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestJitter checks jitter's bounds: within a tenth of interval either way,
// and unchanged when interval is too short to have a tenth, where
// rand.Int64N would otherwise be called with n <= 0 and panic.
func TestJitter(t *testing.T) {
	const interval = time.Minute
	low, high := interval-interval/10, interval+interval/10
	for range 200 {
		if got := jitter(interval); got < low || got > high {
			t.Fatalf("jitter(%s) = %s, want within [%s, %s]", interval, got, low, high)
		}
	}
	if got := jitter(5 * time.Nanosecond); got != 5*time.Nanosecond {
		t.Fatalf("jitter(5ns) = %s, want 5ns unchanged", got)
	}
	if got := jitter(0); got != 0 {
		t.Fatalf("jitter(0) = %s, want 0", got)
	}
}

// TestRunEveryDrainsFullBatchesBeforeSleeping checks the inner loop: it calls
// work again immediately while a batch comes back full, and only reaches the
// interval wait once one comes back short. The interval is an hour, so if
// runEvery waited between the full batches, the next assertion's one-second
// budget would catch it.
func TestRunEveryDrainsFullBatchesBeforeSleeping(t *testing.T) {
	h := newHarness(t)
	batches := []int{workerBatch, workerBatch, 3}
	calls := make(chan int, len(batches)+1)
	index := 0
	work := func(context.Context) (int, error) {
		handled := 0
		if index < len(batches) {
			handled = batches[index]
		}
		index++
		calls <- handled
		return handled, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		h.service.runEvery(ctx, "test", time.Hour, work)
		close(done)
	}()

	for i, want := range batches {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("call %d handled %d, want %d", i+1, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("call %d never happened; runEvery is not draining full batches", i+1)
		}
	}
	// The last batch went short, so runEvery should now be waiting out the
	// hour-long interval rather than calling work again.
	select {
	case <-calls:
		t.Fatal("runEvery called work again instead of waiting after a short batch")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runEvery did not return after cancellation")
	}
}

// TestRunEveryReturnsPromptlyOnCancellation checks that cancelling ctx while
// runEvery waits out the interval ends the loop at once, rather than after
// the full hour-long interval.
func TestRunEveryReturnsPromptlyOnCancellation(t *testing.T) {
	h := newHarness(t)
	work := func(context.Context) (int, error) { return 0, nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.service.runEvery(ctx, "test", time.Hour, work)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond) // let it reach the interval wait
	started := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runEvery did not return after cancellation")
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Fatalf("runEvery took %s to return after cancellation, want well under the hour-long interval", elapsed)
	}
}

// TestRunEveryWaitsAboutTheIntervalBetweenShortBatches checks that the wait
// between empty batches is close to interval, jitter included, rather than
// none at all or some unrelated duration.
func TestRunEveryWaitsAboutTheIntervalBetweenShortBatches(t *testing.T) {
	h := newHarness(t)
	const interval = 40 * time.Millisecond
	calls := make(chan time.Time, 4)
	work := func(context.Context) (int, error) {
		calls <- time.Now()
		return 0, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.service.runEvery(ctx, "test", interval, work)

	var calledAt []time.Time
	for i := range 4 {
		select {
		case at := <-calls:
			calledAt = append(calledAt, at)
		case <-time.After(2 * time.Second):
			t.Fatalf("call %d never happened", i+1)
		}
	}
	for i := 1; i < len(calledAt); i++ {
		if gap := calledAt[i].Sub(calledAt[i-1]); gap < interval/2 || gap > interval*2 {
			t.Fatalf("gap between calls %d and %d = %s, want roughly %s", i, i+1, gap, interval)
		}
	}
}

// TestRunEveryLogsAndContinuesAfterAnError checks that a failing call to work
// is logged, by name, and does not stop the loop.
func TestRunEveryLogsAndContinuesAfterAnError(t *testing.T) {
	var buf bytes.Buffer
	h := newHarness(t, func(dependencies *Dependencies) { dependencies.Logger = slog.New(slog.NewTextHandler(&buf, nil)) })
	boom := errors.New("boom")
	calls := make(chan struct{}, 4)
	first := true
	work := func(context.Context) (int, error) {
		calls <- struct{}{}
		if first {
			first = false
			return 0, boom
		}
		return 0, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.service.runEvery(ctx, "worker-test", 20*time.Millisecond, work)

	for i := range 2 {
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("call %d never happened; runEvery stopped after the error", i+1)
		}
	}
	if log := buf.String(); !strings.Contains(log, "worker-test failed") || !strings.Contains(log, boom.Error()) {
		t.Fatalf("log = %s, want it to record the failure by name", log)
	}
}

// TestRunHeartbeatAndRunCleanupDelegateToRunEvery checks that both exported
// loops actually run their work through runEvery: each returns promptly once
// ctx is cancelled, rather than only after CheckHealth's or CleanUp's own
// interval, and each logs the failure runEvery sees when the fake store
// cannot serve it (fakes_uncovered_test.go leaves ClaimHealthChecks and
// ClaimCredentialDeletions unimplemented on purpose).
func TestRunHeartbeatAndRunCleanupDelegateToRunEvery(t *testing.T) {
	for name, pick := range map[string]func(*Service) func(context.Context){
		"RunHeartbeat": func(service *Service) func(context.Context) { return service.RunHeartbeat },
		"RunCleanup":   func(service *Service) func(context.Context) { return service.RunCleanup },
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			h := newHarness(t, func(dependencies *Dependencies) { dependencies.Logger = slog.New(slog.NewTextHandler(&buf, nil)) })
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			run := pick(h.service)
			go func() {
				run(ctx)
				close(done)
			}()
			time.Sleep(100 * time.Millisecond) // let at least one iteration fail against the fake store
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s did not return after cancellation", name)
			}
			if log := buf.String(); !strings.Contains(log, "failed") || !strings.Contains(log, errUncovered.Error()) {
				t.Fatalf("%s log = %s, want it to log the fake store's error", name, log)
			}
		})
	}
}
