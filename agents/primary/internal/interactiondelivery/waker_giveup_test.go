package interactiondelivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

// approvedDelivery is the approval's delivery of irq_1 to its agent, on its
// attempt'th try.
func approvedDelivery(attempt int) Delivery {
	delivery := testDelivery("evt_approved", KindTargetApproved, 2)
	delivery.Status, delivery.Attempts = interactions.StatusApproved, attempt
	return delivery
}

// wakeUntilDropped delivers the same delivery once per attempt the waker
// allows, as the store hands it back after each failure, and says after each
// attempt whether the request had been closed.
func wakeUntilDropped(t *testing.T, store *fakeDeliveries, requests *fakeRequests, waker *Waker, delivery Delivery) {
	t.Helper()
	for attempt := range maxAttempts {
		delivery.Attempts = attempt
		store.pending = []Delivery{delivery}
		if err := waker.WakeDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if last := attempt == maxAttempts-1; !last && len(requests.closed) != 0 {
			t.Fatalf("attempt %d of %d closed the request already", attempt+1, maxAttempts)
		}
	}
	if len(store.dropped) != 1 || store.dropped[0] != delivery.EventID {
		t.Fatalf("dropped=%v, want the delivery dropped after its last attempt", store.dropped)
	}
}

// TestApprovedWorkThatKeepsFailingIsClosedWhenItIsDropped: approved work
// whose runs keep failing is dropped after its last attempt, and its request
// is closed then, failed with the reason, rather than left approved with
// nobody working on it; the member who asked hears of it from the failed
// result's event.
func TestApprovedWorkThatKeepsFailingIsClosedWhenItIsDropped(t *testing.T) {
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest(interactions.StatusApproved)}}
	store := &fakeDeliveries{}
	waker := NewWaker(store, &fakeAgentRuns{fail: true}, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	wakeUntilDropped(t, store, requests, waker, approvedDelivery(0))

	closed := requests.get("irq_1")
	if len(requests.closed) != 1 || closed.Status != interactions.StatusFailed || closed.Result == nil || closed.Result.Summary != closedAfterAttempts {
		t.Fatalf("closed %v, request %+v; want it failed once, saying why", requests.closed, closed)
	}
}

// TestApprovedWorkWhoseAgentIsGoneIsClosed: an approval whose agent was
// deleted can never be worked; its delivery is dropped at once and its
// request closed, saying so.
func TestApprovedWorkWhoseAgentIsGoneIsClosed(t *testing.T) {
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest(interactions.StatusApproved)}}
	store := &fakeDeliveries{pending: []Delivery{approvedDelivery(0)}}
	waker := NewWaker(store, &fakeAgentRuns{err: managedagents.ErrNotFound}, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed := requests.get("irq_1")
	if len(store.dropped) != 1 || closed.Status != interactions.StatusFailed || closed.Result == nil || closed.Result.Summary != closedAgentGone {
		t.Fatalf("dropped=%v request=%+v, want it dropped and the request failed saying its agent is gone", store.dropped, closed)
	}
}

