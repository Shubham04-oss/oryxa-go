package surfaces

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// systemDecider is Oryxa closing a request, with the reason it records.
func systemDecider(reason string) agents.InteractionDecider {
	return agents.InteractionDecider{Kind: interactions.DecidedBySystem, Reason: reason}
}

func TestTheAskersLineNamesWhoTheyAskedAndWhereAndHowItWent(t *testing.T) {
	approved := memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack)
	for name, test := range map[string]struct {
		request agents.Interaction
		want    []string
	}{
		"waiting": {pendingRequest(), []string{"asked: Asked Raya's agent in #launch"}},
		"answered": {completedWith(approved, "The 14th."), []string{
			"asked: Asked Raya's agent in #launch", "answered: Asked Raya's agent in #launch · answered"}},
		"held": {heldWith(approved, "held"), []string{
			"asked: Asked Raya's agent in #launch", "held: Asked Raya's agent in #launch · held"}},
		"declined": {memberDecided(pendingRequest(), interactions.DecisionRejected, "", interactions.SurfaceWeb), []string{
			"asked: Asked Raya's agent in #launch", "declined: Asked Raya's agent in #launch · declined"}},
		"closed": {decidedBy(pendingRequest(), interactions.DecisionRejected, systemDecider(""), ""), []string{
			"asked: Asked Raya's agent in #launch", "closed: Asked Raya's agent in #launch · no answer"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.ledger.put(test.request)
			h.mustPass("irq_1")
			if got := h.notes.texts(); fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("notes = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTheAskersLineSaysSlackWhenTheChannelHasNoNameToSay(t *testing.T) {
	for name, behaviour := range map[string]channelBehaviour{
		"it is gone":             {status: http.StatusNotFound, code: "not_found"},
		"it needs another scope": {status: http.StatusUnprocessableEntity, code: "channel_unreadable"},
		"apps is failing":        {status: http.StatusServiceUnavailable, code: "unavailable"},
		"it has no name":         {view: map[string]any{"channel_id": channelID, "is_private": true}},
		"it is a direct message": {view: map[string]any{"channel_id": channelID, "name": "ada", "is_im": true}},
		"it is a group message":  {view: map[string]any{"channel_id": channelID, "name": "mpdm-ada--bo-1", "is_mpim": true}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.api.setChannel(channelID, behaviour)
			h.ledger.put(pendingRequest())
			// A channel that cannot be read is no reason for the asker's line, or
			// anything else, to fail.
			h.mustPass("irq_1")
			if got := h.notes.texts(); len(got) != 1 || got[0] != "asked: Asked Raya's agent in Slack" {
				t.Fatalf("notes = %q", got)
			}
		})
	}
	h := newHarness(t)
	h.api.setChannel(channelID, channelBehaviour{view: map[string]any{"channel_id": channelID, "name": "#launch"}})
	h.ledger.put(pendingRequest())
	h.mustPass("irq_1")
	if got := h.notes.texts(); len(got) != 1 || got[0] != "asked: Asked Raya's agent in #launch" {
		t.Fatalf("notes = %q, want the name without a doubled #", got)
	}
}

func TestTheAskersLineCallsTheOwnerWhatTheyAreCalledInSlackOrNothingAtAll(t *testing.T) {
	for name, test := range map[string]struct {
		chosen   string
		identity bool
		want     string
	}{
		"the name they chose":         {"Raya", true, "Asked Raya's agent in #launch"},
		"the name Slack has for them": {"", true, "Asked Raya S.'s agent in #launch"},
		"no name to call them":        {"", false, "Asked their agent in #launch"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			request := pendingRequest()
			request.Target.UserDisplayName = test.chosen
			if !test.identity {
				h.users.forget("user_raya")
			}
			h.ledger.put(request)
			h.mustPass("irq_1")
			if got := h.notes.texts(); len(got) != 1 || got[0] != "asked: "+test.want {
				t.Fatalf("notes = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTheNoteIsTheAskersOwnAndInTheirPrimaryAgentsName(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.mustPass("irq_1")
	written := h.notes.all()
	if len(written) != 1 {
		t.Fatalf("notes = %+v", written)
	}
	member, note := written[0].Member, written[0].Note
	if member != (threads.Member{UserID: "user_asker", OrganizationID: "org_1", AgentID: "agt_asker", AgentName: "Krude", AgentKind: "primary"}) {
		t.Fatalf("member = %+v, want the one who asked, not the one asked", member)
	}
	if note.RequestID != "irq_1" || note.State != threads.NoteAsked {
		t.Fatalf("note = %+v", note)
	}
}

func TestAnAnsweredRequestKeepsItsEarlierLineAndAddsItsEnd(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.mustPass("irq_1")
	h.ledger.put(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceSlack))
	h.mustPass("irq_1")
	h.ledger.put(completedWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentAccess, interactions.SurfaceSlack), "The 14th."))
	h.mustPass("irq_1")
	h.mustPass("irq_1")
	if got := h.notes.texts(); len(got) != 2 {
		t.Fatalf("notes = %q, want the asking and the answer once each", got)
	}
	// The thread service was asked for each line once, not on every pass.
	if h.notes.calls != 2 {
		t.Fatalf("the thread service was called %d times, want 2", h.notes.calls)
	}
}

func TestALineThatFailsIsTriedAgainAndTheEarlierOnesAreNotWrittenAgain(t *testing.T) {
	h := newHarness(t)
	h.ledger.put(pendingRequest())
	h.mustPass("irq_1")
	h.ledger.put(completedWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), "The 14th."))
	h.notes.failures = 1
	if err := h.pass("irq_1"); err == nil || !strings.Contains(err.Error(), "note") {
		t.Fatalf("pass = %v, want the line's failure", err)
	}
	// What Slack was told is told, whatever the line did.
	if got := h.api.sentWith("answer"); len(got) != 1 {
		t.Fatalf("answers = %+v", got)
	}
	h.mustPass("irq_1")
	if got := h.notes.texts(); len(got) != 2 || !strings.HasPrefix(got[1], "answered:") {
		t.Fatalf("notes = %q", got)
	}
	if h.notes.calls != 3 {
		t.Fatalf("the thread service was called %d times, want the first line once and the second twice", h.notes.calls)
	}
	if len(h.api.sentWith("answer")) != 1 || h.api.postRequests() != 2 {
		t.Fatalf("a retry for a line said things in Slack again: %d post requests", h.api.postRequests())
	}
}

func TestALineThatCanNeverBeWrittenIsGivenUpOn(t *testing.T) {
	h := newHarness(t)
	h.notes.failures, h.notes.failWith = 100, threads.ErrInvalid
	h.ledger.put(pendingRequest())
	if err := h.pass("irq_1"); err != nil {
		t.Fatalf("pass = %v, want a line that is refused for good to be given up on", err)
	}
	if link, ok := h.links.get("irq_1", roleNotePrefix+threads.NoteAsked); !ok || link.State != stateFailed {
		t.Fatalf("link = %+v, %v", link, ok)
	}
	calls := h.notes.calls
	h.mustPass("irq_1")
	if h.notes.calls != calls {
		t.Fatal("a line that was given up on was tried again")
	}
}

func TestARequestAnotherOwnerAnsweredHasNoEndingLineOfItsOwn(t *testing.T) {
	h := newHarness(t)
	ask := baseAsk()
	ask.WinnerRequestID = "irq_2"
	h.ledger.putAsk(ask)
	h.ledger.put(decidedBy(pendingRequest(), interactions.DecisionRejected,
		systemDecider(interactions.ReasonAnsweredElsewhere), ""))
	h.mustPass("irq_1")
	if got := h.notes.texts(); len(got) != 1 || !strings.HasPrefix(got[0], "asked:") {
		t.Fatalf("notes = %q, want only the asking: the answer is in the other owner's line", got)
	}
}

func TestWithoutAThreadServiceNothingIsNotedAndEverythingElseIsSaid(t *testing.T) {
	h := newHarness(t)
	service, err := NewService(h.links, Deps{
		Requests: h.ledger, Asks: h.ledger, Tickets: h.ledger, Slack: h.client, Users: h.users,
		Now: func() time.Time { return h.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.ledger.put(pendingRequest())
	if err := service.Handle(t.Context(), Job{RequestID: "irq_1", OrganizationID: "org_1"}); err != nil {
		t.Fatal(err)
	}
	if got := templates(h.api.sent()); len(got) != 1 || got[0] != "owner_notice" {
		t.Fatalf("posted %v", got)
	}
	if h.notes.calls != 0 {
		t.Fatal("notes were written with no service to write them")
	}
}

// fakeMembers says who still belongs to the organization, and counts asking.
type fakeMembers struct {
	mu     sync.Mutex
	member bool
	err    error
	calls  int
	asked  []string
}

func (m *fakeMembers) IsMember(_ context.Context, organizationID, userID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.asked = append(m.asked, organizationID+"/"+userID)
	return m.member, m.err
}

// serviceWithMembers is the harness's service that also checks who still belongs.
func (h *harness) serviceWithMembers(members Members) *Service {
	h.t.Helper()
	service, err := NewService(h.links, Deps{
		Requests: h.ledger, Asks: h.ledger, Tickets: h.ledger, Slack: h.client, Users: h.users, Notes: h.notes,
		Members: members, Now: func() time.Time { return h.now },
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return service
}

func TestNoNoteIsWrittenForAnAskerWhoHasLeftTheOrganizationAndEverythingElseIsStillSaid(t *testing.T) {
	h := newHarness(t)
	members := &fakeMembers{member: false}
	service := h.serviceWithMembers(members)
	h.ledger.put(pendingRequest())
	if err := service.Handle(t.Context(), Job{RequestID: "irq_1", OrganizationID: "org_1"}); err != nil {
		t.Fatalf("pass = %v, want an asker who left to be no reason to fail", err)
	}
	if h.notes.calls != 0 {
		t.Fatalf("the thread service was called %d times for a member who left", h.notes.calls)
	}
	if got := members.asked; len(got) != 1 || got[0] != "org_1/user_asker" {
		t.Fatalf("membership was asked of %v, want the asker in their organization", got)
	}
	if _, ok := h.links.get("irq_1", roleNotePrefix+threads.NoteAsked); ok {
		t.Fatal("a line was recorded for one that was not written")
	}
	// What Slack is told does not depend on it.
	if got := templates(h.api.sent()); fmt.Sprint(got) != "[owner_notice]" {
		t.Fatalf("posted %v", got)
	}
}

func TestAnAskerWhoStillBelongsGetsTheirLinesAndMembershipIsNotAskedAgainOnceTheyAreWritten(t *testing.T) {
	h := newHarness(t)
	members := &fakeMembers{member: true}
	service := h.serviceWithMembers(members)
	h.ledger.put(pendingRequest())
	for range 3 {
		if err := service.Handle(t.Context(), Job{RequestID: "irq_1", OrganizationID: "org_1"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.notes.texts(); len(got) != 1 {
		t.Fatalf("notes = %q", got)
	}
	if members.calls != 1 {
		t.Fatalf("membership was asked %d times, want once: nothing was due on the passes after the first", members.calls)
	}
	// An ending is due, and asks again.
	h.ledger.put(completedWith(memberDecided(pendingRequest(), interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), "The 14th."))
	if err := service.Handle(t.Context(), Job{RequestID: "irq_1", OrganizationID: "org_1"}); err != nil {
		t.Fatal(err)
	}
	if members.calls != 2 || len(h.notes.texts()) != 2 {
		t.Fatalf("membership asked %d times, notes %q", members.calls, h.notes.texts())
	}
}

func TestAMembershipCheckThatFailsIsTriedAgainAndWritesNothingUntilItAnswers(t *testing.T) {
	h := newHarness(t)
	members := &fakeMembers{err: errors.New("auth's tables are unavailable")}
	service := h.serviceWithMembers(members)
	h.ledger.put(pendingRequest())
	err := service.Handle(t.Context(), Job{RequestID: "irq_1", OrganizationID: "org_1"})
	if err == nil || slackrouter.IsPermanent(err) || !strings.Contains(err.Error(), "belongs") {
		t.Fatalf("pass = %v, want a failure to try again", err)
	}
	if h.notes.calls != 0 {
		t.Fatal("a line was written before it was known that the asker belongs")
	}
	members.err, members.member = nil, true
	if err := service.Handle(t.Context(), Job{RequestID: "irq_1", OrganizationID: "org_1"}); err != nil {
		t.Fatal(err)
	}
	if got := h.notes.texts(); len(got) != 1 {
		t.Fatalf("notes = %q", got)
	}
	// The notice went once, whichever pass it went in.
	if len(h.api.sentWith("owner_notice")) != 1 {
		t.Fatalf("posted %v", templates(h.api.sent()))
	}
}
