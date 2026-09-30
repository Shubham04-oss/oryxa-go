package interactions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// heldInSlack is a request of a new ask by the asker, in a thread of its own,
// to owner, approved by them with a summary and answered, and held by the
// consent guard for their review.
func (ledger *slackLedger) heldInSlack(trigger string, owners ...string) (AskResult, agents.Interaction) {
	ledger.t.Helper()
	result := ledger.askIn(SlackThread{TeamID: "T1", ChannelID: "C1", ThreadTS: trigger, TriggerTS: trigger}, owners...)
	request := requestTo(ledger.t, result, owners[0])
	ledger.decide(owners[0], request, ChoiceSummary)
	if _, err := ledger.answer(request); err != nil {
		ledger.t.Fatalf("answer: %v", err)
	}
	held := ledger.read(request.ID)
	if held.Status != StatusReview || held.Held == nil {
		ledger.t.Fatalf("the answer is %+v, want it held for review", held)
	}
	return result, held
}

// TestAHeldAnswerIsReviewedOnceInSlackOrOnTheWeb: the owner's click in Slack and
// their review on the web at the same moment, or twice over, review the answer
// once, by the row lock: one review is recorded, one completed event is sent,
// and every other call finds the answer reviewed. The review says where it was
// made: in Slack for a click, and as it always did for the web.
func TestAHeldAnswerIsReviewedOnceInSlackOrOnTheWeb(t *testing.T) {
	ledger := newSlackLedger(t, func(config *Config) { config.ConsentGuard = holding(consentguard.KindIrrelevant) })
	ctx := context.Background()
	const attempts = 16
	wins := map[string]int{}
	for round := range 3 {
		_, held := ledger.heldInSlack(fmt.Sprintf("179100000%d.000100", round), raya)
		tickets, err := ledger.service.ReviewTickets(ctx, held.ID, "review_"+held.ID)
		if err != nil {
			t.Fatal(err)
		}
		if tickets.SendMasked != "" {
			t.Fatalf("round %d: an answer the consent guard held has no masked version, and got a button for one", round)
		}
		type outcome struct {
			surface, action string
			err             error
		}
		outcomes := make([]outcome, attempts)
		var start, done sync.WaitGroup
		start.Add(1)
		for index := range attempts {
			done.Go(func() {
				action := []string{ReviewSend, ReviewWithhold}[(index/2+round)%2]
				start.Wait()
				if index%2 == 0 {
					ticket := map[string]string{ReviewSend: tickets.Send, ReviewWithhold: tickets.Withhold}[action]
					_, err := ledger.service.ReviewFromSlack(ctx, ticket, raya)
					outcomes[index] = outcome{SurfaceSlack, action, err}
					return
				}
				_, err := ledger.service.ReviewAsOwner(ctx, ledger.owner(raya), "member-token", held.ID, agents.InteractionReviewInput{Action: action})
				outcomes[index] = outcome{SurfaceWeb, action, err}
			})
		}
		start.Done()
		done.Wait()

		var winners []outcome
		for _, got := range outcomes {
			switch {
			case got.err == nil:
				winners = append(winners, got)
			case !errors.Is(got.err, ErrConflict):
				t.Fatalf("round %d: a review failed with %v, want it to win or find the answer reviewed", round, got.err)
			}
		}
		if len(winners) != 1 {
			t.Fatalf("round %d: %d reviews won, want exactly one", round, len(winners))
		}
		winner := winners[0]
		stored := ledger.read(held.ID)
		wantStatus := map[string]string{ReviewSend: StatusCompleted, ReviewWithhold: StatusWithheld}[winner.action]
		wantDecider := agents.InteractionDecider{Kind: DecidedByMember, UserID: raya}
		if winner.surface == SurfaceSlack {
			wantDecider.Surface = SurfaceSlack
		}
		if stored.Status != wantStatus || stored.Held != nil || stored.Review == nil || stored.Review.Action != winner.action ||
			stored.Review.DecidedBy != wantDecider {
			t.Fatalf("round %d: %s won with %s, and the request is %+v, reviewed %+v", round, winner.surface, winner.action, stored, stored.Review)
		}
		events := ledger.events(held.ID)
		if len(events) != 4 || events[2] != agentsv1.InteractionCompletedDetailType+":review" || events[3] != agentsv1.InteractionCompletedDetailType+":"+wantStatus {
			t.Fatalf("round %d: events = %v, want requested, decided, held and one review", round, events)
		}
		wins[winner.surface]++
	}
	t.Logf("who reviewed first, of three rounds: %v", wins)
}

// TestAnAnswerSentFromSlackSettlesItsAsk: sending a held answer delivers it, so
// it wins its ask as an answer delivered outright does, and the ask's other
// owner is told someone answered.
func TestAnAnswerSentFromSlackSettlesItsAsk(t *testing.T) {
	ledger := newSlackLedger(t, func(config *Config) { config.ConsentGuard = holding(consentguard.KindUngrounded) })
	ctx := context.Background()
	result, held := ledger.heldInSlack("1791000009.000100", raya, maya)
	tickets, err := ledger.service.ReviewTickets(ctx, held.ID, "review_"+held.ID)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := ledger.service.ReviewFromSlack(ctx, tickets.Send, raya)
	if err != nil || sent.Status != StatusCompleted || sent.Result == nil {
		t.Fatalf("sent = %+v, %v", sent, err)
	}
	ask, err := ledger.service.Ask(ctx, result.Ask.ID)
	if err != nil || ask.WinnerRequestID != held.ID {
		t.Fatalf("the ask = %+v, %v; want Raya's sent answer to have won it", ask, err)
	}
	if other := ledger.read(requestTo(t, result, maya).ID); other.Status != StatusRejected || !AnsweredElsewhere(ask, other) {
		t.Fatalf("the other owner's request = %+v, want it closed as answered elsewhere", other)
	}
	// Her click again, on either button, is refused: the review is over.
	for _, ticket := range []string{tickets.Send, tickets.Withhold} {
		if _, err := ledger.service.ReviewFromSlack(ctx, ticket, raya); !errors.Is(err, ErrConflict) {
			t.Fatalf("a second click: %v, want ErrConflict", err)
		}
	}
}
