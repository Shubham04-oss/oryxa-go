package interactiondelivery

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

// slackRequest is a request the Slack app made for the member who mentioned it,
// in status: held for its owner, whatever it names.
func slackRequest(status string) agents.Interaction {
	request := testRequest(status)
	request.Origin = interactions.OriginSlack
	request.Approval = interactions.ApprovalOwner
	request.AskID = "iak_1"
	return request
}

// TestAnAskedInSlackRequestDoesNotWakeTheAskersAgent: the member who asked is
// told in Slack, in a note written without a model, so how their request ended
// is not a run of their agent: each requester delivery of a Slack request is
// finished with none, whatever ended it, and nothing else of it changes.
func TestAnAskedInSlackRequestDoesNotWakeTheAskersAgent(t *testing.T) {
	for _, testCase := range []struct {
		kind   string
		status string
	}{
		{KindRequesterRejected, interactions.StatusRejected},
		{KindRequesterCompleted, interactions.StatusCompleted},
		{KindRequesterCompleted, interactions.StatusFailed},
		{KindRequesterCompleted, interactions.StatusWithheld},
	} {
		delivery := testDelivery("evt_1", testCase.kind, 3)
		delivery.RecipientUserID, delivery.RecipientAgentID, delivery.Status = "user_a", "agt_a", testCase.status
		store := &fakeDeliveries{pending: []Delivery{delivery}}
		requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": slackRequest(testCase.status)}}
		runs := &fakeAgentRuns{}
		waker := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil)
		if err := waker.WakeDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(runs.calls) != 0 || len(store.done) != 1 || store.done[0] != "evt_1" || len(store.released) != 0 || len(store.dropped) != 0 {
			t.Errorf("%s of a %s Slack request: runs=%d done=%v released=%v dropped=%v; want it finished with no run",
				testCase.kind, testCase.status, len(runs.calls), store.done, store.released, store.dropped)
		}
	}
}

// The same delivery of a request an agent made in the ordinary way still wakes
// the agent that asked, as it always did, and the owner's deliveries of a Slack
// request still wake the owner's agent.
func TestOnlyTheSlackAskersReportIsSkipped(t *testing.T) {
	// An ordinary request's requester is still told by its agent.
	delivery := testDelivery("evt_1", KindRequesterCompleted, 3)
	delivery.RecipientUserID, delivery.RecipientAgentID, delivery.Status = "user_a", "agt_a", interactions.StatusCompleted
	store := &fakeDeliveries{pending: []Delivery{delivery}}
	ordinary := testRequest(interactions.StatusCompleted)
	requests := &fakeRequests{requests: map[string]agents.Interaction{"irq_1": ordinary}}
	runs := &fakeAgentRuns{}
	if err := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil).WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 1 || runs.calls[0].agentID != "agt_a" {
		t.Fatalf("an ordinary request's report started %+v, want the asker's agent", runs.calls)
	}

	// The owner's agent is woken to do a Slack request's approved work, in a run
	// that reaches what the owner's approval allows.
	approved := testDelivery("evt_2", KindTargetApproved, 2)
	approved.Status = interactions.StatusApproved
	request := slackRequest(interactions.StatusApproved)
	request.Decision = &agents.InteractionDecision{
		Decision: interactions.DecisionApproved, AllowedTools: []string{agents.KnowledgeToolboxName},
		GrantedPermissions: []string{interactions.PermissionConsentSummary},
		DecidedBy:          &agents.InteractionDecider{Kind: interactions.DecidedByMember, UserID: "user_b", Surface: interactions.SurfaceSlack},
	}
	store = &fakeDeliveries{pending: []Delivery{approved}}
	requests = &fakeRequests{requests: map[string]agents.Interaction{"irq_1": request}}
	runs = &fakeAgentRuns{onExecute: func() { requests.setStatus("irq_1", interactions.StatusCompleted) }}
	if err := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil).WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 1 || runs.calls[0].agentID != "agt_b" || runs.calls[0].reach != "delivery_quiet" ||
		runs.calls[0].input.Delivery.Purpose != agents.DeliveryWorks {
		t.Fatalf("a Slack request's approved work started %+v, want the owner's agent with knowledge alone", runs.calls)
	}
	// The run is told, in its state, what it reaches, so that its answer is judged
	// by what it could read and never by what it says it read.
	if got := runs.calls[0].input.Delivery.Sources; !slices.Equal(got, []string{agents.KnowledgeToolboxName}) {
		t.Fatalf("the run was recorded as reaching %v, want the knowledge", got)
	}
	// A new Slack request waits for its owner as any request outside a teamspace
	// does: its delivery is finished with no run.
	requested := testDelivery("evt_3", KindTargetRequested, 1)
	store = &fakeDeliveries{pending: []Delivery{requested}}
	requests = &fakeRequests{requests: map[string]agents.Interaction{"irq_1": slackRequest(interactions.StatusPending)}}
	runs = &fakeAgentRuns{}
	if err := NewWaker(store, runs, requests, Config{Interval: time.Second, Lease: time.Minute}, nil).WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runs.calls) != 0 || len(store.done) != 1 {
		t.Fatalf("a Slack request waiting for its owner started %+v, done %v", runs.calls, store.done)
	}
}

