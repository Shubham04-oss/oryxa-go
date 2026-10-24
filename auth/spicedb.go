package auth

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
)

// relationships writes org roles to SpiceDB the way the projector does from
// auth's events.
type relationships struct{ engine *projector.Engine }

func (r relationships) apply(ctx context.Context, change projector.Change, err error) error {
	if err != nil {
		return err
	}
	return r.engine.Apply(ctx, change, time.Now())
}

func (r relationships) MemberJoined(ctx context.Context, organizationID, userID string, role domain.Role) error {
	change, err := projector.MemberJoined(organizationID, userID, string(role))
	return r.apply(ctx, change, err)
}

func (r relationships) MemberRoleChanged(ctx context.Context, organizationID, userID string, role domain.Role) error {
	change, err := projector.MemberRoleChanged(organizationID, userID, string(role))
	return r.apply(ctx, change, err)
}

func (r relationships) MemberRemoved(ctx context.Context, organizationID, userID string) error {
	change, err := projector.MemberRemoved(organizationID, userID)
	return r.apply(ctx, change, err)
}

func (r relationships) OrganizationDeleted(ctx context.Context, organizationID string) error {
	change, err := projector.OrganizationDeleted(organizationID)
	return r.apply(ctx, change, err)
}
