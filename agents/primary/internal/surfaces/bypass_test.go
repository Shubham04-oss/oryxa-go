package surfaces

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

// markedChannel is what a private, unshared channel an administrator marked
// reads as.
func markedChannel() channelBehaviour {
	return channelBehaviour{view: map[string]any{
		"channel_id": channelID, "name": "launch", "is_private": true, "is_shared": false, "high_priority": true,
	}}
}

func TestABypassInAMarkedChannelAnswersInTheThreadAndTheOwnerIsTold(t *testing.T) {
	h := newHarness(t)
	h.api.setChannel(channelID, markedChannel())
	h.ledger.put(completedWith(bypassed(pendingRequest(), "priority_channel"), "The launch is on the 14th."))
	h.mustPass("irq_1")

	// The owner is told at once, in a direct message, with why and where.
	told := h.api.sentWith("bypass_notice")
	if len(told) != 1 {
		t.Fatalf("posted %v, want the owner told once", templates(h.api.sent()))
	}
	notice := told[0]
	if want := (slackrouter.Target{UserID: slackRaya}); notice.Target != want {
		t.Fatalf("bypass notice target = %+v, want the owner's direct message", notice.Target)
	}
	if notice.param("reason") != "priority_channel" || notice.param("channel_id") != channelID || notice.param("asker_user_id") != slackAsker ||
		notice.param("question") != "What is the launch date for Project Falcon?" || len(notice.Params) != 4 {
		t.Fatalf("bypass notice params = %v", notice.Params)
	}
	if notice.Key != "surfaces:irq_1:bypass_notice" {
		t.Fatalf("key = %q", notice.Key)
	}
	// The answer goes where the policy says for a priority channel: the thread.
	if consentpolicy.AnswerFor(consentpolicy.PriorityChannel) != consentpolicy.InThread {
		t.Fatal("the policy no longer answers priority channels in the thread")
	}
	answers := h.api.sentWith("answer")
	if len(answers) != 1 || answers[0].Target != h.threadTarget() || answers[0].param("mode") != "bypass" || answers[0].param("text") != "The launch is on the 14th." {
		t.Fatalf("answers = %+v", answers)
	}
	if got := h.api.sentWith("private_note"); len(got) != 0 {
		t.Fatalf("a private note went with an answer given in the thread: %+v", got)
	}
	// Said once, whatever follows.
	h.mustPass("irq_1")
	if len(h.api.sentWith("bypass_notice")) != 1 || len(h.api.sentWith("answer")) != 1 {
		t.Fatalf("posted %v after a second pass", templates(h.api.sent()))
	}
}

func TestABypassOnAuthorityAloneAnswersTheAskerPrivatelyAndTheThreadSaysSo(t *testing.T) {
	h := newHarness(t)
	// A public channel: the owner never agreed to share this with everyone in it.
	h.api.setChannel(channelID, channelBehaviour{view: map[string]any{"channel_id": channelID, "name": "general", "is_private": false}})
	h.ledger.put(completedWith(bypassed(pendingRequest(), "org_chart"), "The launch is on the 14th."))
	h.mustPass("irq_1")

	answers := h.api.sentWith("answer")
	if len(answers) != 1 {
		t.Fatalf("posted %v", templates(h.api.sent()))
	}
	if want := (slackrouter.Target{UserID: slackAsker}); answers[0].Target != want || answers[0].param("mode") != "bypass" {
		t.Fatalf("answer = %+v, want the asker's own direct message", answers[0])
	}
	notes := h.api.sentWith("private_note")
	if len(notes) != 1 || notes[0].Target != h.threadTarget() || len(notes[0].Params) != 0 {
		t.Fatalf("private notes = %+v, want the thread told it was answered privately", notes)
	}
	told := h.api.sentWith("bypass_notice")
	if len(told) != 1 || told[0].param("reason") != "org_chart" || told[0].Target.UserID != slackRaya {
		t.Fatalf("bypass notices = %+v", told)
	}
	// Authority does not read the channel: it is not what the answer depends on.
	link, _ := h.links.get("irq_1", roleAnswer)
	if link.State != stateSentPrivately {
		t.Fatalf("answer link = %+v, want it to say it went privately", link)
	}
	h.mustPass("irq_1")
	if h.api.postRequests() != 3 {
		t.Fatalf("%d post requests, want the three messages sent once each", h.api.postRequests())
	}
}

