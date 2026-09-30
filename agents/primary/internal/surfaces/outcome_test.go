package surfaces

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
)

func TestAnAnswerIsPostedInTheThreadSayingHowFarItsOwnerLetItGo(t *testing.T) {
	for _, test := range []struct {
		name    string
		request agents.Interaction
		mode    string
	}{
		{"an AI summary", memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), "summary"},
		{"access for an hour", memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceWeb), "access"},
		{"a lease", leased(pendingRequest()), "lease"},
		{"an approval that granted no consent", memberDecided(pendingRequest(), interactions.DecisionApproved, "", interactions.SurfaceWeb), "summary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.ledger.put(completedWith(test.request, "The launch is on the 14th."))
			h.mustPass("irq_1")

			answers := h.api.sentWith("answer")
			if len(answers) != 1 {
				t.Fatalf("posted %v, want the answer", templates(h.api.sent()))
			}
			answer := answers[0]
			if answer.Target != h.threadTarget() {
				t.Fatalf("target = %+v, want the thread the question was asked in", answer.Target)
			}
			if answer.param("text") != "The launch is on the 14th." || answer.param("owner_name") != "Raya" || answer.param("mode") != test.mode || len(answer.Params) != 3 {
				t.Fatalf("params = %v", answer.Params)
			}
			if answer.Key != "surfaces:irq_1:answer" {
				t.Fatalf("key = %q", answer.Key)
			}
			// Said in the thread for everyone: nothing else about it.
			if got := h.api.sentWith("private_note"); len(got) != 0 {
				t.Fatalf("a private note went with an answer everyone reads: %+v", got)
			}
			if got := h.notes.texts(); len(got) != 2 || got[1] != "answered: Asked Raya's agent in #launch · answered" {
				t.Fatalf("notes = %v", got)
			}
			// Said once.
			h.mustPass("irq_1")
			h.mustPass("irq_1")
			if len(h.api.sentWith("answer")) != 1 || h.api.postRequests() != 1 {
				t.Fatalf("the answer was posted again: %d requests", h.api.postRequests())
			}
		})
	}
}

func TestAnAnswersWordsAreWhatTheAgentWroteForTheAskerOrElseItsSummary(t *testing.T) {
	for name, test := range map[string]struct {
		content, summary, want string
	}{
		"content and a summary": {"The long answer.", "Short.", "The long answer."},
		"content alone":         {"  Only content.  ", "", "Only content."},
		"a summary alone":       {"", "Only a summary.", "Only a summary."},
		"neither":               {"  ", "\n", noAnswerText},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			request := completedWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), test.content)
			request.Result.Summary = test.summary
			h.ledger.put(request)
			h.mustPass("irq_1")
			if got := h.api.sentWith("answer"); len(got) != 1 || got[0].param("text") != test.want {
				t.Fatalf("answers = %+v, want %q", got, test.want)
			}
		})
	}
}

func TestAnAnswerHeldForReviewIsAStatusWithNoWordOfIt(t *testing.T) {
	const heldText = "Raya's number is 555-0142 and her email is raya@acme.test"
	h := newHarness(t)
	// Its owner is shown it, in their own review, and nobody else is.
	h.api.forbidOutsideReview(heldText, "555-0142", "raya@acme.test", "held summary")
	held := heldWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceSlack), heldText)
	h.ledger.put(held)
	h.mustPass("irq_1")

	sent := h.api.sent()
	if got := templates(sent); fmt.Sprint(got) != "[status held_review]" {
		t.Fatalf("posted %v, want the one status, and the owner's review", got)
	}
	status := sent[0]
	if status.Target != h.threadTarget() || status.param("state") != "held" || status.param("owner_name") != "Raya" || len(status.Params) != 2 {
		t.Fatalf("status = %+v", status)
	}
	if review := sent[1]; review.Target != (slackrouter.Target{UserID: slackRaya}) || review.param("preview") != heldText {
		t.Fatalf("review = %+v, want the answer shown to Raya alone", review)
	}
	if got := h.notes.texts(); len(got) != 2 || got[1] != "held: Asked Raya's agent in #launch · held" {
		t.Fatalf("notes = %v", got)
	}

	// The owner sends the answer as it was held: now it is delivered, and the
	// thread reads it. (The held status stays, as history.)
	sending := completedWith(held, "Here is what Raya found.")
	sending.Held, sending.Guard = nil, nil
	h.ledger.put(sending)
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); fmt.Sprint(got) != "[status held_review answer]" {
		t.Fatalf("posted %v", got)
	}
	if got := h.api.sentWith("answer")[0].param("text"); got != "Here is what Raya found." {
		t.Fatalf("answer = %q", got)
	}
}

