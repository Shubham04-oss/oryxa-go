package surfaces

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
)

// heldForRaya is the request of the usual ask, approved by Raya with a summary
// in Slack, whose answer the guards held for her review for kinds: with a
// masked version when masked is not empty.
func heldForRaya(content, masked string, kinds ...string) agents.Interaction {
	request := heldWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), content)
	request.ExpiresAt = testNow.Add(interactions.ReviewWindow)
	request.Guard = &agents.InteractionGuard{State: answerguard.StateHeld, Kinds: kinds}
	if masked != "" {
		request.Held.MaskedResult = &agents.InteractionResult{Status: interactions.StatusCompleted, Content: masked, Summary: "masked summary"}
	}
	return request
}

// reviewedBy is request once its held answer was reviewed as review says, and
// what that leaves of it.
func reviewedBy(request agents.Interaction, review agents.InteractionReview) agents.Interaction {
	switch review.Action {
	case interactions.ReviewSend:
		result := request.Held.Result
		request.Status, request.Result = result.Status, &result
	case interactions.ReviewSendMasked:
		result := *request.Held.MaskedResult
		request.Status, request.Result = result.Status, &result
	default:
		request.Status, request.Result = interactions.StatusWithheld, nil
	}
	request.Held, request.Review = nil, &review
	request.Revision++
	return request
}

// byRaya is Raya's review, made where surface says (none for the web, as the
// web's review records it), at a moment the tests know.
func byRaya(action, surface string) agents.InteractionReview {
	return agents.InteractionReview{
		Action: action, DecidedAt: testNow.Add(time.Minute),
		DecidedBy: agents.InteractionDecider{Kind: interactions.DecidedByMember, UserID: "user_raya", Surface: surface},
	}
}

// review is the one review posted, and fails unless there is exactly one.
func (h *harness) review() *message {
	h.t.Helper()
	reviews := h.api.sentWith(templateHeldReview)
	if len(reviews) != 1 {
		h.t.Fatalf("posted %v, want one review", templates(h.api.sent()))
	}
	return reviews[0]
}

// TestTheOwnerOfAHeldAnswerIsSentItToReview: in their own direct message, and
// nowhere else, with who asked, what, why it was held, the answer, and the
// tickets the ledger made for its buttons; once, however often the pass runs.
func TestTheOwnerOfAHeldAnswerIsSentItToReview(t *testing.T) {
	const heldText = "The launch moved to the 21st, per Raya's 1:1 notes."
	h := newHarness(t)
	h.api.forbidOutsideReview(heldText, "held summary")
	h.ledger.put(heldForRaya(heldText, "", consentguard.KindIrrelevant))
	h.mustPass("irq_1")

	review := h.review()
	if review.Target != (slackrouter.Target{UserID: slackRaya}) || review.Ephemeral || review.Key != "surfaces:irq_1:review" {
		t.Fatalf("review = %+v, want a direct message to Raya, under its own key", review)
	}
	want := map[string]any{
		"asker_user_id": slackAsker, "question": "What is the launch date for Project Falcon?",
		"reasons": []any{"irrelevant"}, "preview": heldText,
		"tickets": map[string]any{"send": "t1.review.send.irq_1.review_irq_1", "withhold": "t1.review.withhold.irq_1.review_irq_1"},
	}
	if fmt.Sprint(review.Params) != fmt.Sprint(want) {
		t.Fatalf("params = %v, want %v", review.Params, want)
	}
	if got := h.ledger.reviews(); fmt.Sprint(got) != "[irq_1/review_irq_1]" {
		t.Fatalf("tickets were made for %v, want the one review", got)
	}
	if link, ok := h.links.get("irq_1", roleReview); !ok || link.State != statePending || link.ChannelID != "DRAYA001" || link.TS != review.TS ||
		link.OwnerUserID != "user_raya" || link.TeamID != "T0ACME001" || link.ExpiresAt != nil {
		t.Fatalf("link = %+v, %v, want the review recorded with its buttons", link, ok)
	}
	// The thread is told it is held, and nothing of it.
	if statuses := h.api.sentWith("status"); len(statuses) != 1 || statuses[0].Target != h.threadTarget() || statuses[0].param("state") != "held" {
		t.Fatalf("statuses = %+v", statuses)
	}
	// Said once.
	posts := h.api.postRequests()
	h.mustPass("irq_1")
	h.mustPass("irq_1")
	if h.api.postRequests() != posts || len(h.ledger.reviews()) != 1 || h.api.updateRequests() != 0 {
		t.Fatalf("the review was posted again, or its tickets made again: %d posts, %v", h.api.postRequests(), h.ledger.reviews())
	}
}

