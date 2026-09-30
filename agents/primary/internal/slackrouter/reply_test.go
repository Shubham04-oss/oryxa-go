package slackrouter

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// replyTicket is the shape of a reply's ticket, which the router must not read
// either.
const replyTicket = "t1.eyJyIjoiaXJxXzEiLCJvIjoidXNyX3JheWEiLCJuIjoibm90aWNlX2lycV8xIiwiYyI6InBlbmRpbmciLCJrIjoicmVwbHkiLCJleHAiOjE3MDAwMDM2MDB9.Qm8y-X7w6V5u4T3s2R1q0P9o8N7m6L5k4J3i2H1g0F9e"

// replyWords are an owner's reply, which is the answer, and which the router
// must neither log nor carry on.
const replyWords = "We ship on the 14th, and I sign it off."

// sent records a reply sent by a Slack user from the modal of their notice, as
// apps keeps it, and returns its job: an action's, as the bus names it.
func (h *harness) sent(userID, ticket, text string) Job {
	h.t.Helper()
	h.seq++
	id := fmt.Sprintf("in_%d", h.seq)
	h.slack.inbound[id] = Inbound{
		ID: id, OrganizationID: testOrg, TeamID: testTeam, Kind: KindReply,
		ReceivedAt: time.Unix(1700000000, 0).UTC(),
		Reply:      &Reply{UserID: userID, ChannelID: "D1", MessageTS: "1700000001.000001", Value: ticket, Text: text},
	}
	return Job{
		InboundID: id, Kind: KindAction, OrganizationID: testOrg, TeamID: testTeam,
		EventID: "evt_" + id, ReceivedAt: time.Unix(1700000000, 0).UTC(),
	}
}

// TestAReplyGoesToTheLedgerUnchanged: the ticket, the member who sent it and
// their words, and nothing to the decision or the review; the router records
// nothing, reads only the sender, and says nothing.
func TestAReplyGoesToTheLedgerUnchanged(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.mustRun(h.sent("URAYA", replyTicket, replyWords))
	if got, want := h.asks.replies, []replyCall{{Ticket: replyTicket, Sender: "usr_raya", Text: replyWords}}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Reply(%+v), want %+v", got, want)
	}
	if len(h.asks.consent) != 0 || len(h.asks.review) != 0 {
		t.Fatalf("a reply reached the decision or the review: %+v %+v", h.asks.consent, h.asks.review)
	}
	if len(h.asks.created) != 0 || h.asks.countCalls != 0 || len(h.slack.sent()) != 0 || h.slack.channelReads() != 0 {
		t.Fatalf("a reply did more than reach the ledger: %+v %+v", h.asks.created, h.slack.sent())
	}
	if len(h.slack.profileAsk) != 1 || strings.Join(h.slack.profileAsk[0], ",") != "URAYA" {
		t.Fatalf("profile reads = %+v, want the sender alone", h.slack.profileAsk)
	}
}

// TestAReplyIsSentAsTheMemberTheSenderIsNow: the member handed to the ledger is
// the one whose Slack profile, at this moment, has the address the sender has;
// nobody who is not a member sends a reply.
func TestAReplyIsSentAsTheMemberTheSenderIsNow(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.member("UMAYA", "Maya")
	h.profile("URAYA", func(p *Profile) { p.Email = "maya@acme.com" })
	h.mustRun(h.sent("URAYA", replyTicket, replyWords))
	if got := h.asks.replies[0].Sender; got != "usr_maya" {
		t.Fatalf("sender = %q, want the member the address belongs to", got)
	}
	for name, change := range map[string]func(p *Profile){
		"a guest":                func(p *Profile) { p.Guest = true },
		"a bot":                  func(p *Profile) { p.Bot = true },
		"an unconfirmed address": func(p *Profile) { p.EmailConfirmed = false },
		"another workspace":      func(p *Profile) { p.TeamID = "T2" },
	} {
		h := newHarness(t)
		h.member("URAYA", "Raya")
		h.profile("URAYA", change)
		if err := h.run(h.sent("URAYA", replyTicket, replyWords)); !IsPermanent(err) || len(h.asks.replies) != 0 {
			t.Errorf("%s: err = %v, replies = %+v; want it dropped", name, err, h.asks.replies)
		}
	}
}

