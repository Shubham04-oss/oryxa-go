package slackrouter

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// ticket is the shape of a real one, t1.<payload>.<mac>, in base64url. The
// router must not read it, so the test only needs it to be hard to alter by
// accident.
const ticket = "t1.eyJyIjoiaXJxXzEiLCJvIjoidXNyX3JheWEiLCJuIjoibl8xIiwiYyI6InN1bW1hcnkiLCJleHAiOjE3MDAwMDM2MDB9.q-8_Zk0aAAA1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6Q"

func TestAClickGoesToConsentUnchanged(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	job := h.click("URAYA", ticket)
	h.mustRun(job)

	if len(h.asks.consent) != 1 {
		t.Fatalf("Consent called %d times", len(h.asks.consent))
	}
	if got, want := h.asks.consent[0], (consentCall{Ticket: ticket, Clicker: "usr_raya", Surface: "slack"}); got != want {
		t.Fatalf("Consent(%+v), want %+v", got, want)
	}
	// The router never decides: it files nothing, counts nothing, asks the
	// chart nothing and says nothing. What the click decided is for the
	// surfaces to show, from the ledger.
	if len(h.asks.created) != 0 || h.asks.countCalls != 0 || h.asks.createCalls != 0 || len(h.authority.asked) != 0 || len(h.slack.sent()) != 0 {
		t.Fatalf("a click did more than reach Consent: %+v %d %v %+v", h.asks.created, h.asks.countCalls, h.authority.asked, h.slack.sent())
	}
	// Only the clicker was looked up.
	if len(h.slack.profileAsk) != 1 || strings.Join(h.slack.profileAsk[0], ",") != "URAYA" || h.slack.channelReads() != 0 {
		t.Fatalf("profile reads = %+v, channel reads = %d", h.slack.profileAsk, h.slack.channelReads())
	}
}

// TestAClickCarriesNoOneElsesIdentity: the member handed to Consent is the one
// whose Slack profile, at this moment, has the address the click's user has;
// it is never taken from the ticket or from what was kept before.
func TestAClickIsMadeAsTheMemberTheClickerIsNow(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.member("UMAYA", "Maya")
	// Raya's Slack user now has Maya's address, confirmed: they are Maya, to
	// Oryxa, as of this click.
	h.profile("URAYA", func(p *Profile) { p.Email = "MAYA@acme.com" })
	h.mustRun(h.click("URAYA", ticket))
	if got := h.asks.consent[0].Clicker; got != "usr_maya" {
		t.Fatalf("clicker = %q, want the member the address belongs to", got)
	}
}

func TestAClickBySomeoneWhoIsNotAMemberIsDropped(t *testing.T) {
	for name, change := range map[string]func(p *Profile){
		"a guest":                 func(p *Profile) { p.Guest = true },
		"a deactivated account":   func(p *Profile) { p.Deleted = true },
		"a bot":                   func(p *Profile) { p.Bot = true },
		"another workspace":       func(p *Profile) { p.TeamID = "T2" },
		"a Slack Connect user":    func(p *Profile) { p.Stranger = true },
		"an unconfirmed address":  func(p *Profile) { p.EmailConfirmed = false },
		"an address of no member": func(p *Profile) { p.Email = "raya@elsewhere.example" },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.member("URAYA", "Raya")
			h.profile("URAYA", change)
			err := h.run(h.click("URAYA", ticket))
			if !IsPermanent(err) {
				t.Fatalf("err = %v, want the click dropped", err)
			}
			if len(h.asks.consent) != 0 {
				t.Fatalf("Consent called for a click by %s: %+v", name, h.asks.consent)
			}
		})
	}
	t.Run("a Slack user Slack does not know", func(t *testing.T) {
		h := newHarness(t)
		if err := h.run(h.click("UGHOST", ticket)); !IsPermanent(err) || len(h.asks.consent) != 0 {
			t.Fatalf("err = %v, consent = %+v", err, h.asks.consent)
		}
	})
}