// TestAMaskedVersionIsAllTheReviewShows: where the guards made one, the owner is
// shown it, with a button to send it, and the answer as held never reaches
// Slack, though Send sends it.
func TestAMaskedVersionIsAllTheReviewShows(t *testing.T) {
	const heldText = "Call Priya on 555-0142."
	h := newHarness(t)
	h.api.forbid(heldText, "555-0142")
	h.ledger.put(heldForRaya(heldText, "Call Priya on {PHONE}.", "PHONE"))
	h.mustPass("irq_1")
	review := h.review()
	tickets, _ := review.Params["tickets"].(map[string]any)
	if review.param("preview") != "Call Priya on {PHONE}." || review.Params["masked"] != true ||
		tickets["send_masked"] != "t1.review.send_masked.irq_1.review_irq_1" || tickets["send"] == nil || tickets["withhold"] == nil {
		t.Fatalf("params = %v, want the masked version, and a button to send it", review.Params)
	}
	if got := review.Params["reasons"]; fmt.Sprint(got) != "[phone]" {
		t.Fatalf("reasons = %v", got)
	}
}

// TestAHeldAnswerWithNoWordsIsShownAsNone: the preview says the agent wrote
// nothing, and its summary, when it wrote one, stands in, as for an answer.
func TestAHeldAnswerWithNoWordsIsShownAsNone(t *testing.T) {
	for name, test := range map[string]struct{ content, summary, want string }{
		"a summary alone": {"", "Only a summary.", "Only a summary."},
		"nothing":         {"  ", "", noAnswerText},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			request := heldForRaya(test.content, "", consentguard.KindUngrounded)
			request.Held.Result.Summary = test.summary
			h.ledger.put(request)
			h.mustPass("irq_1")
			if got := h.review().param("preview"); got != test.want {
				t.Fatalf("preview = %q, want %q", got, test.want)
			}
		})
	}
}

// TestTheReviewSaysWhyTheAnswerWasHeldInReasonsAppsHasWordsFor: from the kinds
// the guards recorded, in one order, each once; the kinds that are always
// masked are no reason; a kind this service has no reason for is said
// generally; and an answer no guard held was held by its organization.
func TestTheReviewSaysWhyTheAnswerWasHeldInReasonsAppsHasWordsFor(t *testing.T) {
	held := func(kinds ...string) *agents.InteractionGuard {
		return &agents.InteractionGuard{State: answerguard.StateHeld, Kinds: kinds}
	}
	for name, test := range map[string]struct {
		guard *agents.InteractionGuard
		want  []string
	}{
		"the live example, an answer that does not answer": {held(consentguard.KindIrrelevant), []string{"irrelevant"}},
		"an answer its run does not stand on":              {held(consentguard.KindNoSources, consentguard.KindUngrounded), []string{"ungrounded", "no_sources"}},
		"personal details, in any order":                   {held("PHONE", "ADDRESS", "EMAIL"), []string{"email", "phone", "address"}},
		"what a summary must not do":                       {held(consentguard.KindLink, consentguard.KindCopied), []string{"copied", "link"}},
		"a topic never let out":                            {held(consentguard.KindDeniedTopic), []string{"denied_topic"}},
		"what the relevance check found, named":            {held(answerguard.OffTopic, string(relevance.Health), string(relevance.HR)), []string{"health", "hr"}},
		"what the relevance check found, unnamed":          {held(answerguard.OffTopic), []string{"off_topic"}},
		"an answer that could not be checked":              {held(answerguard.Unchecked), []string{"unchecked"}},
		"credentials masked beside a hold":                 {held(consentguard.KindSlackToken, "PIN", consentguard.KindIrrelevant), []string{"irrelevant"}},
		"only what is always masked":                       {held("PASSWORD"), []string{"other"}},
		"a kind with no reason, beside one with a reason":  {held("SOMETHING_NEWER", "PHONE"), []string{"phone", "other"}},
		"a hold that names nothing":                        {held(), []string{"other"}},
		"no guard":                                         {nil, []string{"policy"}},
		"a guard that found nothing to hold":               {&agents.InteractionGuard{State: answerguard.StateMasked, Kinds: []string{"PIN"}}, []string{"policy"}},
		"a guard that found nothing at all":                {&agents.InteractionGuard{State: answerguard.StateClear}, []string{"policy"}},
	} {
		t.Run(name, func(t *testing.T) {
			request := heldForRaya("held", "")
			request.Guard = test.guard
			if got := heldReasons(request); !slices.Equal(got, test.want) {
				t.Fatalf("reasons = %v, want %v", got, test.want)
			}
		})
	}
}

