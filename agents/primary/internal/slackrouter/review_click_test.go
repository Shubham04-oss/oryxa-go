package slackrouter

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
)

// reviewTicket is the shape of a review's ticket. The router must not read it
// either, and it is not the consent ticket, so a click that hands one over
// hands over what it was given.
const reviewTicket = "t1.eyJyIjoiaXJxXzEiLCJvIjoidXNyX3JheWEiLCJuIjoicmV2aWV3X2lycV8xIiwiYyI6InNlbmQiLCJrIjoicmV2aWV3IiwiZXhwIjoxNzAwMDAzNjAwfQ.Z9y8X7w6V5u4T3s2R1q0P9o8N7m6L5k4J3i2H1g0F9e"

// TestAClickOnAReviewButtonGoesToTheReviewUnchanged: the ticket, and the member
// who clicked, and nothing to the decision; for each of the review's buttons.
func TestAClickOnAReviewButtonGoesToTheReviewUnchanged(t *testing.T) {
	for _, action := range []string{ActionReviewSend, ActionReviewSendMasked, ActionReviewWithhold} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			h.member("URAYA", "Raya")
			h.mustRun(h.clickAction(action, "URAYA", reviewTicket))
			if got, want := h.asks.review, []reviewCall{{Ticket: reviewTicket, Clicker: "usr_raya"}}; len(got) != 1 || got[0] != want[0] {
				t.Fatalf("Review(%+v), want %+v", got, want)
			}
			if len(h.asks.consent) != 0 {
				t.Fatalf("a review's click reached the decision: %+v", h.asks.consent)
			}
			// The router reviews nothing itself, and says nothing: what the click
			// did is for the surfaces to show, from the ledger.
			if len(h.asks.created) != 0 || h.asks.countCalls != 0 || len(h.slack.sent()) != 0 {
				t.Fatalf("a review's click did more than reach the review: %+v %+v", h.asks.created, h.slack.sent())
			}
		})
	}
}

// TestEveryOtherClickStillGoesToTheDecision: the notice's buttons, and any
// button the router does not know, which is what a notice's were before
// reviews had buttons.
func TestEveryOtherClickStillGoesToTheDecision(t *testing.T) {
	for _, action := range []string{"consent_summary", "consent_access", "consent_deny", "consent", "review", "review_send_later", ""} {
		h := newHarness(t)
		h.member("URAYA", "Raya")
		h.mustRun(h.clickAction(action, "URAYA", ticket))
		if len(h.asks.consent) != 1 || len(h.asks.review) != 0 {
			t.Fatalf("a click on %q: consent %+v, review %+v; want the decision alone", action, h.asks.consent, h.asks.review)
		}
	}
}

// TestTheRouterNamesTheReviewsButtonsAsAppsDoes: its copies of apps' action ids
// are the ones apps renders (apps' TestEveryTemplateRendersAsPinned pins them,
// in the held_review goldens), and none is a notice's.
func TestTheRouterNamesTheReviewsButtonsAsAppsDoes(t *testing.T) {
	for action, want := range map[string]string{
		ActionReviewSend: "review_send", ActionReviewSendMasked: "review_send_masked", ActionReviewWithhold: "review_withhold",
	} {
		if action != want || !IsReviewAction(action) {
			t.Errorf("the router names a review's button %q, apps names it %q", action, want)
		}
	}
	for _, notice := range []string{"consent_summary", "consent_access", "consent_deny"} {
		if IsReviewAction(notice) {
			t.Errorf("%s is taken for a review's button", notice)
		}
	}
	// The ledger's own names for what the buttons do are what the action ids
	// end in, which is all the two share: the ticket says the rest.
	for _, review := range []string{interactions.ReviewSend, interactions.ReviewSendMasked, interactions.ReviewWithhold} {
		if !IsReviewAction("review_" + review) {
			t.Errorf("the ledger's %s has no button", review)
		}
	}
}

// TestAReviewClickBySomeoneWhoIsNotAMemberIsDropped: nobody who is not a member
// can be the owner a review's ticket names.
func TestAReviewClickBySomeoneWhoIsNotAMemberIsDropped(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.profile("URAYA", func(p *Profile) { p.Guest = true })
	if err := h.run(h.clickAction(ActionReviewSend, "URAYA", reviewTicket)); !IsPermanent(err) {
		t.Fatalf("err = %v, want the click dropped", err)
	}
	if len(h.asks.review) != 0 {
		t.Fatalf("Review called for a guest's click: %+v", h.asks.review)
	}
}

// TestWhatTheReviewAnswersIsRelayedToTheQueueWithoutTheTicket: a refusal is
// permanent and a failure is tried again, as for a decision, and the ticket is
// never carried on or logged.
func TestWhatTheReviewAnswersIsRelayedToTheQueueWithoutTheTicket(t *testing.T) {
	var logs bytes.Buffer
	h := newHarness(t)
	h.member("URAYA", "Raya")
	router, err := NewRouter(Deps{
		Slack: h.slack, Members: h.members, Identities: h.identities, Asks: h.asks,
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.router = router

	h.mustRun(h.clickAction(ActionReviewWithhold, "URAYA", reviewTicket))
	if !strings.Contains(logs.String(), "a click was handed to the consent core") || !strings.Contains(logs.String(), ActionReviewWithhold) {
		t.Fatalf("the click was not logged by what it was:\n%s", logs.String())
	}

	// The answer was reviewed already, on either surface: refused the same way
	// again, and the ticket it names is not carried on.
	h.asks.reviewErr = Permanent(errors.New("ticket " + reviewTicket + " is for an answer no longer in review"))
	err = h.run(h.clickAction(ActionReviewSend, "URAYA", reviewTicket))
	if !IsPermanent(err) || strings.Contains(err.Error(), reviewTicket) || !strings.Contains(err.Error(), "[ticket] is for an answer no longer in review") {
		t.Fatalf("err = %v, want a permanent refusal without the ticket", err)
	}
	// A database that did not answer is not an answer.
	h.asks.reviewErr = errors.New("database down")
	if err := h.run(h.clickAction(ActionReviewSend, "URAYA", reviewTicket)); err == nil || IsPermanent(err) {
		t.Fatalf("err = %v, want a failure to retry", err)
	}
	if strings.Contains(logs.String(), reviewTicket) || strings.Contains(logs.String(), "t1.") {
		t.Fatalf("the ticket is in the log:\n%s", logs.String())
	}
}
