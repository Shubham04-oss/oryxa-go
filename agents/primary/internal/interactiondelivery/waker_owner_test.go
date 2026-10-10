package interactiondelivery

import (
	"context"
	"errors"
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
	request.Requester = agents.InteractionParty{UserID: "user_a", AgentID: "agt_a", AgentName: "Crew"}
	request.Target = agents.InteractionParty{UserID: "user_b", AgentID: "agt_b", AgentName: "Carl"}
	return request
}

// fakeStanding reads every request again with the same verdict, recording
// which it was asked about; Stands unless told otherwise.
type fakeStanding struct {
	verdict interactions.Verdict
	err     error
	asked   []string
}

func (standing *fakeStanding) Recheck(_ context.Context, request agents.Interaction) (agents.Interaction, interactions.Verdict, error) {
	standing.asked = append(standing.asked, request.ID)
	if standing.err != nil {
		return request, "", standing.err
	}
	if standing.verdict == "" {
		return request, interactions.VerdictStands, nil
	}
	return request, standing.verdict, nil
}

// standsStill is a waker config whose teamspace requests still stand.
func standsStill() Config {
	return Config{Interval: time.Second, Lease: time.Minute, Standing: &fakeStanding{}}
}

// decidedBy is the request approved, as decider recorded it, allowing the
// member's apps and knowledge.
func decidedBy(request agents.Interaction, decider *agents.InteractionDecider) agents.Interaction {
	request.Status = interactions.StatusApproved
	request.Decision = &agents.InteractionDecision{
		Decision: interactions.DecisionApproved, Message: "Go ahead.", DecidedAt: time.Now(), DecidedBy: decider,
		AllowedTools: []string{agents.AppsToolboxName, agents.KnowledgeToolboxName},
	}
	return request
}

// byTheMember is user_b, the addressed agent's member, in their own session.
var byTheMember = &agents.InteractionDecider{Kind: interactions.DecidedByMember, UserID: "user_b"}