// TestTheReasonsAreTheReviewTemplatesOwn: what a review may say is apps'
// held_review template's vocabulary, which apps' TestEveryReasonAgentsNamesHasItsOwnWords
// pins from its side; and every kind a guard holds an answer for has a reason,
// so none is said only generally.
func TestTheReasonsAreTheReviewTemplatesOwn(t *testing.T) {
	vocabulary := []string{
		"irrelevant", "ungrounded", "no_sources", "copied", "link", "denied_topic",
		"email", "phone", "address",
		"off_topic", "personal_life", "health", "hr", "compensation", "others_private", "credentials",
		"unchecked", "policy", "other",
	}
	if !slices.Equal(reasonOrder, vocabulary) {
		t.Fatalf("the reasons are %v, and apps has words for %v", reasonOrder, vocabulary)
	}
	for kind, reason := range heldKindReasons {
		if !slices.Contains(reasonOrder, reason) {
			t.Errorf("kind %s is said as %q, which apps has no words for", kind, reason)
		}
		if maskedKinds[kind] {
			t.Errorf("kind %s is always masked, and holds nothing", kind)
		}
	}
	kinds := append([]string{
		consentguard.KindIrrelevant, consentguard.KindUngrounded, consentguard.KindNoSources, consentguard.KindCopied,
		consentguard.KindLink, consentguard.KindDeniedTopic, consentguard.KindUnchecked, answerguard.Unchecked, answerguard.OffTopic,
	}, answerguard.HeldKinds()...)
	for _, category := range relevance.Categories() {
		kinds = append(kinds, string(category))
	}
	for _, kind := range kinds {
		if _, ok := heldKindReasons[kind]; !ok {
			t.Errorf("the guards hold an answer for %s, and the review has no reason for it", kind)
		}
	}
}

