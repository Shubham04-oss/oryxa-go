// Package authstub stands in for auth during the stack k6 journey.
//
// Teams and files forward the caller's bearer token or session cookie here.
// The stub answers the caller's organization membership, and the
// organization's member list. This is not auth and must not be deployed.
package authstub

import (
	"encoding/json"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/test/identities"
)

func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/organizations/{organizationID}", organization)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/members", members)
	return mux
}

func organization(writer http.ResponseWriter, request *http.Request) {
	caller, ok := lookup(writer, request)
	if !ok {
		return
	}
	organizationID := request.PathValue("organizationID")
	if !caller.InOrg || organizationID == "" {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "resource not found"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"organization": map[string]string{"id": organizationID, "name": "Simulated Org"},
		"membership": map[string]string{
			"organization_id": organizationID,
			"user_id":         caller.UserID,
			"email":           caller.Email,
			"display_name":    caller.DisplayName,
			"role":            caller.Role,
		},
	})
}

func members(writer http.ResponseWriter, request *http.Request) {
	caller, ok := lookup(writer, request)
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

func lookup(writer http.ResponseWriter, request *http.Request) (identities.Person, bool) {
	token, present := identities.TokenFrom(request)
	if !present {
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "authentication failed"})
		return identities.Person{}, false
	}
	caller, ok := identities.People[token]
	if !ok {
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "authentication failed"})
		return identities.Person{}, false
	}
	return caller, true
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