func TestAnAnswerWithheldByItsOwnerIsAnOwnerWhoChoseNotToShare(t *testing.T) {
	h := newHarness(t)
	h.api.forbid("the withheld answer text")
	request := heldWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceSlack), "the withheld answer text")
	request.Status, request.Held = interactions.StatusWithheld, nil
	// Even a result that should not be there is never read: only a request that
	// completed has an answer to say.
	request.Result = &agents.InteractionResult{Status: interactions.StatusCompleted, Content: "the withheld answer text"}
	request.Review = &agents.InteractionReview{Action: interactions.ReviewWithhold, DecidedBy: agents.InteractionDecider{Kind: interactions.DecidedByMember, UserID: "user_raya"}, DecidedAt: testNow}
	h.ledger.put(request)
	h.mustPass("irq_1")
	sent := h.api.sent()
	if len(sent) != 1 || sent[0].Template != "status" || sent[0].param("state") != "denied" ||
		sent[0].Target.UserID != slackAsker || sent[0].Target.ThreadTS != threadTS {
		t.Fatalf("posted %+v, want the asker told alone that Raya chose not to share", sent)
	}
	if got := h.notes.texts(); len(got) != 2 || got[1] != "declined: Asked Raya's agent in #launch · declined" {
		t.Fatalf("notes = %v", got)
	}
}

func TestAnAnswerWithheldBecauseItExpiredInReviewIsARequestThatEndedUnanswered(t *testing.T) {
	h := newHarness(t)
	request := heldWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceSlack), "held")
	request.Status, request.Held, request.Result = interactions.StatusWithheld, nil, nil
	request.Review = &agents.InteractionReview{Action: interactions.ReviewWithhold, DecidedBy: agents.InteractionDecider{Kind: interactions.DecidedBySystem}, DecidedAt: testNow, Reason: interactions.ExpiredBeforeReview}
	h.ledger.put(request)
	h.mustPass("irq_1")
	sent := h.api.sent()
	if len(sent) != 1 || sent[0].param("state") != "expired" || sent[0].Target.UserID != slackAsker {
		t.Fatalf("posted %+v, want the asker told alone that it expired", sent)
	}
}

func TestAnOwnerWhoDeclinesIsToldToTheAskerAlone(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(memberDecided(pendingRequest(), interactions.DecisionRejected, "", interactions.SurfaceSlack))
	h.mustPass("irq_1")
	sent := h.api.sent()
	if len(sent) != 1 || sent[0].Template != "status" {
		t.Fatalf("posted %v, want the one status", templates(sent))
	}
	status := sent[0]
	// Ephemeral: the thread, and only the asker in it.
	if want := (slackrouter.Target{ChannelID: channelID, ThreadTS: threadTS, UserID: slackAsker}); status.Target != want || !status.Ephemeral {
		t.Fatalf("target = %+v ephemeral=%v, want %+v", status.Target, status.Ephemeral, want)
	}
	if status.param("state") != "denied" || status.param("owner_name") != "Raya" {
		t.Fatalf("params = %v", status.Params)
	}
	h.mustPass("irq_1")
	if h.api.postRequests() != 1 {
		t.Fatal("the asker was told twice")
	}
}