// TestAReviewIsSettledWithHowItEnded: whichever surface it ended on, the review
// is replaced with how, which takes its buttons, and the answer, away; once.
func TestAReviewIsSettledWithHowItEnded(t *testing.T) {
	decidedAt := testNow.Add(time.Minute).Format(time.RFC3339)
	for name, test := range map[string]struct {
		end       func(agents.Interaction) agents.Interaction
		ask       func(*interactions.Ask)
		outcome   string
		decidedAt bool
		// answered is whether the answer is then posted in the thread.
		answered string
	}{
		"sent from Slack": {end: func(r agents.Interaction) agents.Interaction {
			return reviewedBy(r, byRaya(interactions.ReviewSend, interactions.SurfaceSlack))
		}, outcome: reviewSent, decidedAt: true, answered: "The launch moved to the 21st."},
		"sent masked from Slack": {end: func(r agents.Interaction) agents.Interaction {
			return reviewedBy(r, byRaya(interactions.ReviewSendMasked, interactions.SurfaceSlack))
		}, outcome: reviewSentMasked, decidedAt: true, answered: "The launch moved to the {DATE}."},
		"not sent, from Slack": {end: func(r agents.Interaction) agents.Interaction {
			return reviewedBy(r, byRaya(interactions.ReviewWithhold, interactions.SurfaceSlack))
		}, outcome: reviewNotSent, decidedAt: true},
		"sent on the web": {end: func(r agents.Interaction) agents.Interaction {
			return reviewedBy(r, byRaya(interactions.ReviewSend, ""))
		}, outcome: reviewDecidedElsewhere, decidedAt: true, answered: "The launch moved to the 21st."},
		"not sent, on the web": {end: func(r agents.Interaction) agents.Interaction {
			return reviewedBy(r, byRaya(interactions.ReviewWithhold, interactions.SurfaceWeb))
		}, outcome: reviewDecidedElsewhere, decidedAt: true},
		"withheld by Oryxa when its time ran out": {end: func(r agents.Interaction) agents.Interaction {
			return reviewedBy(r, agents.InteractionReview{Action: interactions.ReviewWithhold, DecidedBy: agents.InteractionDecider{Kind: interactions.DecidedBySystem},
				DecidedAt: testNow, Reason: interactions.ExpiredBeforeReview})
		}, outcome: reviewExpired},
		"its time ran out, before Oryxa withheld it": {end: func(r agents.Interaction) agents.Interaction {
			r.ExpiresAt = testNow
			return r
		}, outcome: reviewExpired},
		"withheld by Oryxa when another owner answered": {end: func(r agents.Interaction) agents.Interaction {
			return reviewedBy(r, agents.InteractionReview{Action: interactions.ReviewWithhold,
				DecidedBy: agents.InteractionDecider{Kind: interactions.DecidedBySystem, Reason: interactions.ReasonAnsweredElsewhere},
				DecidedAt: testNow, Reason: "Answered by Maya"})
		}, ask: func(a *interactions.Ask) { a.WinnerRequestID = "irq_2" }, outcome: reviewAnsweredByOther},
		"withheld by Oryxa for a reason of its own": {end: func(r agents.Interaction) agents.Interaction {
			return reviewedBy(r, agents.InteractionReview{Action: interactions.ReviewWithhold, DecidedBy: agents.InteractionDecider{Kind: interactions.DecidedBySystem},
				DecidedAt: testNow, Reason: "Oryxa withheld this answer because the team is gone."})
		}, outcome: reviewNotSent},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			held := heldForRaya("The launch moved to the 21st.", "The launch moved to the {DATE}.", "EMAIL")
			h.ledger.put(held)
			h.mustPass("irq_1")
			review := h.review()

			h.ledger.put(test.end(held))
			if test.ask != nil {
				ask := baseAsk()
				test.ask(&ask)
				h.ledger.putAsk(ask)
			}
			h.mustPass("irq_1")
			if len(review.Updates) != 1 || review.Updates[0].Template != templateHeldReviewSettled {
				t.Fatalf("updates = %+v, want the review settled once", review.Updates)
			}
			params := review.Updates[0].Params
			want := map[string]any{"asker_user_id": slackAsker, "question": "What is the launch date for Project Falcon?", "outcome": test.outcome}
			if test.decidedAt {
				want["decided_at"] = decidedAt
			}
			if fmt.Sprint(params) != fmt.Sprint(want) {
				t.Fatalf("settled with %v, want %v", params, want)
			}
			if link, _ := h.links.get("irq_1", roleReview); link.State != test.outcome {
				t.Fatalf("link = %+v, want it to say %s", link, test.outcome)
			}
			// What was sent, from either surface, is posted in the thread as an
			// answer delivered outright is.
			answers := h.api.sentWith("answer")
			switch {
			case test.answered == "" && len(answers) != 0:
				t.Fatalf("an answer was posted for a review that sent none: %+v", answers)
			case test.answered != "" && (len(answers) != 1 || answers[0].param("text") != test.answered || answers[0].Target != h.threadTarget()):
				t.Fatalf("answers = %+v, want %q in the thread", answers, test.answered)
			}
			// Settled once.
			h.mustPass("irq_1")
			if len(review.Updates) != 1 {
				t.Fatalf("the review was settled %d times", len(review.Updates))
			}
		})
	}
}

