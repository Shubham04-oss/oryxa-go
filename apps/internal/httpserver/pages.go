package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
)

// Knowledge reads a member's Oryxa connections through this route, one page
// at a time, with a member token of its own as the files source already has.
// It is the only way an Oryxa connection's documents are read: the read proxy
// is Composio's, and Composio holds no credential for such a connection.
//
// The body is an integration.PageRequest and the answer an integration.Page,
// one for one with knowledge's own sources.Page. Both are bounded by
// integration.MaxPageBytes, which is a page of documents rather than a tool
// argument: an item is often a whole file.

// pages answers one page of one target of one of the caller's own
// connections. It is a member route, so the member is asked who they are and
// the connection's owner is checked again in the application.
func (server *Server) pages(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body integration.PageRequest
	if !server.decodePage(writer, request, &body) {
		return
	}
	page, err := server.service.Pages(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("connectionID"), body)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, page)
}

// decodePage reads a page request, which is a document payload rather than a
// tool argument, so it is bounded by the page size and not by maxJSONBytes. A
// body past it is refused before it is read whole.
func (server *Server) decodePage(writer http.ResponseWriter, request *http.Request, target any) bool {
	body, err := io.ReadAll(io.LimitReader(request.Body, integration.MaxPageBytes+1))
	switch {
	case err != nil:
		writeError(writer, http.StatusBadRequest, "invalid_request", "the request body could not be read")
		return false
	case len(body) > integration.MaxPageBytes:
		writeError(writer, http.StatusRequestEntityTooLarge, "invalid_request", "the page request is too large")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.More() {
		writeError(writer, http.StatusBadRequest, "invalid_request", "request body must be one JSON object with known fields")
		return false
	}
	if err := cleantext.Check(target); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	return true
}