// A request that ended without an answer says how: it ran out of time (expired),
// or it could not be done or Oryxa closed it (failed). It never says the time ran
// out of one that failed.
func TestARequestThatEndedWithoutAnAnswerIsToldToTheAskerAlone(t *testing.T) {
	for name, test := range map[string]struct {
		edit  func(agents.Interaction) agents.Interaction
		state string
	}{
		"the owner's agent failed": {func(r agents.Interaction) agents.Interaction {
			r = memberDecided(r, interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack)
			r.Status = interactions.StatusFailed
			r.Result = &agents.InteractionResult{Status: interactions.StatusFailed, Summary: "could not find it"}
			return r
		}, "failed"},
		"the owner's agent failed, after its time was up": {func(r agents.Interaction) agents.Interaction {
			r = memberDecided(r, interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack)
			r.Status = interactions.StatusFailed
			r.Result = &agents.InteractionResult{Status: interactions.StatusFailed, Summary: "could not find it"}
			r.ExpiresAt = testNow.Add(-time.Hour)
			return r
		}, "failed"},
		"Oryxa closed it": {func(r agents.Interaction) agents.Interaction {
			return decidedBy(r, interactions.DecisionRejected, agents.InteractionDecider{Kind: interactions.DecidedBySystem}, "")
		}, "failed"},
		"approved and never answered": {func(r agents.Interaction) agents.Interaction {
			r = memberDecided(r, interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack)
			r.ExpiresAt = testNow.Add(-time.Hour)
			return r
		}, "expired"},
		"never decided": {func(r agents.Interaction) agents.Interaction {
			r.ExpiresAt = testNow.Add(-time.Hour)
			return r
		}, "expired"},
		"completed with a failed result": {func(r agents.Interaction) agents.Interaction {
			r = completedWith(memberDecided(r, interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), "x")
			r.Result.Status = interactions.StatusFailed
			return r
		}, "failed"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.ledger.put(test.edit(pendingRequest()))
			h.mustPass("irq_1")
			var statuses []*message
			for _, made := range h.api.sent() {
				if made.Template == "answer" {
					t.Fatalf("an answer was posted for a request with none: %+v", made)
				}
				if made.Template == "status" {
					statuses = append(statuses, made)
				}
			}
			if len(statuses) != 1 || statuses[0].param("state") != test.state || statuses[0].Target.UserID != slackAsker {
				t.Fatalf("statuses = %+v, want the asker told alone that it ended unanswered, as %s", statuses, test.state)
			}
			if got := h.notes.texts(); len(got) != 2 || !strings.HasSuffix(got[1], "· no answer") {
				t.Fatalf("notes = %v", got)
			}
		})
	}
}

func TestASecretIsNeverPostedToSlack(t *testing.T) {
	// The owner's agent wrote something that looks like the credential asked
	// for. The answer to an ask of a secret is read in Oryxa, where the owner
	// shares a value as a one-time view; the thread is told only that it was
	// answered privately.
	const secret = "sk-live-4eC39HqLyjWDarjtT1zdp7dc"
	const password = "the root password is hunter2hunter2"
	for name, edit := range map[string]func(agents.Interaction) agents.Interaction{
		"approved with access": func(r agents.Interaction) agents.Interaction {
			return memberDecided(r, interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceSlack)
		},
		"approved with a summary": func(r agents.Interaction) agents.Interaction {
			return memberDecided(r, interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack)
		},
		"under a lease":                  leased,
		"by a bypass in a priority room": func(r agents.Interaction) agents.Interaction { return bypassed(r, "priority_channel") },
		"by a bypass on authority alone": func(r agents.Interaction) agents.Interaction { return bypassed(r, "org_chart") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.api.forbid(secret, password, "hunter2")
			ask := baseAsk()
			ask.Kind = interactions.AskSecret
			h.ledger.putAsk(ask)
			request := completedWith(edit(pendingRequest()), password+" and the key is "+secret)
			request.Result.Summary = secret
			h.ledger.put(request)
			h.mustPass("irq_1")

			for _, made := range h.api.sent() {
				if made.Template == "answer" {
					t.Fatalf("the answer to a secret was posted: %+v", made)
				}
			}
			notes := h.api.sentWith("private_note")
			if len(notes) != 1 || notes[0].Target != h.threadTarget() || len(notes[0].Params) != 0 {
				t.Fatalf("private notes = %+v, want the thread told once that it was answered privately", notes)
			}
			// Nothing of it in the asker's agent chat's line either.
			for _, text := range h.notes.texts() {
				if strings.Contains(text, "hunter2") || strings.Contains(text, "sk-live") {
					t.Fatalf("a note says the secret: %q", text)
				}
			}
			h.mustPass("irq_1")
			if len(h.api.sentWith("private_note")) != 1 {
				t.Fatal("the note was posted again")
			}
		})
	}
}

func TestASecretAskThatIsHeldOrDeclinedSaysNothingOfIt(t *testing.T) {
	h := newHarness(t)
	// Not even to its owner: a secret is never in Slack.
	h.api.forbid("hunter2")
	ask := baseAsk()
	ask.Kind = interactions.AskSecret
	h.ledger.putAsk(ask)
	h.ledger.put(heldWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceSlack), "password hunter2"))
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); fmt.Sprint(got) != "[status held_review]" {
		t.Fatalf("posted %v", got)
	}
	review := h.api.sentWith("held_review")[0]
	if review.Params["secret"] != true || review.param("preview") != "" || review.Params["tickets"] != nil {
		t.Fatalf("review = %v, want the owner told to review it in Oryxa, with no answer and no buttons", review.Params)
	}
}

