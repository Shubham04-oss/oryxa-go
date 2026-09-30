package interactions

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func reviewClaims() TicketClaims {
	return TicketClaims{
		RequestID: "irq_1", OwnerUserID: "user_b", NoticeID: "review_irq_1", Kind: TicketReview, Choice: ReviewSendMasked,
		ExpiresAt: ticketNow.Add(time.Hour),
	}
}

// TestAReviewTicketReadsBackAsAReview: it is a ticket like any other, the
// version, the claims and an HMAC over both, with its kind among the claims, and
// short enough to be a Slack button's value.
func TestAReviewTicketReadsBackAsAReview(t *testing.T) {
	tickets := testTickets(t)
	ticket, err := tickets.Mint(reviewClaims())
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
	const want = `{"r":"irq_1","o":"user_b","n":"review_irq_1","c":"send_masked","k":"review","exp":1790694000}`
	if string(body) != want {
		t.Fatalf("claims = %s, want %s", body, want)
	}
	claims, err := tickets.Verify(ticket, ticketNow)
	if err != nil || claims != reviewClaims() {
		t.Fatalf("Verify = %+v, %v; want %+v", claims, err, reviewClaims())
	}
}

// maxActionValue is Slack's own limit on a button's value, which apps holds a
// ticket to.
const maxActionValue = 2000

// TestTheKindsOfTicketAreApart: a ticket is made for one kind of button and one
// of that kind's choices, and nothing reads it as the other: not a choice of the
// other kind under either, not a kind this ledger does not make, not a body that
// names the kind "consent", which no consent ticket ever has.
func TestTheKindsOfTicketAreApart(t *testing.T) {
	tickets := testTickets(t)
	for name, change := range map[string]func(*TicketClaims){
		"a consent that sends":        func(c *TicketClaims) { c.Kind, c.Choice = TicketConsent, ReviewSend },
		"a consent that withholds":    func(c *TicketClaims) { c.Kind, c.Choice = TicketConsent, ReviewWithhold },
		"a review that gives access":  func(c *TicketClaims) { c.Kind, c.Choice = TicketReview, ChoiceAccess },
		"a review that denies":        func(c *TicketClaims) { c.Kind, c.Choice = TicketReview, ChoiceDeny },
		"a kind nobody knows":         func(c *TicketClaims) { c.Kind = "forward" },
		"no kind at all":              func(c *TicketClaims) { c.Kind = "" },
		"a review with no choice":     func(c *TicketClaims) { c.Choice = "" },
		"a review with its own words": func(c *TicketClaims) { c.Choice = "send it now" },
	} {
		claims := reviewClaims()
		change(&claims)
		if _, err := tickets.Mint(claims); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Mint = %v, want ErrInvalid", name, err)
		}
	}
	// Signed by this ledger's own key, a body that mixes the kinds is still no
	// ticket: the signature says who made it, not that it says something this
	// ledger makes.
	for name, body := range map[string]string{
		"a consent ticket that sends":     `{"r":"irq_1","o":"user_b","n":"ntc_1","c":"send","exp":1790694000}`,
		"a review ticket that summarizes": `{"r":"irq_1","o":"user_b","n":"review_irq_1","c":"summary","k":"review","exp":1790694000}`,
		"a kind spelled out as consent":   `{"r":"irq_1","o":"user_b","n":"ntc_1","c":"summary","k":"consent","exp":1790694000}`,
		"a kind nobody knows":             `{"r":"irq_1","o":"user_b","n":"ntc_1","c":"summary","k":"forward","exp":1790694000}`,
		"a kind that is not text":         `{"r":"irq_1","o":"user_b","n":"ntc_1","c":"summary","k":1,"exp":1790694000}`,
	} {
		if _, err := tickets.Verify(resign(t, ticketKey, body), ticketNow); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("%s: Verify = %v, want ErrTicketInvalid", name, err)
		}
	}
	// A genuine review ticket with its kind taken out, or a genuine consent
	// ticket with one put in, is not signed: neither can be turned into the other.
	review, err := tickets.Mint(reviewClaims())
	if err != nil {
		t.Fatal(err)
	}
	consent, err := tickets.Mint(validClaims())
	if err != nil {
		t.Fatal(err)
	}
	reviewParts, consentParts := strings.Split(review, "."), strings.Split(consent, ".")
	stripped := base64.RawURLEncoding.EncodeToString([]byte(`{"r":"irq_1","o":"user_b","n":"review_irq_1","c":"send_masked","exp":1790694000}`))
	added := base64.RawURLEncoding.EncodeToString([]byte(`{"r":"irq_1","o":"user_b","n":"ntc_1","c":"access","k":"review","exp":1790694000}`))
	for name, altered := range map[string]string{
		"a review's kind taken out": reviewParts[0] + "." + stripped + "." + reviewParts[2],
		"a consent's kind put in":   consentParts[0] + "." + added + "." + consentParts[2],
	} {
		if _, err := tickets.Verify(altered, ticketNow); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("%s: Verify = %v, want ErrTicketInvalid", name, err)
		}
	}
}

// TestEachReviewHasATicketOfItsOwn: the three buttons of a review differ from
// each other and from a notice's, and each reads back as its own.
func TestEachReviewHasATicketOfItsOwn(t *testing.T) {
	tickets := testTickets(t)
	seen := map[string]string{}
	for _, kinded := range []struct{ kind, choice string }{
		{TicketReview, ReviewSend}, {TicketReview, ReviewSendMasked}, {TicketReview, ReviewWithhold},
		{TicketConsent, ChoiceSummary}, {TicketConsent, ChoiceAccess}, {TicketConsent, ChoiceDeny},
	} {
		claims := reviewClaims()
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
		read, err := tickets.Verify(ticket, ticketNow)
		if err != nil || read.Kind != kinded.kind || read.Choice != kinded.choice {
			t.Fatalf("%s read back as %+v, %v", name, read, err)
		}
	}
}

// TestTicketsOfBothKindsAreMadeAndReadAtOnce: the ledger's tickets are shared
// by every request the service handles at once, so making and reading them, of
// either kind, from many goroutines reads back each as it was made. Run with
// -race.
func TestTicketsOfBothKindsAreMadeAndReadAtOnce(t *testing.T) {
	tickets := testTickets(t)
	var wait sync.WaitGroup
	failures := make(chan string, 64)
	for worker := range 16 {
		wait.Go(func() {
			for round := range 100 {
				claims := reviewClaims()
				claims.RequestID = fmt.Sprintf("irq_%d_%d", worker, round)
				if round%2 == 0 {
					claims.Kind, claims.Choice, claims.NoticeID = TicketConsent, ChoiceDeny, "notice_"+claims.RequestID
				}
				ticket, err := tickets.Mint(claims)
				if err != nil {
					failures <- err.Error()
					return
				}
				read, err := tickets.Verify(ticket, ticketNow)
				if err != nil || read != claims {
					failures <- fmt.Sprintf("%+v read back as %+v, %v", claims, read, err)
					return
				}
			}
		})
	}
	wait.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}
