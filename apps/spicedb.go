package apps

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
)

// relationships writes a connection's place in SpiceDB the way the projector
// does from its events.
type relationships struct{ engine *projector.Engine }

func (r relationships) ConnectionStarted(ctx context.Context, connection domain.Connection) error {
	return r.engine.ConnectionOwned(ctx, connection.OrganizationID, connection.ID, connection.MemberID, time.Now())
}
