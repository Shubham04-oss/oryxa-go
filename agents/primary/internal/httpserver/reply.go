package httpserver

import (
	"errors"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
)

// replyRequest is an owner's own answer to a request.
type replyRequest struct {
	Text string `json:"text"`
}

// replyInteraction is the member answering a request addressed to them in their
// own words, rather than letting their agent:
//
//	POST /v1/organizations/{org}/agent-requests/{id}/reply {"text": "..."}
//
// It is the member's own call in their own session: a cookie needs an allowed
// origin, and an agent's token is refused. The text is trimmed, and holds 1 to
// 4000 characters (400 otherwise). A request that is pending is approved and
// answered with it at once, and their agent never runs for it; one whose answer
// is held for their review has that answer replaced by it. It answers 200 with
// the request as the member now reads it:
//
//	{"request": {...}}
//
// Whoever decides first wins: a reply, a choice or a review, here or in Slack.
// A reply that lost is 409 already_decided, carrying the winner as the consent
// route does:
//
//	{"error": {"code": "already_decided", "message": "..."}, "winner": {...}}
func (server *Server) replyInteraction(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	var body replyRequest
	if !server.decode(writer, request, &body) {
		return
	}
	replied, err := server.interactions.ReplyAsOwner(request.Context(), interactionActorOf(member), member.AccessToken, request.PathValue("requestID"), body.Text)
	if errors.Is(err, interactions.ErrAlreadyDecided) {
		writeJSON(writer, http.StatusConflict, map[string]any{
			"error":  map[string]string{"code": "already_decided", "message": message(err)},
			"winner": winnerOf(replied),
		})
		return
	}
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"request": replied})
}
