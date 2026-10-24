package files

import (
	"context"
	"errors"

	"github.com/Shubham04-oss/oryxa-go/files/internal/application"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector/tokens"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

// withEngine switches files to decide from SpiceDB when ACCESS_ENGINE_FILES
// says so (docs/design/permissions, step 5). It answers the client's close,
// or nil when files stay on the code.
func withEngine(platform host.Platform, service *application.Service) (func(), error) {
	engine, err := access.Engine(platform.Getenv, Name)
	if err != nil || engine != access.EngineSpiceDB {
		return nil, err
	}
	decider, ok := platform.Shadow.(access.Decider)
	if !ok || decider == nil {
		return nil, errors.New("ACCESS_ENGINE_FILES=spicedb needs SpiceDB: set SPICEDB_ENDPOINT")
	}
	client, err := spicedb.Dial(spicedb.FromEnv())
	if err != nil {
		return nil, err
	}
	service.WithEngine(decider, relationships{projector.NewWriter(client, tokens.New(platform.Database.Pool))})
	platform.Logger.Info("files decide access from spicedb")
	return func() { _ = client.Close() }, nil
}

// relationships writes a file's place in SpiceDB the way the projector does
// from its events.
type relationships struct{ writer *projector.Writer }

func (r relationships) FileCreated(ctx context.Context, object domain.Object) error {
	change, err := projector.Placed("file", object.ID, projector.Placement{Org: object.OrganizationID, Scope: string(object.Scope),
		Team: object.TeamID, Member: object.MemberID, Access: string(object.Access), CreatedBy: object.CreatedBy})
	if err != nil {
		return err
	}
	_, err = r.writer.Apply(ctx, change)
	return err
}

func (r relationships) FileDeleted(ctx context.Context, organizationID, fileID string) error {
	change, err := projector.Deleted("file", organizationID, fileID)
	if err != nil {
		return err
	}
	_, err = r.writer.Apply(ctx, change)
	return err
}
