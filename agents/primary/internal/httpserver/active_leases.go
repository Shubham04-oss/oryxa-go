package httpserver

import (
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
)

// activeLeases reads live windows from their ledger, rather than inferring
// coverage from the member's bounded activity history. Only their own
// signed-in membership determines who and which workspace is read.
func (server *Server) activeLeases(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	limit, ok := server.limit(writer, request)
	if !ok {
		return
	}
	page, err := server.interactions.ActiveLeases(request.Context(), interactionActorOf(member), interactions.LeaseQuery{
		Limit: limit, Cursor: request.URL.Query().Get("cursor"),
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}
