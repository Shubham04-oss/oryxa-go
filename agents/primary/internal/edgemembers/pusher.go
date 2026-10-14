// Package edgemembers keeps rooms' members on the oryxa-edge Worker in step
// with Go (live-layer A18): Go is the source of truth for who is in a room,
// and the edge refuses anyone it hasn't been told about.
//
// A member is admitted when they open a room and Go has checked they may
// (the admit route in httpserver), and removed from every room they were
// admitted to when they leave the room's team or the organization (the bus
// events the inbound consumer hands here). What was pushed is kept in
// PostgreSQL, so a removal knows which rooms to tell.
package edgemembers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// pushTimeout bounds one membership change on the edge.
const pushTimeout = 15 * time.Second

// pusherAgent names the pusher in the edge's log, as the actor of a
// membership change.
const pusherAgent = "edgemembers"

// Caller is the edge API; *edgeclient.Client satisfies it.
type Caller interface {
	Do(context.Context, edgeclient.Call, any) error
}

// Memberships is what Pusher keeps; *Store satisfies it.
type Memberships interface {
	Record(context.Context, Membership) error
	Forget(context.Context, Membership) error
	Rooms(ctx context.Context, org, member string, teamspaceIDs []string) ([]Membership, error)
	ForgetOrganization(ctx context.Context, org string) error
}

// TeamspacesOfTeam lists the ids of the teamspaces bound to a human team.
type TeamspacesOfTeam func(ctx context.Context, org, teamID string) ([]string, error)

// Pusher sets rooms' members on the edge.
type Pusher struct {
	edge       Caller
	store      Memberships
	teamspaces TeamspacesOfTeam
	logger     *slog.Logger
}

// New returns a Pusher. teamspaces may be nil when teamspaces aren't
// configured; then leaving a team removes nobody from teamspace rooms.
func New(edge Caller, store Memberships, teamspaces TeamspacesOfTeam, logger *slog.Logger) (*Pusher, error) {
	if edge == nil || store == nil {
		return nil, errors.New("edgemembers: an edge client and a store are required")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Pusher{edge: edge, store: store, teamspaces: teamspaces, logger: logger}, nil
}

// push sends one membership change to a room.
func (pusher *Pusher) push(ctx context.Context, org string, room edgev1.Room, change edgev1.MembersRequest) error {
	path, err := edgev1.RoomPath(room)
	if err != nil {
		return err
	}
	return pusher.edge.Do(ctx, edgeclient.Call{
		Claims: edgev1.TokenClaims{Org: org, Room: room, Agent: pusherAgent, Scopes: []edgev1.Scope{edgev1.ScopeRoomMembers}},
		Method: http.MethodPut, Path: path + "/members", Body: change,
		Timeout: pushTimeout, Idempotent: true,
	}, &edgev1.MembersResponse{})
}

// Admit makes membership's member one of its room's members on the edge,
// and remembers it. The caller has checked that they may be; this trusts it.
func (pusher *Pusher) Admit(ctx context.Context, membership Membership) error {
	if !edgev1.ValidID(membership.Org) || membership.Member == "" {
		return errors.New("edgemembers: a membership needs an organization and a member")
	}
	if err := membership.Room.Validate(); err != nil {
		return err
	}
	// Remembered first: a removal racing this admission then finds the room,
	// and the worst case is a removal pushed for a member never added.
	if err := pusher.store.Record(ctx, membership); err != nil {
		return err
	}
	if err := pusher.push(ctx, membership.Org, membership.Room, edgev1.MembersRequest{Add: []string{membership.Member}}); err != nil {
		return fmt.Errorf("admit a member to the edge room: %w", err)
	}
	return nil
}

// remove takes member out of each room in memberships, forgetting each one
// the edge confirmed. It tries them all and reports every failure, so a
// redelivered event finishes what this one couldn't.
func (pusher *Pusher) remove(ctx context.Context, memberships []Membership, why string) error {
	var failures []error
	for _, membership := range memberships {
		if err := pusher.push(ctx, membership.Org, membership.Room, edgev1.MembersRequest{Remove: []string{membership.Member}}); err != nil {
			failures = append(failures, fmt.Errorf("remove a member from edge room %s/%s: %w", membership.Room.Kind, membership.Room.ID, err))
			continue
		}
		if err := pusher.store.Forget(ctx, membership); err != nil {
			failures = append(failures, err)
		}
	}
	if len(memberships) > 0 {
		pusher.logger.InfoContext(ctx, "removed a member from edge rooms", "organization_id", memberships[0].Org,
			"rooms", len(memberships), "failed", len(failures), "why", why)
	}
	return errors.Join(failures...)
}

// MemberLeftTeam removes member from the rooms of every teamspace bound to
// team: its room and its side conversations.
func (pusher *Pusher) MemberLeftTeam(ctx context.Context, org, teamID, member string) error {
	if pusher.teamspaces == nil {
		return nil
	}
	ids, err := pusher.teamspaces(ctx, org, teamID)
	if err != nil {
		return fmt.Errorf("find the team's teamspaces: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	memberships, err := pusher.store.Rooms(ctx, org, member, ids)
	if err != nil {
		return err
	}
	return pusher.remove(ctx, memberships, "left the team")
}

// MemberLeftOrganization removes member from every room they were admitted
// to in org.
func (pusher *Pusher) MemberLeftOrganization(ctx context.Context, org, member string) error {
	memberships, err := pusher.store.Rooms(ctx, org, member, nil)
	if err != nil {
		return err
	}
	return pusher.remove(ctx, memberships, "left the organization")
}

// Forget is the inbound consumer's forgetter for auth's events: a member
// who left the organization leaves every room, and an organization deleted
// (no member) is forgotten.
func (pusher *Pusher) Forget(ctx context.Context, org, member string) error {
	if member == "" {
		return pusher.OrganizationDeleted(ctx, org)
	}
	return pusher.MemberLeftOrganization(ctx, org, member)
}

// OrganizationDeleted forgets the organization's memberships. Its rooms are
// unreachable once it is gone: no token is issued for it and no session
// names it.
func (pusher *Pusher) OrganizationDeleted(ctx context.Context, org string) error {
	return pusher.store.ForgetOrganization(ctx, org)
}
