package interactions

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
)

// heldFromSlack is a ledger with the tickets of a known key and, in it, a
// request asked in Slack, approved with a summary, whose answer the guard held
// for user_b's review as verdict says: with a masked version to send when the
// verdict has one. logs, when given, is where the ledger logs.
func heldFromSlack(t *testing.T, verdict answerguard.Verdict, logs *bytes.Buffer) (*Service, *fakeRepository, agents.Interaction) {
	t.Helper()
	var service *Service
	var repo *fakeRepository
	var approved agents.Interaction
	if logs != nil {
		service, repo, approved = guardedService(t, &fakeGuard{verdict: &verdict}, logs)
	} else {
		service, repo, approved = guardedService(t, &fakeGuard{verdict: &verdict}, nil)
	}
	request := repo.requests[approved.ID]
	request.Origin = OriginSlack
	request.Decision.Consent = ConsentSummary
	repo.requests[approved.ID] = request
	tickets, err := NewTickets(ticketKey)
	if err != nil {
		t.Fatal(err)
	}
	service.tickets = tickets
	if _, err := service.Respond(context.Background(), target, approved.ID, answerGiven); err != nil {
		t.Fatal(err)
	}
	held := repo.requests[approved.ID]
	if held.Status != StatusReview || held.Held == nil {
		t.Fatalf("the answer is %+v, want it held for review", held)
	}
	return service, repo, held
}

// reviewTickets are the tickets of a held answer's review in Slack.
func reviewTickets(t *testing.T, service *Service, request agents.Interaction) ReviewTickets {
	t.Helper()
	tickets, err := service.ReviewTickets(context.Background(), request.ID, "review_"+request.ID)
	if err != nil {
		t.Fatalf("ReviewTickets: %v", err)
	}
	return tickets
}

