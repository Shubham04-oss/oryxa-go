package interactiondelivery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/jev"
)

// ownerJudgment is a Jev that finds a request needs its owner's own answer when
// its words ask for an approval, and not otherwise, and answers what it is
// asked about tools with none.
func ownerJudgment(_ context.Context, request jev.Request) (jev.Result, error) {
	shown, err := json.Marshal(request.State)
	if err != nil {
		return jev.Result{}, err
	}
	answers := map[string]jev.Answer{}
	for id, question := range request.Questions {
		switch {
		case question.Type != jev.TypeNoul:
			answers[id] = jev.Answer{Type: jev.TypeChoice, Choice: "none", Confidence: 0.9}
		case strings.Contains(string(shown), "approve"):
			answers[id] = jev.Answer{Type: jev.TypeNoul, Noul: 0.92}
		default:
			answers[id] = jev.Answer{Type: jev.TypeNoul, Noul: 0.05}
		}
	}
	return jev.Result{Model: "jev-test", Answers: answers}, nil
}

// automaticSpecialist seats B's specialist agt_auto as a contribution with
// automatic delegation: what is asked of it there is its own to decide.
type automaticSpecialist struct{}

func (automaticSpecialist) RequestSeat(_ context.Context, _ agents.TeamspaceActor, _ agents.OrganizationDirectory, _, targetAgentID string) (agents.TeamspaceSeat, error) {
	return agents.TeamspaceSeat{AgentID: targetAgentID, OwnerUserID: "user_b", Kind: "specialist", DelegationMode: "automatic"}, nil
}

func (automaticSpecialist) RequestStanding(_ context.Context, _ string, _ agents.OrganizationDirectory, _ string, _, target agents.InteractionParty) (agents.TeamspaceStanding, error) {
	return agents.TeamspaceStanding{Seat: agents.TeamspaceSeat{AgentID: target.AgentID, OwnerUserID: target.UserID, Kind: "specialist", DelegationMode: "automatic"}}, nil
}

// teammateBSpecialist is A's view of the organization: B and B's specialist.
type teammateBSpecialist struct{ agents.Directory }

func (teammateBSpecialist) Load(context.Context, string, string) (agents.OrganizationDirectory, error) {
	return agents.OrganizationDirectory{People: []agents.DirectoryPerson{{
		UserID: "user_b", DisplayName: "B",
		Agents: []agents.DirectoryAgent{{ID: "agt_auto", OwnerUserID: "user_b", Kind: "specialist", Name: "Researcher"}},
	}}}, nil
}

