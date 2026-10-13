package surfaces

import (
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
)

const rayasReply = "It moved to the 21st; I'll confirm on Friday."

// repliedTo is request as the ledger leaves it once Raya answered it herself
// while it waited for her decision, where surface says: approved with a reply's
// consent, granting nothing, and completed with her words.
func repliedTo(request agents.Interaction, surface string) agents.Interaction {
	decider := agents.InteractionDecider{Kind: interactions.DecidedByMember, UserID: request.Target.UserID, Surface: surface}
	request.Status = interactions.StatusCompleted
	request.Decision = &agents.InteractionDecision{
		Decision: interactions.DecisionApproved, Consent: interactions.ConsentReply, AllowedTools: []string{}, GrantedPermissions: []string{},
		DecidedAt: testNow.Add(-20 * time.Second), DecidedBy: &decider,
	}
	request.Result = &agents.InteractionResult{
		Status: interactions.StatusCompleted, Summary: rayasReply, ToolsUsed: []string{}, Artifacts: []string{},
		CompletedAt: testNow.Add(-20 * time.Second), WrittenByOwner: true,
	}
	request.Revision += 2
	return request
}

// repliedInsteadOfReview is a request whose held answer Raya replaced with her
// own words, where surface says: the held answer is gone, and her reply is the
// review.
func repliedInsteadOfReview(request agents.Interaction, surface string) agents.Interaction {
	request.Status = interactions.StatusCompleted
	request.Result = &agents.InteractionResult{
		Status: interactions.StatusCompleted, Summary: rayasReply, ToolsUsed: []string{}, Artifacts: []string{},
		CompletedAt: testNow.Add(time.Minute), WrittenByOwner: true,
	}
	request.Held, request.Guard = nil, nil
	request.Review = &agents.InteractionReview{
		Action: interactions.ReviewReply, DecidedAt: testNow.Add(time.Minute),
		DecidedBy: agents.InteractionDecider{Kind: interactions.DecidedByMember, UserID: "user_raya", Surface: surface},
	}
	request.Revision++
	return request
}

// TestAReplyToThePendingRequestSettlesTheNoticeWithTheOwnersWords: whichever
// surface Raya replied on, her notice loses its buttons and says what she
// replied, and her words are posted in the thread as hers, with nothing of an
// agent's in the attribution.
func TestAReplyToThePendingRequestSettlesTheNoticeWithTheOwnersWords(t *testing.T) {
	for _, surface := range []string{interactions.SurfaceSlack, interactions.SurfaceWeb} {
		t.Run(surface, func(t *testing.T) {
			h := newHarness(t)
			h.ledger.put(pendingRequest())
			h.mustPass("irq_1")
			notice := h.api.sentWith(templateOwnerNotice)
			if len(notice) != 1 {
				t.Fatalf("posted %v, want the notice", templates(h.api.sent()))
			}
			h.ledger.put(repliedTo(pendingRequest(), surface))
			h.mustPass("irq_1")

			if len(notice[0].Updates) != 1 || notice[0].Updates[0].Template != templateNoticeSettled {
				t.Fatalf("the notice was updated %+v, want settled once", notice[0].Updates)
			}
			settled := notice[0].Updates[0].Params
			if settled["decision"] != stateReplied || settled["reply"] != rayasReply || settled["decided_at"] != "2026-09-29T11:59:40Z" ||
				settled["decided_by_user_id"] != nil || settled["question"] != "What is the launch date for Project Falcon?" {
				t.Fatalf("the settled notice = %v, want Raya's reply, and when", settled)
			}
			if link, _ := h.links.get("irq_1", roleNotice); link.State != stateReplied {
				t.Fatalf("the notice's link says %q, want replied", link.State)
			}
			answers := h.api.sentWith(templateAnswer)
			if len(answers) != 1 || answers[0].Target != h.threadTarget() || answers[0].param("mode") != modeReply ||
				answers[0].param("text") != rayasReply || answers[0].param("owner_name") != "Raya" {
				t.Fatalf("answers = %+v, want Raya's words in the thread, as hers", answers)
			}
			if got := h.notes.texts(); len(got) != 2 || got[1] != "answered: Asked Raya's agent in #launch · answered" {
				t.Fatalf("notes = %v", got)
			}
			// Said once.
			posts, updates := h.api.postRequests(), h.api.updateRequests()
			h.mustPass("irq_1")
			if h.api.postRequests() != posts || h.api.updateRequests() != updates {
				t.Fatal("a second pass said something again")
			}
		})
	}
}

// TestAReplyInsteadOfTheReviewSettlesItWithTheOwnersWords: the review Raya was
// sent loses its buttons, and the answer it showed, and says what she replied
// instead, on either surface; her words, never the held answer, are posted in
// the thread as hers.
func TestAReplyInsteadOfTheReviewSettlesItWithTheOwnersWords(t *testing.T) {
	const heldText = "The launch moved to the 21st, per Raya's 1:1 notes."
	for _, surface := range []string{interactions.SurfaceSlack, interactions.SurfaceWeb} {
		t.Run(surface, func(t *testing.T) {
			h := newHarness(t)
			h.api.forbidOutsideReview(heldText, "held summary")
			held := heldForRaya(heldText, "", "EMAIL")
			h.ledger.put(held)
			h.mustPass("irq_1")
			review := h.review()
			if tickets, _ := review.Params["tickets"].(map[string]any); tickets["reply"] != "t1.reply.review.irq_1.review_irq_1" {
				t.Fatalf("the review's tickets = %v, want a Reply yourself", tickets)
			}
			h.ledger.put(repliedInsteadOfReview(held, surface))
			h.mustPass("irq_1")

			if len(review.Updates) != 1 || review.Updates[0].Template != templateHeldReviewSettled {
				t.Fatalf("the review was updated %+v, want settled once", review.Updates)
			}
			settled := review.Updates[0].Params
			if settled["outcome"] != reviewReplied || settled["reply"] != rayasReply || settled["decided_at"] != "2026-09-29T12:01:00Z" {
				t.Fatalf("the settled review = %v, want Raya's reply instead, and when", settled)
			}
			answers := h.api.sentWith(templateAnswer)
			if len(answers) != 1 || answers[0].param("mode") != modeReply || answers[0].param("text") != rayasReply {
				t.Fatalf("answers = %+v, want Raya's own words", answers)
			}
		})
	}
}

