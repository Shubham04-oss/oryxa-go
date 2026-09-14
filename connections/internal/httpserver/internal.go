package httpserver

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/application"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// ServiceVerifier checks a signed service token naming connections as its
// audience. A nil ServiceVerifier means service tokens are not accepted.
type ServiceVerifier interface {
	Verify(context.Context, string) (serviceauth.Claims, error)
}

// actor resolves who is calling an internal route. Internal routes hand out
// provider credentials, so every call must come from a service that proves its
// identity with a signed token in serviceauth.Header: a member holding their
// own bearer token must never be able to pull a team or organization
// connection's raw token. A service carrying out a member's action also
// forwards that member's Authorization bearer token, so connections checks the
// member's access live. The session cookie is never read here, and neither the
// service token nor a member's credentials are logged.
func (server *Server) actor(writer http.ResponseWriter, request *http.Request) (application.Actor, bool) {
	token := request.Header.Get(serviceauth.Header)
	if token == "" {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "a service token is required")
		return application.Actor{}, false
	}
	if server.services == nil {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "service identity is not accepted")
		return application.Actor{}, false
	}
	claims, err := server.services.Verify(request.Context(), token)
	switch {
	case errors.Is(err, serviceauth.ErrPurposeNotAllowed):
		writeError(writer, http.StatusForbidden, "forbidden", "the service may not use this purpose")
		return application.Actor{}, false
	case errors.Is(err, serviceauth.ErrInvalidToken):
		writeError(writer, http.StatusUnauthorized, "unauthorized", "service token is invalid")
		return application.Actor{}, false
	case err != nil:
		server.logger.ErrorContext(request.Context(), "service identity could not be checked", "error", err)
		writeError(writer, http.StatusServiceUnavailable, "unavailable", "service identity could not be checked; retry")
		return application.Actor{}, false
	}
	actor := application.Actor{Service: claims.Service, Purpose: domain.Purpose(claims.Purpose)}
	if forwarded := request.Header.Get("Authorization"); forwarded != "" {
		if !isBearer(forwarded) {
			writeError(writer, http.StatusBadRequest, "invalid_request", "a forwarded member must be an Authorization bearer token")
			return application.Actor{}, false
		}
		actor.Credentials = authclient.Credentials{Authorization: forwarded}
	}
	return actor, true
}

// issueToken answers a credential for a connection to a service: for a
// member's action it forwards, or for work it runs on its own.
func (server *Server) issueToken(writer http.ResponseWriter, request *http.Request) {
	actor, ok := server.actor(writer, request)
	if !ok {
		return
	}
	var body struct {
		OrganizationID string   `json:"organization_id"`
		Purpose        string   `json:"purpose"`
		ActingMemberID string   `json:"acting_member_id"`
		Scopes         []string `json:"scopes"`
		Token          string   `json:"token"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	token, err := server.service.IssueToken(request.Context(), actor, request.PathValue("connectionID"), application.TokenRequest{
		OrganizationID: body.OrganizationID, Purpose: domain.Purpose(body.Purpose), ActingMemberID: body.ActingMemberID,
		Scopes: body.Scopes, Token: body.Token,
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	// A credential must never be cached by an intermediary.
	writer.Header().Set("Pragma", "no-cache")
	writeJSON(writer, http.StatusOK, token)
}

// internalConnection answers a connection's status, scope, owner, granted
// scopes and provider account for another service.
func (server *Server) internalConnection(writer http.ResponseWriter, request *http.Request) {
	actor, ok := server.actor(writer, request)
	if !ok {
		return
	}
	connection, err := server.service.InternalConnection(request.Context(), actor, request.URL.Query().Get("organization_id"), request.PathValue("connectionID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, connection)
}

// addHold keeps a connection's credentials past disconnect until the holder
// releases it.
func (server *Server) addHold(writer http.ResponseWriter, request *http.Request) {
	actor, ok := server.actor(writer, request)
	if !ok {
		return
	}
	var body struct {
		OrganizationID string `json:"organization_id"`
		Holder         string `json:"holder"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	if err := server.service.AddHold(request.Context(), actor, body.OrganizationID, request.PathValue("connectionID"), body.Holder); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// releaseHold releases a previously registered hold.
func (server *Server) releaseHold(writer http.ResponseWriter, request *http.Request) {
	actor, ok := server.actor(writer, request)
	if !ok {
		return
	}
	organizationID := request.URL.Query().Get("organization_id")
	if err := server.service.ReleaseHold(request.Context(), actor, organizationID, request.PathValue("connectionID"), request.PathValue("holder")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// report tells connections that a caller saw the provider reject a
// credential, triggering an immediate health check.
func (server *Server) report(writer http.ResponseWriter, request *http.Request) {
	actor, ok := server.actor(writer, request)
	if !ok {
		return
	}
	var body struct {
		OrganizationID string `json:"organization_id"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	if err := server.service.Report(request.Context(), actor, body.OrganizationID, request.PathValue("connectionID")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
