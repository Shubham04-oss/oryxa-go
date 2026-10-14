package httpserver

import (
	"context"
	"net/http"
	"slices"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgemembers"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// edgeAdmitPath is where web-next asks, as the member, to be let into a room
// on the edge before it opens it there (live-layer A18). Go checks the
// member may be in the room the way its own routes do, then tells the edge.
const edgeAdmitPath = "POST /v1/organizations/{organizationID}/edge/rooms/{kind}/{id}/admit"

// EdgeMembers admits a member the server has checked (*edgemembers.Pusher).
type EdgeMembers interface {
	Admit(context.Context, edgemembers.Membership) error
}

func (server *Server) routeEdgeMembers() {
	server.handle(edgeAdmitPath, server.edgeAdmit)
}

// edgeAdmit lets the signed-in member into a room on the edge when they may
// be in it: a thread they own, a teamspace they can see, or a direct message
// or side conversation whose id names them (and, for a side conversation,
// whose teamspace they can see). Anything else is refused as not found, so
// a room's existence isn't confirmed to someone who can't be in it.
func (server *Server) edgeAdmit(writer http.ResponseWriter, request *http.Request) {
	if server.edgeMembers == nil {
		writeError(writer, http.StatusNotFound, "not_found", "not found")
		return
	}
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	room := edgev1.Room{Kind: edgev1.RoomKind(request.PathValue("kind")), ID: request.PathValue("id")}
	if room.Validate() != nil {
		writeError(writer, http.StatusNotFound, "not_found", "no such room")
		return
	}
	ctx := request.Context()
	membership := edgemembers.Membership{Org: member.OrganizationID, Room: room, Member: member.UserID}

	notFound := func() { writeError(writer, http.StatusNotFound, "not_found", "no such room") }
	seesTeamspace := func(id string) bool {
		if server.teamspaces == nil {
			return false
		}
		_, err := server.teamspaces.Get(ctx, teamspaceActorOf(member), id)
		return err == nil
	}
	switch room.Kind {
	case edgev1.RoomThread:
		if _, err := server.threads.Get(ctx, member, room.ID); err != nil {
			notFound()
			return
		}
	case edgev1.RoomTeamspace:
		if !seesTeamspace(room.ID) {
			notFound()
			return
		}
		membership.TeamspaceID = room.ID
	case edgev1.RoomDM:
		members, err := edgev1.ParseDirectRoomID(room.ID)
		if err != nil || !slices.Contains(members[:], member.UserID) {
			notFound()
			return
		}
	case edgev1.RoomSide:
		teamspace, members, err := edgev1.ParseSideRoomID(room.ID)
		if err != nil || !slices.Contains(members[:], member.UserID) || !seesTeamspace(teamspace) {
			notFound()
			return
		}
		membership.TeamspaceID = teamspace
	}

	if err := server.edgeMembers.Admit(ctx, membership); err != nil {
		server.logger.WarnContext(ctx, "admit a member to an edge room", "organization_id", member.OrganizationID, "room_kind", string(room.Kind), "error", err)
		writeError(writer, http.StatusServiceUnavailable, "edge_unavailable", "the room could not be opened; try again")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
