package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRoleElevationApprovalRejectsReusedMemberOrTeamNames(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		configuration string
		teamMember    bool
		replaceTeam   bool
	}{
		{"organization member replacement", "people: {members: [{email: member@acme.com, role: admin}]}", false, false},
		{"team member replacement", "teams: [{name: Engineering, leads: [member@acme.com]}]", true, false},
		{"new team admin replacement", "teams: [{name: Engineering, leads: [member@acme.com]}]", false, false},
		{"team replacement", "teams: [{name: Engineering, leads: [member@acme.com]}]", true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var replaced atomic.Bool
			var writes atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					writes.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				memberID, teamID := "reviewed-user", "reviewed-team"
				if replaced.Load() {
					if scenario.replaceTeam {
						teamID = "replacement-team"
					} else {
						memberID = "replacement-user"
					}
				}
				switch r.URL.Path {
				case "/v1/auth/me":
					io.WriteString(w, `{"user":{"id":"owner1","email":"owner@acme.com"}}`)
				case "/v1/organizations":
					io.WriteString(w, `{"organizations":[{"id":"org1","slug":"acme"}]}`)
				case "/v1/organizations/org1":
					io.WriteString(w, `{"organization":{"id":"org1","slug":"acme"},"membership":{"organization_id":"org1","user_id":"owner1","role":"owner"}}`)
				case "/v1/organizations/org1/directory":
					json.NewEncoder(w).Encode(map[string]any{"members": []map[string]string{{"user_id": "owner1", "email": "owner@acme.com", "org_role": "owner"}, {"user_id": memberID, "email": "member@acme.com", "org_role": "member"}}})
				case "/v1/organizations/org1/members":
					json.NewEncoder(w).Encode(map[string]any{"members": []map[string]string{{"user_id": "owner1", "email": "owner@acme.com", "role": "owner"}, {"user_id": memberID, "email": "member@acme.com", "role": "member"}}})
				case "/v1/organizations/org1/invitations":
					io.WriteString(w, `{"invitations":[]}`)
				case "/v1/organizations/org1/teams":
					json.NewEncoder(w).Encode(map[string]any{"teams": []map[string]string{{"id": teamID, "name": "Engineering", "path": "engineering"}}})
				case "/v1/organizations/org1/teams/" + teamID + "/members":
					members := []map[string]string{}
					if scenario.teamMember {
						members = append(members, map[string]string{"user_id": memberID, "email": "member@acme.com", "role": "member"})
					}
					json.NewEncoder(w).Encode(map[string]any{"members": members})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer api.Close()
			repository := &memoryRepository{}
			server, err := New(repository, api.URL)
			if err != nil {
				t.Fatal(err)
			}
			workspace := httptest.NewServer(server)
			defer workspace.Close()
			path := "/v1/organizations/org1/workspace"
			document := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\n" + scenario.configuration + "\n"
			plan := reviewedPlan(t, workspace, path, document)
			replaced.Store(true)
			response := workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": document, "approved_plan": plan})
			defer response.Body.Close()
			var failure struct {
				Code string `json:"code"`
			}
			if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusConflict || failure.Code != "stale_plan" || writes.Load() != 0 || len(repository.versions) != 0 {
				t.Fatalf("replacement inherited elevation approval: status=%d code=%s writes=%d", response.StatusCode, failure.Code, writes.Load())
			}
		})
	}
}