// TestAReplyToAnAskOfASecretIsNeverInSlack: its notice has no Reply yourself,
// and when its owner replies in Oryxa, her notice says she replied and not what,
// and the thread is told only that it was answered privately.
func TestAReplyToAnAskOfASecretIsNeverInSlack(t *testing.T) {
	h := newHarness(t)
	h.api.forbid(rayasReply)
	ask := baseAsk()
	ask.Kind = interactions.AskSecret
	h.ledger.putAsk(ask)
	h.ledger.put(pendingRequest())
	h.mustPass("irq_1")
	notice := h.api.sentWith(templateOwnerNotice)
	if len(notice) != 1 {
		t.Fatalf("posted %v, want the notice", templates(h.api.sent()))
	}
	if tickets, _ := notice[0].Params["tickets"].(map[string]any); tickets["reply"] != nil || len(tickets) != 3 {
		t.Fatalf("the notice of an ask of a secret offers %v, want the three choices alone", tickets)
	}
	h.ledger.put(repliedTo(pendingRequest(), interactions.SurfaceWeb))
	h.mustPass("irq_1")
	if updates := notice[0].Updates; len(updates) != 1 || updates[0].Params["decision"] != stateReplied || updates[0].Params["reply"] != nil {
		t.Fatalf("the settled notice = %+v, want that she replied, and not what", updates)
	}
	if answers := h.api.sentWith(templateAnswer); len(answers) != 0 {
		t.Fatalf("an answer to an ask of a secret was posted: %+v", answers)
	}
	if notes := h.api.sentWith(templatePrivateNote); len(notes) != 1 {
		t.Fatalf("private notes = %d, want the thread told it was answered privately", len(notes))
	}
}

// TestAnAnswerTheOwnerWroteIsTheirsWhateverLetItGo: the mode an answer says is
// a reply's for any answer its owner wrote, and an agent's for any other.
func TestAnAnswerTheOwnerWroteIsTheirsWhateverLetItGo(t *testing.T) {
	ownWords := &agents.InteractionResult{Status: interactions.StatusCompleted, Summary: "Mine.", WrittenByOwner: true}
	byTheAgent := &agents.InteractionResult{Status: interactions.StatusCompleted, Summary: "Its."}
	access := &agents.InteractionDecision{Decision: interactions.DecisionApproved, Consent: interactions.ConsentAccess}
	for name, test := range map[string]struct {
		request agents.Interaction
		want    string
	}{
		"a reply":                         {agents.Interaction{Result: ownWords, Decision: &agents.InteractionDecision{Consent: interactions.ConsentReply}}, modeReply},
		"the owner's words, with access":  {agents.Interaction{Result: ownWords, Decision: access}, modeReply},
		"the agent's words, with access":  {agents.Interaction{Result: byTheAgent, Decision: access}, modeAccess},
		"the agent's words, as a summary": {agents.Interaction{Result: byTheAgent}, modeSummary},
	} {
		if got := answerMode(test.request); got != test.want {
			t.Errorf("%s: mode %q, want %q", name, got, test.want)
		}
	}
}

// TestAnAskThatNeedsTheOwnersOwnAnswerLeadsWithReplyYourself: an owner-first
// ask's notice asks apps for the notice that leads with Reply yourself, with
// every ticket; an ordinary ask's says nothing of who answers; and an ask of a
// secret, which has no Reply yourself, never leads with it.
func TestAnAskThatNeedsTheOwnersOwnAnswerLeadsWithReplyYourself(t *testing.T) {
	for _, test := range []struct {
		name, answerBy, kind, want string
		replyTicket                bool
	}{
		{"owner-first", interactions.AnswerByOwner, interactions.AskQuestion, interactions.AnswerByOwner, true},
		{"for the agent", interactions.AnswerByAgent, interactions.AskQuestion, "", true},
		{"from before answer_by", "", interactions.AskQuestion, "", true},
		{"a secret", interactions.AnswerByOwner, interactions.AskSecret, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			ask := baseAsk()
			ask.Kind = test.kind
			h.ledger.putAsk(ask)
			request := pendingRequest()
			request.AnswerBy = test.answerBy
			h.ledger.put(request)
			h.mustPass("irq_1")
			notice := h.api.sentWith(templateOwnerNotice)
			if len(notice) != 1 {
				t.Fatalf("posted %v, want the notice", templates(h.api.sent()))
			}
			if got := notice[0].param("answer_by"); got != test.want {
				t.Fatalf("answer_by = %q, want %q", got, test.want)
			}
			tickets, _ := notice[0].Params["tickets"].(map[string]any)
			if (tickets["reply"] != nil) != test.replyTicket || tickets["summary"] == nil || tickets["deny"] == nil {
				t.Fatalf("tickets = %v", tickets)
			}
			// The ledger mints no access ticket for an owner-first request, so none is
			// sent: not even an empty one.
			if _, access := tickets["access"]; access == (test.answerBy == interactions.AnswerByOwner) {
				t.Fatalf("tickets = %v, want an access ticket only for a request that is not owner-first", tickets)
			}
		})
	}
}
