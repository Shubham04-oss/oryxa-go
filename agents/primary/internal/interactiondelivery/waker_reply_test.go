package interactiondelivery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
)

// repliedByItsOwner is a request its owner answered themself while it waited
// for them: approved with a reply's consent and completed with their words, in
// one change.
func repliedByItsOwner(origin string) agents.Interaction {
	request := testRequest(interactions.StatusCompleted)
	request.Origin = origin
	request.Requester = agents.InteractionParty{UserID: "user_a", AgentID: "agt_a", AgentName: "Crew"}
	request.Target = agents.InteractionParty{UserID: "user_b", AgentID: "agt_b", AgentName: "Carl"}
	request.RequesterThreadID, request.RequesterThreadAppName = "thread_origin", agents.RootName
	request.Decision = &agents.InteractionDecision{
		Decision: interactions.DecisionApproved, Consent: interactions.ConsentReply, AllowedTools: []string{}, GrantedPermissions: []string{},
		DecidedAt: time.Now(), DecidedBy: &agents.InteractionDecider{Kind: interactions.DecidedByMember, UserID: "user_b", Surface: interactions.SurfaceWeb},
	}
	request.Result = &agents.InteractionResult{Status: interactions.StatusCompleted, Summary: "We ship on the 14th.", WrittenByOwner: true}
	return request
}

// TestAnOwnersReplyNeverStartsTheirAgent: the approval a reply makes asks no
// work of the owner's agent, since the same change answered it. Its delivery is
// finished without a run, whether the request reads answered, as it always does
// once the change is made, or, were it ever read in between, approved.
func TestAnOwnersReplyNeverStartsTheirAgent(t *testing.T) {
	for name, status := range map[string]string{"answered": interactions.StatusCompleted, "approved": interactions.StatusApproved} {
		t.Run(name, func(t *testing.T) {
			for _, origin := range []string{interactions.OriginAgent, interactions.OriginSlack} {
				request := repliedByItsOwner(origin)
				request.Status = status
				approved := testDelivery("evt_approved", KindTargetApproved, 2)
				approved.Status = interactions.StatusApproved
				store := &fakeDeliveries{pending: []Delivery{approved}}
				runs := &fakeAgentRuns{}
				waker := NewWaker(store, runs, &fakeRequests{requests: map[string]agents.Interaction{request.ID: request}}, standsStill(), nil)
				if err := waker.WakeDue(context.Background()); err != nil {
					t.Fatal(err)
				}
				if len(runs.calls) != 0 {
					t.Fatalf("%s: the owner's agent was started for their reply: %+v", origin, runs.calls)
				}
				if len(store.done) != 1 || store.done[0] != "evt_approved" || len(store.dropped) != 0 || len(store.released) != 0 {
					t.Fatalf("%s: done=%v dropped=%v released=%v, want the delivery finished", origin, store.done, store.dropped, store.released)
				}
			}
		})
	}
	// An approval by any other choice of the owner's still starts the work.
	request := decidedBy(testRequest(interactions.StatusApproved), byTheMember)
	request.Decision.Consent = interactions.ConsentSummary
	approved := testDelivery("evt_approved", KindTargetApproved, 2)
	store := &fakeDeliveries{pending: []Delivery{approved}}
	requests := &fakeRequests{requests: map[string]agents.Interaction{request.ID: request}}
	runs := &fakeAgentRuns{onExecute: func() { requests.setStatus(request.ID, interactions.StatusCompleted) }}
	if err := NewWaker(store, runs, requests, standsStill(), nil).WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 1 {
		t.Fatalf("an approval that is no reply started %d runs, want one", len(runs.calls))
	}
}

// TestTheAskerHearsTheOwnersReplyAsTheirs: an answer to a request made in the
// app reaches the asking chat as every answer does, and the agent reporting it
// is told the owner wrote it; one asked in Slack wakes nobody, as ever.
func TestTheAskerHearsTheOwnersReplyAsTheirs(t *testing.T) {
	request := repliedByItsOwner(interactions.OriginAgent)
	store := &fakeDeliveries{pending: []Delivery{requesterDelivery(KindRequesterCompleted)}}
	runs := &fakeAgentRuns{}
	waker := NewWaker(store, runs, &fakeRequests{requests: map[string]agents.Interaction{request.ID: request}}, Config{Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 1 || runs.calls[0].agentID != "agt_a" || runs.calls[0].threadID != "thread_origin" || !runs.calls[0].input.Delivery.PrimaryChat {
		t.Fatalf("calls = %+v, want the asking agent in the chat it asked from", runs.calls)
	}
	message := runs.calls[0].input.Message
	if !strings.Contains(message, "answered it themself, in their own words") || strings.Contains(message, "We ship on the 14th") {
		t.Fatalf("the asking agent was told %q", message)
	}
	// An agent's answer is reported as it always was.
	byTheAgent := originRequest()
	store = &fakeDeliveries{pending: []Delivery{requesterDelivery(KindRequesterCompleted)}}
	runs = &fakeAgentRuns{}
	waker = NewWaker(store, runs, &fakeRequests{requests: map[string]agents.Interaction{byTheAgent.ID: byTheAgent}}, Config{Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 1 || strings.Contains(runs.calls[0].input.Message, "themself") {
		t.Fatalf("an agent's answer was reported as %+v", runs.calls)
	}

	fromSlack := repliedByItsOwner(interactions.OriginSlack)
	store = &fakeDeliveries{pending: []Delivery{requesterDelivery(KindRequesterCompleted)}}
	runs = &fakeAgentRuns{}
	waker = NewWaker(store, runs, &fakeRequests{requests: map[string]agents.Interaction{fromSlack.ID: fromSlack}}, Config{Lease: time.Minute}, nil)
	if err := waker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 0 || len(store.done) != 1 {
		t.Fatalf("a reply to a question asked in Slack woke %+v; done=%v", runs.calls, store.done)
	}
}
