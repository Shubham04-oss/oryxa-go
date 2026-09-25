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
// directory is the organization as the requester sees it, which the request
// ledger has already loaded with the requester's own token to find the
// target: it is not loaded a second time.
//
// The requester must be a current member of the teamspace's team, the space
// and its organization agent must both be active, and the asking agent must
// itself hold a seat there — the requester's own whisperer, or a specialist
// they contributed — as for any agent participation (collaborationParticipant).
// The target holds a seat when it is a teammate's primary agent, their
// whisperer, or an active specialist a current teammate contributed to the
// space; anything else is refused with ErrInvalid.
func (service *Service) RequestSeat(ctx context.Context, requester primaryagents.TeamspaceActor, directory primaryagents.OrganizationDirectory, id, targetAgentID string) (primaryagents.TeamspaceSeat, error) {
	if strings.TrimSpace(requester.OrganizationID) == "" || strings.TrimSpace(requester.UserID) == "" {
		return primaryagents.TeamspaceSeat{}, fmt.Errorf("%w: organization and requester are required", ErrInvalid)
	}
	space, err := service.repository.Get(ctx, requester.OrganizationID, strings.TrimSpace(id))
	if err != nil {
		return primaryagents.TeamspaceSeat{}, err
	}
	access, err := accessIn(directory, requester.UserID, space.TeamID)
	if err != nil {
		return primaryagents.TeamspaceSeat{}, err
	}
	if !access.isMember && !access.canManage {
		return primaryagents.TeamspaceSeat{}, ErrNotFound
	}
	space = decorate(space, access)
	if !space.IsMember {
		return primaryagents.TeamspaceSeat{}, fmt.Errorf("%w: only a current member of this teamspace's team can ask its agents for help there", ErrForbidden)
	}
	if !collaborationAvailable(space) {
		return primaryagents.TeamspaceSeat{}, ErrDisabled
	}
	if !collaborationParticipant(space, requester) {
		return primaryagents.TeamspaceSeat{}, fmt.Errorf("%w: only an agent with a seat in this teamspace can ask for help there: your primary agent, or a specialist you contributed to it", ErrForbidden)
	}
	seat, ok := targetSeat(space, strings.TrimSpace(targetAgentID))
	if !ok {
		return primaryagents.TeamspaceSeat{}, fmt.Errorf("%w: that agent has no seat in this teamspace; ask a teammate's primary agent or a specialist a teammate contributed here", ErrInvalid)
	}
	return seat.TeamspaceSeat, nil
}

// seat is where one agent sits in a space, with the name the space shows.
type seat struct {
	primaryagents.TeamspaceSeat
	name string
}

// seatOf is the one walk every seat check shares: where agentID sits in the
// space, whether in its owner's whisperer seat, as a teammate's primary
// agent, or as an active specialist contributed to it. Each caller adds its
// own rule for whose seat it must be; agent ids are unique, and a
// contribution is always a specialist, so the first match is the only one.
func seatOf(space Space, agentID string) (seat, bool) {
	for _, whisperer := range space.Whisperers {
		if whisperer.AgentID == agentID {
			return seat{TeamspaceSeat: primaryagents.TeamspaceSeat{
				AgentID: whisperer.AgentID, OwnerUserID: whisperer.UserID, Kind: string(managedagents.KindPrimary),
			}, name: whisperer.AgentName}, true
		}
	}
	for _, contribution := range space.Contributions {
		if contribution.AgentID == agentID && contribution.Status == managedagents.StatusActive {
			return seat{TeamspaceSeat: primaryagents.TeamspaceSeat{
				AgentID: contribution.AgentID, OwnerUserID: contribution.OwnerUserID,
				Kind: string(managedagents.KindSpecialist), DelegationMode: string(contribution.DelegationMode),
			}, name: contribution.Name}, true
		}
	}
	return seat{}, false
}

// targetSeat is the seat a request's target holds: seatOf, except that a
// contribution outlives its owner's place on the team only until Teams'
// removal event is handled, and gives no seat meanwhile.
func targetSeat(space Space, agentID string) (seat, bool) {
	found, ok := seatOf(space, agentID)
	if !ok || found.Kind == string(managedagents.KindSpecialist) && !onTeam(space, found.OwnerUserID) {
		return seat{}, false
	}
	return found, true
}

// onTeam is whether a member is on the space's team now: the whisperer
// roster is derived from the live directory on every read.
func onTeam(space Space, userID string) bool {
	return slices.ContainsFunc(space.Whisperers, func(whisperer Whisperer) bool { return whisperer.UserID == userID })
}
