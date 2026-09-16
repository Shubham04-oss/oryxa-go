package httpserver

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

// agentGrantStateCookieName is the double-submit cookie for the agent grant
// authorize/callback pair, scoped to the callback path only, the same
// technique login's own stateCookieName uses for /v1/auth/callback.
const agentGrantStateCookieName = "oryxa_agent_grant_state"

// AgentGrantsFlow issues and revokes the refresh token an agent uses to act
// for a member when the member is absent.
type AgentGrantsFlow interface {
	// Status returns the member's agent grant, or domain.ErrNotFound.
	Status(ctx context.Context, userID string) (domain.AgentGrant, error)
	// Authorize returns Cognito's authorization URL for the agent client and
	// the opaque state the caller must round-trip back as a cookie.
	Authorize(ctx context.Context, memberID, returnTo string) (authorizationURL, state string, err error)
	// Complete finishes the callback and returns the return_to the flow was
	// started with.
	Complete(ctx context.Context, state, stateCookie, code string) (returnTo string, err error)
	// Revoke revokes the member's grant. A missing grant is not an error.
	Revoke(ctx context.Context, userID string) error
	// MintToken exchanges the member's stored refresh token for a short-lived
	// access token.
	MintToken(ctx context.Context, userID string) (accessToken string, expiresIn int, err error)
}

func (server *Server) agentGrantStatus(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if server.config.AgentGrants == nil {
		writeError(writer, http.StatusNotFound, "agent grants are not configured")
		return
	}
	authenticated := requestAuth(request)
	grant, err := server.config.AgentGrants.Status(request.Context(), authenticated.Claims.Subject)
	if errors.Is(err, domain.ErrNotFound) {
		writeJSON(writer, http.StatusOK, map[string]any{"granted": false})
		return
	}
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"granted": true, "created_at": grant.CreatedAt})
}

func (server *Server) agentGrantAuthorize(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if server.config.AgentGrants == nil {
		writeError(writer, http.StatusNotFound, "agent grants are not configured")
		return
	}
	var input struct {
		ReturnTo string `json:"return_to"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	authenticated := requestAuth(request)
	authorizationURL, state, err := server.config.AgentGrants.Authorize(request.Context(), authenticated.Claims.Subject, input.ReturnTo)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name:     agentGrantStateCookieName,
		Value:    state,
		Path:     "/v1/agent-grant/callback",
		Domain:   server.config.CookieDomain,
		MaxAge:   600,
		HttpOnly: true,
		Secure:   server.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(writer, http.StatusOK, map[string]string{"authorization_url": authorizationURL})
}

func (server *Server) agentGrantCallback(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if server.config.AgentGrants == nil {
		writeError(writer, http.StatusNotFound, "agent grants are not configured")
		return
	}
	if providerError := request.URL.Query().Get("error"); providerError != "" {
		writeError(writer, http.StatusUnauthorized, "identity provider rejected the agent grant")
		return
	}
	stateCookie, err := request.Cookie(agentGrantStateCookieName)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "agent grant state cookie is missing")
		return
	}
	returnTo, err := server.config.AgentGrants.Complete(
		request.Context(),
		request.URL.Query().Get("state"),
		stateCookie.Value,
		request.URL.Query().Get("code"),
	)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	server.clearCookie(writer, agentGrantStateCookieName, "/v1/agent-grant/callback")
	http.Redirect(writer, request, returnTo, http.StatusSeeOther)
}

func (server *Server) agentGrantRevoke(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if server.config.AgentGrants == nil {
		writeError(writer, http.StatusNotFound, "agent grants are not configured")
		return
	}
	authenticated := requestAuth(request)
	if err := server.config.AgentGrants.Revoke(request.Context(), authenticated.Claims.Subject); err != nil {
		server.handleError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
