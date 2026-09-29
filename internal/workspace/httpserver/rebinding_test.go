package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

func TestReviewedApplyRecreatesDeletedMappedTeamWithoutWaitingForEvent(t *testing.T) {
	base := fakeOrganizationAPI(false)
	defer base.Close()
	created := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/organizations/org1/teams":
			if created > 0 {
				io.WriteString(w, `{"teams":[{"id":"replacement-team","name":"Engineering","path":"engineering"}]}`)
			} else {
				io.WriteString(w, `{"teams":[]}`)
			}
		case "POST /v1/organizations/org1/teams":
			created++
			io.WriteString(w, `{"id":"replacement-team"}`)
		case "GET /v1/organizations/org1/teams/replacement-team/members":
			io.WriteString(w, `{"members":[{"user_id":"u1","email":"owner@acme.com","role":"admin"}]}`)
		default:
			base.Config.Handler.ServeHTTP(w, r)
		}
	}))
	defer api.Close()
	document := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nteams: [{name: Engineering}]\ndirectory:\n  groups: [{source: 'google_workspace:acme.com', externalId: group1, team: Engineering}]\n"
	repo := &memoryRepository{versions: []store.Version{{Number: 1, Document: []byte(document)}}, mappings: []store.ExternalMapping{{Source: "google_workspace:acme.com", Kind: "team", ExternalID: "group1", Reference: "Engineering", ResourceID: "deleted-team"}}}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	path := "/v1/organizations/org1/workspace"
	plan := reviewedPlan(t, workspace, path, document)
	if len(runtime.SensitiveChanges(plan)) != 1 || runtime.SensitiveChanges(plan)[0].Kind != "external_team_binding" {
		t.Fatalf("rebind not marked sensitive: %+v", plan)
	}
	response := workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": document})
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || created != 0 {
		t.Fatal("unapproved rebind caused side effects")
	}
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": document, "approved_plan": plan})
	defer response.Body.Close()
	var result runtime.WorkspaceApplyResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || result.Status != "done" || created != 1 || repo.mappings[0].ResourceID != "replacement-team" {
		t.Fatalf("reviewed recreation did not converge: status=%d result=%+v mappings=%+v", response.StatusCode, result, repo.mappings)
	}
}
