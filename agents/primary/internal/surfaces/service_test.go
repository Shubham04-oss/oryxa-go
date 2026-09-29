package surfaces

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
)

func TestARequestedEventPostsTheOwnersNoticeWithTheThreeButtons(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.mustPass("irq_1")

	sent := h.api.sent()
	if got := templates(sent); len(got) != 1 || got[0] != "owner_notice" {
		t.Fatalf("posted %v, want the one notice", got)
	}
	notice := sent[0]
	if want := (slackrouter.Target{UserID: slackRaya}); notice.Target != want {
		t.Fatalf("target = %+v, want the owner's direct message %+v", notice.Target, want)
	}
	if notice.Key != "surfaces:irq_1:notice" {
		t.Fatalf("idempotency key = %q", notice.Key)
	}
	if notice.param("asker_user_id") != slackAsker || notice.param("question") != "What is the launch date for Project Falcon?" || notice.param("asker_name") != "" {
		t.Fatalf("params = %v", notice.Params)
	}
	tickets, _ := notice.Params["tickets"].(map[string]any)
	if tickets["summary"] != "t1.summary.irq_1.notice_irq_1" || tickets["access"] != "t1.access.irq_1.notice_irq_1" || tickets["deny"] != "t1.deny.irq_1.notice_irq_1" || len(tickets) != 3 {
		t.Fatalf("tickets = %v, want the ledger's for the notice's own id", tickets)
	}
	if got := h.ledger.tickets(); len(got) != 1 || got[0] != "irq_1/notice_irq_1" {
		t.Fatalf("tickets made for %v", got)
	}
	// Where the notice is, so that it can be replaced, and when its buttons stop working.
	link, ok := h.links.get("irq_1", roleNotice)
	if !ok || link.State != statePending || link.ChannelID != notice.ChannelID || link.TS != notice.TS || link.TeamID != "T0ACME001" ||
		link.AskerUserID != "user_asker" || link.OwnerUserID != "user_raya" || link.ExpiresAt == nil || !link.ExpiresAt.Equal(pendingRequest().ExpiresAt) {
		t.Fatalf("link = %+v, %v", link, ok)
	}
	// The asker's own line in their agent chat.
	if got := h.notes.texts(); len(got) != 1 || got[0] != "asked: Asked Raya's agent in #launch" {
		t.Fatalf("notes = %v", got)
	}
	if note := h.notes.all()[0]; note.Member.UserID != "user_asker" || note.Member.OrganizationID != "org_1" || note.Member.AgentID != "agt_asker" || note.Note.RequestID != "irq_1" {
		t.Fatalf("note = %+v, want it in the asker's own agent chat", note)
	}
}

func TestAPassThatFindsNothingNewSaysNothingAgain(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	for range 4 {
		h.mustPass("irq_1")
	}
	if got := len(h.api.sent()); got != 1 || h.api.postRequests() != 1 || h.api.updateRequests() != 0 {
		t.Fatalf("%d messages after %d post requests and %d updates, want the notice once and nothing else", got, h.api.postRequests(), h.api.updateRequests())
	}
	if got := h.ledger.tickets(); len(got) != 1 {
		t.Fatalf("tickets were made %d times, want once", len(got))
	}
	if got := h.notes.texts(); len(got) != 1 {
		t.Fatalf("notes = %v, want one", got)
	}
}

func TestAPostThatWasMadeButNotRecordedIsNotMadeTwice(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	// Slack has the notice, and the database did not take its link.
	h.links.putFailures = 1
	if err := h.pass("irq_1"); err == nil {
		t.Fatal("pass = nil, want the failure to record the notice")
	}
	if got := len(h.api.sent()); got != 1 {
		t.Fatalf("%d messages after the first pass, want the notice", got)
	}
	if _, ok := h.links.get("irq_1", roleNotice); ok {
		t.Fatal("the link was recorded")
	}
	// Tried again, the same key makes apps answer where the first went, and the
	// link is recorded now.
	h.mustPass("irq_1")
	if got := len(h.api.sent()); got != 1 || h.api.postRequests() != 2 {
		t.Fatalf("%d messages after %d post requests, want one message however often it was asked for", got, h.api.postRequests())
	}
	link, ok := h.links.get("irq_1", roleNotice)
	if !ok || link.TS != h.api.sent()[0].TS || link.State != statePending {
		t.Fatalf("link = %+v, %v, want the first post's", link, ok)
	}
}

