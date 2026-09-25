package v1

import (
	"encoding/json"
	"testing"
	"time"
)

// A request's events say where it was made, who decides it and who did, and
// leave each out when there is nothing to say, so what an older build
// published still reads the same.
func TestInteractionChangedCarriesItsTeamspaceApprovalAndDecider(t *testing.T) {
	at := time.Date(2026, time.September, 25, 9, 30, 0, 0, time.UTC)
	base := InteractionChanged{
		RequestID: "irq_1", OrganizationID: "org_1", RequesterUserID: "user_a", RequesterAgentID: "agt_a",
		TargetUserID: "user_b", TargetAgentID: "agt_b", Status: "approved", Revision: 2, ChangedAt: at,
	}
	const prefix = `{"request_id":"irq_1","organization_id":"org_1","requester_user_id":"user_a","requester_agent_id":"agt_a",` +
		`"target_user_id":"user_b","target_agent_id":"agt_b","status":"approved","revision":2,"changed_at":"2026-09-25T09:30:00Z"`
	withAll := base
	withAll.TeamspaceID, withAll.Approval = "tsp_1", InteractionApprovalOwner
	withAll.DecidedBy = &InteractionDecider{Kind: DecidedByMember, UserID: "user_b"}
	byAgent := base
	byAgent.Approval = InteractionApprovalAgent
	byAgent.DecidedBy = &InteractionDecider{Kind: DecidedByAgent, UserID: "user_b", AgentID: "agt_b"}
	bySystem := base
	bySystem.TeamspaceID, bySystem.Approval = "tsp_1", InteractionApprovalOwner
	bySystem.DecidedBy = &InteractionDecider{Kind: DecidedBySystem}

	for name, test := range map[string]struct {
		value InteractionChanged
		want  string
	}{
		"as an older build published it": {base, prefix + `}`},
		"decided by its member in a teamspace": {withAll,
			prefix + `,"teamspace_id":"tsp_1","approval":"owner","decided_by":{"kind":"member","user_id":"user_b"}}`},
		"decided by an agent outside a teamspace": {byAgent,
			prefix + `,"approval":"agent","decided_by":{"kind":"agent","user_id":"user_b","agent_id":"agt_b"}}`},
		"closed by Oryxa": {bySystem,
			prefix + `,"teamspace_id":"tsp_1","approval":"owner","decided_by":{"kind":"system"}}`},
	} {
		encoded, err := json.Marshal(test.value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != test.want {
			t.Errorf("%s:\n got %s\nwant %s", name, encoded, test.want)
		}
		// A consumer built before the fields reads the event as it always
		// has, and one built after reads them back.
		var decoded InteractionChanged
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.TeamspaceID != test.value.TeamspaceID || decoded.Approval != test.value.Approval ||
			(decoded.DecidedBy == nil) != (test.value.DecidedBy == nil) ||
			(decoded.DecidedBy != nil && *decoded.DecidedBy != *test.value.DecidedBy) {
			t.Errorf("%s: read back as %+v", name, decoded)
		}
	}
}