func (requests *fakeRequests) set(request agents.Interaction) {
	requests.mu.Lock()
	defer requests.mu.Unlock()
	requests.requests[request.ID] = request
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
	waker := NewWaker(store, runs, requests, standsStill(), nil)
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
	requests.set(decidedBy(requestWithApproval(interactions.StatusPending, interactions.ApprovalOwner), byTheMember))
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
	if woken.agentID != "agt_b" || woken.member.UserID != "user_b" || woken.reach != managedagents.DeliveryReads {
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

// TestAHeldRequestTheOwnerRejectsReachesOnlyTheRequester: the request's own
// delivery, while it waits for the owner, wakes nobody; when the owner
// rejects it, the rejection is the requester's news, told exactly as any
// rejection is, and the addressed agent never runs at all.
func TestAHeldRequestTheOwnerRejectsReachesOnlyTheRequester(t *testing.T) {
	store := &fakeDeliveries{pending: []Delivery{testDelivery("evt_requested", KindTargetRequested, 1)}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": requestWithApproval(interactions.StatusPending, interactions.ApprovalOwner)}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, requests, standsStill(), nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 0 || len(store.done) != 1 {
		t.Fatalf("while held: calls=%+v done=%v, want the delivery completed without a run", runs.calls, store.done)
	}

	// The owner rejects it in their own session; the decision's event arrives.
	requests.setStatus("irq_1", interactions.StatusRejected)
	rejected := testDelivery("evt_rejected", KindRequesterRejected, 2)
	rejected.RecipientUserID, rejected.RecipientAgentID, rejected.Status = "user_a", "agt_a", interactions.StatusRejected
	store.pending = []Delivery{rejected}
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 1 || len(store.done) != 2 {
		t.Fatalf("after the rejection: calls=%+v done=%v, want one run for the requester", runs.calls, store.done)
	}
	told := runs.calls[0]
	if told.agentID != "agt_a" || told.member.UserID != "user_a" || told.reach != managedagents.DeliveryReport || told.input.Message != Message(rejected) {
		t.Fatalf("told=%+v, want the requester's agent told of the rejection as before", told)
	}
}

// TestADelegatedTeamspaceRequestNoLongerWakesItsAgent: a request to a
// specialist its owner contributed with automatic delegation is still the
// specialist's to decide, but only in a conversation its member started: its
// delivery wakes nobody, as a request held for its owner's does, and the
// request waits.
func TestADelegatedTeamspaceRequestNoLongerWakesItsAgent(t *testing.T) {
	store := &fakeDeliveries{pending: []Delivery{testDelivery("evt_requested", KindTargetRequested, 1)}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": requestWithApproval(interactions.StatusPending, interactions.ApprovalAgent)}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, requests, standsStill(), nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 0 || len(store.done) != 1 || requests.get("irq_1").Status != interactions.StatusPending {
		t.Fatalf("calls=%+v done=%v, want no run, the delivery done, the request pending", runs.calls, store.done)
	}
}

// TestOnlyApprovedWorkIsToldItsOwnerApproved: the owner-approved wording is
// for approved work alone, and the published Message keeps its old wording.
func TestOnlyApprovedWorkIsToldItsOwnerApproved(t *testing.T) {
	held := decidedBy(requestWithApproval(interactions.StatusPending, interactions.ApprovalOwner), byTheMember)
	for _, kind := range []string{KindTargetRequested, KindRequesterRejected, KindRequesterCompleted} {
		if message := messageFor(testDelivery("evt_1", kind, 1), held, between{}); message != Message(testDelivery("evt_1", kind, 1)) {
			t.Errorf("%s was reworded for a request held for its owner: %s", kind, message)
		}
	}
	approved := testDelivery("evt_1", KindTargetApproved, 2)
	if strings.Contains(Message(approved), "themself") || !strings.Contains(messageFor(approved, held, between{}), "themself") {
		t.Fatalf("Message=%q messageFor=%q", Message(approved), messageFor(approved, held, between{}))
	}
}

// TestAnApprovalNobodyKnowsNeverWakesTheAgentToDecide: the waker holds what
// the ledger holds, failing closed on an approval it does not know.
func TestAnApprovalNobodyKnowsNeverWakesTheAgentToDecide(t *testing.T) {
	store := &fakeDeliveries{pending: []Delivery{testDelivery("evt_requested", KindTargetRequested, 1)}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": requestWithApproval(interactions.StatusPending, "team")}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, requests, standsStill(), nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 0 || len(store.done) != 1 {
		t.Fatalf("calls=%+v done=%v, want the delivery completed without a run", runs.calls, store.done)
	}
}

// TestATeamspaceRequestNamingNoApprovalNeverWakesTheAgentToDecide: the waker
// fails closed with the ledger — a request made in a teamspace that names no
// approval is its owner's, and its delivery wakes nobody.
func TestATeamspaceRequestNamingNoApprovalNeverWakesTheAgentToDecide(t *testing.T) {
	store := &fakeDeliveries{pending: []Delivery{testDelivery("evt_requested", KindTargetRequested, 1)}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": requestWithApproval(interactions.StatusPending, "")}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, requests, standsStill(), nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 0 || len(store.done) != 1 {
		t.Fatalf("calls=%+v done=%v, want the delivery completed without a run", runs.calls, store.done)
	}
}

// TestTheOwnerApprovedWordingFollowsWhoDecided: the agent is told its member
// approved in person only when the decision records the member as its
// decider. A held request an agent approved — which only an older build's
// Decide could do — or whose decision names no decider is not the member's
// approval, whatever its approval says; and a request its member approved in
// person is, even one outside any teamspace.
func TestTheOwnerApprovedWordingFollowsWhoDecided(t *testing.T) {
	approved := testDelivery("evt_approved", KindTargetApproved, 2)
	held := requestWithApproval(interactions.StatusPending, interactions.ApprovalOwner)
	outside := held
	outside.TeamspaceID, outside.Approval = "", interactions.ApprovalAgent
	for name, testCase := range map[string]struct {
		request agents.Interaction
		owner   bool
	}{
		"held, approved by its member":            {decidedBy(held, byTheMember), true},
		"held, approved by an agent":              {decidedBy(held, &agents.InteractionDecider{Kind: interactions.DecidedByAgent, UserID: "user_b", AgentID: "agt_b"}), false},
		"held, approved by nobody recorded":       {decidedBy(held, nil), false},
		"held, approved by another member":        {decidedBy(held, &agents.InteractionDecider{Kind: interactions.DecidedByMember, UserID: "user_a"}), false},
		"outside a teamspace, approved by member": {decidedBy(outside, byTheMember), true},
	} {
		if told := strings.Contains(messageFor(approved, testCase.request, between{}), "approved this request themself"); told != testCase.owner {
			t.Errorf("%s: told the member approved = %t, want %t", name, told, testCase.owner)
		}
	}
}

// TestTheWakerReadsATeamspaceRequestAgainBeforeWakingItsAgent: a request
// made in a teamspace is read again before its own delivery or its
// approval's is finished, and an approval's wakes the agent only if it still
// stands. Its own delivery wakes nobody either way; one held for its owner, or
// closed, wakes nobody and its delivery is done; one that could not be read is
// retried; and without a way to read it at all, it wakes nobody.
func TestTheWakerReadsATeamspaceRequestAgainBeforeWakingItsAgent(t *testing.T) {
	approvedDelivery := testDelivery("evt_approved", KindTargetApproved, 2)
	approvedDelivery.Status = interactions.StatusApproved
	delegated := requestWithApproval(interactions.StatusPending, interactions.ApprovalAgent)
	for _, testCase := range []struct {
		name     string
		delivery Delivery
		request  agents.Interaction
		standing Standing
		woken    bool
		done     bool
	}{
		{"its own delivery, still standing", testDelivery("evt_requested", KindTargetRequested, 1), delegated, &fakeStanding{}, false, true},
		{"its own delivery, now held", testDelivery("evt_requested", KindTargetRequested, 1), delegated, &fakeStanding{verdict: interactions.VerdictHeld}, false, true},
		{"its own delivery, closed", testDelivery("evt_requested", KindTargetRequested, 1), delegated, &fakeStanding{verdict: interactions.VerdictClosed}, false, true},
		{"its approval, closed", approvedDelivery, decidedBy(delegated, byTheMember), &fakeStanding{verdict: interactions.VerdictClosed}, false, true},
		{"its approval, held again", approvedDelivery, decidedBy(delegated, byTheMember), &fakeStanding{verdict: interactions.VerdictHeld}, false, true},
		{"not read again", testDelivery("evt_requested", KindTargetRequested, 1), delegated, &fakeStanding{err: errors.New("auth did not answer")}, false, false},
		{"no way to read it again", approvedDelivery, decidedBy(delegated, byTheMember), nil, false, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			store := &fakeDeliveries{pending: []Delivery{testCase.delivery}}
			requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testCase.request}}
			runs := &fakeAgentRuns{onExecute: func() { requests.setStatus("irq_1", interactions.StatusCompleted) }}
			waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute, Standing: testCase.standing}, nil)
			if err := waker.WakeDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if woken := len(runs.calls) == 1; woken != testCase.woken {
				t.Fatalf("runs=%+v, want woken=%t", runs.calls, testCase.woken)
			}
			if done := len(store.done) == 1; done != testCase.done || !testCase.done && !store.released[testCase.delivery.EventID] {
				t.Fatalf("done=%v released=%v, want done=%t, else released to retry", store.done, store.released, testCase.done)
			}
			if standing, ok := testCase.standing.(*fakeStanding); ok && (len(standing.asked) != 1 || standing.asked[0] != "irq_1") {
				t.Fatalf("read again %v, want irq_1 once", standing.asked)
			}
		})
	}
}

