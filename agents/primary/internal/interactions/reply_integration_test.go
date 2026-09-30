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

const rayasWords = "The keys are in the vault; ask me for the path."

// TestAReplyToAPendingRequestIsOneChangeAndWinsItsAsk: approved with a reply's
// consent and answered with the owner's words in one transaction, announced as
// a decision and then an answer; the ask is won, and the other owner's request
// closed as answered elsewhere.
func TestAReplyToAPendingRequestIsOneChangeAndWinsItsAsk(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	result := ledger.ask("1792000001.000100", raya, maya)
	request := requestTo(t, result, raya)

	replied, err := ledger.service.ReplyAsOwner(ctx, ledger.owner(raya), "member-token", request.ID, "  "+rayasWords+" ")
	if err != nil {
		t.Fatal(err)
	}
	stored := ledger.read(request.ID)
	decider := agents.InteractionDecider{Kind: DecidedByMember, UserID: raya, Surface: SurfaceWeb}
	if stored.Status != StatusCompleted || stored.Revision != 3 || stored.Decision == nil || stored.Decision.Consent != ConsentReply ||
		stored.Decision.Decision != DecisionApproved || stored.Decision.DecidedBy == nil || *stored.Decision.DecidedBy != decider ||
		len(stored.Decision.AllowedTools) != 0 || len(stored.Decision.GrantedPermissions) != 0 {
		t.Fatalf("the request is %+v, decided %+v; want approved by Raya's reply and completed, granting nothing", stored, stored.Decision)
	}
	if stored.Result == nil || stored.Result.Summary != rayasWords || !stored.Result.WrittenByOwner || stored.Held != nil ||
		!stored.Result.CompletedAt.After(stored.Decision.DecidedAt) {
		t.Fatalf("the result is %+v, want Raya's words, after her approval", stored.Result)
	}
	if replied.Result == nil || replied.Result.Summary != rayasWords {
		t.Fatalf("the reply answered %+v", replied.Result)
	}
	events := ledger.events(request.ID)
	want := []string{
		agentsv1.InteractionRequestedDetailType + ":pending", agentsv1.InteractionDecidedDetailType + ":approved",
		agentsv1.InteractionCompletedDetailType + ":completed",
	}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	// Both events say how far the approval let the answer go: the owner's own
	// words, which is what keeps the owner's agent asleep.
	if n := ledger.count(`SELECT count(*) FROM agents_primary.agents_outbox WHERE detail->>'request_id'=$1 AND detail->>'consent'='reply'`, request.ID); n != 2 {
		t.Fatalf("%d events name the reply's consent, want the decision and the answer", n)
	}
	ask, err := ledger.service.Ask(ctx, result.Ask.ID)
	if err != nil || ask.WinnerRequestID != request.ID {
		t.Fatalf("the ask = %+v, %v; want Raya's reply to have won it", ask, err)
	}
	if other := ledger.read(requestTo(t, result, maya).ID); other.Status != StatusRejected || !AnsweredElsewhere(ask, other) {
		t.Fatalf("the other owner's request = %+v, want it closed as answered elsewhere", other)
	}
	var consent, via string
	if err := ledger.database.Pool.QueryRow(ctx, `SELECT consent, decided_via FROM agents_primary.agent_interactions WHERE id=$1`, request.ID).Scan(&consent, &via); err != nil ||
		consent != ConsentReply || via != SurfaceWeb {
		t.Fatalf("the row says consent %q via %q (%v)", consent, via, err)
	}
	ledger.answeredInOwnWords(request.ID, raya, asker)
}

// answeredInOwnWords fails the test unless every member named reads, in their
// activity, the answer of the request as written by its owner.
func (ledger *slackLedger) answeredInOwnWords(requestID string, members ...string) {
	ledger.t.Helper()
	for _, member := range members {
		entries, err := ledger.service.Activity(context.Background(), ledger.owner(member), 0)
		if err != nil {
			ledger.t.Fatalf("%s's activity: %v", member, err)
		}
		answered := 0
		for _, entry := range entries {
			if entry.RequestID != requestID || entry.Kind != ActivityAnswered {
				continue
			}
			answered++
			if !entry.WrittenByOwner || entry.Status != StatusCompleted {
				ledger.t.Fatalf("%s reads the answer as %+v, want it written by its owner", member, entry)
			}
		}
		if answered != 1 {
			ledger.t.Fatalf("%s's activity has %d answers for %s, want the reply: %+v", member, answered, requestID, entries)
		}
	}
}