// TestAnOwnerFirstRequestWakesNoAgentUntilItsOwnerLetsOne runs a request A's
// agent makes in a teamspace of a specialist B contributed with automatic
// delegation, through the real ledger, its outbox events, the delivery store and
// the waker. Its agent would decide such a request itself, but Jev judged this
// one needs B's own answer: it is stored answer_by "owner", its delivery wakes
// nobody, the specialist cannot decide it, and only B's approval, which is "Let
// my agent answer", wakes the specialist to answer it.
func TestAnOwnerFirstRequestWakesNoAgentUntilItsOwnerLetsOne(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	a, b := world.a, world.b
	specialist := agents.InteractionActor{OrganizationID: world.organizationID, UserID: "user_b", AgentID: "agt_auto", AgentName: "Researcher", AgentKind: "specialist"}
	requests, err := interactions.New(interactions.Config{
		Repository: world.ledger, Directory: teammateBSpecialist{}, Teamspaces: automaticSpecialist{}, Tokens: memberTokens{}, Jev: ownerJudgment,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := requests.Create(ctx, a, "member-token", "owner-first", agents.InteractionCreateInput{
		TargetAgentID: "agt_auto", TeamspaceID: "tsp_launch", Purpose: "Launch", Task: "Can you approve the launch budget?",
		RequestedTools: []string{agents.KnowledgeToolboxName},
	})
	if err != nil || created.AnswerBy != interactions.AnswerByOwner || created.Approval != interactions.ApprovalOwner {
		t.Fatalf("created = %+v, %v; want answer_by owner, held for its owner", created, err)
	}
	var stored string
	if err := world.database.Pool.QueryRow(ctx, `SELECT answer_by FROM agents_primary.agent_interactions WHERE id=$1`, created.ID).Scan(&stored); err != nil || stored != interactions.AnswerByOwner {
		t.Fatalf("the ledger keeps answer_by %q (%v)", stored, err)
	}

	runs := &fakeAgentRuns{}
	waker := NewWaker(world.deliveries, runs, world.ledger, Config{Interval: time.Second, Lease: time.Minute, Standing: requests}, nil)
	requested := world.deliver(t, agentsv1.InteractionRequestedDetailType, KindTargetRequested, specialist, created.ID)
	waker.wake(ctx, requested)
	if len(runs.calls) != 0 || !world.handled(t, requested.EventID) {
		t.Fatalf("the request's own delivery: runs=%+v handled=%v, want it finished with no agent woken", runs.calls, world.handled(t, requested.EventID))
	}
	approve := agents.InteractionDecisionInput{Decision: interactions.DecisionApproved, Message: "Go ahead.", AllowedTools: []string{agents.KnowledgeToolboxName}}
	if _, err := requests.Decide(ctx, specialist, created.ID, approve); !errors.Is(err, interactions.ErrForbidden) {
		t.Fatalf("the specialist deciding it: %v, want ErrForbidden", err)
	}
	if got, err := world.ledger.Get(ctx, created.ID); err != nil || got.Status != interactions.StatusPending {
		t.Fatalf("before its owner decides, the request is %+v (%v), want it pending", got, err)
	}

	owner := b
	owner.Session = agents.SessionMember
	if approved, err := requests.DecideAsOwner(ctx, owner, "member-token", created.ID, approve); err != nil || approved.Status != interactions.StatusApproved {
		t.Fatalf("B letting their agent answer = %+v, %v", approved, err)
	}
	runs.onExecute = func() {
		if _, err := requests.Respond(ctx, specialist, created.ID, agents.InteractionResponseInput{
			Status: interactions.StatusCompleted, Summary: "Budget noted.", Content: "B allowed me to answer.",
		}); err != nil {
			t.Errorf("the specialist answering: %v", err)
		}
	}
	approved := world.deliver(t, agentsv1.InteractionDecidedDetailType, KindTargetApproved, specialist, created.ID)
	waker.wake(ctx, approved)
	if len(runs.calls) != 1 || runs.calls[0].agentID != "agt_auto" || !world.handled(t, approved.EventID) {
		t.Fatalf("B's approval: runs=%+v handled=%v, want the specialist woken once", runs.calls, world.handled(t, approved.EventID))
	}
}

// TestAnOwnerFirstSlackAskWaitsForItsOwnerWhateverStandsForIt runs Slack asks
// through the real ledger and the waker. B gives A access in a thread, so the
// lease approves A's next factual question there with no click; but A's ask for
// an approval in the same thread, and one the router would bypass, each need
// B's own answer: neither is approved as it is made, their deliveries wake
// nobody, and only B's Let my agent answer (the summary choice) wakes B's agent.
func TestAnOwnerFirstSlackAskWaitsForItsOwnerWhateverStandsForIt(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	b := world.b
	requests, err := interactions.New(interactions.Config{
		Repository: world.ledger, Directory: teammateB{}, Agents: slackPrimaries{}, Tokens: memberTokens{}, Jev: ownerJudgment,
		TicketKey: []byte("0123456789abcdef0123456789abcdef-slack-tickets"),
	})
	if err != nil {
		t.Fatal(err)
	}
	thread := interactions.SlackThread{TeamID: "T1", ChannelID: "C1", ThreadTS: "1790001000.000100"}
	ask := func(trigger, question, bypass string) agents.Interaction {
		t.Helper()
		thread := thread
		thread.TriggerTS = trigger
		result, err := requests.CreateAsk(ctx, interactions.AskInput{
			OrganizationID: world.organizationID, AskerUserID: "user_a", Slack: thread, Question: question,
			Owners: []interactions.AskOwner{{UserID: "user_b", DisplayName: "Bea", Email: "b@example.com", Bypass: bypass}},
		})
		if err != nil || len(result.Requests) != 1 {
			t.Fatalf("CreateAsk(%q) = %+v, %v", question, result, err)
		}
		return result.Requests[0]
	}

	// B gives A access in the thread.
	first := ask("1790001000.000100", "Where is the launch plan?", "")
	if first.AnswerBy != interactions.AnswerByAgent {
		t.Fatalf("a factual question reads answer_by %q", first.AnswerBy)
	}
	tickets, err := requests.ConsentTickets(ctx, first.ID, "ntc_first")
	if err != nil {
		t.Fatal(err)
	}
	if given, err := requests.Consent(ctx, tickets.Access, "user_b", interactions.SurfaceSlack); err != nil || given.Lease == nil {
		t.Fatalf("B giving access = %+v, %v", given, err)
	}
	if factual := ask("1790001100.000100", "Which doc has the launch dates?", ""); factual.Status != interactions.StatusApproved {
		t.Fatalf("a factual follow-up under the lease is %s, want approved", factual.Status)
	}

	runs := &fakeAgentRuns{}
	waker := NewWaker(world.deliveries, runs, world.ledger, Config{Interval: time.Second, Lease: time.Minute}, nil)
	for _, bypass := range []string{"", "high_priority_channel"} {
		trigger := "1790001200.000100"
		if bypass != "" {
			trigger = "1790001300.000100"
		}
		ownerFirst := ask(trigger, "Can you approve the launch budget?", bypass)
		if ownerFirst.AnswerBy != interactions.AnswerByOwner || ownerFirst.Status != interactions.StatusPending || ownerFirst.Decision != nil {
			t.Fatalf("bypass %q: the ask for an approval = %+v, want it pending for its owner, answer_by owner", bypass, ownerFirst)
		}
		if _, err := requests.Bypass(ctx, ownerFirst.ID, "high_priority_channel"); !errors.Is(err, interactions.ErrInvalid) {
			t.Fatalf("bypass %q: bypassing it later: %v, want ErrInvalid", bypass, err)
		}
		requested := world.deliver(t, agentsv1.InteractionRequestedDetailType, KindTargetRequested, b, ownerFirst.ID)
		waker.wake(ctx, requested)
		if len(runs.calls) != 0 || !world.handled(t, requested.EventID) {
			t.Fatalf("bypass %q: its delivery woke %+v (handled %v), want nobody", bypass, runs.calls, world.handled(t, requested.EventID))
		}
		if got, err := world.ledger.Get(ctx, ownerFirst.ID); err != nil || got.Status != interactions.StatusPending {
			t.Fatalf("bypass %q: before B chooses, the ask is %+v (%v)", bypass, got, err)
		}

		tickets, err := requests.ConsentTickets(ctx, ownerFirst.ID, "ntc_"+trigger)
		if err != nil {
			t.Fatal(err)
		}
		if let, err := requests.Consent(ctx, tickets.Summary, "user_b", interactions.SurfaceSlack); err != nil || let.Request.Status != interactions.StatusApproved {
			t.Fatalf("bypass %q: B letting their agent answer = %+v, %v", bypass, let, err)
		}
		runs.onExecute = func() {
			if _, err := requests.Respond(ctx, b, ownerFirst.ID, agents.InteractionResponseInput{
				Status: interactions.StatusCompleted, Summary: "B will decide on Friday.", Content: "Friday.",
			}); err != nil {
				t.Errorf("bypass %q: B's agent answering: %v", bypass, err)
			}
		}
		calls := len(runs.calls)
		approved := world.deliver(t, agentsv1.InteractionDecidedDetailType, KindTargetApproved, b, ownerFirst.ID)
		waker.wake(ctx, approved)
		if len(runs.calls) != calls+1 || runs.calls[calls].agentID != "agt_b" || !world.handled(t, approved.EventID) {
			t.Fatalf("bypass %q: B's choice: runs=%+v, want B's agent woken once", bypass, runs.calls)
		}
		runs.calls = nil
	}
}