// TestNoNewRequestWakesItsAgentWhoeverMayDecideIt: a new request's delivery
// wakes nobody, whether its agent may decide it (a specialist with automatic
// delegation, decided in a conversation its member started), it is held for
// its owner, or it was made outside any teamspace, whatever approval an older
// build stored for it.
func TestNoNewRequestWakesItsAgentWhoeverMayDecideIt(t *testing.T) {
	outside := requestWithApproval(interactions.StatusPending, interactions.ApprovalAgent)
	outside.TeamspaceID = ""
	outsideHeld := requestWithApproval(interactions.StatusPending, interactions.ApprovalOwner)
	outsideHeld.TeamspaceID = ""
	for name, testCase := range map[string]struct {
		request agents.Interaction
		woken   bool
	}{
		"a specialist its member let decide":                   {requestWithApproval(interactions.StatusPending, interactions.ApprovalAgent), false},
		"a request held for its owner":                         {requestWithApproval(interactions.StatusPending, interactions.ApprovalOwner), false},
		"a request made outside any teamspace":                 {outsideHeld, false},
		"one an older build stored outside any as its agent's": {outside, false},
	} {
		store := &fakeDeliveries{pending: []Delivery{testDelivery("evt_requested", KindTargetRequested, 1)}}
		requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testCase.request}}
		runs := &fakeAgentRuns{onExecute: func() { requests.setStatus("irq_1", interactions.StatusApproved) }}
		if err := NewWaker(store, runs, requests, standsStill(), nil).WakeDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if woken := len(runs.calls) == 1; woken != testCase.woken || len(store.done) != 1 {
			t.Fatalf("%s: runs %+v, done %v; want woken %t, its delivery done", name, runs.calls, store.done, testCase.woken)
		}
	}
}

// TestOnlyATeamspaceRequestsAgentIsReadAgain: a report to the member who
// asked wakes their agent as it always did, unread; a new request made
// outside any teamspace, whatever approval an older build stored for it, is
// its member's to decide, so it wakes nobody, and there is no teamspace to
// read it again in.
func TestOnlyATeamspaceRequestsAgentIsReadAgain(t *testing.T) {
	outside := requestWithApproval(interactions.StatusPending, interactions.ApprovalAgent)
	outside.TeamspaceID = ""
	rejected := requestWithApproval(interactions.StatusRejected, interactions.ApprovalOwner)
	report := testDelivery("evt_rejected", KindRequesterRejected, 2)
	report.RecipientUserID, report.RecipientAgentID, report.Status = "user_a", "agt_a", interactions.StatusRejected
	for name, testCase := range map[string]struct {
		delivery Delivery
		request  agents.Interaction
		woken    bool
	}{
		"a request outside any teamspace": {testDelivery("evt_requested", KindTargetRequested, 1), outside, false},
		"a rejection's report":            {report, rejected, true},
	} {
		standing := &fakeStanding{verdict: interactions.VerdictHeld}
		store := &fakeDeliveries{pending: []Delivery{testCase.delivery}}
		requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": testCase.request}}
		runs := &fakeAgentRuns{onExecute: func() { requests.setStatus("irq_1", interactions.StatusRejected) }}
		waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute, Standing: standing}, nil)
		if err := waker.WakeDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if woken := len(runs.calls) == 1; len(standing.asked) != 0 || woken != testCase.woken || len(store.done) != 1 {
			t.Fatalf("%s: read again %v, runs %+v, done %v; want it unread, woken %t, its delivery done", name, standing.asked, runs.calls, store.done, testCase.woken)
		}
	}
}
