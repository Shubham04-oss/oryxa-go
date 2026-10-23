package interactions

import (
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
)

// A member sees who reached their things, as the owner, and what their own
// agents reached, as the asker, with the rule that allowed each.
func TestAnAccessDecisionIsShownFromTheMembersSide(t *testing.T) {
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	me := agents.InteractionActor{OrganizationID: "org_1", UserID: "u1"}

	theirs := access.Decision{At: at, ResourceKind: "file", ResourceID: "f1", OwnerID: "u1", ActorUserID: "u2",
		ActorAgentID: "agt_u2", Reason: access.ReasonTeamMember}
	entry := accessEntry(me, theirs)
	if entry.Kind != ActivityAccessed || entry.Role != ActivityOwner || entry.WithUserID != "u2" || entry.Access.Reason != access.ReasonTeamMember || !entry.At.Equal(at) {
		t.Fatalf("someone reaching my file = %+v", entry)
	}

	mine := access.Decision{At: at, ResourceKind: "corpus", ResourceID: "cor_1", ActorUserID: "u1",
		ActorAgentID: "agt_u1", ActorAgentRole: access.AgentSpecialist, Reason: access.ReasonGrant}
	entry = accessEntry(me, mine)
	if entry.Role != ActivityAsker || entry.WithUserID != "" || entry.Access.ActorAgentID != "agt_u1" {
		t.Fatalf("my specialist reaching the org's corpus = %+v", entry)
	}

	ownByAgent := access.Decision{At: at, ResourceKind: "file", ResourceID: "f2", OwnerID: "u1", ActorUserID: "u1",
		ActorAgentID: "agt_u1", Reason: access.ReasonOwner}
	if entry = accessEntry(me, ownByAgent); entry.Role != ActivityOwner || entry.WithUserID != "" {
		t.Fatalf("my agent reaching my own file = %+v", entry)
	}
}