// ticketFor is a review's ticket for any action, made with the ledger's own key
// as ReviewTickets would, whether or not it would.
func ticketFor(t *testing.T, service *Service, request agents.Interaction, owner, action string, expires time.Time) string {
	t.Helper()
	ticket, err := service.tickets.Mint(TicketClaims{
		RequestID: request.ID, OwnerUserID: owner, NoticeID: "review_" + request.ID, Kind: TicketReview, Choice: action, ExpiresAt: expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

// slackReviewer is how the review in Slack records who made it: the member the
// ticket was made for, in Slack.
var slackReviewer = agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b", Surface: SurfaceSlack}

func TestAHeldAnswersReviewHasATicketForEachButton(t *testing.T) {
	ctx := context.Background()
	service, _, request := heldFromSlack(t, heldVerdict, nil)
	tickets := reviewTickets(t, service, request)
	if !tickets.ExpiresAt.Equal(request.ExpiresAt) || !request.ExpiresAt.Equal(testNow.Add(ReviewWindow)) {
		t.Fatalf("the tickets expire at %s, want with the review, at %s", tickets.ExpiresAt, request.ExpiresAt)
	}
	for action, ticket := range map[string]string{ReviewSend: tickets.Send, ReviewSendMasked: tickets.SendMasked, ReviewWithhold: tickets.Withhold} {
		claims, err := service.tickets.Verify(ticket, testNow)
		want := TicketClaims{
			RequestID: request.ID, OwnerUserID: "user_b", NoticeID: "review_" + request.ID, Kind: TicketReview, Choice: action,
			ExpiresAt: request.ExpiresAt.Truncate(time.Second),
		}
		if err != nil || claims != want {
			t.Errorf("%s: the ticket reads %+v, %v; want %+v", action, claims, err, want)
		}
	}

	// An answer that could not be checked has no masked version, and no button
	// to send one.
	unchecked, _, held := heldFromSlack(t, uncheckedOne, nil)
	if made := reviewTickets(t, unchecked, held); made.SendMasked != "" || made.Send == "" || made.Withhold == "" {
		t.Fatalf("tickets = %+v, want send and withhold alone", made)
	}

	// Only an answer held for review, before it expires, of a request asked in
	// Slack, has buttons, and only where there is a key to sign them with.
	approved, err := service.Create(ctx, requester, "token", "another", agents.InteractionCreateInput{TargetAgentID: "agt_engineering", Purpose: "P", Task: "T"})
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		change    func(*Service)
		requestID string
		want      error
	}{
		"a request that is not in review": {nil, approved.ID, ErrInvalid},
		"a request that is gone":          {nil, "irq_missing", ErrNotFound},
		"a review that has run out": {func(s *Service) {
			s.now = func() time.Time { return testNow.Add(ReviewWindow) }
		}, request.ID, ErrExpired},
		"a ledger with no ticket key": {func(s *Service) { s.tickets = nil }, request.ID, ErrConsentUnavailable},
	} {
		copied := *service
		if test.change != nil {
			test.change(&copied)
		}
		if _, err := copied.ReviewTickets(ctx, test.requestID, "review_x"); !errors.Is(err, test.want) {
			t.Errorf("%s: ReviewTickets = %v, want %v", name, err, test.want)
		}
	}
	// The request Create made was asked by an agent, not in Slack: it is
	// ErrInvalid before anything else. One asked in Slack that is not in review
	// is ErrConflict.
	pending := service.repository.(*fakeRepository).requests[approved.ID]
	pending.Origin = OriginSlack
	service.repository.(*fakeRepository).requests[approved.ID] = pending
	if _, err := service.ReviewTickets(ctx, approved.ID, "review_x"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a Slack request that is not in review: %v, want ErrConflict", err)
	}
}

// TestEachReviewButtonIsTheOwnersReview: a click on Send sends the answer as it
// was held, Send masked its masked version, and Don't send withholds it; each is
// recorded as the owner's review, made in Slack, now; and a second click, on any
// button, reviews nothing more.
func TestEachReviewButtonIsTheOwnersReview(t *testing.T) {
	ctx := context.Background()
	for action, want := range map[string]struct {
		status  string
		summary string
	}{
		ReviewSend:       {StatusCompleted, textsAsHeld[0]},
		ReviewSendMasked: {StatusCompleted, textsMasked[0]},
		ReviewWithhold:   {StatusWithheld, ""},
	} {
		service, repo, request := heldFromSlack(t, heldVerdict, nil)
		tickets := reviewTickets(t, service, request)
		ticket := map[string]string{ReviewSend: tickets.Send, ReviewSendMasked: tickets.SendMasked, ReviewWithhold: tickets.Withhold}[action]
		reviewed, err := service.ReviewFromSlack(ctx, ticket, "user_b")
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if reviewed.Status != want.status || reviewed.Held != nil {
			t.Fatalf("%s: reviewed = %+v, want %s with nothing held", action, reviewed, want.status)
		}
		if want.summary == "" && reviewed.Result != nil || want.summary != "" && (reviewed.Result == nil || reviewed.Result.Summary != want.summary) {
			t.Fatalf("%s: result = %+v, want summary %q", action, reviewed.Result, want.summary)
		}
		if review := reviewed.Review; review == nil || review.Action != action || review.DecidedBy != slackReviewer || !review.DecidedAt.Equal(testNow) {
			t.Fatalf("%s: review = %+v, want the owner's own, in Slack, now", action, review)
		}
		for _, again := range []string{tickets.Send, tickets.SendMasked, tickets.Withhold} {
			if _, err := service.ReviewFromSlack(ctx, again, "user_b"); !errors.Is(err, ErrConflict) {
				t.Fatalf("%s, clicked again: %v, want ErrConflict", action, err)
			}
		}
		if repo.reviews != 1 {
			t.Fatalf("%s: %d reviews recorded, want one", action, repo.reviews)
		}
	}
}

// TestOnlyTheOwnerTheTicketWasMadeForReviewsWithIt: a click by anyone but the
// member the ticket names, whatever they hold, reviews nothing, and neither does
// a ticket that names someone the request is not addressed to.
func TestOnlyTheOwnerTheTicketWasMadeForReviewsWithIt(t *testing.T) {
	ctx := context.Background()
	service, repo, request := heldFromSlack(t, heldVerdict, nil)
	tickets := reviewTickets(t, service, request)
	for name, clicker := range map[string]string{
		"the member who asked": "user_a",
		"another member":       "user_x",
		"nobody":               "",
		"a blank":              "  ",
		"the owner, padded":    " user_b",
	} {
		if _, err := service.ReviewFromSlack(ctx, tickets.Send, clicker); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s clicked Send: %v, want ErrForbidden", name, err)
		}
	}
	// A ticket made for another member, which only a leaked key could make, is
	// theirs to click, and still reviews nothing: the request is not theirs.
	stranger := ticketFor(t, service, request, "user_x", ReviewSend, request.ExpiresAt)
	if _, err := service.ReviewFromSlack(ctx, stranger, "user_x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a ticket for someone the request is not addressed to: %v, want ErrForbidden", err)
	}
	if stored := repo.requests[request.ID]; stored.Status != StatusReview || stored.Held == nil || repo.reviews != 0 {
		t.Fatalf("a refused click changed the request: %+v, %d reviews", stored, repo.reviews)
	}
	// The owner's own click still reviews it.
	if _, err := service.ReviewFromSlack(ctx, tickets.Withhold, "user_b"); err != nil {
		t.Fatal(err)
	}
}

