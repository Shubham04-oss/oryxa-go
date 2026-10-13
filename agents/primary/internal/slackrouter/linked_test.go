package slackrouter

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

// linkedHarness is the router trusting nothing but links, with the asker, Ada,
// linked as herself and Raya a member who may be asked.
func linkedHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.linkedOnly = true
	h.rebuild()
	h.link(testAsker, "usr_ada")
	h.member("URAYA", "Raya")
	h.link("URAYA", "usr_raya")
	return h
}

// link has apps say slackID is linked to memberID.
func (h *harness) link(slackID, memberID string) {
	h.t.Helper()
	h.profile(slackID, func(p *Profile) { p.LinkedMemberID = memberID })
}

// TestWithOnlyLinksTrustedALinkedAskerAsks: an asker who linked their account
// asks as ever, and the identity the surfaces send to is kept from the link.
func TestWithOnlyLinksTrustedALinkedAskerAsks(t *testing.T) {
	h := linkedHarness(t)
	h.mustRun(h.mention(toBot("ask <@URAYA> when the launch is")))
	if len(h.asks.created) != 1 || h.asks.created[0].Asker.UserID != "usr_ada" || h.asks.created[0].Owners[0].Person.UserID != "usr_raya" {
		t.Fatalf("asks = %+v", h.asks.created)
	}
	if kept := h.identities.kept[testTeam+"|URAYA"]; kept.MemberUserID != "usr_raya" {
		t.Fatalf("Raya's identity = %+v", kept)
	}
}

// TestAnAccountMatchedByEmailAloneAsksNothing: an account whose confirmed email
// is a member's, but which that member never linked, as an admin of the
// workspace can make one, is told to link and asks nobody; nothing is screened
// and nothing kept.
func TestAnAccountMatchedByEmailAloneAsksNothing(t *testing.T) {
	h := linkedHarness(t)
	h.profile(testAsker, func(p *Profile) { p.LinkedMemberID = "" })
	job := h.mention(toBot("ask <@URAYA> when the launch is"))
	h.mustRun(job)
	if _, params := h.refusal(job); params.Reason != consentpolicy.NotLinked {
		t.Fatalf("refusal = %+v, want not_linked", params)
	}
	if len(h.screen.calls()) != 0 {
		t.Fatal("an account nobody linked was screened")
	}
	if _, kept := h.identities.kept[testTeam+"|"+testAsker]; kept {
		t.Fatal("an account nobody linked was kept as the member's")
	}
}

// TestAnEmailChangeMovesNothing: a second account set to Ada's email, as an
// admin of the workspace can, is not Ada: it asks nothing, and its click on a
// button of Ada's decides nothing; Ada's own linked account still does both.
func TestAnEmailChangeMovesNothing(t *testing.T) {
	h := linkedHarness(t)
	h.slack.profiles["UEVIL"] = Profile{UserID: "UEVIL", TeamID: testTeam, Email: "ada@acme.com", EmailConfirmed: true, Name: "Ada (2)"}

	job := h.mention(toBot("ask <@URAYA> anything"), func(m *Mention) { m.UserID = "UEVIL" })
	h.mustRun(job)
	if _, params := h.refusal(job); params.Reason != consentpolicy.NotLinked {
		t.Fatalf("the other account's ask = %+v, want not_linked", params)
	}

	if err := h.run(h.click("UEVIL", "t1.consent.ticket")); err == nil || !IsPermanent(err) {
		t.Fatalf("the other account's click = %v, want dropped", err)
	}
	if len(h.asks.consent) != 0 {
		t.Fatalf("the other account's click decided %+v", h.asks.consent)
	}
	h.mustRun(h.click(testAsker, "t1.consent.ticket"))
	if len(h.asks.consent) != 1 || h.asks.consent[0].Clicker != "usr_ada" {
		t.Fatalf("Ada's own click = %+v", h.asks.consent)
	}
}

// TestAnOwnerWhoNeverLinkedIsAskedInOryxaAlone: a named member whose account is
// not linked is still asked, but nothing of theirs in Slack is kept, so no
// notice goes to that account, and the asker is told they will see it in
// Oryxa.
func TestAnOwnerWhoNeverLinkedIsAskedInOryxaAlone(t *testing.T) {
	h := linkedHarness(t)
	h.member("UMAYA", "Maya")
	job := h.mention(toBot("ask <@UMAYA> when the launch is"))
	h.mustRun(job)
	if len(h.asks.created) != 1 || h.asks.created[0].Owners[0].Person.UserID != "usr_maya" || h.asks.created[0].Owners[0].Person.SlackUserID != "" {
		t.Fatalf("asks = %+v, want Maya asked with no Slack account", h.asks.created)
	}
	if _, kept := h.identities.kept[testTeam+"|UMAYA"]; kept {
		t.Fatal("an owner's account nobody linked was kept as theirs")
	}
	var told bool
	for _, post := range h.slack.sent() {
		if params, ok := post.Params.(RefusalParams); ok && params.Reason == consentpolicy.OwnerNotLinked && params.UserID == "UMAYA" {
			told = true
		}
	}
	if !told {
		t.Fatalf("posts = %+v, want the asker told Maya is asked in Oryxa", h.slack.sent())
	}
}

// TestALinkToSomeoneWhoLeftIsNobody: an account linked to a member who has
// since left the organization is no member.
func TestALinkToSomeoneWhoLeftIsNobody(t *testing.T) {
	h := linkedHarness(t)
	h.link(testAsker, "usr_gone")
	job := h.mention(toBot("ask <@URAYA> anything"))
	h.mustRun(job)
	if _, params := h.refusal(job); params.Reason != consentpolicy.NotMember {
		t.Fatalf("refusal = %+v, want not_member", params)
	}
}

// TestWithEmailsStillTrustedNothingChangesButTheLog: by default the email
// decides, as before, and an account that would not pass with links alone is
// logged as such, by id and why.
func TestWithEmailsStillTrustedNothingChangesButTheLog(t *testing.T) {
	h := newHarness(t)
	var logs bytes.Buffer
	h.logger = slog.New(slog.NewTextHandler(&logs, nil))
	h.rebuild()
	h.member("URAYA", "Raya")
	h.mustRun(h.mention(toBot("ask <@URAYA> when the launch is")))
	if len(h.asks.created) != 1 {
		t.Fatalf("asks = %+v", h.asks.created)
	}
	if got := logs.String(); !strings.Contains(got, "would not pass with links alone") || !strings.Contains(got, testAsker) {
		t.Fatalf("logs:\n%s", got)
	}
}
