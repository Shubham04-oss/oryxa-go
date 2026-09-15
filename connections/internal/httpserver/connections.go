package httpserver

import (
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/application"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// createConnection makes a key-based or client-credentials connection
// without a provider redirect.
func (server *Server) createConnection(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		AppID      string                 `json:"app_id"`
		Scope      string                 `json:"scope"`
		TeamID     string                 `json:"team_id"`
		Usage      string                 `json:"usage"`
		Label      string                 `json:"label"`
		Credential domain.CredentialInput `json:"credential"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	connection, err := server.service.CreateConnection(request.Context(), credentials, request.PathValue("organizationID"), application.CreateConnectionInput{
		AppID: body.AppID, Scope: domain.Scope(body.Scope), TeamID: body.TeamID, Usage: domain.Usage(body.Usage),
		Label: body.Label, Credential: body.Credential,
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, connection)
}

// updateConnection changes a connection's label or default flag. A body
// naming neither is rejected: there is nothing to do.
func (server *Server) updateConnection(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		Label   *string `json:"label"`
		Default *bool   `json:"default"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	if body.Label == nil && body.Default == nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "label or default is required")
		return
	}
	connection, err := server.service.UpdateConnection(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("connectionID"),
		application.UpdateConnectionInput{Label: body.Label, Default: body.Default})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, connection)
}

// replaceCredential replaces a key-based connection's secret.
func (server *Server) replaceCredential(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		Credential domain.CredentialInput `json:"credential"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	connection, err := server.service.ReplaceCredential(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("connectionID"), body.Credential)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, connection)
}
