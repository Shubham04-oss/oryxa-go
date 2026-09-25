package interactiondelivery

import (
	"context"
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

// TestApprovedWorkWhoseAgentIsGoneOrDisabledIsClosed: an approval whose
// agent no longer exists, or has been disabled, can never be worked; its
// delivery is dropped at once and its request closed, saying which.
func TestApprovedWorkWhoseAgentIsGoneOrDisabledIsClosed(t *testing.T) {
	for _, testCase := range []struct {
		err    error
		reason string
	}{
		{managedagents.ErrNotFound, closedAgentGone},
		{managedagents.ErrDisabled, closedAgentDisabled},
	} {
		requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest(interactions.StatusApproved)}}
		store := &fakeDeliveries{pending: []Delivery{approvedDelivery(0)}}
		waker := NewWaker(store, &fakeAgentRuns{err: testCase.err}, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
		if err := waker.WakeDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		closed := requests.get("irq_1")
		if len(store.dropped) != 1 || closed.Status != interactions.StatusFailed || closed.Result == nil || closed.Result.Summary != testCase.reason {
			t.Fatalf("%v: dropped=%v request=%+v, want it dropped and the request failed saying %q", testCase.err, store.dropped, closed, testCase.reason)
		}
	}
}

// TestOnlyApprovedWorkIsClosedWhenDropped: a new request whose deciding run
// kept failing stays pending, for its member to decide or to expire, and
// one that run approved before failing is left for the approval's own
// delivery to work; a report whose run kept failing changes nothing about
// its request; and a request answered meanwhile is left answered.
func TestOnlyApprovedWorkIsClosedWhenDropped(t *testing.T) {
	report := testDelivery("evt_completed", KindRequesterCompleted, 3)
	report.RecipientUserID, report.RecipientAgentID, report.Status = "user_a", "agt_a", interactions.StatusCompleted
	requested := testDelivery("evt_requested", KindTargetRequested, 1)
	for name, testCase := range map[string]struct {
		delivery Delivery
		status   string
		// answered is what the request becomes while the run goes on.
		answered string
		want     string
	}{
		"a new request":                  {requested, interactions.StatusPending, "", interactions.StatusPending},
		"a new request approved at last": {requested, interactions.StatusPending, interactions.StatusApproved, interactions.StatusApproved},
		"an outcome's report":            {report, interactions.StatusCompleted, "", interactions.StatusCompleted},
		"approved work answered too":     {approvedDelivery(0), interactions.StatusApproved, interactions.StatusCompleted, interactions.StatusCompleted},
	} {
		t.Run(name, func(t *testing.T) {
			requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testRequest(testCase.status)}}
			runs := &fakeAgentRuns{fail: true}
			if testCase.answered != "" {
				runs.onExecute = func() { requests.setStatus("irq_1", testCase.answered) }
			}
			store := &fakeDeliveries{}
			waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
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
