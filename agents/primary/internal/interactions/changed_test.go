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