func TestADecisionReplacesTheNoticeWithHowItEnded(t *testing.T) {
	for _, test := range []struct {
		name     string
		decide   func(agents.Interaction) agents.Interaction
		decision string
		decided  bool
	}{
		{"an AI summary, chosen in Slack", func(r agents.Interaction) agents.Interaction {
			return memberDecided(r, interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack)
		}, "approved_summary", true},
		{"access for an hour, chosen on the web", func(r agents.Interaction) agents.Interaction {
			return memberDecided(r, interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceWeb)
		}, "approved_access", true},
		{"a denial", func(r agents.Interaction) agents.Interaction {
			return memberDecided(r, interactions.DecisionRejected, "", interactions.SurfaceSlack)
		}, "denied", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			h.ledger.put(pendingRequest())
			h.mustPass("irq_1")
			notice := h.api.sent()[0]

			h.ledger.put(test.decide(pendingRequest()))
			h.mustPass("irq_1")
			if len(notice.Updates) != 1 {
				t.Fatalf("the notice was updated %d times, want once", len(notice.Updates))
			}
			replaced := notice.Updates[0]
			if replaced.Template != "notice_settled" {
				t.Fatalf("template = %q", replaced.Template)
			}
			params := replaced.Params
			if params["decision"] != test.decision || params["decided_by_user_id"] != slackRaya || params["asker_user_id"] != slackAsker ||
				params["question"] != "What is the launch date for Project Falcon?" {
				t.Fatalf("params = %v", params)
			}
			if at, _ := params["decided_at"].(string); at != testNow.Add(-30*time.Second).Format(time.RFC3339) {
				t.Fatalf("decided_at = %q, want when the ledger says it was decided", at)
			}
			link, _ := h.links.get("irq_1", roleNotice)
			if link.State != test.decision || link.ExpiresAt != nil {
				t.Fatalf("link = %+v, want it to say how it ended and no longer expire", link)
			}
			// The buttons are gone once, and stay gone.
			h.mustPass("irq_1")
			h.mustPass("irq_1")
			if h.api.updateRequests() != 1 {
				t.Fatalf("updates = %d, want one however many passes follow", h.api.updateRequests())
			}
			// A denial is also told to the asker, alone; an approval says nothing
			// to them until there is an answer.
			want := "[owner_notice]"
			if test.decision == "denied" {
				want = "[owner_notice status]"
			}
			if got := fmt.Sprint(templates(h.api.sent())); got != want {
				t.Fatalf("posted %s, want %s", got, want)
			}
		})
	}
}

func TestAnOwnerWhoDecidedOnTheWebBeforeTheNoticeWentGetsNone(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceWeb))
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); len(got) != 0 {
		t.Fatalf("posted %v, want nothing: the owner already decided", got)
	}
	if got := h.ledger.tickets(); len(got) != 0 {
		t.Fatalf("tickets were made for a decided request: %v", got)
	}
	if _, ok := h.links.get("irq_1", roleNotice); ok {
		t.Fatal("a link was recorded for a notice never sent")
	}
}

func TestARequestDecidedWhileItsNoticeWasBeingMadeGetsNoButtons(t *testing.T) {
	// The pass read the request pending, and the owner decided before the
	// tickets were made: the ledger says so, and there is no notice to send.
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.ledger.ticketErr = interactions.ErrConflict
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); len(got) != 0 {
		t.Fatalf("posted %v, want none", got)
	}
	h.ledger.ticketErr = interactions.ErrExpired
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); len(got) != 0 {
		t.Fatalf("posted %v, want none for a lapsed request", got)
	}
	// A ledger that cannot make tickets is a notice that cannot be sent, said in
	// the log and never tried again.
	h.ledger.ticketErr = interactions.ErrConsentUnavailable
	h.mustPass("irq_1")
	if link, ok := h.links.get("irq_1", roleNotice); !ok || link.State != stateFailed {
		t.Fatalf("link = %+v, %v, want the notice given up on", link, ok)
	}
	calls := len(h.ledger.tickets())
	h.mustPass("irq_1")
	if len(h.ledger.tickets()) != calls {
		t.Fatal("tickets were asked for again after the notice was given up on")
	}
}