// TestAnAnswerWithNoMaskedVersionIsNeverSentMasked: its review has no button
// for it, and a ticket for it, however it was made, is refused by the same rule
// the web's review keeps.
func TestAnAnswerWithNoMaskedVersionIsNeverSentMasked(t *testing.T) {
	ctx := context.Background()
	service, repo, request := heldFromSlack(t, uncheckedOne, nil)
	masked := ticketFor(t, service, request, "user_b", ReviewSendMasked, request.ExpiresAt)
	if _, err := service.ReviewFromSlack(ctx, masked, "user_b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("send masked from Slack: %v, want ErrConflict", err)
	}
	if _, err := service.ReviewAsOwner(ctx, inOwnSession(targetPrimary), "member-token", request.ID, agents.InteractionReviewInput{Action: ReviewSendMasked}); !errors.Is(err, ErrConflict) {
		t.Fatalf("send masked on the web: %v, want ErrConflict", err)
	}
	if stored := repo.requests[request.ID]; stored.Status != StatusReview || repo.reviews != 0 {
		t.Fatalf("a refused review changed the request: %+v", stored)
	}
	// Sent as it was held, it is sent.
	if reviewed, err := service.ReviewFromSlack(ctx, reviewTickets(t, service, request).Send, "user_b"); err != nil || reviewed.Status != StatusCompleted {
		t.Fatalf("send: %+v, %v", reviewed, err)
	}
}

// TestAConsentTicketNeverReviewsAndAReviewTicketNeverDecides: each is refused by
// the other's door as a ticket this ledger did not make would be, whoever holds
// it, and changes nothing.
func TestAConsentTicketNeverReviewsAndAReviewTicketNeverDecides(t *testing.T) {
	ctx := context.Background()
	service, repo, request := heldFromSlack(t, heldVerdict, nil)
	for _, choice := range []string{ChoiceSummary, ChoiceAccess, ChoiceDeny} {
		consent, err := service.tickets.Mint(TicketClaims{
			RequestID: request.ID, OwnerUserID: "user_b", NoticeID: "notice_" + request.ID, Kind: TicketConsent, Choice: choice, ExpiresAt: request.ExpiresAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ReviewFromSlack(ctx, consent, "user_b"); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("a consent ticket (%s) handed to the review: %v, want ErrTicketInvalid", choice, err)
		}
	}
	tickets := reviewTickets(t, service, request)
	for _, ticket := range []string{tickets.Send, tickets.SendMasked, tickets.Withhold} {
		if _, err := service.Consent(ctx, ticket, "user_b", SurfaceSlack); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("a review ticket handed to the decision: %v, want ErrTicketInvalid", err)
		}
	}
	if stored := repo.requests[request.ID]; stored.Status != StatusReview || stored.Held == nil || repo.reviews != 0 {
		t.Fatalf("a ticket of the other kind changed the request: %+v", stored)
	}
}

// TestAReviewInSlackEndsWithTheReview: its tickets run out when the review does,
// and a ticket that somehow outlived it still reviews nothing.
func TestAReviewInSlackEndsWithTheReview(t *testing.T) {
	ctx := context.Background()
	service, repo, request := heldFromSlack(t, heldVerdict, nil)
	tickets := reviewTickets(t, service, request)
	longer := ticketFor(t, service, request, "user_b", ReviewSend, request.ExpiresAt.Add(24*time.Hour))
	service.now = func() time.Time { return request.ExpiresAt }
	if _, err := service.ReviewFromSlack(ctx, tickets.Send, "user_b"); !errors.Is(err, ErrTicketExpired) {
		t.Fatalf("a ticket at its review's end: %v, want ErrTicketExpired", err)
	}
	if _, err := service.ReviewFromSlack(ctx, longer, "user_b"); !errors.Is(err, ErrExpired) {
		t.Fatalf("a ticket that outlives its review: %v, want ErrExpired", err)
	}
	if repo.reviews != 0 {
		t.Fatal("an expired review was recorded")
	}
}

