package interactiondelivery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// slackPrimaries finds a member's primary agent as agt_<name>, for user_<name>,
// as the world's own actors are named.
type slackPrimaries struct{}

func (slackPrimaries) Primary(_ context.Context, scope managedagents.Scope) (managedagents.Definition, error) {
	return managedagents.Definition{
		ID: "agt_" + strings.TrimPrefix(scope.UserID, "user_"), OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID,
		Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive,
	}, nil
}

// TestASlackAskLivesItsWholeLifeAgainstTheDatabase runs a request the Slack app
// makes of B's primary agent for A through the real ledger, its outbox events,
// the delivery store and the waker; only the directory and the agent runs are
// stand-ins. Its own delivery wakes nobody, since it waits for its owner; B's
// click on the notice's button decides it, and only then is B's agent woken, to
// read B's knowledge alone and told to answer with a summary; and A's agent is
// never woken to say how it went, since A is told in Slack. A follow-up from A
// in the same thread, within the lease B's access gave, is answered with no click
// and wakes B's agent the same way.
func TestASlackAskLivesItsWholeLifeAgainstTheDatabase(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	a, b := world.a, world.b
	ticketKey := []byte("0123456789abcdef0123456789abcdef-slack-tickets")
	requests, err := interactions.New(interactions.Config{
		Repository: world.ledger, Directory: teammateB{}, Agents: slackPrimaries{}, TicketKey: ticketKey, Tokens: memberTokens{},
	})
	if err != nil {
		t.Fatal(err)
	}
	thread := interactions.SlackThread{TeamID: "T1", ChannelID: "C1", ThreadTS: "1790000000.000100", TriggerTS: "1790000000.000100"}
	ask := func(trigger string) agents.Interaction {
		t.Helper()
		thread := thread
		thread.TriggerTS = trigger
		result, err := requests.CreateAsk(ctx, interactions.AskInput{
			OrganizationID: world.organizationID, AskerUserID: "user_a", Slack: thread,
			Question: "Where do the dev keys live?", Owners: []interactions.AskOwner{{UserID: "user_b", DisplayName: "Bea", Email: "b@example.com"}},
		})
		if err != nil || len(result.Requests) != 1 {
			t.Fatalf("CreateAsk = %+v, %v", result, err)
		}
		return result.Requests[0]
	}

	runs := &fakeAgentRuns{}
	waker := NewWaker(world.deliveries, runs, world.ledger, Config{Interval: time.Second, Lease: time.Minute}, nil)
	first := ask("1790000000.000100")
	if first.Origin != interactions.OriginSlack || first.Status != interactions.StatusPending || first.Target.AgentID != "agt_b" || first.Requester.AgentID != "agt_a" {
		t.Fatalf("the ask made %+v", first)
	}

	// Its own delivery waits for its owner: nobody is woken.
	requested := world.deliver(t, agentsv1.InteractionRequestedDetailType, KindTargetRequested, b, first.ID)
	waker.wake(ctx, requested)
	if len(runs.calls) != 0 || !world.handled(t, requested.EventID) {
		t.Fatalf("the ask's own delivery: runs=%+v handled=%v, want it completed without waking anyone", runs.calls, world.handled(t, requested.EventID))
	}

	// B clicks the button that gives an AI summary alone.
	tickets, err := requests.ConsentTickets(ctx, first.ID, "ntc_1")
	if err != nil {
		t.Fatal(err)
	}
	if clicked, err := requests.Consent(ctx, tickets.Summary, "user_b", interactions.SurfaceSlack); err != nil || clicked.Request.Status != interactions.StatusApproved {
		t.Fatalf("the click = %+v, %v", clicked, err)
	}
	runs.onExecute = func() {
		if _, err := requests.Respond(ctx, b, first.ID, agents.InteractionResponseInput{
			Status: interactions.StatusCompleted, Summary: "The keys are in the vault.", Content: "Ask for the dev vault path.",
		}); err != nil {
			t.Errorf("B's agent answering the approved request: %v", err)
		}
	}
	approved := world.deliver(t, agentsv1.InteractionDecidedDetailType, KindTargetApproved, b, first.ID)
	waker.wake(ctx, approved)
	if len(runs.calls) != 1 || !world.handled(t, approved.EventID) {
		t.Fatalf("the approval's delivery: runs=%+v handled=%v, want one run that answered", runs.calls, world.handled(t, approved.EventID))
	}
	woken := runs.calls[0]
	if woken.agentID != "agt_b" || woken.member.UserID != "user_b" || woken.reach != managedagents.DeliveryQuiet ||
		woken.input.Delivery.Purpose != agents.DeliveryWorks || len(woken.input.Delivery.Sources) != 1 || woken.input.Delivery.Sources[0] != agents.KnowledgeToolboxName ||
		!strings.Contains(woken.input.Message, "approved this request themself") || !strings.Contains(woken.input.Message, "AI summary only") ||
		strings.Contains(woken.input.Message, "dev keys") {
		t.Fatalf("woken=%+v, want B's agent reading knowledge alone, told to summarise", woken)
	}

	// A's agent is not woken to tell A: A is told in Slack.
	completed := world.deliver(t, agentsv1.InteractionCompletedDetailType, KindRequesterCompleted, a, first.ID)
	waker.wake(ctx, completed)
	if len(runs.calls) != 1 || !world.handled(t, completed.EventID) {
		t.Fatalf("A's delivery: runs=%d handled=%v, want it finished with no run", len(runs.calls), world.handled(t, completed.EventID))
	}
	if got, err := world.ledger.Get(ctx, first.ID); err != nil || got.Status != interactions.StatusCompleted || got.Result == nil || got.Result.Summary != "The keys are in the vault." {
		t.Fatalf("the request = %+v, %v", got, err)
	}

	// The same again with access: the lease it gives approves A's follow-up, and
	// B's agent is woken for it with no click and told it may quote.
	second := ask("1790000100.000200")
	tickets, err = requests.ConsentTickets(ctx, second.ID, "ntc_2")
	if err != nil {
		t.Fatal(err)
	}
	given, err := requests.Consent(ctx, tickets.Access, "user_b", interactions.SurfaceSlack)
	if err != nil || given.Lease == nil {
		t.Fatalf("the click for access = %+v, %v", given, err)
	}
	runs.onExecute = func() {}
	followUp := ask("1790000200.000300")
	if followUp.Status != interactions.StatusApproved || followUp.Decision == nil || followUp.Decision.DecidedBy.Kind != interactions.DecidedByLease {
		t.Fatalf("the follow-up = %+v, want it approved under the lease", followUp)
	}
	runs.onExecute = func() {
		if _, err := requests.Respond(ctx, b, followUp.ID, agents.InteractionResponseInput{
			Status: interactions.StatusCompleted, Summary: "Same place.", Content: "It is the dev vault path.",
		}); err != nil {
			t.Errorf("B's agent answering the follow-up: %v", err)
		}
	}
	leased := world.deliver(t, agentsv1.InteractionDecidedDetailType, KindTargetApproved, b, followUp.ID)
	waker.wake(ctx, leased)
	if len(runs.calls) != 2 || !world.handled(t, leased.EventID) {
		t.Fatalf("the follow-up's delivery: runs=%d handled=%v, want one more run that answered", len(runs.calls), world.handled(t, leased.EventID))
	}
	if woken := runs.calls[1]; woken.agentID != "agt_b" || woken.reach != managedagents.DeliveryQuiet ||
		!strings.Contains(woken.input.Message, "you may quote short excerpts") || strings.Contains(woken.input.Message, "approved this request themself") {
		t.Fatalf("woken for the follow-up = %+v", woken)
	}
}