// TestADeliveryWaitsForItsDisabledAgent: an agent its member disabled may be
// turned on again, so a delivery that asks it to decide or to work is not
// given up: it is released to be tried again, uncounted, and its request is
// left as it is. Only the request's expiry ends the wait, and approved work
// that runs out is closed by the poll. A report to a disabled requester's
// agent is dropped, as it always was, since nothing else would end its wait.
func TestADeliveryWaitsForItsDisabledAgent(t *testing.T) {
	report := testDelivery("evt_completed", KindRequesterCompleted, 3)
	report.RecipientUserID, report.RecipientAgentID, report.Status = "user_a", "agt_a", interactions.StatusCompleted
	for name, testCase := range map[string]struct {
		delivery Delivery
		status   string
		waits    bool
	}{
		"approved work":       {approvedDelivery(maxAttempts - 1), interactions.StatusApproved, true},
		"an outcome's report": {report, interactions.StatusCompleted, false},
	} {
		t.Run(name, func(t *testing.T) {
			requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": decidable(testCase.status)}}
			store := &fakeDeliveries{pending: []Delivery{testCase.delivery}}
			waker := NewWaker(store, &fakeAgentRuns{err: managedagents.ErrDisabled}, requests, standsStill(), nil)
			if err := waker.WakeDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			failed, released := store.released[testCase.delivery.EventID]
			if waits := released && !failed && len(store.dropped) == 0; waits != testCase.waits {
				t.Fatalf("released=%v dropped=%v, want waiting %t", store.released, store.dropped, testCase.waits)
			}
			if got := requests.get("irq_1"); len(requests.closed) != 0 || got.Status != testCase.status {
				t.Fatalf("closed=%v request=%+v, want it left %s", requests.closed, got, testCase.status)
			}
		})
	}

	// The approval runs out while its agent is still disabled: the poll
	// closes it, saying it expired.
	expired := testRequest(interactions.StatusApproved)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": expired}}
	waker := NewWaker(&fakeDeliveries{pending: []Delivery{approvedDelivery(0)}}, &fakeAgentRuns{err: managedagents.ErrDisabled}, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closed := requests.get("irq_1"); closed.Status != interactions.StatusFailed || closed.Result == nil || closed.Result.Summary != closedExpired {
		t.Fatalf("the approval that ran out = %+v, want it failed saying it expired", closed)
	}
}

// TestOnlyApprovedWorkIsClosedWhenDropped: a report whose run kept failing
// changes nothing about its request, and a request answered meanwhile is left
// answered. (A new request starts no run to fail.)
func TestOnlyApprovedWorkIsClosedWhenDropped(t *testing.T) {
	report := testDelivery("evt_completed", KindRequesterCompleted, 3)
	report.RecipientUserID, report.RecipientAgentID, report.Status = "user_a", "agt_a", interactions.StatusCompleted
	for name, testCase := range map[string]struct {
		delivery Delivery
		status   string
		// answered is what the request becomes while the run goes on.
		answered string
		want     string
	}{
		"an outcome's report":        {report, interactions.StatusCompleted, "", interactions.StatusCompleted},
		"approved work answered too": {approvedDelivery(0), interactions.StatusApproved, interactions.StatusCompleted, interactions.StatusCompleted},
	} {
		t.Run(name, func(t *testing.T) {
			requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": decidable(testCase.status)}}
			runs := &fakeAgentRuns{fail: true}
			if testCase.answered != "" {
				runs.onExecute = func() { requests.setStatus("irq_1", testCase.answered) }
			}
			store := &fakeDeliveries{}
			waker := NewWaker(store, runs, requests, standsStill(), nil)
			if testCase.answered != "" {
				// The run answers, and fails after, on its last attempt.
				store.failures = map[string]int{testCase.delivery.EventID: maxAttempts - 1}
				store.released = map[string]bool{}
				store.pending = []Delivery{testCase.delivery}
				if err := waker.WakeDue(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				wakeUntilDropped(t, store, requests, waker, testCase.delivery)
			}
			if got := requests.get("irq_1"); len(requests.closed) != 0 || got.Status != testCase.want || got.Result != nil {
				t.Fatalf("closed=%v request=%+v, want it left %s", requests.closed, got, testCase.want)
			}
		})
	}
}

// TestAnApprovalThatExpiredUnansweredIsClosedOnThePoll: an approved request
// whose expiry passed lately before anyone answered it can never be
// answered, so the poll closes it, saying so, whether or not a delivery is
// still about. One that ran out longer ago than the window, a pending request
// that ran out, and an approval still in time, are left.
func TestAnApprovalThatExpiredUnansweredIsClosedOnThePoll(t *testing.T) {
	expiredApproval := testRequest(interactions.StatusApproved)
	expiredApproval.ExpiresAt = time.Now().Add(-time.Minute)
	expiredPending := testRequest(interactions.StatusPending)
	expiredPending.ID, expiredPending.ExpiresAt = "irq_2", time.Now().Add(-time.Minute)
	inTime := testRequest(interactions.StatusApproved)
	inTime.ID = "irq_3"
	longAgo := testRequest(interactions.StatusApproved)
	longAgo.ID, longAgo.ExpiresAt = "irq_4", time.Now().Add(-expiredWithin-time.Minute)
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": expiredApproval, "irq_2": expiredPending, "irq_3": inTime, "irq_4": longAgo}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(&fakeDeliveries{}, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed := requests.get("irq_1")
	if len(requests.closed) != 1 || closed.Status != interactions.StatusFailed || closed.Result == nil || closed.Result.Summary != closedExpired {
		t.Fatalf("closed %v, irq_1 %+v; want only the lately expired approval failed, saying it expired", requests.closed, closed)
	}
	for id, status := range map[string]string{"irq_2": interactions.StatusPending, "irq_3": interactions.StatusApproved, "irq_4": interactions.StatusApproved} {
		if got := requests.get(id); got.Status != status || got.Result != nil {
			t.Fatalf("%s = %+v, want it left %s", id, got, status)
		}
	}
	if len(runs.calls) != 0 {
		t.Fatalf("runs=%d, want nothing run", len(runs.calls))
	}
}

// TestADeliveryCutShortByAStopIsNotAFailedAttempt: a run the waker's stop
// cancels, a deploy say, did not fail. On what would be its last attempt its
// delivery is released to be tried again, uncounted, and its request is not
// closed.
func TestADeliveryCutShortByAStopIsNotAFailedAttempt(t *testing.T) {
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest(interactions.StatusApproved)}}
	store := &fakeDeliveries{
		pending:  []Delivery{approvedDelivery(maxAttempts - 1)},
		failures: map[string]int{"evt_approved": maxAttempts - 1}, released: map[string]bool{},
	}
	ctx, stop := context.WithCancel(context.Background())
	runs := &fakeAgentRuns{fail: true, onExecute: stop}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	_ = waker.WakeDue(ctx)
	if failed, released := store.released["evt_approved"]; !released || failed || len(store.dropped) != 0 {
		t.Fatalf("released=%v dropped=%v, want it released uncounted, not dropped", store.released, store.dropped)
	}
	if len(requests.closed) != 0 || requests.get("irq_1").Status != interactions.StatusApproved {
		t.Fatalf("closed=%v, want the request left approved", requests.closed)
	}
}

