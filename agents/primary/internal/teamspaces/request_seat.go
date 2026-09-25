package teamspaces

import (
	"context"
	"errors"
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

// The reasons a request made in a teamspace no longer stands there, as
// RequestStanding reports them.
const (
	lapsedSpaceGone      = "the teamspace it was made in no longer exists"
	lapsedSpaceDisabled  = "the teamspace it was made in, or its organization agent, is disabled"
	lapsedRequesterLeft  = "the member who asked is no longer on the teamspace's team"
	lapsedRequesterAgent = "the agent that asked no longer holds a seat in the teamspace"
	lapsedTargetSeat     = "the agent it asks no longer holds a seat in the teamspace"
)

// RequestStanding reads again, after a request was made, where it stands in
// the teamspace it was made in: what RequestSeat checked then, checked now.
// The teamspace must still exist and be active with its organization agent,
// the member who asked must still be on its team with the asking agent still
// seated, and the target must still hold its seat — whose standing, returned
// as the Seat, may have changed since.
//
// directory is the organization as the addressed member sees it, loaded
// with their token: holding a seat means being on the space's team, which
// the directory then shows in full, so a team missing from it is a target
// whose member has left. A request that no longer stands is reported as a
// Lapsed reason, not an error; the error is for failing to read at all.
func (service *Service) RequestStanding(ctx context.Context, organizationID string, directory primaryagents.OrganizationDirectory, id string, requester, target primaryagents.InteractionParty) (primaryagents.TeamspaceStanding, error) {
	space, err := service.repository.Get(ctx, organizationID, strings.TrimSpace(id))
	if errors.Is(err, ErrNotFound) {
		return primaryagents.TeamspaceStanding{Lapsed: lapsedSpaceGone}, nil
	}
	if err != nil {
		return primaryagents.TeamspaceStanding{}, err
	}
	access, err := accessIn(directory, requester.UserID, space.TeamID)
	if err != nil {
		return primaryagents.TeamspaceStanding{Lapsed: lapsedTargetSeat}, nil
	}
	space = decorate(space, access)
	switch {
	case !collaborationAvailable(space):
		return primaryagents.TeamspaceStanding{Lapsed: lapsedSpaceDisabled}, nil
	case !access.isMember:
		return primaryagents.TeamspaceStanding{Lapsed: lapsedRequesterLeft}, nil
	}
	if asking, ok := seatOf(space, requester.AgentID); !ok || asking.OwnerUserID != requester.UserID {
		return primaryagents.TeamspaceStanding{Lapsed: lapsedRequesterAgent}, nil
	}
	seated, ok := targetSeat(space, target.AgentID)
	if !ok || seated.OwnerUserID != target.UserID {
		return primaryagents.TeamspaceStanding{Lapsed: lapsedTargetSeat}, nil
	}
	return primaryagents.TeamspaceStanding{Seat: seated.TeamspaceSeat}, nil
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