func TestATransientFailureToMakeTicketsIsTriedAgain(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.ledger.ticketErr = errors.New("the ledger is unavailable")
	err := h.pass("irq_1")
	if err == nil || slackrouter.IsPermanent(err) {
		t.Fatalf("pass = %v, want a failure to try again", err)
	}
	h.ledger.ticketErr = nil
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); len(got) != 1 || got[0] != "owner_notice" {
		t.Fatalf("posted %v", got)
	}
}

func TestAnOwnerWhoIsNotMatchedToASlackUserIsNotToldAndTheRequestStands(t *testing.T) {
	h := newHarness(t)
	h.users.forget("user_raya")
	h.ledger.put(pendingRequest())
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); len(got) != 0 {
		t.Fatalf("posted %v, want nothing for an owner who cannot be reached in Slack", got)
	}
	if link, ok := h.links.get("irq_1", roleNotice); !ok || link.State != stateFailed {
		t.Fatalf("link = %+v, %v, want the notice given up on", link, ok)
	}
	// The asker's line is still written, with no name to say.
	if got := h.notes.texts(); len(got) != 1 || got[0] != "asked: Asked Raya's agent in #launch" {
		t.Fatalf("notes = %v", got)
	}
}

func TestAnAskerNobodyMatchedIsNamedByAPlainWord(t *testing.T) {
	h := newHarness(t)
	h.users.forget("user_asker")
	h.ledger.put(pendingRequest())
	h.mustPass("irq_1")
	notice := h.api.sent()[0]
	if notice.param("asker_user_id") != "" || notice.param("asker_name") != askerName {
		t.Fatalf("params = %v, want the asker named by a word", notice.Params)
	}
}

func TestAnotherOwnersAnswerSettlesThisOwnersNoticeAndSaysWhoAnswered(t *testing.T) {
	h := newHarness(t)
	ask := baseAsk()
	h.ledger.putAsk(ask)
	rayas := pendingRequest()
	h.ledger.put(rayas)
	h.mustPass("irq_1")
	notice := h.api.sent()[0]

	// Maya's request won the ask; Raya's was closed as Oryxa, saying so.
	mayas := pendingRequest()
	mayas.ID = "irq_2"
	mayas.Target = agents.InteractionParty{UserID: "user_maya", AgentID: "agt_maya", AgentName: "Krude", UserDisplayName: "Maya"}
	h.ledger.put(completedWith(memberDecided(mayas, interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), "It is the 14th."))
	ask.WinnerRequestID = "irq_2"
	h.ledger.putAsk(ask)
	closed := decidedBy(rayas, interactions.DecisionRejected,
		agents.InteractionDecider{Kind: interactions.DecidedBySystem, Reason: interactions.ReasonAnsweredElsewhere}, "")
	h.ledger.put(closed)

	h.mustPass("irq_1")
	if len(notice.Updates) != 1 || notice.Updates[0].Template != "notice_settled" {
		t.Fatalf("updates = %+v", notice.Updates)
	}
	params := notice.Updates[0].Params
	if params["decision"] != "answered_by_other" || params["decided_by_user_id"] != slackMaya {
		t.Fatalf("params = %v, want it to say Maya answered", params)
	}
	// The asker is told nothing on this request's account: Maya's answer is theirs.
	for _, made := range h.api.sent() {
		if made.Template == "status" || made.Template == "answer" {
			t.Fatalf("a request another owner answered told the asker something: %+v", made)
		}
	}
	// And the winner's own request does say its answer.
	h.mustPass("irq_2")
	if got := h.api.sentWith("answer"); len(got) != 1 || got[0].param("text") != "It is the 14th." || got[0].param("owner_name") != "Maya" {
		t.Fatalf("answers = %+v", got)
	}
}

