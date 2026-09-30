package interactions

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func replyClaims() TicketClaims {
	return TicketClaims{
		RequestID: "irq_1", OwnerUserID: "user_b", NoticeID: "notice_irq_1", Kind: TicketReply, Choice: StatusPending,
		ExpiresAt: ticketNow.Add(time.Hour),
	}
}

// TestAReplyTicketReadsBackAsAReply: it is a ticket like the others, with its
// kind, and the status its request must still be in, among the claims, and is
// short enough to be a Slack button's value, and a modal's private metadata.
func TestAReplyTicketReadsBackAsAReply(t *testing.T) {
	tickets := testTickets(t)
	for _, stage := range []string{StatusPending, StatusReview} {
		claims := replyClaims()
		claims.Choice = stage
		ticket, err := tickets.Mint(claims)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(ticket, ".")
		if len(parts) != 3 || parts[0] != "t1" || len(ticket) > maxActionValue {
			t.Fatalf("ticket = %q, want t1.<claims>.<mac> short enough for a button", ticket)
		}
		body, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"r":"irq_1","o":"user_b","n":"notice_irq_1","c":"` + stage + `","k":"reply","exp":1790694000}`; string(body) != want {
			t.Fatalf("claims = %s, want %s", body, want)
		}
		read, err := tickets.Verify(ticket, ticketNow)
		if err != nil || read != claims {
			t.Fatalf("Verify = %+v, %v; want %+v", read, err, claims)
		}
	}
}

// TestTheReplyKindIsApartFromTheOthers: a reply ticket names a status a reply
// lands on and nothing else, no other kind names one, and nothing mixes them:
// not a mint, not a body signed by this ledger's own key, not a genuine ticket
// with its kind put in or taken out.
func TestTheReplyKindIsApartFromTheOthers(t *testing.T) {
	tickets := testTickets(t)
	for name, change := range map[string]func(*TicketClaims){
		"a reply that summarizes":       func(c *TicketClaims) { c.Choice = ChoiceSummary },
		"a reply that denies":           func(c *TicketClaims) { c.Choice = ChoiceDeny },
		"a reply that sends":            func(c *TicketClaims) { c.Choice = ReviewSend },
		"a reply that withholds":        func(c *TicketClaims) { c.Choice = ReviewWithhold },
		"a reply to an approval":        func(c *TicketClaims) { c.Choice = StatusApproved },
		"a reply to a completed answer": func(c *TicketClaims) { c.Choice = StatusCompleted },
		"a reply named after itself":    func(c *TicketClaims) { c.Choice = ReviewReply },
		"a reply with no stage":         func(c *TicketClaims) { c.Choice = "" },
		"a consent that replies":        func(c *TicketClaims) { c.Kind, c.Choice = TicketConsent, StatusPending },
		"a consent that replies again":  func(c *TicketClaims) { c.Kind, c.Choice = TicketConsent, ChoiceReplyWord },
		"a review that replies":         func(c *TicketClaims) { c.Kind, c.Choice = TicketReview, StatusReview },
		"a review that replies again":   func(c *TicketClaims) { c.Kind, c.Choice = TicketReview, ReviewReply },
	} {
		claims := replyClaims()
		change(&claims)
		if _, err := tickets.Mint(claims); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Mint = %v, want ErrInvalid", name, err)
		}
	}
	for name, body := range map[string]string{
		"a reply ticket that summarizes":  `{"r":"irq_1","o":"user_b","n":"notice_irq_1","c":"summary","k":"reply","exp":1790694000}`,
		"a reply ticket that sends":       `{"r":"irq_1","o":"user_b","n":"review_irq_1","c":"send","k":"reply","exp":1790694000}`,
		"a reply ticket to an approval":   `{"r":"irq_1","o":"user_b","n":"notice_irq_1","c":"approved","k":"reply","exp":1790694000}`,
		"a consent ticket that replies":   `{"r":"irq_1","o":"user_b","n":"notice_irq_1","c":"pending","exp":1790694000}`,
		"a review ticket that replies":    `{"r":"irq_1","o":"user_b","n":"review_irq_1","c":"review","k":"review","exp":1790694000}`,
		"a review ticket that says reply": `{"r":"irq_1","o":"user_b","n":"review_irq_1","c":"reply","k":"review","exp":1790694000}`,
		"a reply kind in capitals":        `{"r":"irq_1","o":"user_b","n":"notice_irq_1","c":"pending","k":"REPLY","exp":1790694000}`,
	} {
		if _, err := tickets.Verify(resign(t, ticketKey, body), ticketNow); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("%s: Verify = %v, want ErrTicketInvalid", name, err)
		}
	}
	reply, err := tickets.Mint(replyClaims())
	if err != nil {
		t.Fatal(err)
	}
	consent, err := tickets.Mint(validClaims())
	if err != nil {
		t.Fatal(err)
	}
	replyParts, consentParts := strings.Split(reply, "."), strings.Split(consent, ".")
	asReview := base64.RawURLEncoding.EncodeToString([]byte(`{"r":"irq_1","o":"user_b","n":"notice_irq_1","c":"pending","k":"review","exp":1790694000}`))
	asConsent := base64.RawURLEncoding.EncodeToString([]byte(`{"r":"irq_1","o":"user_b","n":"notice_irq_1","c":"pending","exp":1790694000}`))
	putIn := base64.RawURLEncoding.EncodeToString([]byte(`{"r":"irq_1","o":"user_b","n":"ntc_1","c":"access","k":"reply","exp":1790694000}`))
	otherStage := base64.RawURLEncoding.EncodeToString([]byte(`{"r":"irq_1","o":"user_b","n":"notice_irq_1","c":"review","k":"reply","exp":1790694000}`))
	for name, altered := range map[string]string{
		"a reply's kind made a review's":     replyParts[0] + "." + asReview + "." + replyParts[2],
		"a reply's kind taken out":           replyParts[0] + "." + asConsent + "." + replyParts[2],
		"a consent's kind made a reply's":    consentParts[0] + "." + putIn + "." + consentParts[2],
		"a notice's reply made the review's": replyParts[0] + "." + otherStage + "." + replyParts[2],
	} {
		if _, err := tickets.Verify(altered, ticketNow); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("%s: Verify = %v, want ErrTicketInvalid", name, err)
		}
	}
	// Every kind and choice there is makes a ticket of its own.
	seen := map[string]string{}
	for _, kinded := range []struct{ kind, choice string }{
		{TicketConsent, ChoiceSummary}, {TicketConsent, ChoiceAccess}, {TicketConsent, ChoiceDeny},
		{TicketReview, ReviewSend}, {TicketReview, ReviewSendMasked}, {TicketReview, ReviewWithhold},
		{TicketReply, StatusPending}, {TicketReply, StatusReview},
	} {
		claims := replyClaims()
		claims.Kind, claims.Choice = kinded.kind, kinded.choice
		ticket, err := tickets.Mint(claims)
		if err != nil {
			t.Fatal(err)
		}
		name := kinded.kind + "/" + kinded.choice
		if other, dup := seen[ticket]; dup {
			t.Fatalf("%s and %s made the same ticket", name, other)
		}
		seen[ticket] = name
		if read, err := tickets.Verify(ticket, ticketNow); err != nil || read.Kind != kinded.kind || read.Choice != kinded.choice {
			t.Fatalf("%s read back as %+v, %v", name, read, err)
		}
	}
}

// ChoiceReplyWord is a word a consent ticket might be tempted to carry for a
// reply, and must not: a consent ticket's choice decides, and "reply" decides
// nothing.
const ChoiceReplyWord = "reply"

// TestNoTicketOpensAnotherKindsDoor: a reply ticket handed to the decision or to
// the review is refused as a ticket this ledger did not make, and so is a
// consent's or a review's handed to the reply, whoever holds it; none changes
// anything.
func TestNoTicketOpensAnotherKindsDoor(t *testing.T) {
	ctx := context.Background()
	service, repo, request := heldFromSlack(t, heldVerdict, nil)
	review := reviewTickets(t, service, request)
	if review.Reply == "" {
		t.Fatal("the review has no Reply yourself ticket")
	}
	if _, err := service.Consent(ctx, review.Reply, "user_b", SurfaceSlack); !errors.Is(err, ErrTicketInvalid) {
		t.Errorf("a reply ticket handed to the decision: %v, want ErrTicketInvalid", err)
	}
	if _, err := service.ReviewFromSlack(ctx, review.Reply, "user_b"); !errors.Is(err, ErrTicketInvalid) {
		t.Errorf("a reply ticket handed to the review: %v, want ErrTicketInvalid", err)
	}
	for name, ticket := range map[string]string{"send": review.Send, "send masked": review.SendMasked, "withhold": review.Withhold} {
		if _, err := service.ReplyFromSlack(ctx, ticket, "user_b", "My own words."); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("a review ticket (%s) handed to the reply: %v, want ErrTicketInvalid", name, err)
		}
	}
	for _, choice := range []string{ChoiceSummary, ChoiceAccess, ChoiceDeny} {
		consent, err := service.tickets.Mint(TicketClaims{
			RequestID: request.ID, OwnerUserID: "user_b", NoticeID: "notice_" + request.ID, Kind: TicketConsent, Choice: choice, ExpiresAt: request.ExpiresAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ReplyFromSlack(ctx, consent, "user_b", "My own words."); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("a consent ticket (%s) handed to the reply: %v, want ErrTicketInvalid", choice, err)
		}
	}
	if stored := repo.requests[request.ID]; stored.Status != StatusReview || stored.Held == nil || repo.reviews != 0 || repo.replies != 0 {
		t.Fatalf("a ticket of another kind changed the request: %+v, %d reviews, %d replies", stored, repo.reviews, repo.replies)
	}
	// Its own door opens.
	if replied, err := service.ReplyFromSlack(ctx, review.Reply, "user_b", "My own words."); err != nil || replied.Status != StatusCompleted {
		t.Fatalf("the review's own reply ticket: %+v, %v", replied, err)
	}
}

// TestTheNoticeAndTheReviewEachHaveAReplyTicketForTheirOwnStage: the notice's
// Reply yourself lands only while the request is pending, the review's only
// while its answer is in review; each expires with the others on its message.
func TestTheNoticeAndTheReviewEachHaveAReplyTicketForTheirOwnStage(t *testing.T) {
	ctx := context.Background()
	service, repo := slackReplyService(t)
	pending := pendingFromSlack(repo)
	notice, err := service.ConsentTickets(ctx, pending.ID, "notice_"+pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := service.tickets.Verify(notice.Reply, testNow)
	want := TicketClaims{
		RequestID: pending.ID, OwnerUserID: "user_b", NoticeID: "notice_" + pending.ID, Kind: TicketReply, Choice: StatusPending,
		ExpiresAt: pending.ExpiresAt.Truncate(time.Second),
	}
	if err != nil || claims != want || !notice.ExpiresAt.Equal(pending.ExpiresAt) {
		t.Fatalf("the notice's reply ticket reads %+v, %v; want %+v", claims, err, want)
	}

	held, _, request := heldFromSlack(t, heldVerdict, nil)
	review := reviewTickets(t, held, request)
	claims, err = held.tickets.Verify(review.Reply, testNow)
	want = TicketClaims{
		RequestID: request.ID, OwnerUserID: "user_b", NoticeID: "review_" + request.ID, Kind: TicketReply, Choice: StatusReview,
		ExpiresAt: request.ExpiresAt.Truncate(time.Second),
	}
	if err != nil || claims != want {
		t.Fatalf("the review's reply ticket reads %+v, %v; want %+v", claims, err, want)
	}
}
