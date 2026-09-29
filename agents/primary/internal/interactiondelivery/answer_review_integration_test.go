package interactiondelivery

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// holdingGuard holds every answer for an email address in its summary,
// masking it in its masked version.
type holdingGuard struct{}

func (holdingGuard) CheckWith(_ context.Context, texts []string, _ *answerguard.RelevanceInput) answerguard.Verdict {
	masked := slices.Clone(texts)
	masked[0] = strings.ReplaceAll(masked[0], "priya@example.com", "{EMAIL}")
	return answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{"EMAIL"}, Texts: slices.Clone(texts), Masked: masked}
}

// TestAHeldAnswerLivesItsWholeLifeAgainstTheDatabase runs a request through
// the real ledger, its outbox, the delivery store and the waker: B approves
// A's request allowing B's apps alone, and B's agent is woken with those
// and not B's knowledge; its answer holds an email address, so the guard
// holds it in review, where A sees nothing of it and nobody is woken; B
// sends it masked, and only then is A's agent woken, to report the masked
// answer.
func TestAHeldAnswerLivesItsWholeLifeAgainstTheDatabase(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	a, b := world.a, world.b
	requests, err := interactions.New(interactions.Config{
		Repository: world.ledger, Directory: teammateB{}, Teamspaces: primarySeat{}, Tokens: memberTokens{}, Guard: holdingGuard{},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := requests.Create(ctx, a, "member-token", "held-answer", agents.InteractionCreateInput{
		TargetAgentID: "agt_b", TeamspaceID: "tsp_launch", Purpose: "Contact", Task: "How do I reach Priya?",
		RequestedTools: []string{agents.AppsToolboxName, agents.KnowledgeToolboxName},
	})
	if err != nil {
		t.Fatal(err)
	}
	bSignedIn := b
	bSignedIn.Session = agents.SessionMember
	if _, err := requests.DecideAsOwner(ctx, bSignedIn, "member-token", created.ID, agents.InteractionDecisionInput{
		Decision: interactions.DecisionApproved, Message: "Look in my mail.", AllowedTools: []string{agents.AppsToolboxName},
	}); err != nil {
		t.Fatal(err)
	}

	runs := &fakeAgentRuns{onExecute: func() {
		if _, err := requests.Respond(ctx, b, created.ID, agents.InteractionResponseInput{
			Status: interactions.StatusCompleted, Summary: "Priya is at priya@example.com.", ToolsUsed: []string{agents.AppsToolboxName},
		}); err != nil {
			t.Errorf("B's agent answering: %v", err)
		}
	}}
	waker := NewWaker(world.deliveries, runs, world.ledger, Config{Interval: time.Second, Lease: time.Minute, Standing: requests}, nil)
	approval := world.deliver(t, agentsv1.InteractionDecidedDetailType, KindTargetApproved, b, created.ID)
	waker.wake(ctx, approval)
	if len(runs.calls) != 1 || runs.calls[0].reach != managedagents.DeliveryApps || !world.handled(t, approval.EventID) {
		t.Fatalf("the approval woke %+v (handled %v), want B's agent once, with B's apps alone", runs.calls, world.handled(t, approval.EventID))
	}

	// Held: A sees the status alone, B both versions; the review's event is
	// the completed event with status review, which wakes nobody.
	fromA, err := requests.Get(ctx, a, created.ID)
	if err != nil || fromA.Status != interactions.StatusReview || fromA.Result != nil || fromA.Held != nil || fromA.Guard != nil {
		t.Fatalf("A sees %+v (%v), want the status alone", fromA, err)
	}
	fromB, err := requests.Get(ctx, bSignedIn, created.ID)
	if err != nil || fromB.Held == nil || fromB.Held.MaskedResult == nil || fromB.Held.MaskedResult.Summary != "Priya is at {EMAIL}." {
		t.Fatalf("B sees %+v (%v), want both versions", fromB, err)
	}
	var reviewEvents int
	if err := world.database.Pool.QueryRow(ctx, `SELECT count(*) FROM agents_primary.agents_outbox
		WHERE detail_type=$1 AND detail->>'request_id'=$2 AND detail->>'status'='review'`,
		agentsv1.InteractionCompletedDetailType, created.ID).Scan(&reviewEvents); err != nil || reviewEvents != 1 {
		t.Fatalf("%d review events (%v), want one", reviewEvents, err)
	}

	sent, err := requests.ReviewAsOwner(ctx, bSignedIn, "member-token", created.ID, agents.InteractionReviewInput{Action: interactions.ReviewSendMasked})
	if err != nil || sent.Status != interactions.StatusCompleted {
		t.Fatalf("B sending it masked = %+v, %v", sent, err)
	}
	runs.onExecute = nil
	report := world.deliver(t, agentsv1.InteractionCompletedDetailType, KindRequesterCompleted, a, created.ID)
	if report.Status != interactions.StatusCompleted {
		t.Fatalf("A's report = %+v, want the completed answer", report)
	}
	waker.wake(ctx, report)
	if len(runs.calls) != 2 || runs.calls[1].agentID != "agt_a" || runs.calls[1].reach != managedagents.DeliveryReport || !world.handled(t, report.EventID) {
		t.Fatalf("the report woke %+v, want A's agent reading the request alone", runs.calls)
	}
	fromA, err = requests.Get(ctx, a, created.ID)
	if err != nil || fromA.Result == nil || fromA.Result.Summary != "Priya is at {EMAIL}." || fromA.Review == nil || fromA.Review.Action != interactions.ReviewSendMasked {
		t.Fatalf("A finally sees %+v (%v), want the masked answer, reviewed", fromA, err)
	}
}

// TestAnAnswerNobodyReviewedIsWithheldWhenItsRequestExpires: the waker's poll
// withholds an answer still in review once its request has expired, and the
// withheld event tells A's agent, whose message says so.
func TestAnAnswerNobodyReviewedIsWithheldWhenItsRequestExpires(t *testing.T) {
	world := newOwnerApprovalWorld(t)
	ctx := context.Background()
	requests, err := interactions.New(interactions.Config{
		Repository: world.ledger, Directory: teammateB{}, Teamspaces: primarySeat{}, Tokens: memberTokens{}, Guard: holdingGuard{},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := requests.Create(ctx, world.a, "member-token", "expiring-answer", agents.InteractionCreateInput{
		TargetAgentID: "agt_b", TeamspaceID: "tsp_launch", Purpose: "Contact", Task: "How do I reach Priya?",
	})
	if err != nil {
		t.Fatal(err)
	}
	bSignedIn := world.b
	bSignedIn.Session = agents.SessionMember
	if _, err := requests.DecideAsOwner(ctx, bSignedIn, "member-token", created.ID, agents.InteractionDecisionInput{Decision: interactions.DecisionApproved, Message: "Fine."}); err != nil {
		t.Fatal(err)
	}
	if held, err := requests.Respond(ctx, world.b, created.ID, agents.InteractionResponseInput{
		Status: interactions.StatusCompleted, Summary: "Priya is at priya@example.com.",
	}); err != nil || held.Status != interactions.StatusReview {
		t.Fatalf("held=%+v err=%v", held, err)
	}
	if _, err := world.database.Pool.Exec(ctx, `UPDATE agents_primary.agent_interactions SET expires_at=now() - interval '1 second' WHERE id=$1`, created.ID); err != nil {
		t.Fatal(err)
	}

	runs := &fakeAgentRuns{}
	waker := NewWaker(world.deliveries, runs, world.ledger, Config{Interval: time.Second, Lease: time.Minute, Standing: requests}, nil)
	waker.tidy(ctx)
	withheld, err := world.ledger.Get(ctx, created.ID)
	if err != nil || withheld.Status != interactions.StatusWithheld || withheld.Held != nil || withheld.Review == nil ||
		withheld.Review.Reason != interactions.ExpiredBeforeReview || withheld.Review.DecidedBy.Kind != interactions.DecidedBySystem {
		t.Fatalf("after the poll the request is %+v (%v), want withheld by Oryxa, expired before review", withheld, err)
	}
	report := world.deliver(t, agentsv1.InteractionCompletedDetailType, KindRequesterCompleted, world.a, created.ID)
	if report.Status != interactions.StatusWithheld {
		t.Fatalf("A's report = %+v, want withheld", report)
	}
	waker.wake(ctx, report)
	if len(runs.calls) != 1 || runs.calls[0].agentID != "agt_a" || !strings.Contains(runs.calls[0].input.Message, "withheld") || !world.handled(t, report.EventID) {
		t.Fatalf("the report woke %+v, want A's agent told it was withheld", runs.calls)
	}
}