func TestAnOwnerWhoWasApprovedByALeaseIsNotSentANotice(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(leased(pendingRequest()))
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); len(got) != 0 {
		t.Fatalf("posted %v, want nothing before there is an answer: the owner already agreed to this", got)
	}
	h.ledger.put(completedWith(leased(pendingRequest()), "Still the 14th."))
	h.mustPass("irq_1")
	answers := h.api.sentWith("answer")
	if len(answers) != 1 || answers[0].param("mode") != "lease" || answers[0].Target != h.threadTarget() {
		t.Fatalf("answers = %+v, want one in the thread saying it was under the owner's 1-hour access", answers)
	}
	if got := h.api.sentWith("owner_notice"); len(got) != 0 {
		t.Fatalf("a notice was sent for a lease: %+v", got)
	}
	if got := h.api.sentWith("bypass_notice"); len(got) != 0 {
		t.Fatalf("a bypass notice was sent for a lease: %+v", got)
	}
}

func (h *harness) threadTarget() slackrouter.Target {
	return slackrouter.Target{ChannelID: channelID, ThreadTS: threadTS}
}

func TestANoticeWhoseRequestLapsedIsReplacedAndTheAskerToldAlone(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.mustPass("irq_1")
	notice := h.api.sent()[0]

	// Nothing announces a request lapsing: a later pass finds it so.
	h.now = testNow.Add(25 * time.Hour)
	h.mustPass("irq_1")
	if len(notice.Updates) != 1 || notice.Updates[0].Params["decision"] != "expired" || notice.Updates[0].Params["decided_by_user_id"] != nil {
		t.Fatalf("updates = %+v, want the notice to say it expired", notice.Updates)
	}
	closed := h.api.sentWith("status")
	if len(closed) != 1 || closed[0].param("state") != "expired" || closed[0].Target.UserID != slackAsker || closed[0].Target.ThreadTS != threadTS {
		t.Fatalf("statuses = %+v, want the asker told alone that it expired", closed)
	}
	if got := h.notes.texts(); len(got) != 2 || got[1] != "closed: Asked Raya's agent in #launch · no answer" {
		t.Fatalf("notes = %v", got)
	}
	h.mustPass("irq_1")
	if h.api.updateRequests() != 1 || len(h.api.sentWith("status")) != 1 {
		t.Fatal("a later pass said it again")
	}
}

func TestAPassForARequestThatIsNotOursIsGivenUp(t *testing.T) {
	h := newHarness(t)
	agentRequest := pendingRequest()
	agentRequest.ID, agentRequest.Origin, agentRequest.AskID = "irq_agent", interactions.OriginAgent, ""
	h.ledger.put(agentRequest)
	other := pendingRequest()
	other.ID, other.OrganizationID = "irq_other", "org_2"
	h.ledger.put(other)
	noAsk := pendingRequest()
	noAsk.ID, noAsk.AskID = "irq_noask", "iak_missing"
	h.ledger.put(noAsk)
	foreignAsk := baseAsk()
	foreignAsk.ID, foreignAsk.OrganizationID = "iak_foreign", "org_2"
	h.ledger.putAsk(foreignAsk)
	crossed := pendingRequest()
	crossed.ID, crossed.AskID = "irq_crossed", "iak_foreign"
	h.ledger.put(crossed)

	for name, request := range map[string]string{
		"an agent's request":     "irq_agent",
		"another organization's": "irq_other",
		"one whose ask is gone":  "irq_noask",
		"a request that is gone": "irq_gone",
		"an ask of another org":  "irq_crossed",
	} {
		if err := h.pass(request); !slackrouter.IsPermanent(err) {
			t.Errorf("%s: pass = %v, want Permanent", name, err)
		}
	}
	if got := len(h.api.sent()); got != 0 {
		t.Fatalf("%d messages posted for requests that are not ours", got)
	}
}

func TestALedgerThatDoesNotAnswerIsTriedAgain(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.ledger.getFailures = 1
	if err := h.pass("irq_1"); err == nil || slackrouter.IsPermanent(err) {
		t.Fatalf("pass = %v, want a failure to try again", err)
	}
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); len(got) != 1 {
		t.Fatalf("posted %v", got)
	}
}

