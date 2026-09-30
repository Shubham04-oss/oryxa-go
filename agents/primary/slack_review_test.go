package primary

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	agentinteractions "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
)

// reviewLedger is as much of the ledger as a click on a review reads: one
// request, or a database that does not answer. Anything else it is asked
// panics on the nil it embeds, which is the test saying a click did more than
// it should.
type reviewLedger struct {
	agentinteractions.Repository
	request agents.Interaction
	getErr  error
	reviews int
}

func (ledger *reviewLedger) Get(_ context.Context, id string) (agents.Interaction, error) {
	switch {
	case ledger.getErr != nil:
		return agents.Interaction{}, ledger.getErr
	case id != ledger.request.ID:
		return agents.Interaction{}, agentinteractions.ErrNotFound
	}
	return ledger.request, nil
}

func (ledger *reviewLedger) Review(_ context.Context, id string, review agents.InteractionReview, at time.Time) (agents.Interaction, error) {
	ledger.reviews++
	request := ledger.request
	request.Status, request.Held, request.Review = agentinteractions.StatusWithheld, nil, &review
	ledger.request = request
	return request, nil
}

type noDirectory struct{ agents.Directory }

// TestAClickOnAReviewIsRefusedForGoodOrTriedAgain: what the ledger refuses, the
// same click would be refused again, and the router drops it; a ledger that did
// not answer is tried again.
func TestAClickOnAReviewIsRefusedForGoodOrTriedAgain(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	key := []byte(strings.Repeat("k", 32))
	held := agents.Interaction{
		ID: "irq_1", OrganizationID: "org_1", Origin: agentinteractions.OriginSlack, Status: agentinteractions.StatusReview,
		Target: agents.InteractionParty{UserID: "user_raya"}, ExpiresAt: now.Add(time.Hour),
		Held: &agents.InteractionHeld{Result: agents.InteractionResult{Status: agentinteractions.StatusCompleted, Content: "held"}},
	}
	ledger := &reviewLedger{request: held}
	service, err := agentinteractions.New(agentinteractions.Config{
		Repository: ledger, Directory: noDirectory{}, TicketKey: key, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	tickets, err := agentinteractions.NewTickets(key)
	if err != nil {
		t.Fatal(err)
	}
	mint := func(kind, choice string) string {
		ticket, err := tickets.Mint(agentinteractions.TicketClaims{
			RequestID: "irq_1", OwnerUserID: "user_raya", NoticeID: "review_irq_1", Kind: kind, Choice: choice, ExpiresAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		return ticket
	}
	asks := slackAsks{service: service}
	ctx := context.Background()

	for name, test := range map[string]struct {
		ticket, clicker string
	}{
		"a ticket the ledger did not make":          {"t1.forged.ticket", "user_raya"},
		"a consent ticket on a review's button":     {mint(agentinteractions.TicketConsent, agentinteractions.ChoiceAccess), "user_raya"},
		"a click by another member":                 {mint(agentinteractions.TicketReview, agentinteractions.ReviewSend), "user_ada"},
		"sending masked what has no masked version": {mint(agentinteractions.TicketReview, agentinteractions.ReviewSendMasked), "user_raya"},
	} {
		if err := asks.Review(ctx, test.ticket, test.clicker); !slackrouter.IsPermanent(err) {
			t.Errorf("%s: Review = %v, want it refused for good", name, err)
		}
	}
	// A review ticket on a notice's button decides nothing either.
	if err := asks.Consent(ctx, mint(agentinteractions.TicketReview, agentinteractions.ReviewSend), "user_raya", slackrouter.SurfaceSlack); !slackrouter.IsPermanent(err) {
		t.Errorf("a review ticket on a notice's button: Consent = %v, want it refused for good", err)
	}
	if ledger.reviews != 0 {
		t.Fatal("a refused click reviewed the answer")
	}

	ledger.getErr = errors.New("database down")
	if err := asks.Review(ctx, mint(agentinteractions.TicketReview, agentinteractions.ReviewWithhold), "user_raya"); err == nil || slackrouter.IsPermanent(err) {
		t.Fatalf("a ledger that did not answer: Review = %v, want it tried again", err)
	}
	ledger.getErr = nil

	withhold := mint(agentinteractions.TicketReview, agentinteractions.ReviewWithhold)
	if err := asks.Review(ctx, withhold, "user_raya"); err != nil || ledger.reviews != 1 {
		t.Fatalf("the owner's click: Review = %v with %d reviews, want it recorded", err, ledger.reviews)
	}
	if ledger.request.Review.DecidedBy != (agents.InteractionDecider{Kind: agentinteractions.DecidedByMember, UserID: "user_raya", Surface: agentinteractions.SurfaceSlack}) {
		t.Fatalf("the review is recorded as %+v", ledger.request.Review.DecidedBy)
	}
	// Clicked again, the review is over: refused for good, not tried again.
	if err := asks.Review(ctx, withhold, "user_raya"); !slackrouter.IsPermanent(err) || ledger.reviews != 1 {
		t.Fatalf("a second click: Review = %v with %d reviews", err, ledger.reviews)
	}
}