func TestWhatConsentAnswersIsRelayedToTheQueue(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")

	// A ticket that is forged, expired, for someone else, or for a request
	// already decided is refused the same way again.
	h.asks.consentErr = Permanent(errors.New("the ticket is not valid"))
	err := h.run(h.click("URAYA", ticket))
	if !IsPermanent(err) || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("err = %v, want a permanent failure", err)
	}
	// A database that did not answer is not an answer.
	h.asks.consentErr = errors.New("database down")
	if err := h.run(h.click("URAYA", ticket)); err == nil || IsPermanent(err) {
		t.Fatalf("err = %v, want a failure to retry", err)
	}
}

func TestAClickThatCarriesNothingIsDropped(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	for name, job := range map[string]Job{
		"no ticket": h.click("URAYA", ""),
		"no user":   h.click("", ticket),
	} {
		if err := h.run(job); !IsPermanent(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	empty := h.click("URAYA", ticket)
	inbound := h.slack.inbound[empty.InboundID]
	inbound.Click = nil
	h.slack.inbound[empty.InboundID] = inbound
	if err := h.run(empty); !IsPermanent(err) {
		t.Errorf("no click: err = %v", err)
	}
	if len(h.asks.consent) != 0 {
		t.Fatalf("Consent called: %+v", h.asks.consent)
	}
}

// TestTheTicketIsNeverLogged: it is a credential for the one decision it
// names, and a log is read by more people than the owner it was made for.
func TestTheTicketIsNeverLogged(t *testing.T) {
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
	h.mustRun(h.click("URAYA", ticket))
	h.asks.consentErr = Permanent(errors.New("the ticket is not valid"))
	_ = h.run(h.click("URAYA", ticket))
	if logs.Len() == 0 {
		t.Fatal("nothing was logged")
	}
	if strings.Contains(logs.String(), ticket) || strings.Contains(logs.String(), "t1.") {
		t.Fatalf("the ticket is in the log:\n%s", logs.String())
	}
}

// TestATicketAConsentLayerEchoesIsNotCarriedOn: the ticket does not reach the
// job's row or the log through an error message, whichever layer wrote it, and
// what the error means is unchanged.
func TestATicketAConsentLayerEchoesIsNotCarriedOn(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.asks.consentErr = Permanent(errors.New("ticket " + ticket + " is not valid (again: " + ticket + ")"))
	err := h.run(h.click("URAYA", ticket))
	if !IsPermanent(err) {
		t.Fatalf("err = %v, want it to stay permanent", err)
	}
	if strings.Contains(err.Error(), ticket) || !strings.Contains(err.Error(), "[ticket] is not valid") {
		t.Fatalf("err = %q carries the ticket, or lost what it said", err)
	}
	if got := scrub(errors.New("no secret here"), ticket, "x"); got.Error() != "no secret here" {
		t.Fatalf("scrub changed an error that did not say it: %v", got)
	}
	if scrub(nil, ticket, "x") != nil || scrub(errors.New("a"), "", "x").Error() != "a" {
		t.Fatal("scrub mishandles nothing")
	}
}

// TestWhatPeopleWroteIsNeverLogged: a message is content, kept by apps and
// purged there.
func TestWhatPeopleWroteIsNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	h := newHarness(t)
	h.member("URAYA", "Raya")
	router, err := NewRouter(Deps{
		Slack: h.slack, Members: h.members, Identities: h.identities, Asks: h.asks, Screen: h.screen,
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.router = router
	h.mustRun(h.mention(toBot("ask <@URAYA> for the SECRET-QUARTERLY-FIGURES")))
	h.mustRun(h.mention(toBot("what is the SECRET-QUARTERLY-FIGURES?")))
	if logs.Len() == 0 {
		t.Fatal("nothing was logged")
	}
	if strings.Contains(logs.String(), "SECRET-QUARTERLY-FIGURES") || strings.Contains(logs.String(), "Raya") {
		t.Fatalf("what was written, or a name, is in the log:\n%s", logs.String())
	}
}

func TestAnActionRowThatCannotBeRead(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	job := h.click("URAYA", ticket)
	h.slack.profilesErr = errors.New("apps timed out")
	if err := h.router.Handle(context.Background(), job); err == nil || IsPermanent(err) {
		t.Fatalf("err = %v, want a failure to retry", err)
	}
	if len(h.asks.consent) != 0 {
		t.Fatal("Consent called without knowing who clicked")
	}
}