func TestARateLimitIsWaitedOutByTheClientAndTheNoticeIsStillSentOnce(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.api.respond = func(n int, _ map[string]any, w http.ResponseWriter) bool {
		if n <= 2 {
			w.Header().Set("Retry-After", "2")
			h.api.refusal(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
			return true
		}
		return false
	}
	h.mustPass("irq_1")
	if len(h.waits) != 2 || h.waits[0] != 2*time.Second {
		t.Fatalf("waits = %v, want the two the limit asked for", h.waits)
	}
	if got := templates(h.api.sent()); len(got) != 1 || h.api.postRequests() != 3 {
		t.Fatalf("posted %v after %d requests, want one message", got, h.api.postRequests())
	}
}

func TestARateLimitThatDoesNotLiftFailsThePassSayingHowLongAndTheNextPassSendsIt(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	limited := true
	h.api.respond = func(_ int, _ map[string]any, w http.ResponseWriter) bool {
		if limited {
			w.Header().Set("Retry-After", "30")
			h.api.refusal(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
			return true
		}
		return false
	}
	err := h.pass("irq_1")
	if err == nil || slackrouter.IsPermanent(err) || retryAfterOf(err) != 30*time.Second {
		t.Fatalf("pass = %v (retry after %v), want a failure that asks for 30s", err, retryAfterOf(err))
	}
	// What the worker will wait: the limit's own word when it is longer than its backoff.
	if delay := min(max(retryDelay(1), retryAfterOf(err)), maxRetryDelay); delay != 30*time.Second {
		t.Fatalf("worker delay = %v", delay)
	}
	if _, ok := h.links.get("irq_1", roleNotice); ok {
		t.Fatal("a notice that was not sent has a link")
	}
	limited = false
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); len(got) != 1 || got[0] != "owner_notice" {
		t.Fatalf("posted %v", got)
	}
}

func TestOneStepFailingDoesNotStopTheOthersAndTheRetryDoesNotRepeatWhatWasDone(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.notes.failures = 1
	err := h.pass("irq_1")
	if err == nil || !strings.Contains(err.Error(), "note") {
		t.Fatalf("pass = %v, want the note's failure", err)
	}
	if got := templates(h.api.sent()); len(got) != 1 {
		t.Fatalf("posted %v, want the notice sent whatever the note did", got)
	}
	h.mustPass("irq_1")
	if got := templates(h.api.sent()); len(got) != 1 || h.api.postRequests() != 1 {
		t.Fatalf("posted %v after %d requests, want the retry to leave the notice alone", got, h.api.postRequests())
	}
	if got := h.notes.texts(); len(got) != 1 {
		t.Fatalf("notes = %v", got)
	}
}

func TestNeverAnAskAckWhateverBecomesOfTheRequest(t *testing.T) {
	// The router acknowledges an ask to the one who made it. Whatever the
	// service goes on to say, it never says that too. (The fake apps also fails
	// any test that posts one.)
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.mustPass("irq_1")
	h.ledger.put(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceSlack))
	h.mustPass("irq_1")
	h.ledger.put(completedWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceSlack), "It is the 14th."))
	h.mustPass("irq_1")
	for _, name := range templates(h.api.sent()) {
		if name == "ask_ack" || name == "refusal" {
			t.Fatalf("the service posted %s", name)
		}
	}
	if got := templates(h.api.sent()); fmt.Sprint(got) != "[owner_notice answer]" {
		t.Fatalf("posted %v", got)
	}
}

func TestANewServiceNeedsWhatItReads(t *testing.T) {
	h := newHarness(t)
	full := Deps{Requests: h.ledger, Asks: h.ledger, Tickets: h.ledger, Slack: h.client, Users: h.users}
	if _, err := NewService(h.links, full); err != nil {
		t.Fatalf("a service without notes: %v", err)
	}
	for name, mutate := range map[string]func(*Deps){
		"requests": func(d *Deps) { d.Requests = nil },
		"asks":     func(d *Deps) { d.Asks = nil },
		"tickets":  func(d *Deps) { d.Tickets = nil },
		"slack":    func(d *Deps) { d.Slack = nil },
		"users":    func(d *Deps) { d.Users = nil },
	} {
		deps := full
		mutate(&deps)
		if _, err := NewService(h.links, deps); err == nil {
			t.Errorf("a service without %s was built", name)
		}
	}
	if _, err := NewService(nil, full); err == nil {
		t.Error("a service without links was built")
	}
	if _, err := New(nil, full, WorkerConfig{}); err == nil {
		t.Error("a worker without a store was built")
	}
}