func TestABypassAnswerIsNotPostedInAChannelThatIsNoLongerWhatTheBypassNeeded(t *testing.T) {
	for name, view := range map[string]map[string]any{
		"the mark was removed":                   {"channel_id": channelID, "is_private": true, "is_shared": false, "high_priority": false},
		"it is shared with another organization": {"channel_id": channelID, "is_private": true, "is_shared": true, "high_priority": true},
		"it is public now":                       {"channel_id": channelID, "is_private": false, "is_shared": false, "high_priority": true},
		"it is a group message":                  {"channel_id": channelID, "is_private": true, "is_mpim": true, "high_priority": true},
		"it is a direct message":                 {"channel_id": channelID, "is_private": true, "is_im": true, "high_priority": true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.api.setChannel(channelID, channelBehaviour{view: view})
			h.ledger.put(completedWith(bypassed(pendingRequest(), "priority_channel"), "The launch is on the 14th."))
			h.mustPass("irq_1")

			for _, made := range h.api.sent() {
				if made.Target.ThreadTS != "" && made.Template == "answer" {
					t.Fatalf("the answer was posted in a thread of a channel that no longer allows it: %+v", made)
				}
			}
			answers := h.api.sentWith("answer")
			if len(answers) != 1 || answers[0].Target != (slackrouter.Target{UserID: slackAsker}) {
				t.Fatalf("answers = %+v, want the answer to the asker alone", answers)
			}
			if got := h.api.sentWith("private_note"); len(got) != 1 || got[0].Target != h.threadTarget() {
				t.Fatalf("private notes = %+v, want the thread told it was answered privately", got)
			}
		})
	}
}

func TestAChannelSlackWillNotDescribeIsNotOneAnAnswerIsPostedIn(t *testing.T) {
	for name, behaviour := range map[string]channelBehaviour{
		"it is gone":             {status: http.StatusNotFound, code: "not_found"},
		"it needs another scope": {status: http.StatusUnprocessableEntity, code: "channel_unreadable"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.api.setChannel(channelID, behaviour)
			h.ledger.put(completedWith(bypassed(pendingRequest(), "priority_channel"), "The 14th."))
			h.mustPass("irq_1")
			answers := h.api.sentWith("answer")
			if len(answers) != 1 || answers[0].Target != (slackrouter.Target{UserID: slackAsker}) {
				t.Fatalf("answers = %+v, want the asker alone", answers)
			}
		})
	}
}

func TestAChannelThatCannotBeReadJustNowIsNeitherPostedInNorLeftFor(t *testing.T) {
	h := newHarness(t)
	h.api.setChannel(channelID, channelBehaviour{status: http.StatusServiceUnavailable, code: "unavailable"})
	h.ledger.put(completedWith(bypassed(pendingRequest(), "priority_channel"), "The 14th."))
	err := h.pass("irq_1")
	if err == nil || slackrouter.IsPermanent(err) {
		t.Fatalf("pass = %v, want a failure to try again", err)
	}
	if got := h.api.sentWith("answer"); len(got) != 0 {
		t.Fatalf("an answer was posted before the channel was known: %+v", got)
	}
	// The owner is still told, whatever became of the answer.
	if got := h.api.sentWith("bypass_notice"); len(got) != 1 {
		t.Fatalf("posted %v, want the owner told", templates(h.api.sent()))
	}
	// Known again, and marked: the thread.
	h.api.setChannel(channelID, markedChannel())
	h.mustPass("irq_1")
	answers := h.api.sentWith("answer")
	if len(answers) != 1 || answers[0].Target != h.threadTarget() {
		t.Fatalf("answers = %+v, want the thread", answers)
	}
	if len(h.api.sentWith("bypass_notice")) != 1 {
		t.Fatal("the owner was told again")
	}
}

func TestTheMarkIsReadAgainForEachAnswerAndNotRememberedFromTheDecision(t *testing.T) {
	// One request decided while the channel was marked, answered after it was
	// unmarked; another, in the same room, answered while it still was.
	h := newHarness(t)
	h.api.setChannel(channelID, markedChannel())
	first := completedWith(bypassed(pendingRequest(), "priority_channel"), "First answer.")
	second := first
	second.ID = "irq_2"
	result := *first.Result
	result.Content = "Second answer."
	second.Result = &result
	h.ledger.put(first)
	h.ledger.put(second)
	h.mustPass("irq_1")
	// An administrator unmarks the channel.
	h.api.setChannel(channelID, channelBehaviour{view: map[string]any{"channel_id": channelID, "is_private": true, "high_priority": false}})
	h.mustPass("irq_2")
	answers := h.api.sentWith("answer")
	if len(answers) != 2 {
		t.Fatalf("posted %v", templates(h.api.sent()))
	}
	if answers[0].Target != h.threadTarget() || answers[1].Target != (slackrouter.Target{UserID: slackAsker}) {
		t.Fatalf("targets = %+v and %+v, want the thread and then the asker alone", answers[0].Target, answers[1].Target)
	}
}