// TestAReplyReplacesAHeldAnswerInTheLedger: the held answer is dropped, never
// sent, and the reply delivered in its place with the review saying so; what the
// guards found in the held answer goes with it.
func TestAReplyReplacesAHeldAnswerInTheLedger(t *testing.T) {
	ledger := newSlackLedger(t, func(config *Config) { config.ConsentGuard = holding(consentguard.KindUngrounded) })
	ctx := context.Background()
	result, held := ledger.heldInSlack("1792000002.000100", raya, maya)
	tickets, err := ledger.service.ReviewTickets(ctx, held.ID, "review_"+held.ID)
	if err != nil || tickets.Reply == "" {
		t.Fatalf("the review's tickets = %+v, %v; want a Reply yourself", tickets, err)
	}
	if _, err := ledger.service.ReplyFromSlack(ctx, tickets.Reply, raya, rayasWords); err != nil {
		t.Fatal(err)
	}
	stored := ledger.read(held.ID)
	if stored.Status != StatusCompleted || stored.Held != nil || stored.Result == nil || stored.Result.Summary != rayasWords ||
		!stored.Result.WrittenByOwner || stored.Guard != nil {
		t.Fatalf("the request is %+v, guard %+v; want Raya's words delivered, nothing held and nothing found", stored, stored.Guard)
	}
	if review := stored.Review; review == nil || review.Action != ReviewReply ||
		review.DecidedBy != (agents.InteractionDecider{Kind: DecidedByMember, UserID: raya, Surface: SurfaceSlack}) {
		t.Fatalf("the review = %+v, want Raya's reply, in Slack", stored.Review)
	}
	if stored.Decision == nil || stored.Decision.Consent != ConsentSummary {
		t.Fatalf("the decision became %+v, want her earlier choice as it was", stored.Decision)
	}
	var heldResult, guard *string
	if err := ledger.database.Pool.QueryRow(ctx, `SELECT held_result::text, guard::text FROM agents_primary.agent_interactions WHERE id=$1`, held.ID).
		Scan(&heldResult, &guard); err != nil || heldResult != nil || guard != nil {
		t.Fatalf("the row keeps held %v and guard %v (%v), want neither", heldResult, guard, err)
	}
	events := ledger.events(held.ID)
	if len(events) != 4 || events[2] != agentsv1.InteractionCompletedDetailType+":review" || events[3] != agentsv1.InteractionCompletedDetailType+":completed" {
		t.Fatalf("events = %v, want requested, decided, held and the reply", events)
	}
	ask, err := ledger.service.Ask(ctx, result.Ask.ID)
	if err != nil || ask.WinnerRequestID != held.ID {
		t.Fatalf("the ask = %+v, %v; want Raya's reply to have won it", ask, err)
	}
	// The review is over: her buttons, and a second reply, change nothing.
	if _, err := ledger.service.ReviewFromSlack(ctx, tickets.Send, raya); !errors.Is(err, ErrConflict) {
		t.Fatalf("Send after a reply: %v, want ErrConflict", err)
	}
	if _, err := ledger.service.ReplyFromSlack(ctx, tickets.Reply, raya, "Something else."); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("a second reply: %v, want ErrAlreadyDecided", err)
	}
	if again := ledger.read(held.ID); again.Result == nil || again.Result.Summary != rayasWords {
		t.Fatalf("the answer became %+v", again.Result)
	}
	ledger.answeredInOwnWords(held.ID, raya, asker)
}

// TestAReplyAndAChoiceAtOnceDecideOnce: the owner replying and choosing, on the
// web and in Slack, at the same moment, decide the request once, by the row
// lock: one decided event, and every other call finds it decided.
func TestAReplyAndAChoiceAtOnceDecideOnce(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	const attempts = 16
	wins := map[string]int{}
	for round := range 3 {
		request := requestTo(t, ledger.ask(fmt.Sprintf("179200010%d.000100", round), raya), raya)
		tickets, err := ledger.service.ConsentTickets(ctx, request.ID, "notice_"+request.ID)
		if err != nil {
			t.Fatal(err)
		}
		kinds := []string{"reply on the web", "reply in Slack", "summary in Slack", "deny on the web"}
		errs := make([]error, attempts)
		var start, done sync.WaitGroup
		start.Add(1)
		for index := range attempts {
			done.Go(func() {
				start.Wait()
				switch kinds[(index+round)%len(kinds)] {
				case "reply on the web":
					_, errs[index] = ledger.service.ReplyAsOwner(ctx, ledger.owner(raya), "member-token", request.ID, rayasWords)
				case "reply in Slack":
					_, errs[index] = ledger.service.ReplyFromSlack(ctx, tickets.Reply, raya, rayasWords)
				case "summary in Slack":
					_, errs[index] = ledger.service.Consent(ctx, tickets.Summary, raya, SurfaceSlack)
				default:
					_, errs[index] = ledger.service.ConsentAsOwner(ctx, ledger.owner(raya), request.ID, ChoiceDeny)
				}
			})
		}
		start.Done()
		done.Wait()
		var won []string
		for index, err := range errs {
			switch {
			case err == nil:
				won = append(won, kinds[(index+round)%len(kinds)])
			case !errors.Is(err, ErrAlreadyDecided):
				t.Fatalf("round %d: a call failed with %v, want it to win or find the request decided", round, err)
			}
		}
		if len(won) != 1 {
			t.Fatalf("round %d: %v won, want exactly one", round, won)
		}
		if n := ledger.count(`SELECT count(*) FROM agents_primary.agents_outbox WHERE detail_type=$1 AND detail->>'request_id'=$2`,
			agentsv1.InteractionDecidedDetailType, request.ID); n != 1 {
			t.Fatalf("round %d: %d decided events, want one", round, n)
		}
		stored := ledger.read(request.ID)
		replied := stored.Decision != nil && stored.Decision.Consent == ConsentReply
		if replied != (won[0] == "reply on the web" || won[0] == "reply in Slack") || replied != (stored.Status == StatusCompleted) {
			t.Fatalf("round %d: %s won, and the request is %s, decided %+v", round, won[0], stored.Status, stored.Decision)
		}
		wins[won[0]]++
	}
	t.Logf("who decided first, of three rounds: %v", wins)
}