// TestAReviewThatEndedBeforeItWasSentIsNeverSent: an owner who reviewed on the
// web before the pass came to it has nothing to review in Slack.
func TestAReviewThatEndedBeforeItWasSentIsNeverSent(t *testing.T) {
	h := newHarness(t)
	held := heldForRaya("The 21st.", "", "EMAIL")
	h.ledger.put(reviewedBy(held, byRaya(interactions.ReviewSend, "")))
	h.mustPass("irq_1")
	if got := h.api.sentWith(templateHeldReview); len(got) != 0 {
		t.Fatalf("reviews = %+v, want none", got)
	}
	if _, ok := h.links.get("irq_1", roleReview); ok || h.api.updateRequests() != 0 {
		t.Fatal("a review that was never sent was recorded or settled")
	}
	// Reviewed between this pass's read and its tickets: nothing is sent, and the
	// pass that follows the review has nothing to settle.
	h = newHarness(t)
	h.ledger.put(heldForRaya("The 21st.", "", "EMAIL"))
	h.ledger.reviewErr = interactions.ErrConflict
	h.mustPass("irq_1")
	if got := h.api.sentWith(templateHeldReview); len(got) != 0 {
		t.Fatalf("reviews = %+v, want none", got)
	}
	if _, ok := h.links.get("irq_1", roleReview); ok {
		t.Fatal("a review with no buttons to send was recorded")
	}
}

// TestAReviewThatCannotBeSentIsGivenUpOnAndTheRestIsStillSaid: an owner Slack
// does not know, and a request the ledger will make no tickets for, can be
// reviewed in Oryxa; nothing is tried again for them, and the thread is still
// told.
func TestAReviewThatCannotBeSentIsGivenUpOnAndTheRestIsStillSaid(t *testing.T) {
	for name, setup := range map[string]func(*harness){
		"an owner not matched to a Slack user": func(h *harness) { h.users.forget("user_raya") },
		"a request the ledger makes no tickets for": func(h *harness) {
			h.ledger.reviewErr = fmt.Errorf("%w: only the answer to a request asked in Slack is reviewed there", interactions.ErrInvalid)
		},
		"apps refusing the review": func(h *harness) {
			h.api.respond = func(_ int, body map[string]any, w http.ResponseWriter) bool {
				if _, isReview := body["reasons"]; isReview {
					h.api.refusal(w, http.StatusBadRequest, "invalid_request", "the held_review template does not take those params")
					return true
				}
				return false
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			setup(h)
			h.ledger.put(heldForRaya("The 21st.", "", "EMAIL"))
			h.mustPass("irq_1")
			if link, ok := h.links.get("irq_1", roleReview); !ok || link.State != stateFailed {
				t.Fatalf("link = %+v, %v, want the review given up on", link, ok)
			}
			if got := h.api.sentWith("status"); len(got) != 1 {
				t.Fatalf("statuses = %+v, want the thread told all the same", got)
			}
			posts, reviews := h.api.postRequests(), len(h.ledger.reviews())
			h.mustPass("irq_1")
			if h.api.postRequests() != posts || len(h.ledger.reviews()) != reviews {
				t.Fatal("a review that cannot be sent was tried again")
			}
		})
	}
}

// TestAReviewIsSentOnceAcrossFailures: a pass that failed before the review was
// posted posts it next time, and one that failed after posting it, before it
// was recorded, finds apps' first post under the same key: one message, with the
// same tickets.
func TestAReviewIsSentOnceAcrossFailures(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(heldForRaya("The 21st.", "", "EMAIL"))

	// Tickets that could not be made are tried again.
	h.ledger.reviewErr = errors.New("the ledger is unavailable")
	if err := h.pass("irq_1"); err == nil || slackrouter.IsPermanent(err) {
		t.Fatalf("pass = %v, want a failure to try again", err)
	}
	h.ledger.reviewErr = nil

	// Apps failing is tried again.
	failing := true
	h.api.respond = func(_ int, body map[string]any, w http.ResponseWriter) bool {
		if _, isReview := body["reasons"]; isReview && failing {
			h.api.refusal(w, http.StatusBadGateway, "slack_error", "slack refused the request: internal_error")
			return true
		}
		return false
	}
	if err := h.pass("irq_1"); err == nil || slackrouter.IsPermanent(err) {
		t.Fatalf("pass = %v, want a failure to try again", err)
	}
	if _, ok := h.links.get("irq_1", roleReview); ok {
		t.Fatal("a review that was not sent has a link")
	}
	failing = false

	// Posted, and the record of it lost.
	h.links.putFailures = 1
	if err := h.pass("irq_1"); err == nil {
		t.Fatal("pass = nil, want the lost record to fail it")
	}
	h.mustPass("irq_1")
	h.review()
	if link, ok := h.links.get("irq_1", roleReview); !ok || link.State != statePending {
		t.Fatalf("link = %+v, %v", link, ok)
	}
}

