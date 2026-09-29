package interactions

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// approvedBoth is an ask to Raya and Maya that both have approved, as a summary
// and as access: what a two owner ask is once each has chosen.
func approvedBoth(t *testing.T, ledger *slackLedger) (AskResult, agents.Interaction, agents.Interaction) {
	t.Helper()
	result := ledger.ask("1790000000.000100", raya, maya)
	forRaya := ledger.decide(raya, requestTo(t, result, raya), ChoiceSummary).Request
	forMaya := ledger.decide(maya, requestTo(t, result, maya), ChoiceAccess).Request
	return result, forRaya, forMaya
}

// TestTheFirstDeliveredAnswerWinsTheAsk: the first request completed with a
// delivered answer wins its ask, and the other owner's request, still waiting
// for its answer, is closed, saying who answered, with the event that tells
// everyone; an answer that comes after is refused.
func TestTheFirstDeliveredAnswerWinsTheAsk(t *testing.T) {
	ledger := newSlackLedger(t)
	result, forRaya, forMaya := approvedBoth(t, ledger)

	answered, err := ledger.answer(forRaya)
	if err != nil || answered.Status != StatusCompleted {
		t.Fatalf("Raya's answer = %+v, %v", answered, err)
	}
	ask, err := ledger.service.Ask(context.Background(), result.Ask.ID)
	if err != nil || ask.WinnerRequestID != forRaya.ID || ask.Open() || ask.SettledAt == nil {
		t.Fatalf("the ask = %+v, %v; want Raya's request the winner", ask, err)
	}

	closed := ledger.read(forMaya.ID)
	if closed.Status != StatusFailed || closed.Result == nil || closed.Result.Status != StatusFailed || closed.Result.Summary != "Answered by Name of "+raya {
		t.Fatalf("Maya's request = %+v, result %+v; want it failed, saying who answered", closed, closed.Result)
	}
	if got := ledger.events(forMaya.ID); !slices.Equal(got, []string{
		agentsv1.InteractionRequestedDetailType + ":pending", agentsv1.InteractionDecidedDetailType + ":approved", agentsv1.InteractionCompletedDetailType + ":failed",
	}) {
		t.Fatalf("Maya's events = %v", got)
	}
	if got := ledger.events(forRaya.ID); got[len(got)-1] != agentsv1.InteractionCompletedDetailType+":completed" {
		t.Fatalf("Raya's events = %v", got)
	}
	// The ask says which request won, and so which were answered elsewhere.
	if !AnsweredElsewhere(ask, ledger.read(forMaya.ID)) || AnsweredElsewhere(ask, ledger.read(forRaya.ID)) ||
		AnsweredElsewhere(Ask{}, ledger.read(forMaya.ID)) || AnsweredElsewhere(ask, agents.Interaction{ID: "irq_other"}) {
		t.Fatalf("AnsweredElsewhere is wrong for %+v", ask)
	}
	// Maya's agent, still at work, has nothing to record its answer on.
	if late, err := ledger.answer(forMaya); !errors.Is(err, ErrConflict) || late.Status == StatusCompleted {
		t.Fatalf("Maya's late answer = %+v, %v; want ErrConflict", late, err)
	}
	// The asker reads Raya's answer, and Maya's request as one that failed.
	if open, err := ledger.service.CountOpenAsks(context.Background(), ledger.org, asker); err != nil || open != 0 {
		t.Fatalf("open asks = %d, %v", open, err)
	}
}

// A request that had not been decided when the other owner answered is
// rejected as it stands: by Oryxa, saying who answered, with the reason the bus
// carries, and a click after that learns so.
func TestARequestNobodyDecidedYetIsRejectedWhenAnotherOwnerAnswers(t *testing.T) {
	ledger := newSlackLedger(t)
	result := ledger.ask("1790000000.000100", raya, maya)
	forRaya := ledger.decide(raya, requestTo(t, result, raya), ChoiceSummary).Request
	pending := requestTo(t, result, maya)
	tickets, err := ledger.service.ConsentTickets(context.Background(), pending.ID, "ntc_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.answer(forRaya); err != nil {
		t.Fatal(err)
	}

	closed := ledger.read(pending.ID)
	if closed.Status != StatusRejected || closed.Decision == nil || closed.Decision.Message != "Answered by Name of "+raya ||
		closed.Decision.DecidedBy == nil || *closed.Decision.DecidedBy != (agents.InteractionDecider{Kind: DecidedBySystem, Reason: ReasonAnsweredElsewhere}) ||
		DecidedByItsOwner(closed) || closed.Decision.Consent != "" {
		t.Fatalf("Maya's request = %+v, decision %+v", closed, closed.Decision)
	}
	if got := ledger.events(pending.ID); !slices.Equal(got, []string{
		agentsv1.InteractionRequestedDetailType + ":pending", agentsv1.InteractionDecidedDetailType + ":rejected",
	}) {
		t.Fatalf("Maya's events = %v", got)
	}
	var event string
	if err := ledger.database.Pool.QueryRow(context.Background(), `SELECT detail::text FROM agents_primary.agents_outbox
		WHERE detail->>'request_id'=$1 AND detail_type=$2`, pending.ID, agentsv1.InteractionDecidedDetailType).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(event, `"reason": "answered_elsewhere"`) || strings.Contains(event, "Name of") {
		t.Fatalf("the closing event = %s, want the reason as a code and no name", event)
	}
	// Her button is still a good ticket, and finds the ask answered.
	late, err := ledger.service.Consent(context.Background(), tickets.Access, maya, SurfaceSlack)
	if !errors.Is(err, ErrAlreadyDecided) || late.Request.Status != StatusRejected || late.Lease != nil {
		t.Fatalf("Maya's click after = %+v, %v", late, err)
	}
	if ledger.leases() != 0 {
		t.Fatal("a click on a closed request gave a lease")
	}
}