func TestOnlyWhatTheOwnersAgentWroteForTheAskerIsEverReadFromARequest(t *testing.T) {
	// A request approved but not yet answered carries no answer; one in review
	// carries only what is held. Whatever fields hold text, nothing outside the
	// delivered result reaches Slack.
	h := newHarness(t)
	h.api.forbid("SECRET-CONTEXT-VALUE", "SECRET-HELD-VALUE", "SECRET-PURPOSE-VALUE")
	request := memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceSlack)
	request.Context = []agents.InteractionContextItem{{Label: "note", Value: "SECRET-CONTEXT-VALUE"}}
	request.Purpose = "SECRET-PURPOSE-VALUE"
	request.Held = &agents.InteractionHeld{Result: agents.InteractionResult{Content: "SECRET-HELD-VALUE"}}
	h.ledger.put(request)
	h.mustPass("irq_1")
	request = completedWith(request, "The public answer.")
	h.ledger.put(request)
	h.mustPass("irq_1")
	if got := h.api.sentWith("answer"); len(got) != 1 || got[0].param("text") != "The public answer." {
		t.Fatalf("answers = %+v", got)
	}
}

func TestAnAnswerThatCannotBePostedInTheThreadIsGivenUpOnWithoutBlockingTheRest(t *testing.T) {
	h := newHarness(t)
	// apps has not seen the thread (its inbound row was purged): a refusal that
	// asking again will not change.
	h.api.respond = func(_ int, _ map[string]any, w http.ResponseWriter) bool {
		h.api.refusal(w, http.StatusForbidden, "forbidden", "apps has not seen that conversation in a verified Slack message")
		return true
	}
	h.ledger.put(completedWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), "It is the 14th."))
	if err := h.pass("irq_1"); err != nil {
		t.Fatalf("pass = %v, want a refusal that will not change to be given up on and not retried", err)
	}
	if link, ok := h.links.get("irq_1", roleAnswer); !ok || link.State != stateFailed {
		t.Fatalf("link = %+v, %v, want the answer given up on", link, ok)
	}
	// The asker's note is still written: Oryxa holds the answer for them.
	if got := h.notes.texts(); len(got) != 2 || got[1] != "answered: Asked Raya's agent in #launch · answered" {
		t.Fatalf("notes = %v", got)
	}
	requests := h.api.postRequests()
	h.mustPass("irq_1")
	if h.api.postRequests() != requests {
		t.Fatal("a message that cannot be sent was tried again")
	}
}

func TestAnOwnerNameFallsBackFromTheOneTheyChoseToSlacksToAPlainWord(t *testing.T) {
	for name, test := range map[string]struct {
		chosen   string
		identity bool
		want     string
	}{
		"the name they chose":             {"Raya", true, "Raya"},
		"the name Slack has for them":     {"", true, "Raya S."},
		"a plain word when there is none": {"", false, "the owner"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			request := completedWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), "The 14th.")
			request.Target.UserDisplayName = test.chosen
			if !test.identity {
				h.users.forget("user_raya")
			}
			h.ledger.put(request)
			h.mustPass("irq_1")
			if got := h.api.sentWith("answer"); len(got) != 1 || got[0].param("owner_name") != test.want {
				t.Fatalf("answers = %+v, want the owner called %q", got, test.want)
			}
		})
	}
}

func TestAnAnswerIsRefusedByAppsForAReasonThatMayPassAndIsTriedAgain(t *testing.T) {
	h := newHarness(t)
	failing := true
	h.api.respond = func(_ int, _ map[string]any, w http.ResponseWriter) bool {
		if failing {
			h.api.refusal(w, http.StatusBadGateway, "slack_error", "slack refused the request: internal_error")
			return true
		}
		return false
	}
	h.ledger.put(completedWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), "The 14th."))
	err := h.pass("irq_1")
	if err == nil || slackrouter.IsPermanent(err) {
		t.Fatalf("pass = %v, want a failure to try again", err)
	}
	if _, ok := h.links.get("irq_1", roleAnswer); ok {
		t.Fatal("an answer that was not sent has a link")
	}
	failing = false
	h.mustPass("irq_1")
	if got := h.api.sentWith("answer"); len(got) != 1 {
		t.Fatalf("answers = %+v", got)
	}
}
