package httpserver

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/rooms"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// edgeParleyNotesPath is where the edge's voice agent leaves a parley's notes
// in the teamspace room the call was in, when the call ends. Like dispatch,
// it is under /v1/edge rather than an organization: the organization is the
// token's, never the path's.
const edgeParleyNotesPath = "POST /v1/edge/teamspaces/{teamspaceID}/parley-notes"

// edgeParleyNotes posts a call's notes in its room as the organization
// agent. Only an edge→agents token for that very room, carrying log:write,
// may: notes are written into the room's record, which is what that scope
// lets the edge do. The same call's notes sent again answer 200 with the
// message already posted, and nothing new is said.
func (server *Server) edgeParleyNotes(writer http.ResponseWriter, request *http.Request) {
	config := server.edge
	if config == nil || config.Tokens == nil || server.rooms == nil {
		writeError(writer, http.StatusNotFound, "not_found", "not found")
		return
	}
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, edgev1.AuthorizationScheme) {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "an edge token is required")
		return
	}
	claims, err := config.Tokens.Verify(strings.TrimSpace(strings.TrimPrefix(header, edgev1.AuthorizationScheme)))
	if err != nil {
		server.logger.WarnContext(request.Context(), "edge parley notes token refused", "error", err)
		writeError(writer, http.StatusUnauthorized, "unauthorized", "the edge token was refused")
		return
	}
	if !claims.HasScope(edgev1.ScopeLogWrite) {
		writeError(writer, http.StatusForbidden, "forbidden", "the edge token lacks log:write")
		return
	}
	room := edgev1.Room{Kind: edgev1.RoomTeamspace, ID: request.PathValue("teamspaceID")}
	if claims.Room != room {
		server.logger.WarnContext(request.Context(), "edge parley notes name another room than their token",
			"organization_id", claims.Org, "room_kind", string(claims.Room.Kind))
		writeError(writer, http.StatusForbidden, "forbidden", "the notes must be for the token's teamspace room")
		return
	}

	var notes rooms.ParleyNotes
	if !server.decode(writer, request, &notes) {
		return
	}
	message, created, err := server.rooms.PostParleyNotes(request.Context(), claims.Org, room.ID, notes)
	switch {
	case errors.Is(err, rooms.ErrRoomNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "no such teamspace")
	case err != nil:
		server.handleError(writer, request, err)
	case created:
		writeJSON(writer, http.StatusCreated, message)
	default:
		writeJSON(writer, http.StatusOK, message)
	}
}