// An answer that fails, or that is held for review, wins nothing: another
// owner's can still win, and does.
func TestAFailedAnswerWinsNothing(t *testing.T) {
	ledger := newSlackLedger(t)
	result, forRaya, forMaya := approvedBoth(t, ledger)
	failed, err := ledger.service.Respond(context.Background(), ledger.agentOf(raya), forRaya.ID, agents.InteractionResponseInput{
		Status: StatusFailed, Summary: "I could not find anything.", ToolsUsed: []string{"knowledge"},
	})
	if err != nil || failed.Status != StatusFailed {
		t.Fatalf("the failed answer = %+v, %v", failed, err)
	}
	if ask, err := ledger.service.Ask(context.Background(), result.Ask.ID); err != nil || !ask.Open() {
		t.Fatalf("the ask = %+v, %v; a failed answer must not win it", ask, err)
	}
	if got := ledger.read(forMaya.ID); got.Status != StatusApproved {
		t.Fatalf("Maya's request = %+v; a failed answer must not close it", got)
	}
	if open, err := ledger.service.CountOpenAsks(context.Background(), ledger.org, asker); err != nil || open != 1 {
		t.Fatalf("open asks = %d, %v; Maya's request is still waiting", open, err)
	}
	if won, err := ledger.answer(forMaya); err != nil || won.Status != StatusCompleted {
		t.Fatalf("Maya's answer = %+v, %v", won, err)
	}
	if ask, err := ledger.service.Ask(context.Background(), result.Ask.ID); err != nil || ask.WinnerRequestID != forMaya.ID {
		t.Fatalf("the ask = %+v, %v; want Maya's request the winner", ask, err)
	}
}