// TestAReplyThatCarriesNothingIsDropped: no ticket, no sender, no words, or no
// reply at all, and nothing reaches the ledger.
func TestAReplyThatCarriesNothingIsDropped(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	for name, job := range map[string]Job{
		"no ticket": h.sent("URAYA", "", replyWords),
		"no sender": h.sent("", replyTicket, replyWords),
		"no words":  h.sent("URAYA", replyTicket, "  \n "),
	} {
		if err := h.run(job); !IsPermanent(err) {
			t.Errorf("%s: err = %v, want it dropped", name, err)
		}
	}
	empty := h.sent("URAYA", replyTicket, replyWords)
	inbound := h.slack.inbound[empty.InboundID]
	inbound.Reply = nil
	h.slack.inbound[empty.InboundID] = inbound
	if err := h.run(empty); !IsPermanent(err) {
		t.Errorf("a reply row with no reply: err = %v", err)
	}
	if len(h.asks.replies) != 0 {
		t.Fatalf("the ledger was handed %+v", h.asks.replies)
	}
}

// TestAReplyIsOnlyAnInteractionsRow: an interaction's event names a click or a
// reply, and a message's event never names a reply, nor an interaction's a
// message.
func TestAReplyIsOnlyAnInteractionsRow(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	job := h.sent("URAYA", replyTicket, replyWords)
	job.Kind = KindMessage
	if err := h.run(job); !IsPermanent(err) || len(h.asks.replies) != 0 {
		t.Fatalf("a reply under a message's event: err = %v, replies = %+v", err, h.asks.replies)
	}
	for job, inbound := range map[string]string{KindMessage: KindMessage, KindAction: KindAction} {
		if !sameKind(job, inbound) {
			t.Errorf("a %s event does not name its own row", job)
		}
	}
	if !sameKind(KindAction, KindReply) || sameKind(KindMessage, KindReply) || sameKind(KindReply, KindAction) || sameKind(KindAction, KindMessage) {
		t.Fatal("the kinds an event names are not what they should be")
	}
}

// TestWhatTheLedgerAnswersAReplyIsRelayedWithoutTheTicketOrTheWords: a refusal,
// a reply that lost to another decision among them, is permanent, a failure is
// tried again, and neither the ticket nor the words are carried on or logged.
func TestWhatTheLedgerAnswersAReplyIsRelayedWithoutTheTicketOrTheWords(t *testing.T) {
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
	h.mustRun(h.sent("URAYA", replyTicket, replyWords))
	if !strings.Contains(logs.String(), "a reply was handed to the consent core") {
		t.Fatalf("the reply was not logged:\n%s", logs.String())
	}
	h.asks.replyErr = Permanent(errors.New("ticket " + replyTicket + " lost: " + replyWords + " came after a choice"))
	err = h.run(h.sent("URAYA", replyTicket, replyWords))
	if !IsPermanent(err) || strings.Contains(err.Error(), replyTicket) || strings.Contains(err.Error(), replyWords) ||
		!strings.Contains(err.Error(), "[ticket] lost: [reply] came after a choice") {
		t.Fatalf("err = %v, want a permanent refusal without the ticket or the words", err)
	}
	h.asks.replyErr = errors.New("database down")
	if err := h.run(h.sent("URAYA", replyTicket, replyWords)); err == nil || IsPermanent(err) {
		t.Fatalf("err = %v, want a failure to retry", err)
	}
	for _, secret := range []string{replyTicket, "t1.", replyWords, "14th"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log carries %q:\n%s", secret, logs.String())
		}
	}
}
