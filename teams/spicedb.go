package teams

import (
	"context"
	"errors"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

// relationships writes teams and their members to SpiceDB the way the
// projector does from their events, and a team's parent, which the events
// don't carry.
type relationships struct{ engine *projector.Engine }

func (r relationships) apply(ctx context.Context) func(projector.Change, error) error {
	return func(change projector.Change, err error) error {
		if err != nil {
			return err
		}
		return r.engine.Apply(ctx, change, time.Now())
	}
}

func (r relationships) TeamCreated(ctx context.Context, team domain.Team, parentID string) error {
	return r.apply(ctx)(projector.TeamPlaced(team.OrganizationID, team.ID, parentID))
}

func (r relationships) TeamsDeleted(ctx context.Context, organizationID string, teamIDs []string) error {
	var errs []error
	for _, id := range teamIDs {
		errs = append(errs, r.apply(ctx)(projector.TeamDeleted(organizationID, id)))
	}
	return errors.Join(errs...)
}

func (r relationships) MemberSet(ctx context.Context, organizationID, teamID, userID string, role domain.Role) error {
	return r.apply(ctx)(projector.TeamMemberSet(organizationID, teamID, userID, string(role)))
}

func (r relationships) MemberRemoved(ctx context.Context, organizationID, teamID, userID string) error {
	return r.apply(ctx)(projector.TeamMemberRemoved(organizationID, teamID, userID))
}
