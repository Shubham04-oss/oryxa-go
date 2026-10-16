package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgedispatch"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// edgeDispatchPath is where the voice agent at the edge hands over work
// (docs/design/live-layer/03, B5). It is not under /v1/internal, which the
// WAF blocks: the edge reaches it over the internet, with an edge→agents
// token (contract §4) in place of a member's session.
const edgeDispatchPath = "POST /v1/edge/dispatch"

// edgeRetryAfter is how long, in seconds, the edge waits to send a dispatch
// again while this process stops.
const edgeRetryAfter = "5"

// EdgeTokens verifies edge→agents tokens (*edgedispatch.Verifier).
type EdgeTokens interface {
	Verify(token string) (edgev1.TokenClaims, error)
}

// EdgeDispatcher starts the work a dispatch hands over (*edgedispatch.Service).
type EdgeDispatcher interface {
	Dispatch(ctx context.Context, request edgedispatch.Request) (edgedispatch.Accepted, error)
}

// EdgeDispatch enables the edge's dispatch route. Both are required. Its
// Tokens also admit the edge's parley notes, which need Config.Rooms too.
type EdgeDispatch struct {
	Tokens     EdgeTokens
	Dispatcher EdgeDispatcher
}

func (server *Server) routeEdge() {
	server.handle(edgeDispatchPath, server.edgeDispatch)
	server.handle(edgeParleyNotesPath, server.edgeParleyNotes)
}

// edgeDispatch takes one request the voice agent hands over, for the member
// and room its token names, and answers 202 once the run has started.
func (server *Server) edgeDispatch(writer http.ResponseWriter, request *http.Request) {
	config := server.edge
	if config == nil || config.Tokens == nil || config.Dispatcher == nil {
		// Not configured here: the route is as absent as any other.
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
		server.logger.WarnContext(request.Context(), "edge dispatch token refused", "error", err)
		writeError(writer, http.StatusUnauthorized, "unauthorized", "the edge token was refused")
		return
	}

	// The edge signs a dispatch's token for speaking in the call.
	if !claims.HasScope(edgev1.ScopeCallSpeak) {
		writeError(writer, http.StatusForbidden, "forbidden", "the edge token lacks call:speak")
		return
	}

	var body edgedispatch.Request
	if !server.decode(writer, request, &body) {
		return
	}
	// The token is the edge's word for whom and where; the body may not
	// name anyone or anywhere else.
	if body.Organization != claims.Org || body.Room != claims.Room || body.Member != claims.Member || body.Team != claims.Team {
		server.logger.WarnContext(request.Context(), "edge dispatch names another member or room than its token",
			"organization_id", claims.Org, "room_kind", string(claims.Room.Kind))
		writeError(writer, http.StatusForbidden, "forbidden", "the dispatch must be for the token's organization, room and member")
		return
	}

	accepted, err := config.Dispatcher.Dispatch(request.Context(), body)
	switch {
	case err == nil:
		writeJSON(writer, http.StatusAccepted, accepted)
	case errors.Is(err, edgedispatch.ErrInvalid):
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, edgedispatch.ErrNotMember):
		writeError(writer, http.StatusForbidden, "forbidden", "the member is not in this organization")
	case errors.Is(err, edgedispatch.ErrUnavailable):
		writer.Header().Set("Retry-After", edgeRetryAfter)
		writeError(writer, http.StatusServiceUnavailable, "unavailable", "agents is restarting; send the dispatch again")
	default:
		server.handleError(writer, request, err)
	}
}
