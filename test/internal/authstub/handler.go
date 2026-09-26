// Package authstub stands in for auth during the stack k6 journey and the
// local dev stack.
//
// Teams, files, rag and knowledge forward the caller's bearer token or
// session cookie here. The stub answers the caller's organization
// membership and the organization's member list. It also mints and
// recognizes agent tokens (see agenttokens.go), so a run can act for its
// member the way it does against real auth's Cognito-backed standing
// grants. This is not auth and must not be deployed.
package authstub

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/test/identities"
)

// Handler returns the stand-in with no service-token verification
// configured: POST /v1/internal/agent-tokens refuses every caller with 401,
// exactly as real auth does when AUTH_SERVICE_CALLERS is unset. Everything
// else behaves as it always has. test/cmd/authstub calls NewHandler with
// ConfigFromEnv instead, so the route actually mints tokens against
// LocalStack's KMS.
func Handler() http.Handler {
	return NewHandler(Config{})
}

// NewHandler returns the stand-in configured by config.
func NewHandler(config Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/organizations/{organizationID}", organization)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/members", members)
	mux.HandleFunc("POST /v1/internal/agent-tokens", config.mintAgentToken)
	return mux
}

func organization(writer http.ResponseWriter, request *http.Request) {
	caller, isAgent, ok := lookup(writer, request)
	if !ok {
		return
	}
	organizationID := request.PathValue("organizationID")
	if !caller.InOrg || organizationID == "" {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "resource not found"})
		return
	}
	response := map[string]any{
		"organization": map[string]string{"id": organizationID, "name": "Simulated Org"},
		"membership": map[string]string{
			"organization_id": organizationID,
			"user_id":         caller.UserID,
			"email":           caller.Email,
			"display_name":    caller.DisplayName,
			"role":            caller.Role,
		},
	}
	// Mirrors real auth's own organization handler: a top-level
	// "agent": true for an agent token, the field omitted otherwise, so a
	// caller can tell the two apart without decoding the token itself.
	if isAgent {
		response["agent"] = true
	}
	writeJSON(writer, http.StatusOK, response)
}

func members(writer http.ResponseWriter, request *http.Request) {
	caller, _, ok := lookup(writer, request)
	if !ok {
		return
	}
	if !caller.InOrg || request.PathValue("organizationID") == "" {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "resource not found"})
		return
	}
	listed := make([]map[string]string, 0, 4)
	for _, person := range identities.OrgMembers() {
		listed = append(listed, map[string]string{
			"user_id":      person.UserID,
			"email":        person.Email,
			"display_name": person.DisplayName,
			"role":         person.Role,
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"members": listed})
}

// lookup resolves the request's bearer token or cookie to the stand-in
// person it names, and reports whether that token is the person's agent
// token rather than their own. It writes the response and returns ok=false
// when the token is missing, unrecognized, or an agent token used on a
// route real auth would refuse it on.
func lookup(writer http.ResponseWriter, request *http.Request) (identities.Person, bool, bool) {
	token, present := identities.TokenFrom(request)
	if !present {
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "authentication failed"})
		return identities.Person{}, false, false
	}
	caller, isAgent, ok := personFromToken(token)
	if !ok {
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "authentication failed"})
		return identities.Person{}, false, false
	}
	if isAgent && !agentTokenAllowed(request.Pattern) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "agents cannot do this"})
		return identities.Person{}, false, false
	}
	return caller, isAgent, true
}

// agentTokenPrefix marks a bearer token this stand-in minted from
// POST /v1/internal/agent-tokens: it names the member the token acts for,
// the way real auth's Cognito-issued agent tokens carry the member's own
// sub under the agent app client's client_id. See agenttokens.go.
const agentTokenPrefix = "agent:"

// personFromToken resolves token to the stand-in person it names, and
// whether it is that person's agent token rather than their own session
// token.
func personFromToken(token string) (identities.Person, bool, bool) {
	if userID, ok := strings.CutPrefix(token, agentTokenPrefix); ok {
		person, found := identities.PersonByUserID(userID)
		return person, true, found
	}
	person, found := identities.People[token]
	return person, false, found
}

// agentAllowedPatterns mirrors auth's own agentAllowedPatterns
// (auth/internal/httpserver/server.go): the only authenticated-route
// patterns an agent token may use. Every other authenticated route refuses
// one with 403. Matched against request.Pattern, the exact pattern
// net/http's ServeMux resolved the request to, never a prefix of the raw
// path, so no path trick can make a different route look like one of
// these.
var agentAllowedPatterns = map[string]bool{
	"GET /v1/auth/me":                                true,
	"GET /v1/organizations":                          true,
	"GET /v1/organizations/{organizationID}":         true,
	"GET /v1/organizations/{organizationID}/members": true,
}

func agentTokenAllowed(pattern string) bool {
	return agentAllowedPatterns[pattern]
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