// TestOryxaClosesARequestInUTC: when Oryxa closes a request, the time its
// failed result was completed, and the times the poll looks between, are
// UTC, as every other time the ledger writes is, never this process's zone.
func TestOryxaClosesARequestInUTC(t *testing.T) {
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest(interactions.StatusApproved)}}
	store := &fakeDeliveries{pending: []Delivery{approvedDelivery(0)}}
	waker := NewWaker(store, &fakeAgentRuns{err: managedagents.ErrNotFound}, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed := requests.get("irq_1")
	if closed.Result == nil || closed.Result.CompletedAt.Location() != time.UTC {
		t.Fatalf("the closed request's result = %+v, want it completed at a UTC time", closed.Result)
	}
	if since, at := requests.swept[0], requests.swept[1]; since.Location() != time.UTC || at.Location() != time.UTC {
		t.Fatalf("the poll closed between %v and %v, want UTC times", since, at)
	}
}

// TestNothingIsClosedWhenTheDropIsNotRecorded: when releasing approved work
// on its last attempt fails, even with the drop made before a commit that
// did not happen, the waker does not know the delivery was given up, so it
// leaves the request approved; the delivery comes round again.
func TestNothingIsClosedWhenTheDropIsNotRecorded(t *testing.T) {
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest(interactions.StatusApproved)}}
	store := &fakeDeliveries{
		pending:  []Delivery{approvedDelivery(maxAttempts - 1)},
		failures: map[string]int{"evt_approved": maxAttempts - 1}, released: map[string]bool{},
		releaseErr: errors.New("could not commit the release"),
	}
	waker := NewWaker(store, &fakeAgentRuns{fail: true}, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, released := store.released["evt_approved"]; !released {
		t.Fatal("the failed run's delivery was never released")
	}
	if got := requests.get("irq_1"); len(requests.closed) != 0 || got.Status != interactions.StatusApproved {
		t.Fatalf("closed=%v request=%+v, want it left approved", requests.closed, got)
	}
}
