package httpserver

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// agentServicePurpose is the only serviceauth purpose the internal
// token-mint route accepts, regardless of what other purposes the calling
// service may be configured with.
const agentServicePurpose = "agent"

// ServiceVerifier checks a signed service token naming auth as its
// audience. A nil ServiceVerifier means service tokens are not accepted.
type ServiceVerifier interface {
	Verify(context.Context, string) (serviceauth.Claims, error)
}

// mintAgentToken exchanges a member's stored agent grant for a short-lived
// access token, for a service that acts as an agent while the member is
// absent. The caller proves its identity with a signed token in
// serviceauth.Header, exactly as connections verifies its own callers.
func (server *Server) mintAgentToken(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if server.config.AgentGrants == nil {
		writeError(writer, http.StatusNotFound, "agent grants are not configured")
		return
	}
	token := request.Header.Get(serviceauth.Header)
	if token == "" {
		writeError(writer, http.StatusUnauthorized, "a service token is required")
		return
	}
	if server.config.Services == nil {
		writeError(writer, http.StatusUnauthorized, "service identity is not accepted")
		return
	}
	claims, err := server.config.Services.Verify(request.Context(), token)
	switch {
	case errors.Is(err, serviceauth.ErrPurposeNotAllowed):
		writeError(writer, http.StatusForbidden, "the service may not use this purpose")
		return
	case errors.Is(err, serviceauth.ErrInvalidToken):
		writeError(writer, http.StatusUnauthorized, "service token is invalid")
		return
	case err != nil:
		log.Printf("verify agent token service caller: %v", err)
		writeError(writer, http.StatusServiceUnavailable, "service identity could not be checked; retry")
		return
	}
	if claims.Purpose != agentServicePurpose {
		writeError(writer, http.StatusForbidden, "the service may not use this purpose")
		return
	}

	var input struct {
		UserID string `json:"user_id"`
	}
	if err := readJSON(request, &input); err != nil || strings.TrimSpace(input.UserID) == "" {
		writeError(writer, http.StatusBadRequest, "user_id is required")
		return
	}

	accessToken, expiresIn, err := server.config.AgentGrants.MintToken(request.Context(), input.UserID)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"access_token": accessToken, "expires_in": expiresIn})
}