// TestAHeldAnswerWinsNothingUntilItIsSent: an answer held for its owner's
// review is not delivered, so it wins nothing; if another owner's is delivered
// meanwhile, the held one is withheld, saying who answered, and if the owner
// sends it first it wins as any delivered answer does.
func TestAHeldAnswerWinsNothingUntilItIsSent(t *testing.T) {
	held := func(ledger *slackLedger) {
		ledger.service.policy = fakeWorkspacePolicy{settings: policy.Settings{HoldForReview: true}}
	}
	delivered := func(ledger *slackLedger) {
		ledger.service.policy = fakeWorkspacePolicy{}
	}
	ctx := context.Background()

	t.Run("another owner answers first", func(t *testing.T) {
		ledger := newSlackLedger(t)
		result, forRaya, forMaya := approvedBoth(t, ledger)
		held(ledger)
		inReview, err := ledger.answer(forRaya)
		if err != nil || inReview.Status != StatusReview {
			t.Fatalf("Raya's answer = %+v, %v; want it held", inReview, err)
		}
		if ask, err := ledger.service.Ask(ctx, result.Ask.ID); err != nil || !ask.Open() {
			t.Fatalf("the ask = %+v, %v; a held answer must not win it", ask, err)
		}
		delivered(ledger)
		if won, err := ledger.answer(forMaya); err != nil || won.Status != StatusCompleted {
			t.Fatalf("Maya's answer = %+v, %v", won, err)
		}
		withheld := ledger.read(forRaya.ID)
		if withheld.Status != StatusWithheld || withheld.Held != nil || withheld.Result != nil || withheld.Review == nil ||
			withheld.Review.Action != ReviewWithhold || withheld.Review.DecidedBy.Kind != DecidedBySystem ||
			withheld.Review.Reason != "Answered by Name of "+maya || withheld.Review.DecidedBy.Reason != ReasonAnsweredElsewhere {
			t.Fatalf("Raya's held answer = %+v, review %+v; want it withheld, saying who answered", withheld, withheld.Review)
		}
		if got := ledger.events(forRaya.ID); got[len(got)-1] != agentsv1.InteractionCompletedDetailType+":withheld" {
			t.Fatalf("Raya's events = %v", got)
		}
		// She can no longer send what was withheld.
		if _, err := ledger.service.ReviewAsOwner(ctx, ledger.owner(raya), "", forRaya.ID, agents.InteractionReviewInput{Action: ReviewSend}); !errors.Is(err, ErrConflict) {
			t.Fatalf("Raya's review of a withheld answer: %v, want ErrConflict", err)
		}
	})

	t.Run("its owner sends it first", func(t *testing.T) {
		ledger := newSlackLedger(t)
		result, forRaya, forMaya := approvedBoth(t, ledger)
		held(ledger)
		if inReview, err := ledger.answer(forRaya); err != nil || inReview.Status != StatusReview {
			t.Fatalf("Raya's answer = %+v, %v; want it held", inReview, err)
		}
		sent, err := ledger.service.ReviewAsOwner(ctx, ledger.owner(raya), "", forRaya.ID, agents.InteractionReviewInput{Action: ReviewSend})
		if err != nil || sent.Status != StatusCompleted {
			t.Fatalf("Raya's review = %+v, %v", sent, err)
		}
		if ask, err := ledger.service.Ask(ctx, result.Ask.ID); err != nil || ask.WinnerRequestID != forRaya.ID {
			t.Fatalf("the ask = %+v, %v; want the request whose held answer was sent the winner", ask, err)
		}
		if closed := ledger.read(forMaya.ID); closed.Status != StatusFailed || closed.Result.Summary != "Answered by Name of "+raya {
			t.Fatalf("Maya's request = %+v", closed)
		}
	})

	t.Run("its owner withholds it", func(t *testing.T) {
		ledger := newSlackLedger(t)
		result, forRaya, _ := approvedBoth(t, ledger)
		held(ledger)
		if _, err := ledger.answer(forRaya); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.service.ReviewAsOwner(ctx, ledger.owner(raya), "", forRaya.ID, agents.InteractionReviewInput{Action: ReviewWithhold}); err != nil {
			t.Fatal(err)
		}
		if ask, err := ledger.service.Ask(ctx, result.Ask.ID); err != nil || !ask.Open() {
			t.Fatalf("the ask = %+v, %v; a withheld answer must not win it", ask, err)
		}
	})
}

