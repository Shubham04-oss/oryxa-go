package files

import (
	"context"

	"github.com/Shubham04-oss/oryxa-go/files/internal/application"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

// withEngine switches files to decide from SpiceDB when ACCESS_ENGINE_FILES
// says so (docs/design/permissions, step 5). It answers the engine's close,
// or nil when files stay on the code.
func withEngine(platform host.Platform, service *application.Service) (func(), error) {
	engine, err := projector.OpenEngine(platform, Name)
	if err != nil || engine == nil {
		return nil, err
	}
	service.WithEngine(engine.Decider, relationships{engine})
	return engine.Close, nil
}

// relationships writes a file's place in SpiceDB the way the projector does
// from its events.
type relationships struct{ engine *projector.Engine }

func (r relationships) FileCreated(ctx context.Context, object domain.Object) error {
	return r.engine.Placed(ctx, "file", object.ID, projector.Placement{Org: object.OrganizationID, Scope: string(object.Scope),
		Team: object.TeamID, Member: object.MemberID, Access: string(object.Access), CreatedBy: object.CreatedBy})
}

func (r relationships) FileDeleted(ctx context.Context, organizationID, fileID string) error {
	return r.engine.Deleted(ctx, "file", organizationID, fileID)
}
