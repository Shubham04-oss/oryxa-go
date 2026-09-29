package interactions

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// What a request's events carry: where it was made, who decides it, and who
// did, as the ledger stores them, and none of what it asks or answers.
func TestARequestsEventsSayWhereItWasMadeAndWhoDecides(t *testing.T) {
	at := time.Date(2026, time.September, 25, 9, 30, 0, 0, time.UTC)
	request := func(teamspaceID, approval string, decision *agents.InteractionDecision) agents.Interaction {
		return agents.Interaction{
			ID: "irq_1", OrganizationID: "org_1", TeamspaceID: teamspaceID, Approval: approval,
			Requester: agents.InteractionParty{UserID: "user_a", AgentID: "agt_a", AgentName: "Crew"},
			Target:    agents.InteractionParty{UserID: "user_b", AgentID: "agt_b", AgentName: "Carl", UserEmail: "b@example.com"},
			Purpose:   "Launch", Task: "Review the launch plan.",
			Context: []agents.InteractionContextItem{{Label: "doc", Value: "The plan is secret."}},
			Status:  StatusApproved, Decision: decision, Revision: 2, ExpiresAt: at.Add(time.Hour), CreatedAt: at, UpdatedAt: at,
		}
	}
	decided := func(decider *agents.InteractionDecider) *agents.InteractionDecision {
		return &agents.InteractionDecision{Decision: DecisionApproved, Message: "Only the summary.", DecidedAt: at, DecidedBy: decider}
	}

	for name, test := range map[string]struct {
		request   agents.Interaction
		teamspace string
		approval  string
		decider   *agentsv1.InteractionDecider
	}{
		"pending outside a teamspace": {request("", ApprovalAgent, nil), "", agentsv1.InteractionApprovalAgent, nil},
		"held in a teamspace and approved by its member": {
			request("tsp_1", ApprovalOwner, decided(&agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b"})),
			"tsp_1", agentsv1.InteractionApprovalOwner, &agentsv1.InteractionDecider{Kind: agentsv1.DecidedByMember, UserID: "user_b"},
		},
		"approved by an agent": {
			request("tsp_1", ApprovalAgent, decided(&agents.InteractionDecider{Kind: DecidedByAgent, UserID: "user_b", AgentID: "agt_b"})),
			"tsp_1", agentsv1.InteractionApprovalAgent,
			&agentsv1.InteractionDecider{Kind: agentsv1.DecidedByAgent, UserID: "user_b", AgentID: "agt_b"},
		},
		"closed by Oryxa": {
			request("tsp_1", ApprovalOwner, decided(&agents.InteractionDecider{Kind: DecidedBySystem})),
			"tsp_1", agentsv1.InteractionApprovalOwner, &agentsv1.InteractionDecider{Kind: agentsv1.DecidedBySystem},
		},
		// A decision an older build recorded kept no decider, and none is
		// made up for it.
		"decided by an older build": {request("", ApprovalAgent, decided(nil)), "", agentsv1.InteractionApprovalAgent, nil},
	} {
		event := changed(test.request)
		if event.TeamspaceID != test.teamspace || event.Approval != test.approval ||
			(event.DecidedBy == nil) != (test.decider == nil) || (event.DecidedBy != nil && *event.DecidedBy != *test.decider) {
			t.Errorf("%s: event = %+v (decided by %+v)", name, event, event.DecidedBy)
		}
		if event.RequestID != "irq_1" || event.TargetAgentID != "agt_b" || event.Status != StatusApproved || !event.ChangedAt.Equal(at) {
			t.Errorf("%s: event = %+v", name, event)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, content := range []string{"Review the launch plan", "The plan is secret", "Only the summary", "b@example.com", "Carl"} {
			if strings.Contains(string(encoded), content) {
				t.Errorf("%s: the event carries %q: %s", name, content, encoded)
			}
		}
	}
}

// A request read back from PostgreSQL carries this process's local zone, such
// as +05:30. Its event says when it changed in UTC, as the envelope's
// occurred_at does.
func TestARequestsEventSaysWhenItChangedInUTC(t *testing.T) {
	ist := time.FixedZone("IST", 5*60*60+30*60)
	changedAt := time.Date(2026, time.September, 25, 15, 0, 0, 123456000, ist)
	event := changed(agents.Interaction{
		ID: "irq_1", OrganizationID: "org_1", Status: StatusPending, Revision: 1, Approval: ApprovalAgent,
		Requester: agents.InteractionParty{UserID: "user_a", AgentID: "agt_a"},
		Target:    agents.InteractionParty{UserID: "user_b", AgentID: "agt_b"},
		CreatedAt: changedAt, UpdatedAt: changedAt,
	})
	if !event.ChangedAt.Equal(changedAt) || event.ChangedAt.Location() != time.UTC {
		t.Fatalf("changed_at = %s, want %s in UTC", event.ChangedAt, changedAt.UTC())
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"changed_at":"2026-09-25T09:30:00.123456Z"`) {
		t.Errorf("the event is written %s, want changed_at 2026-09-25T09:30:00.123456Z", encoded)
	}
}

// A Slack request's events carry the ask it belongs to, where it was made, how
// far its owner let the answer go, and where and why it was decided: every one
// of them a code or an id, and none a word of what was asked.
func TestASlackRequestsEventsSayWhichAskWhereFromHowFarAndWhy(t *testing.T) {
	at := time.Date(2026, time.September, 29, 14, 2, 0, 0, time.UTC)
	request := func(decision *agents.InteractionDecision) agents.Interaction {
		return agents.Interaction{
			ID: "irq_1", OrganizationID: "org_1", Approval: ApprovalOwner, AskID: "iak_1", Origin: OriginSlack,
			Requester: agents.InteractionParty{UserID: "user_a", AgentID: "agt_a"},
			Target:    agents.InteractionParty{UserID: "user_b", AgentID: "agt_b", UserEmail: "b@example.com", UserDisplayName: "Raya"},
			Purpose:   slackPurpose, Task: "Where are the dev keys?", RequestedPermissions: []string{PermissionConsentSummary, PermissionConsentAccess},
			Status: StatusApproved, Decision: decision, Revision: 2, ExpiresAt: at.Add(time.Hour), CreatedAt: at, UpdatedAt: at,
		}
	}
	for name, test := range map[string]struct {
		request agents.Interaction
		want    agentsv1.InteractionChanged
	}{
		"pending": {request(nil), agentsv1.InteractionChanged{AskID: "iak_1", Origin: "slack"}},
		"a click in Slack": {
			request(&agents.InteractionDecision{Decision: DecisionApproved, Consent: ConsentSummary, DecidedAt: at,
				DecidedBy: &agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b", Surface: SurfaceSlack}}),
			agentsv1.InteractionChanged{AskID: "iak_1", Origin: "slack", Consent: "summary",
				DecidedBy: &agentsv1.InteractionDecider{Kind: "member", UserID: "user_b", Surface: "slack"}},
		},
		"a lease": {
			request(&agents.InteractionDecision{Decision: DecisionApproved, Consent: ConsentAccess, DecidedAt: at,
				DecidedBy: &agents.InteractionDecider{Kind: DecidedByLease, UserID: "user_b", Surface: SurfaceLease, Reason: "ils_1"}}),
			agentsv1.InteractionChanged{AskID: "iak_1", Origin: "slack", Consent: "access",
				DecidedBy: &agentsv1.InteractionDecider{Kind: "lease", UserID: "user_b", Surface: "lease", Reason: "ils_1"}},
		},
		"a bypass": {
			request(&agents.InteractionDecision{Decision: DecisionApproved, Consent: ConsentAccess, DecidedAt: at,
				DecidedBy: &agents.InteractionDecider{Kind: DecidedByBypass, Surface: SurfaceBypass, Reason: "outranks"}}),
			agentsv1.InteractionChanged{AskID: "iak_1", Origin: "slack", Consent: "access",
				DecidedBy: &agentsv1.InteractionDecider{Kind: "bypass", Surface: "bypass", Reason: "outranks"}},
		},
	} {
		event := changed(test.request)
		if event.AskID != test.want.AskID || event.Origin != test.want.Origin || event.Consent != test.want.Consent ||
			(event.DecidedBy == nil) != (test.want.DecidedBy == nil) || (event.DecidedBy != nil && *event.DecidedBy != *test.want.DecidedBy) {
			t.Errorf("%s: event = %+v (decided by %+v), want %+v (%+v)", name, event, event.DecidedBy, test.want, test.want.DecidedBy)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, content := range []string{"dev keys", "Raya", "b@example.com", slackPurpose} {
			if strings.Contains(string(encoded), content) {
				t.Errorf("%s: the event carries %q: %s", name, content, encoded)
			}
		}
	}
}

// ConsentFor follows from what an approval granted: access beats a summary, a
// rejection has none, and an approval that granted neither, such as every one
// made before Slack asks, has none either.
func TestConsentFollowsFromWhatWasGranted(t *testing.T) {
	for name, test := range map[string]struct {
		decision string
		granted  []string
		want     string
	}{
		"access and a summary": {DecisionApproved, []string{PermissionConsentSummary, PermissionConsentAccess}, ConsentAccess},
		"access alone":         {DecisionApproved, []string{PermissionConsentAccess}, ConsentAccess},
		"access listed first":  {DecisionApproved, []string{PermissionConsentAccess, PermissionConsentSummary}, ConsentAccess},
		"a summary":            {DecisionApproved, []string{PermissionConsentSummary}, ConsentSummary},
		"another permission":   {DecisionApproved, []string{"read:architecture"}, ""},
		"nothing":              {DecisionApproved, nil, ""},
		"a rejection":          {DecisionRejected, []string{PermissionConsentAccess}, ""},
	} {
		if got := ConsentFor(test.decision, test.granted); got != test.want {
			t.Errorf("%s: ConsentFor = %q, want %q", name, got, test.want)
		}
	}
}

// The decision a choice makes is one validateDecision accepts of a request
// that asks for what the Slack app asks for, and grants no more than it asked.
func TestEveryChoiceIsADecisionTheLedgerAccepts(t *testing.T) {
	request := agents.Interaction{
		Status: StatusPending, RequestedTools: []string{"knowledge"},
		RequestedPermissions: []string{PermissionConsentSummary, PermissionConsentAccess},
	}
	if !asksConsent(request) {
		t.Fatal("a request for both consent permissions does not ask for consent")
	}
	for _, choice := range []string{ChoiceSummary, ChoiceAccess, ChoiceDeny} {
		input := consentInput(request, choice, time.Hour)
		if err := validateDecision(request, input); err != nil {
			t.Errorf("%s: %v", choice, err)
		}
		if (choice == ChoiceDeny) != (input.Decision == DecisionRejected) || len(input.Message) == 0 {
			t.Errorf("%s decides %+v", choice, input)
		}
	}
	for name, asks := range map[string]agents.Interaction{
		"an ordinary request":      {RequestedPermissions: []string{"read:architecture"}},
		"only a summary asked for": {RequestedPermissions: []string{PermissionConsentSummary}},
		"only access asked for":    {RequestedPermissions: []string{PermissionConsentAccess}},
		"nothing":                  {},
	} {
		if asksConsent(asks) {
			t.Errorf("%s asks for consent", name)
		}
	}
	for ttl, want := range map[time.Duration]string{
		time.Hour: "1 hour", 2 * time.Hour: "2 hours", 30 * time.Minute: "30 minutes", time.Minute: "1 minute",
		90 * time.Minute: "90 minutes", 10 * time.Second: "1 minute",
	} {
		if got := spellDuration(ttl); got != want {
			t.Errorf("spellDuration(%s) = %q, want %q", ttl, got, want)
		}
	}
}