// TestAReplyAndAReviewAtOnceEndTheReviewOnce: the same, for an answer held for
// review: one completed event after it was held, whichever won.
func TestAReplyAndAReviewAtOnceEndTheReviewOnce(t *testing.T) {
	ledger := newSlackLedger(t, func(config *Config) { config.ConsentGuard = holding(consentguard.KindIrrelevant) })
	ctx := context.Background()
	const attempts = 16
	for round := range 3 {
		_, held := ledger.heldInSlack(fmt.Sprintf("179200020%d.000100", round), raya)
		tickets, err := ledger.service.ReviewTickets(ctx, held.ID, "review_"+held.ID)
		if err != nil {
			t.Fatal(err)
		}
		errs := make([]error, attempts)
		var start, done sync.WaitGroup
		start.Add(1)
		for index := range attempts {
			done.Go(func() {
				start.Wait()
				switch (index + round) % 4 {
				case 0:
					_, errs[index] = ledger.service.ReplyAsOwner(ctx, ledger.owner(raya), "member-token", held.ID, rayasWords)
				case 1:
					_, errs[index] = ledger.service.ReplyFromSlack(ctx, tickets.Reply, raya, rayasWords)
				case 2:
					_, errs[index] = ledger.service.ReviewFromSlack(ctx, tickets.Send, raya)
				default:
					_, errs[index] = ledger.service.ReviewAsOwner(ctx, ledger.owner(raya), "member-token", held.ID, agents.InteractionReviewInput{Action: ReviewWithhold})
				}
			})
		}
		start.Done()
		done.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, ErrAlreadyDecided) && !errors.Is(err, ErrConflict):
				t.Fatalf("round %d: a call failed with %v, want it to win or find the review over", round, err)
			}
		}
		events := ledger.events(held.ID)
		if won != 1 || len(events) != 4 || events[2] != agentsv1.InteractionCompletedDetailType+":review" {
			t.Fatalf("round %d: %d won and the events are %v, want one ending of the review", round, won, events)
		}
		if stored := ledger.read(held.ID); stored.Status == StatusReview || stored.Held != nil {
			t.Fatalf("round %d: the request is %+v, want its review over", round, stored)
		}
	}
}

// TestTheLedgerKeepsAReplysConsentAndNothingLooser: the consent column takes a
// reply on a request that was approved, and still refuses it on one pending or
// rejected, and any consent it does not know.
func TestTheLedgerKeepsAReplysConsentAndNothingLooser(t *testing.T) {
	ledger := newSlackLedger(t)
	ctx := context.Background()
	request := requestTo(t, ledger.ask("1792000301.000100", raya), raya)
	try := func(set string) error {
		_, err := ledger.database.Pool.Exec(ctx, `UPDATE agents_primary.agent_interactions SET `+set+` WHERE id=$1`, request.ID)
		return err
	}
	for name, set := range map[string]string{
		"a reply in Slack":   `status='completed', decided_by_kind='member', decided_by_user_id='` + raya + `', decided_by_agent_id='', decided_via='slack', consent='reply'`,
		"a reply on the web": `status='approved', decided_by_kind='member', decided_by_user_id='` + raya + `', decided_by_agent_id='', decided_via='web', consent='reply'`,
	} {
		if err := try(set); err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
	for name, set := range map[string]string{
		"a reply on a pending request":  `status='pending', consent='reply'`,
		"a reply on a rejected request": `status='rejected', consent='reply'`,
		"a consent nobody knows":        `status='completed', consent='replied'`,
	} {
		if err := try(set); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
}
