package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
)

// Relationships writes a connection's relationships to SpiceDB as it starts,
// so its member can see it before the projector writes it from the event. A
// removed connection keeps them: apps still shows it, marked removed.
type Relationships interface {
	ConnectionStarted(ctx context.Context, connection domain.Connection) error
}

func (s *Service) connectionStarted(ctx context.Context, connection domain.Connection) {
	if s.relationships == nil {
		return
	}
	// The connection stands either way: its event brings the projector too.
	if err := s.relationships.ConnectionStarted(ctx, connection); err != nil {
		s.logger.WarnContext(ctx, "connection relationships not written to spicedb; the projector will",
			"connection_id", connection.ID, "error", err)
	}
}

// decide returns nil when the caller may do action on connection, given the
// code's answer, and domain.ErrNotFound when not. With no engine the code's
// answer stands and SpiceDB is asked in shadow (docs/design/permissions, step
// 4). With one, SpiceDB decides but may only narrow, as in files, rag and
// knowledge: the caller gets in only when both allow. Where SpiceDB has no
// answer (the org agent) the code's answer stands, and a SpiceDB failure
// refuses.
func (s *Service) decide(ctx context.Context, caller domain.OrgMembership, connection domain.Connection, action string, allowed bool) error {
	code := error(nil)
	if !allowed {
		code = domain.ErrNotFound
	}
	if s.engine == nil {
		if s.shadow != nil {
			s.shadow.Compare(ctx, ask(caller, connection, action), allowed)
		}
		return code
	}
	answer, err := s.engine.Decide(ctx, ask(caller, connection, action), allowed)
	switch {
	case errors.Is(err, access.ErrNoAnswer):
		return code
	case err != nil:
		return fmt.Errorf("decide access to connection %s: %w", connection.ID, err)
	case !allowed || !answer:
		return domain.ErrNotFound
	default:
		return nil
	}
}

// ask is a decision on a connection in the terms SpiceDB is asked about it.
func ask(caller domain.OrgMembership, connection domain.Connection, action string) access.Ask {
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
	return ask
}
