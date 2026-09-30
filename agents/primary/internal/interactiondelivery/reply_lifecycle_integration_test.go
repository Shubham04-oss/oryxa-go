package interactiondelivery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// TestAnOwnersReplyLivesItsLifeWithoutWakingTheirAgent runs two requests B
// answers themself, one asked in Slack and one asked by A's agent in the app,
// through the real ledger, its outbox events, the delivery store and the waker.
// The approval each reply makes is announced, and even kept as a delivery for
// B's agent, which the consumer no longer does, it wakes nobody: the request is
// answered by the time the waker reads it. A hears of the answer where they
// asked: in Slack, with no agent woken, or in the chat A's agent asked from,
// told the words are B's own.
func TestAnOwnersReplyLivesItsLifeWithoutWakingTheirAgent(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	a, b := world.a, world.b
	requests, err := interactions.New(interactions.Config{
		Repository: world.ledger, Directory: teammateB{}, Agents: slackPrimaries{}, Tokens: memberTokens{},
		TicketKey: []byte("0123456789abcdef0123456789abcdef-slack-tickets"),
	})
	if err != nil {
		t.Fatal(err)
	}
	runs := &fakeAgentRuns{}
	waker := NewWaker(world.deliveries, runs, world.ledger, Config{Interval: time.Second, Lease: time.Minute}, nil)

	// Asked in Slack, replied to in Slack.
	result, err := requests.CreateAsk(ctx, interactions.AskInput{
		OrganizationID: world.organizationID, AskerUserID: "user_a", Question: "When do we ship?",
		Slack:  interactions.SlackThread{TeamID: "T1", ChannelID: "C1", ThreadTS: "1790000500.000100", TriggerTS: "1790000500.000100"},
		Owners: []interactions.AskOwner{{UserID: "user_b", DisplayName: "Bea", Email: "b@example.com"}},
	})
	if err != nil || len(result.Requests) != 1 {
		t.Fatalf("CreateAsk = %+v, %v", result, err)
	}
	asked := result.Requests[0]
	requested := world.deliver(t, agentsv1.InteractionRequestedDetailType, KindTargetRequested, b, asked.ID)
	waker.wake(ctx, requested)
	tickets, err := requests.ConsentTickets(ctx, asked.ID, "ntc_reply")
	if err != nil {
		t.Fatal(err)
	}
	if replied, err := requests.ReplyFromSlack(ctx, tickets.Reply, "user_b", "We ship on the 14th."); err != nil || replied.Status != interactions.StatusCompleted {
		t.Fatalf("B's reply = %+v, %v", replied, err)
	}
	approved := world.deliver(t, agentsv1.InteractionDecidedDetailType, KindTargetApproved, b, asked.ID)
	waker.wake(ctx, approved)
	answered := world.deliver(t, agentsv1.InteractionCompletedDetailType, KindRequesterCompleted, a, asked.ID)
	waker.wake(ctx, answered)
	if len(runs.calls) != 0 {
		t.Fatalf("a reply to a question asked in Slack woke %+v", runs.calls)
	}
	for _, delivery := range []Delivery{requested, approved, answered} {
		if !world.handled(t, delivery.EventID) {
			t.Fatalf("the %s delivery was not finished", delivery.Kind)
		}
	}

	// Asked in the app, replied to on the web.
	created, err := requests.Create(ctx, a, "member-token", "reply-in-the-app", agents.InteractionCreateInput{
		TargetAgentID: "agt_b", Purpose: "Launch", Task: "When do we ship?",
	})
	if err != nil {
		t.Fatal(err)
	}
	requested = world.deliver(t, agentsv1.InteractionRequestedDetailType, KindTargetRequested, b, created.ID)
	waker.wake(ctx, requested)
	owner := b
	owner.Session = agents.SessionMember
	if replied, err := requests.ReplyAsOwner(ctx, owner, "member-token", created.ID, "The 14th, and I sign it off."); err != nil ||
		replied.Result == nil || !replied.Result.WrittenByOwner {
		t.Fatalf("B's reply on the web = %+v, %v", replied, err)
	}
	approved = world.deliver(t, agentsv1.InteractionDecidedDetailType, KindTargetApproved, b, created.ID)
	waker.wake(ctx, approved)
	if len(runs.calls) != 0 || !world.handled(t, approved.EventID) {
		t.Fatalf("B's agent was woken for their own reply: %+v", runs.calls)
	}
	answered = world.deliver(t, agentsv1.InteractionCompletedDetailType, KindRequesterCompleted, a, created.ID)
	waker.wake(ctx, answered)
	if len(runs.calls) != 1 || runs.calls[0].agentID != "agt_a" || !world.handled(t, answered.EventID) {
		t.Fatalf("A's delivery: runs=%+v, want A's agent woken once to tell A", runs.calls)
	}
	if message := runs.calls[0].input.Message; !strings.Contains(message, "answered it themself, in their own words") || strings.Contains(message, "sign it off") {
		t.Fatalf("A's agent was told %q", message)
	}
}