// TestAForgedReviewTicketReviewsNothing: a ticket this ledger did not make, or
// one it made that was altered, is refused before the request is read.
func TestAForgedReviewTicketReviewsNothing(t *testing.T) {
	ctx := context.Background()
	service, repo, request := heldFromSlack(t, heldVerdict, nil)
	genuine := reviewTickets(t, service, request).Send
	other, err := NewTickets([]byte("another-key-of-at-least-32-bytes-long!!"))
	if err != nil {
		t.Fatal(err)
	}
	forged, err := other.Mint(TicketClaims{
		RequestID: request.ID, OwnerUserID: "user_b", NoticeID: "review_" + request.ID, Kind: TicketReview, Choice: ReviewSend, ExpiresAt: request.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, ticket := range map[string]string{
		"signed under another key": forged,
		"altered":                  genuine[:len(genuine)-2] + map[bool]string{true: "AA", false: "BB"}[!strings.HasSuffix(genuine, "AA")],
		"nothing":                  "",
		"not a ticket":             "send",
	} {
		if _, err := service.ReviewFromSlack(ctx, ticket, "user_b"); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("%s: %v, want ErrTicketInvalid", name, err)
		}
	}
	service.tickets = nil
	if _, err := service.ReviewFromSlack(ctx, genuine, "user_b"); !errors.Is(err, ErrConsentUnavailable) {
		t.Errorf("a ledger with no ticket key: %v, want ErrConsentUnavailable", err)
	}
	if repo.reviews != 0 {
		t.Fatal("a forged ticket reviewed something")
	}
}

// TestTheFirstReviewWinsBetweenSlackAndTheWeb: the owner's review on the web and
// their click in Slack are one review, whichever comes first: the second finds
// the answer no longer in review, and changes nothing. The web's review is as it
// was, and says no surface; Slack's says it was made in Slack.
func TestTheFirstReviewWinsBetweenSlackAndTheWeb(t *testing.T) {
	ctx := context.Background()
	send := agents.InteractionReviewInput{Action: ReviewSend}

	webFirst, repo, request := heldFromSlack(t, heldVerdict, nil)
	tickets := reviewTickets(t, webFirst, request)
	onTheWeb, err := webFirst.ReviewAsOwner(ctx, inOwnSession(targetPrimary), "member-token", request.ID, agents.InteractionReviewInput{Action: ReviewWithhold})
	if err != nil {
		t.Fatal(err)
	}
	if onTheWeb.Review.DecidedBy != (agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b"}) {
		t.Fatalf("the web's review is recorded as %+v, want the member's, as it always was", onTheWeb.Review.DecidedBy)
	}
	if _, err := webFirst.ReviewFromSlack(ctx, tickets.Send, "user_b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a click after the web's review: %v, want ErrConflict", err)
	}
	if stored := repo.requests[request.ID]; stored.Status != StatusWithheld || stored.Result != nil || repo.reviews != 1 {
		t.Fatalf("the click after the web's review changed the request: %+v", stored)
	}

	slackFirst, repo, request := heldFromSlack(t, heldVerdict, nil)
	if _, err := slackFirst.ReviewFromSlack(ctx, reviewTickets(t, slackFirst, request).SendMasked, "user_b"); err != nil {
		t.Fatal(err)
	}
	if _, err := slackFirst.ReviewAsOwner(ctx, inOwnSession(targetPrimary), "member-token", request.ID, send); !errors.Is(err, ErrConflict) {
		t.Fatalf("a web review after Slack's: %v, want ErrConflict", err)
	}
	if stored := repo.requests[request.ID]; stored.Result == nil || stored.Result.Summary != textsMasked[0] || stored.Review.DecidedBy != slackReviewer || repo.reviews != 1 {
		t.Fatalf("the web review after Slack's changed the request: %+v", stored)
	}
}

// lockedRepository is the fake ledger behind one lock, as the store's row lock
// is, for callers on many goroutines.
type lockedRepository struct {
	mu sync.Mutex
	Repository
}

func (repo *lockedRepository) Get(ctx context.Context, id string) (agents.Interaction, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.Repository.Get(ctx, id)
}

func (repo *lockedRepository) Review(ctx context.Context, id string, review agents.InteractionReview, at time.Time) (agents.Interaction, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.Repository.Review(ctx, id, review, at)
}

