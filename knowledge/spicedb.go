package knowledge

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// relationships writes a source's place in SpiceDB as the reconcile does
// from the sources table.
type relationships struct{ engine *projector.Engine }

func (r relationships) SourceRegistered(ctx context.Context, source domain.Source) error {
	return r.engine.SourceRegistered(ctx, source.OrganizationID, source.ID, source.ConnectionID, source.CreatedBy, time.Now())
}