func TestAHeldBypassAnswerIsAnnouncedWhereTheAnswerWouldGo(t *testing.T) {
	// HoldForReview holds a bypassed answer too. The asker is told it is held,
	// but not where everyone reads, when the answer itself would not go there.
	for name, test := range map[string]struct {
		reason string
		want   slackrouter.Target
	}{
		"in the thread, for a priority channel": {"priority_channel", slackrouter.Target{ChannelID: channelID, ThreadTS: threadTS}},
		"to the asker, for authority":           {"org_chart", slackrouter.Target{UserID: slackAsker}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.api.setChannel(channelID, markedChannel())
			h.ledger.put(heldWith(bypassed(pendingRequest(), test.reason), "held text"))
			h.api.forbid("held text")
			h.mustPass("irq_1")
			statuses := h.api.sentWith("status")
			if len(statuses) != 1 || statuses[0].Target != test.want || statuses[0].param("state") != "held" {
				t.Fatalf("statuses = %+v, want the held status at %+v", statuses, test.want)
			}
		})
	}
}

func TestABypassAnswerThatCannotGoPrivatelyBecauseTheAskerIsNotInSlackIsNotAnnouncedAsPrivate(t *testing.T) {
	h := newHarness(t)
	h.users.forget("user_asker")
	h.ledger.put(completedWith(bypassed(pendingRequest(), "org_chart"), "The 14th."))
	h.mustPass("irq_1")
	if got := h.api.sentWith("answer"); len(got) != 0 {
		t.Fatalf("answers = %+v, want none: there is nobody to send it to", got)
	}
	if got := h.api.sentWith("private_note"); len(got) != 0 {
		t.Fatalf("private notes = %+v: nothing was answered privately", got)
	}
	// The owner is still told, by name, since they were asked nothing.
	told := h.api.sentWith("bypass_notice")
	if len(told) != 1 || told[0].param("asker_name") != askerName {
		t.Fatalf("bypass notices = %+v", told)
	}
}

func TestABypassForAReasonTheNoticeHasNoWordsForStillAnswersPrivately(t *testing.T) {
	// A reason this service does not know is answered in the asker's direct
	// message, as consentpolicy.AnswerFor says of every reason but one; and the
	// owner's notice, which has words for two reasons only, is not sent with the
	// wrong ones.
	h := newHarness(t)
	h.ledger.put(completedWith(bypassed(pendingRequest(), "board_override"), "The 14th."))
	h.mustPass("irq_1")
	if got := h.api.sentWith("bypass_notice"); len(got) != 0 {
		t.Fatalf("bypass notices = %+v, want none for a reason the template cannot say", got)
	}
	if link, ok := h.links.get("irq_1", roleBypassNotice); !ok || link.State != stateFailed {
		t.Fatalf("link = %+v, %v, want it given up on", link, ok)
	}
	answers := h.api.sentWith("answer")
	if len(answers) != 1 || answers[0].Target != (slackrouter.Target{UserID: slackAsker}) {
		t.Fatalf("answers = %+v", answers)
	}
}

func TestABypassRequestIsNeverSentAnOwnerNoticeWithButtons(t *testing.T) {
	h := newHarness(t)
	h.api.setChannel(channelID, markedChannel())
	h.ledger.put(bypassed(pendingRequest(), "priority_channel"))
	h.mustPass("irq_1")
	got := fmt.Sprint(templates(h.api.sent()))
	if got != "[bypass_notice]" {
		t.Fatalf("posted %s, want the owner told and no buttons: nobody is asked", got)
	}
	if calls := h.ledger.tickets(); len(calls) != 0 {
		t.Fatalf("tickets were made for a request nobody decides: %v", calls)
	}
}

func TestABypassOwnerWhoIsNotInSlackIsNotToldButTheAnswerIsStillGiven(t *testing.T) {
	h := newHarness(t)
	h.api.setChannel(channelID, markedChannel())
	h.users.forget("user_raya")
	h.ledger.put(completedWith(bypassed(pendingRequest(), "priority_channel"), "The 14th."))
	h.mustPass("irq_1")
	if got := h.api.sentWith("bypass_notice"); len(got) != 0 {
		t.Fatalf("bypass notices = %+v", got)
	}
	if got := h.api.sentWith("answer"); len(got) != 1 || got[0].param("owner_name") != "Raya" {
		t.Fatalf("answers = %+v", got)
	}
}
