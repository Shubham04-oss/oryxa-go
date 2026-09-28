package httpserver

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/authz"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/application"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// This file is the knowledge base policy routes: an organization admin's view
// onto internal/authz's stored Cedar policies for one corpus. Every decision
// about who may see, add or remove them is application.Service's, not this
// layer's — see administerableCorpus in rag/internal/application/policies.go
// — so a handler here only decodes, delegates and shapes the response.

// policyResponse is authz.Policy shaped for the wire: its organization and
// scope are already the URL's own organizationID and corpusID, so repeating
// them in the body would only invite the two to disagree.
type policyResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Text      string    `json:"text"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func newPolicyResponse(policy authz.Policy) policyResponse {
	return policyResponse{
		ID:        policy.ID,
		Name:      policy.Name,
		Text:      policy.Text,
		CreatedBy: policy.CreatedBy,
		CreatedAt: policy.CreatedAt,
		UpdatedAt: policy.UpdatedAt,
	}
}

func (server *Server) listPolicies(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	policies, err := server.service.ListPolicies(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID"))
	if err != nil {
		server.handlePolicyError(writer, request, err)
		return
	}
	response := make([]policyResponse, 0, len(policies))
	for _, policy := range policies {
		response = append(response, newPolicyResponse(policy))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"policies": response})
}

type addPolicyRequest struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

func (server *Server) addPolicy(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body addPolicyRequest
	if !readJSON(writer, request, &body) {
		return
	}
	policy, err := server.service.AddPolicy(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID"),
		application.AddPolicyInput{Name: body.Name, Text: body.Text})
	if err != nil {
		server.handlePolicyError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"policy": newPolicyResponse(policy)})
}

func (server *Server) deletePolicy(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.service.RemovePolicy(request.Context(), credentials,
		request.PathValue("organizationID"), request.PathValue("corpusID"), request.PathValue("policyID")); err != nil {
		server.handlePolicyError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// handlePolicyError is handleError plus one addition just for these three
// routes: a refused administer check answers with the specific reason
// (application.Service names it in the error itself) rather than
// handleError's own generic "you may not do that here", which every other
// forbidden response in this service still shows on purpose. Anything else —
// not found, invalid, conflict, or unexpected — goes through handleError
// exactly as it does for every other route.
func (server *Server) handlePolicyError(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, domain.ErrForbidden) {
		writeError(writer, http.StatusForbidden, forbiddenMessage(err))
		return
	}
	server.handleError(writer, request, err)
}

// forbiddenMessage is err's own message with domain.ErrForbidden's sentinel
// suffix trimmed, the way server.message does for the sentinels this
// service's other routes render literally.
func forbiddenMessage(err error) string {
	return strings.TrimSuffix(err.Error(), ": "+domain.ErrForbidden.Error())
}