// TestAReviewThatCannotBeReplacedIsSettledAsFarAsItCanBe: as for a notice, a
// message apps no longer knows is left as it is and not tried again, and one it
// failed to replace for now is.
func TestAReviewThatCannotBeReplacedIsSettledAsFarAsItCanBe(t *testing.T) {
	h := newHarness(t)
	held := heldForRaya("The 21st.", "", "EMAIL")
	h.ledger.put(held)
	h.mustPass("irq_1")
	h.ledger.put(reviewedBy(held, byRaya(interactions.ReviewSend, interactions.SurfaceSlack)))

	var status int
	h.api.updateRespond = func(_ int, w http.ResponseWriter) bool {
		switch status {
		case http.StatusBadGateway:
			h.api.refusal(w, status, "slack_error", "slack refused the request: internal_error")
			return true
		case http.StatusNotFound:
			h.api.refusal(w, status, "not_found", "apps did not post that message")
			return true
		}
		return false
	}
	status = http.StatusBadGateway
	if err := h.pass("irq_1"); err == nil || slackrouter.IsPermanent(err) {
		t.Fatalf("pass = %v, want a failure to try again", err)
	}
	if link, _ := h.links.get("irq_1", roleReview); link.State != statePending {
		t.Fatalf("link = %+v, want it still pending", link)
	}
	status = http.StatusNotFound
	h.mustPass("irq_1")
	if link, _ := h.links.get("irq_1", roleReview); link.State != reviewSent {
		t.Fatalf("link = %+v, want it settled as far as it can be", link)
	}
	updates := h.api.updateRequests()
	h.mustPass("irq_1")
	if h.api.updateRequests() != updates {
		t.Fatal("a review that cannot be replaced was tried again")
	}
}

// TestNoReviewIsSentForAnAnswerThatWasNeverHeld: an answer delivered as it was,
// a request still waiting, and one that ended unanswered have none.
func TestNoReviewIsSentForAnAnswerThatWasNeverHeld(t *testing.T) {
	approved := memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack)
	for name, request := range map[string]agents.Interaction{
		"waiting":  pendingRequest(),
		"approved": approved,
		"answered": completedWith(approved, "The 14th."),
		"denied":   memberDecided(pendingRequest(), interactions.DecisionRejected, "", interactions.SurfaceSlack),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.ledger.put(request)
			h.mustPass("irq_1")
			if got := h.api.sentWith(templateHeldReview); len(got) != 0 || len(h.ledger.reviews()) != 0 {
				t.Fatalf("reviews = %+v, tickets for %v", got, h.ledger.reviews())
			}
		})
	}
}

// TestAReviewNeverSaysWhatWasAskedOrAnsweredInTheLog: the log names requests
// and states, never words, and never a ticket.
func TestAReviewNeverSaysWhatWasAskedOrAnsweredInTheLog(t *testing.T) {
	h := newHarness(t)
	var logs strings.Builder
	h.service.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	held := heldForRaya("SECRET-HELD-ANSWER", "SECRET-MASKED-ANSWER", "EMAIL")
	h.ledger.put(held)
	h.mustPass("irq_1")
	h.ledger.put(reviewedBy(held, byRaya(interactions.ReviewSendMasked, interactions.SurfaceSlack)))
	h.api.updateRespond = func(_ int, w http.ResponseWriter) bool {
		h.api.refusal(w, http.StatusNotFound, "not_found", "apps did not post that message")
		return true
	}
	h.mustPass("irq_1")
	if logs.Len() == 0 {
		t.Fatal("nothing was logged, so this proves nothing")
	}
	for _, secret := range []string{"SECRET-HELD-ANSWER", "SECRET-MASKED-ANSWER", "Project Falcon", "t1.review"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log carries %q:\n%s", secret, logs.String())
		}
	}
}