// TestARunIsToldWhatItsReachCanRead: every reach a delivery run is given records
// the toolboxes it loads, and only those; a report reads nothing private and
// says so; one this code does not know records nothing, so that its answer is
// judged rather than trusted.
func TestARunIsToldWhatItsReachCanRead(t *testing.T) {
	for profile, want := range map[managedagents.UnattendedProfile][]string{
		managedagents.DeliveryReads:   {agents.AppsToolboxName, agents.KnowledgeToolboxName},
		managedagents.DeliveryApps:    {agents.AppsToolboxName},
		managedagents.DeliveryQuiet:   {agents.KnowledgeToolboxName},
		managedagents.DeliveryReport:  {},
		"delivery_something_new":      nil,
		managedagents.UnattendedReads: nil,
	} {
		got := sourcesOf(profile)
		if !slices.Equal(got, want) || (got == nil) != (want == nil) {
			t.Errorf("%s reaches %#v, want %#v", profile, got, want)
		}
	}
}

// TestTheAgentDoingApprovedWorkIsToldHowFarItsOwnerLetTheAnswerGo: for a
// request made from Slack, the message that wakes the agent says whether its
// owner allowed a summary or excerpts; for any other request it says nothing of
// the kind.
func TestTheAgentDoingApprovedWorkIsToldHowFarItsOwnerLetTheAnswerGo(t *testing.T) {
	approved := testDelivery("evt_1", KindTargetApproved, 2)
	approved.Status = interactions.StatusApproved
	for _, testCase := range []struct {
		name    string
		origin  string
		consent string
		want    string
	}{
		{"a summary from Slack", interactions.OriginSlack, interactions.ConsentSummary, summaryOnly},
		{"access from Slack", interactions.OriginSlack, interactions.ConsentAccess, excerptsOK},
		{"no consent from Slack", interactions.OriginSlack, "", ""},
		{"a summary on an ordinary request", interactions.OriginAgent, interactions.ConsentSummary, ""},
		{"an old request that names no origin", "", "", ""},
	} {
		request := slackRequest(interactions.StatusApproved)
		request.Origin = testCase.origin
		request.Decision = &agents.InteractionDecision{Decision: interactions.DecisionApproved, Consent: testCase.consent}
		got := messageFor(approved, request, between{})
		plain := message(approved, false, between{})
		if testCase.want == "" && got != plain || testCase.want != "" && got != plain+testCase.want {
			t.Errorf("%s: the message is %q, want it to add %q", testCase.name, got, testCase.want)
		}
	}
	// Only the work is told: deciding, and a report, are not about the answer.
	requested := testDelivery("evt_2", KindTargetRequested, 1)
	summary := slackRequest(interactions.StatusApproved)
	summary.Decision = &agents.InteractionDecision{Decision: interactions.DecisionApproved, Consent: interactions.ConsentSummary}
	if got, plain := messageFor(requested, summary, between{}), message(requested, false, between{}); got != plain {
		t.Errorf("a delivery to decide was told %q", got)
	}
}
