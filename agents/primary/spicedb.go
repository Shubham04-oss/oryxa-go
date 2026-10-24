package primary

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
)

// agentRelationships writes an agent and its grants to SpiceDB the way the
// projector does after the agent's events: its place, then its grants read
// back from every source they name.
type agentRelationships struct{ engine *projector.Engine }

func (r agentRelationships) AgentChanged(ctx context.Context, agent managedagents.Definition) error {
	change, err := projector.AgentChanged(agent.OrganizationID, agent.ID, agent.OwnerUserID)
	if err != nil {
		return err
	}
	if err := r.engine.Apply(ctx, change, time.Now()); err != nil {
		return err
	}
	return r.engine.SyncGrants(ctx, agent.OrganizationID, agent.ID)
}

func (r agentRelationships) AgentDeleted(ctx context.Context, organizationID, agentID string) error {
	change, err := projector.AgentDeleted(organizationID, agentID)
	if err != nil {
		return err
	}
	return r.engine.Apply(ctx, change, time.Now())
}
