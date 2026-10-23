package application

import (
	"context"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
)

// compare asks SpiceDB in shadow what the code just decided about a
// connection (docs/design/permissions, step 4); the code's answer stands.
func (s *Service) compare(ctx context.Context, caller domain.OrgMembership, connection domain.Connection, action string, allowed bool) {
	if s.shadow == nil {
		return
	}
	actor := access.Actor{Role: access.Role(caller.Role), Agent: caller.Agent, AgentRole: caller.AgentRole}
	var apps []string
	if caller.AgentGrant != nil {
		apps = caller.AgentGrant.Apps
	}
	ask := access.Ask{
		OrganizationID: connection.OrganizationID, Service: "apps", Action: action,
		ResourceKind: "connection", ResourceID: connection.ID, UserID: caller.UserID,
		Actor: actor, Limited: actor.Limited(apps),
	}
	if caller.Agent {
		ask.AgentID = caller.AgentID
	}
	s.shadow.Compare(ctx, ask, allowed)
}
