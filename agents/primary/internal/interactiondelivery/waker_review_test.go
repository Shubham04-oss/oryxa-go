package interactiondelivery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
)

// inReview is irq_1 answered and held for its member's review.
func inReview(expires time.Time) agents.Interaction {
	request := allowing(testRequest(interactions.StatusApproved), agents.AppsToolboxName)
	request.Status, request.ExpiresAt = interactions.StatusReview, expires
	request.Held = &agents.InteractionHeld{Result: agents.InteractionResult{Status: interactions.StatusCompleted, Summary: "priya@example.com"}}
	return request
}

// TestTheWakerLeavesAnAnswerInReviewAlone: an answer held for review is not
// approved work. The approval's delivery is settled without a run, since
// the ledger already left approved; giving up on it closes nothing; and the
// poll's closing of expired approvals leaves it be. Only its expiry ends it,
// withheld by the poll, and only once it has expired.
func TestTheWakerLeavesAnAnswerInReviewAlone(t *testing.T) {
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": inReview(time.Now().Add(time.Hour))}}
	store := &fakeDeliveries{pending: []Delivery{approvedDelivery(0)}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 0 || len(store.done) != 1 || len(store.released) != 0 || len(store.dropped) != 0 {
		t.Fatalf("runs=%+v done=%v released=%v dropped=%v, want the approval's delivery settled without a run", runs.calls, store.done, store.released, store.dropped)
	}
	waker.giveUp(context.Background(), approvedDelivery(maxAttempts), closedAfterAttempts, waker.logger)
	if request := requests.get("irq_1"); request.Status != interactions.StatusReview || len(requests.closed) != 0 {
		t.Fatalf("giving up left %s, closed %v; want it in review", request.Status, requests.closed)
	}

	// Past its expiry, the poll withholds it, and closes nothing else.
	requests.set(inReview(time.Now().Add(-time.Minute)))
	waker.tidy(context.Background())
	withheld := requests.get("irq_1")
	if withheld.Status != interactions.StatusWithheld || withheld.Held != nil || withheld.Review == nil ||
		withheld.Review.Reason != interactions.ExpiredBeforeReview || withheld.Review.DecidedBy.Kind != interactions.DecidedBySystem {
		t.Fatalf("after its expiry the request is %+v with review %+v, want withheld by Oryxa, expired before review", withheld, withheld.Review)
	}
	if len(requests.closed) != 0 {
		t.Fatalf("the poll closed %v, want nothing", requests.closed)
	}
}

// TestAWithheldAnswerIsReportedAsWithheld: the member who asked hears from
// their agent that the answer was withheld, with nothing to read, and the
// message carries none of the request.
func TestAWithheldAnswerIsReportedAsWithheld(t *testing.T) {
	delivery := testDelivery("evt_withheld", KindRequesterCompleted, 4)
	delivery.RecipientUserID, delivery.RecipientAgentID, delivery.Status = "user_a", "agt_a", interactions.StatusWithheld
	withheld := inReview(time.Now().Add(time.Hour))
	withheld.Status, withheld.Held = interactions.StatusWithheld, nil
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": withheld}}
	store := &fakeDeliveries{pending: []Delivery{delivery}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 1 || len(store.done) != 1 || runs.calls[0].member.UserID != "user_a" {
		t.Fatalf("runs=%+v done=%v, want the requester's agent told", runs.calls, store.done)
	}
	message := runs.calls[0].input.Message
	for _, want := range []string{"withheld", "no result", `request_id "irq_1"`, "untrusted cross-user data"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message lacks %q: %s", want, message)
		}
	}
	if strings.Contains(message, "recorded a final result") || strings.Contains(message, "priya") || strings.Contains(message, "TOP SECRET") {
		t.Fatalf("the message says a result was recorded, or carries the request: %s", message)
	}
}
