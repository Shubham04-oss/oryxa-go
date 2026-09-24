package teamspaces

import (
	"context"
	"fmt"
	"slices"
	"strings"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

// RequestSeat places the agent a cross-user request addresses in the
// teamspace the request is made in, against the live team directory, and
// reports the standing its owner gave it there. interactions.Service turns
// that into who approves the request, so nothing the requester sends decides
// it.
//
// The requester must be a current member of the teamspace's team, and the
// space and its organization agent must both be active, as for any agent
// participation. The target holds a seat when it is a teammate's primary
// agent, their whisperer, or an active specialist a current teammate
// contributed to the space; anything else is refused with ErrInvalid.
func (service *Service) RequestSeat(ctx context.Context, requester primaryagents.TeamspaceActor, token, id, targetAgentID string) (primaryagents.TeamspaceSeat, error) {
	space, err := service.Get(ctx, collaborationActor(requester, token), id)
	if err != nil {
		return primaryagents.TeamspaceSeat{}, err
	}
	if !space.IsMember {
		return primaryagents.TeamspaceSeat{}, fmt.Errorf("%w: only a current member of this teamspace's team can ask its agents for help there", ErrForbidden)
	}
	if !collaborationAvailable(space) {
		return primaryagents.TeamspaceSeat{}, ErrDisabled
	}
	targetAgentID = strings.TrimSpace(targetAgentID)
	for _, whisperer := range space.Whisperers {
		if whisperer.AgentID == targetAgentID {
			return primaryagents.TeamspaceSeat{
				AgentID: whisperer.AgentID, OwnerUserID: whisperer.UserID, Kind: string(managedagents.KindPrimary),
			}, nil
		}
	}
	for _, contribution := range space.Contributions {
		// A contribution outlives its owner's place on the team only until
		// Teams' removal event is handled, and gives no seat meanwhile.
		if contribution.AgentID == targetAgentID && contribution.Status == managedagents.StatusActive && onTeam(space, contribution.OwnerUserID) {
			return primaryagents.TeamspaceSeat{
				AgentID: contribution.AgentID, OwnerUserID: contribution.OwnerUserID,
				Kind: string(managedagents.KindSpecialist), DelegationMode: string(contribution.DelegationMode),
			}, nil
		}
	}
	return primaryagents.TeamspaceSeat{}, fmt.Errorf("%w: that agent has no seat in this teamspace; ask a teammate's primary agent or a specialist a teammate contributed here", ErrInvalid)
}

// onTeam is whether a member is on the space's team now: the whisperer
// roster is derived from the live directory on every read.
func onTeam(space Space, userID string) bool {
	return slices.ContainsFunc(space.Whisperers, func(whisperer Whisperer) bool { return whisperer.UserID == userID })
}
