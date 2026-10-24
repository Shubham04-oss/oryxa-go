package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

// Relationships writes teams and their members to SpiceDB as they change,
// sooner than the projector writes them from the events. A team's parent is
// only known here: its event doesn't carry it.
type Relationships interface {
	TeamCreated(ctx context.Context, team domain.Team, parentID string) error
	TeamsDeleted(ctx context.Context, organizationID string, teamIDs []string) error
	MemberSet(ctx context.Context, organizationID, teamID, userID string, role domain.Role) error
	MemberRemoved(ctx context.Context, organizationID, teamID, userID string) error
}

// WithEngine makes SpiceDB decide who sees and manages a team, and writes
// teams and members to it as they change.
func (s *Service) WithEngine(engine access.Decider, relationships Relationships) *Service {
	s.engine, s.relationships = engine, relationships
	return s
}

// wrote logs a relationships write that failed; the change stands either way,
// and its event brings the projector too.
func (s *Service) wrote(ctx context.Context, what string, err error) {
	if err != nil {
		s.logger.WarnContext(ctx, what+" not written to spicedb; the projector will", "error", err)
	}
}

// decide is what the caller gets for action on team, read being seeing it and
// write managing it, given the code's answer. With no engine the code's
// answer stands and SpiceDB is asked in shadow. With one, SpiceDB decides but
// may only narrow, as in the other services: the caller gets it only when both
// allow. Where SpiceDB has no answer the code's stands; a SpiceDB failure is an
// error, and the caller refuses.
func (s *Service) decide(ctx context.Context, org domain.OrgMembership, team domain.Team, action string, allowed bool) (bool, error) {
	ask := teamAsk(org, team, action)
	if s.engine == nil {
		if s.shadow != nil {
			s.shadow.Compare(ctx, ask, allowed)
		}
		return allowed, nil
	}
	answer, err := s.engine.Decide(ctx, ask, allowed)
	switch {
	case errors.Is(err, access.ErrNoAnswer):
		return allowed, nil
	case err != nil:
		return false, fmt.Errorf("decide access to team %s: %w", team.ID, err)
	}
	return allowed && answer, nil
}

// decideMany is decide for a list, with one round trip to SpiceDB for all of
// asks: it answers what the caller gets for each.
func (s *Service) decideMany(ctx context.Context, asks []access.Ask, allowed []bool) ([]bool, error) {
	if s.engine == nil {
		if s.shadow != nil {
			for i, ask := range asks {
				s.shadow.Compare(ctx, ask, allowed[i])
			}
		}
		return allowed, nil
	}
	allowed, err := s.engine.DecideMany(ctx, asks, allowed)
	if err != nil {
		return nil, fmt.Errorf("decide access to %d teams: %w", len(asks), err)
	}
	return allowed, nil
}

// teamAsk is a decision on team in the terms SpiceDB is asked about it: read
// is seeing it, write is managing it.
func teamAsk(org domain.OrgMembership, team domain.Team, action string) access.Ask {
	// ponytail: teams' membership carries no agent id or role, so an agent
	// is asked as a primary; specialists aren't narrowed on teams today.
	return access.Ask{
		OrganizationID: team.OrganizationID, Service: "teams", Action: action,
		ResourceKind: "team", ResourceID: team.ID, UserID: org.UserID,
		Actor: access.Actor{Role: access.Role(org.Role), Agent: org.Agent},
	}
}
