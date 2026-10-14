package primary

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgemembers"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// mountEdgeMembers sets up the pusher that tells the edge who is in its
// rooms (live-layer A18). Without AGENTS_PRIMARY_EDGE_URL there is no edge,
// and none.
func mountEdgeMembers(settings serviceconfig.Config, platform host.Platform, teamspaceStore *teamspaces.Store, logger *slog.Logger) (*edgemembers.Pusher, error) {
	if settings.EdgeURL == "" {
		return nil, nil
	}
	if settings.ServiceKeyID == "" {
		return nil, fmt.Errorf("the edge's room members need AGENTS_PRIMARY_SERVICE_KEY_ID to sign edge tokens")
	}
	edge, err := edgeclient.New(edgeclient.Config{
		BaseURL: settings.EdgeURL,
		Tokens:  serviceauth.Signer{Client: kms.NewFromConfig(platform.AWS), KeyID: settings.ServiceKeyID, Service: Name},
	})
	if err != nil {
		return nil, fmt.Errorf("the edge client for room members: %w", err)
	}
	// A teamspace is bound to one human team; leaving the team leaves its
	// teamspaces' rooms.
	teamspacesOfTeam := func(ctx context.Context, organizationID, teamID string) ([]string, error) {
		spaces, err := teamspaceStore.List(ctx, organizationID)
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, space := range spaces {
			if space.TeamID == teamID {
				ids = append(ids, space.ID)
			}
		}
		return ids, nil
	}
	pusher, err := edgemembers.New(edge, edgemembers.NewStore(platform.Database), teamspacesOfTeam, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("rooms on the edge learn their members from here", "edge", settings.EdgeURL)
	return pusher, nil
}
