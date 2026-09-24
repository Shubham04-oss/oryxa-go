package interactiondelivery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

func requestWithApproval(status, approval string) agents.Interaction {
	request := testRequest(status)
	request.TeamspaceID, request.Approval = "tsp_1", approval
	return request
}

// TestARequestHeldForItsOwnerWakesTheAgentOnlyOnceTheOwnerApproves: the new
// request's delivery completes without running anything, since only the
// member may decide it; the member's approval then wakes the agent, able to
// read their apps, told its owner approved and that it should do the work
// and answer rather than decide again.
func TestARequestHeldForItsOwnerWakesTheAgentOnlyOnceTheOwnerApproves(t *testing.T) {
	store := &fakeDeliveries{pending: []Delivery{testDelivery("evt_requested", KindTargetRequested, 1)}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": requestWithApproval(interactions.StatusPending, interactions.ApprovalOwner)}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 0 {
		t.Fatalf("a request held for its owner woke the agent: %+v", runs.calls)
	}
	if len(store.done) != 1 || store.done[0] != "evt_requested" || len(store.dropped) != 0 || len(store.released) != 0 {
		t.Fatalf("done=%v dropped=%v released=%v, want the delivery completed without a run", store.done, store.dropped, store.released)
	}

	// The owner approves in their own session; the decision's event arrives.
	requests.setStatus("irq_1", interactions.StatusApproved)
	approved := testDelivery("evt_approved", KindTargetApproved, 2)
	approved.Status = interactions.StatusApproved
	store.pending = []Delivery{approved}
	runs.onExecute = func() { requests.setStatus("irq_1", interactions.StatusCompleted) }
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 1 || len(store.done) != 2 || store.done[1] != "evt_approved" {
		t.Fatalf("calls=%+v done=%v, want one run that finished the approved work", runs.calls, store.done)
	}
	woken := runs.calls[0]
	if woken.agentID != "agt_b" || woken.member.UserID != "user_b" || woken.reach != managedagents.UnattendedReads {
		t.Fatalf("woken=%+v, want agt_b for user_b reading their apps", woken)
	}
	for _, want := range []string{"approved this request themself", "do not call interactions_decide", "interactions_respond", `request_id "irq_1"`, "untrusted cross-user data"} {
		if !strings.Contains(woken.input.Message, want) {
			t.Errorf("the owner-approved prompt lacks %q: %s", want, woken.input.Message)
		}
	}
	if strings.Contains(woken.input.Message, "TOP SECRET") {
		t.Fatalf("the prompt carried request content: %s", woken.input.Message)
	}
}

// TestAHeldRequestTheOwnerRejectedNeverWakesItsAgent: a rejection is the
// requester's news, as before; the addressed agent has nothing to do.
func TestAHeldRequestTheOwnerRejectedNeverWakesItsAgent(t *testing.T) {
	requested := testDelivery("evt_requested", KindTargetRequested, 1)
	store := &fakeDeliveries{pending: []Delivery{requested}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": requestWithApproval(interactions.StatusRejected, interactions.ApprovalOwner)}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 0 || len(store.done) != 1 {
		t.Fatalf("calls=%+v done=%v", runs.calls, store.done)
	}
}

// TestADelegatedTeamspaceRequestWakesItsAgentAsBefore: a specialist its
// owner contributed with automatic delegation is woken to decide it, without
// the member's apps, exactly as a request outside a teamspace is.
func TestADelegatedTeamspaceRequestWakesItsAgentAsBefore(t *testing.T) {
	store := &fakeDeliveries{pending: []Delivery{testDelivery("evt_requested", KindTargetRequested, 1)}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": requestWithApproval(interactions.StatusPending, interactions.ApprovalAgent)}}
	runs := &fakeAgentRuns{onExecute: func() { requests.setStatus("irq_1", interactions.StatusRejected) }}
	waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 1 || runs.calls[0].reach != managedagents.UnattendedQuiet || len(store.done) != 1 {
		t.Fatalf("calls=%+v done=%v, want one quiet run that decided", runs.calls, store.done)
	}
	if message := runs.calls[0].input.Message; !strings.Contains(message, "interactions_decide") || strings.Contains(message, "themself") {
		t.Fatalf("a delegated request was not asked to decide: %s", message)
	}
}

// TestOnlyApprovedWorkIsToldItsOwnerApproved: the owner-approved wording is
// for approved work alone, and the published Message keeps its old wording.
func TestOnlyApprovedWorkIsToldItsOwnerApproved(t *testing.T) {
	held := requestWithApproval(interactions.StatusApproved, interactions.ApprovalOwner)
	for _, kind := range []string{KindTargetRequested, KindRequesterRejected, KindRequesterCompleted} {
		if message := messageFor(testDelivery("evt_1", kind, 1), held); message != Message(testDelivery("evt_1", kind, 1)) {
			t.Errorf("%s was reworded for a request held for its owner: %s", kind, message)
		}
	}
	approved := testDelivery("evt_1", KindTargetApproved, 2)
	if strings.Contains(Message(approved), "themself") || !strings.Contains(messageFor(approved, held), "themself") {
		t.Fatalf("Message=%q messageFor=%q", Message(approved), messageFor(approved, held))
	}
}