// TestReviewsInSlackAndOnTheWebAtOnceReviewOnce: an owner clicking in Slack and
// on the web card at the same moment, or clicking twice, reviews the answer
// once; every other call is refused, and learns nothing was changed by it. Run
// with -race.
func TestReviewsInSlackAndOnTheWebAtOnceReviewOnce(t *testing.T) {
	ctx := context.Background()
	for round := range 3 {
		service, repo, request := heldFromSlack(t, heldVerdict, nil)
		tickets := reviewTickets(t, service, request)
		byAction := map[string]string{ReviewSend: tickets.Send, ReviewSendMasked: tickets.SendMasked, ReviewWithhold: tickets.Withhold}
		actions := []string{ReviewSend, ReviewSendMasked, ReviewWithhold}
		service.repository = &lockedRepository{Repository: repo}

		const attempts = 24
		errs := make([]error, attempts)
		var start, done sync.WaitGroup
		start.Add(1)
		for index := range attempts {
			done.Go(func() {
				action := actions[(index+round)%len(actions)]
				start.Wait()
				if index%2 == 0 {
					_, errs[index] = service.ReviewFromSlack(ctx, byAction[action], "user_b")
					return
				}
				_, errs[index] = service.ReviewAsOwner(ctx, inOwnSession(targetPrimary), "member-token", request.ID, agents.InteractionReviewInput{Action: action})
			})
		}
		start.Done()
		done.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, ErrConflict):
				t.Fatalf("round %d: a review failed with %v, want it to win or find the answer reviewed", round, err)
			}
		}
		if won != 1 || repo.reviews != 1 {
			t.Fatalf("round %d: %d reviews won and %d were recorded, want exactly one", round, won, repo.reviews)
		}
		if stored := repo.requests[request.ID]; stored.Status == StatusReview || stored.Held != nil || stored.Review == nil {
			t.Fatalf("round %d: the request is %+v, want it reviewed", round, stored)
		}
	}
}

// TestAnAnswerToARequestMadeInATeamspaceIsNotSentFromSlack: sending one reads
// the teamspace again with the member's own token, which a click never carries,
// so a click refuses to send it and leaves it for Oryxa. Withholding needs
// nothing, and is done.
func TestAnAnswerToARequestMadeInATeamspaceIsNotSentFromSlack(t *testing.T) {
	ctx := context.Background()
	service, repo, request := heldFromSlack(t, heldVerdict, nil)
	inATeamspace := repo.requests[request.ID]
	inATeamspace.TeamspaceID = "tsp_1"
	repo.requests[request.ID] = inATeamspace
	tickets := reviewTickets(t, service, request)
	for _, ticket := range []string{tickets.Send, tickets.SendMasked} {
		if _, err := service.ReviewFromSlack(ctx, ticket, "user_b"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("sending from Slack an answer made in a teamspace: %v, want ErrInvalid", err)
		}
	}
	if repo.reviews != 0 {
		t.Fatal("an answer made in a teamspace was sent without reading the teamspace again")
	}
	if withheld, err := service.ReviewFromSlack(ctx, tickets.Withhold, "user_b"); err != nil || withheld.Status != StatusWithheld {
		t.Fatalf("withholding it: %+v, %v", withheld, err)
	}
}

// TestOnlyTheAnswerToARequestAskedInSlackIsReviewedThere: a ticket for another
// request's answer, which only a leaked key could make, reviews nothing.
func TestOnlyTheAnswerToARequestAskedInSlackIsReviewedThere(t *testing.T) {
	ctx := context.Background()
	service, repo, request := heldFromSlack(t, heldVerdict, nil)
	byAnAgent := repo.requests[request.ID]
	byAnAgent.Origin = OriginAgent
	repo.requests[request.ID] = byAnAgent
	if _, err := service.ReviewFromSlack(ctx, ticketFor(t, service, request, "user_b", ReviewSend, request.ExpiresAt), "user_b"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a review in Slack of an agent's request: %v, want ErrInvalid", err)
	}
	if _, err := service.ReviewTickets(ctx, request.ID, "review_x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tickets for an agent's request: %v, want ErrInvalid", err)
	}
	if repo.reviews != 0 {
		t.Fatal("an agent's request was reviewed from Slack")
	}
}

// TestAReviewInSlackLogsNoAnswerAndNoTicket: the log says who reviewed what, by
// id, and never what the answer said or the ticket that reviewed it.
func TestAReviewInSlackLogsNoAnswerAndNoTicket(t *testing.T) {
	var logs bytes.Buffer
	service, _, request := heldFromSlack(t, heldVerdict, &logs)
	ticket := reviewTickets(t, service, request).Send
	if _, err := service.ReviewFromSlack(context.Background(), ticket, "user_b"); err != nil {
		t.Fatal(err)
	}
	logged := logs.String()
	if !strings.Contains(logged, "an owner reviewed a held answer from Slack") || !strings.Contains(logged, request.ID) {
		t.Fatalf("the review was not logged:\n%s", logged)
	}
	for _, secret := range []string{ticket, "priya@example.com", "555 0100", "Reach Priya"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("the log carries %q:\n%s", secret, logged)
		}
	}
}