// TestTwoOwnersAnsweringAtOnceOnlyOneWins: both agents answer in the same
// moment, and the ask's lock lets one settle it: that answer is delivered, the
// other request is closed, and its agent is told it can record nothing.
func TestTwoOwnersAnsweringAtOnceOnlyOneWins(t *testing.T) {
	ledger := newSlackLedger(t)
	for round := range 8 {
		thread := SlackThread{TeamID: "T1", ChannelID: "C1", ThreadTS: "17900000" + string(rune('a'+round)) + ".000100", TriggerTS: "17900000" + string(rune('a'+round)) + ".000100"}
		result := ledger.askIn(thread, raya, maya)
		forRaya := ledger.decide(raya, requestTo(t, result, raya), ChoiceSummary).Request
		forMaya := ledger.decide(maya, requestTo(t, result, maya), ChoiceSummary).Request

		requests := []agents.Interaction{forRaya, forMaya}
		results := make([]agents.Interaction, 2)
		errs := make([]error, 2)
		var start, done sync.WaitGroup
		start.Add(1)
		for index := range requests {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				results[index], errs[index] = ledger.answer(requests[index])
			}()
		}
		start.Done()
		done.Wait()

		wins := 0
		for index, err := range errs {
			switch {
			case err == nil && results[index].Status == StatusCompleted:
				wins++
			case !errors.Is(err, ErrConflict):
				t.Fatalf("round %d: an answer failed with %v, want it to win or be refused as a conflict", round, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d answers were delivered, want exactly one", round, wins)
		}
		completed := ledger.count(`SELECT count(*) FROM agents_primary.agent_interactions WHERE ask_id=$1 AND status='completed'`, result.Ask.ID)
		failed := ledger.count(`SELECT count(*) FROM agents_primary.agent_interactions WHERE ask_id=$1 AND status='failed'`, result.Ask.ID)
		if completed != 1 || failed != 1 {
			t.Fatalf("round %d: %d completed and %d failed, want one of each", round, completed, failed)
		}
		var winner string
		if err := ledger.database.Pool.QueryRow(context.Background(), `SELECT winner_request_id FROM agents_primary.interaction_asks WHERE id=$1`, result.Ask.ID).Scan(&winner); err != nil {
			t.Fatal(err)
		}
		if got := ledger.read(winner); got.Status != StatusCompleted {
			t.Fatalf("round %d: the winner recorded is %+v", round, got)
		}
	}
}

// A request that had already expired when another owner answered is left as
// it is: it reads as expired, as it always did, and nobody is told twice.
func TestASiblingThatExpiredIsLeftAlone(t *testing.T) {
	ledger := newSlackLedger(t)
	result := ledger.ask("1790000000.000100", raya, maya)
	forRaya := ledger.decide(raya, requestTo(t, result, raya), ChoiceSummary).Request
	forMaya := requestTo(t, result, maya)
	if _, err := ledger.database.Pool.Exec(context.Background(), `UPDATE agents_primary.agent_interactions SET expires_at=$2 WHERE id=$1`,
		forMaya.ID, ledger.clock.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.answer(forRaya); err != nil {
		t.Fatal(err)
	}
	left := ledger.read(forMaya.ID)
	if left.Status != StatusPending || left.Revision != 1 || len(ledger.events(forMaya.ID)) != 1 {
		t.Fatalf("Maya's expired request = %+v, events %v; want it untouched", left, ledger.events(forMaya.ID))
	}
	viewed, err := ledger.service.Get(context.Background(), ledger.owner(maya), forMaya.ID)
	if err != nil || viewed.Status != StatusExpired {
		t.Fatalf("Maya reads it as %+v, %v; want expired", viewed, err)
	}
}

// answerTexts a guard held or masked are what is delivered, and the guard is
// told, at the owner's level, what the run called and what was asked: a test of
// the wiring between the two, on a Slack ask.
func TestAnAnswerToASlackAskIsReadAtItsOwnersLevelAsItsRunWasAsked(t *testing.T) {
	guard := &fakeGuard{}
	ledger := newSlackLedger(t, func(config *Config) { config.Guard = guard })
	request := ledger.decide(raya, requestTo(t, ledger.ask("1790000000.000100", raya), raya), ChoiceSummary).Request
	ctx := agents.WithRunSources(context.Background(), []string{"interactions_get", "knowledge_search"})
	answered, err := ledger.service.Respond(ctx, ledger.agentOf(raya), request.ID, agents.InteractionResponseInput{
		Status: StatusCompleted, Summary: "The keys live in the vault.", Content: "Ask for the dev vault path.", ToolsUsed: []string{"knowledge"},
	})
	if err != nil || answered.Status != StatusCompleted {
		t.Fatalf("answered = %+v, %v", answered, err)
	}
	if len(guard.relevance) != 1 {
		t.Fatalf("the guard was asked %d times", len(guard.relevance))
	}
	rel := guard.relevance[0]
	if rel.Request != "Where do the dev keys live?" || !slices.Equal(rel.Sources, []string{"interactions_get", "knowledge_search"}) ||
		rel.OrganizationID != ledger.org || rel.UserID != raya || rel.Mode != answerguard.RelevanceOff || rel.Regulated {
		t.Fatalf("the guard was told %+v", rel)
	}
}

// An answer that comes after its ask was won, which closing the siblings makes
// unlikely, is not delivered either: the request is closed, saying who answered,
// and the agent is told. The state is made by hand here, since the ledger's own
// steps close every sibling with the win.
func TestAnAnswerAfterTheAskWasWonIsNotDelivered(t *testing.T) {
	ledger := newSlackLedger(t)
	result, forRaya, forMaya := approvedBoth(t, ledger)
	if _, err := ledger.database.Pool.Exec(context.Background(), `UPDATE agents_primary.interaction_asks
		SET winner_request_id=$2, settled_at=$3 WHERE id=$1`, result.Ask.ID, forRaya.ID, ledger.clock.Now()); err != nil {
		t.Fatal(err)
	}
	answered, err := ledger.answer(forMaya)
	if !errors.Is(err, ErrAnsweredElsewhere) || answered.Status == StatusCompleted {
		t.Fatalf("Maya's answer after the win = %+v, %v; want ErrAnsweredElsewhere", answered, err)
	}
	closed := ledger.read(forMaya.ID)
	if closed.Status != StatusFailed || closed.Result == nil || closed.Result.Summary != "Answered by Name of "+raya {
		t.Fatalf("Maya's request = %+v, result %+v", closed, closed.Result)
	}
	if got := ledger.events(forMaya.ID); got[len(got)-1] != agentsv1.InteractionCompletedDetailType+":failed" {
		t.Fatalf("Maya's events = %v", got)
	}
	// The winner stays the one the ask named.
	if ask, err := ledger.service.Ask(context.Background(), result.Ask.ID); err != nil || ask.WinnerRequestID != forRaya.ID {
		t.Fatalf("the ask = %+v, %v", ask, err)
	}
}
