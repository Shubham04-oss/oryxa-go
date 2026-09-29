package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

func TestPartialApplyRecordsCompletedWritesAndResumesWithoutPublishingPolicies(t *testing.T) {
	name, teamExists, failTeam, orgWrites := "Acme", false, true, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/auth/me":
			io.WriteString(w, `{"user":{"id":"u1","email":"owner@acme.com"}}`)
		case "GET /v1/organizations":
			json.NewEncoder(w).Encode(map[string]any{"organizations": []map[string]string{{"id": "org1", "slug": "acme", "name": name}}})
		case "GET /v1/organizations/org1":
			io.WriteString(w, `{"organization":{"id":"org1","slug":"acme"},"membership":{"organization_id":"org1","user_id":"u1","role":"owner"}}`)
		case "GET /v1/organizations/org1/members":
			io.WriteString(w, `{"members":[{"user_id":"u1","email":"owner@acme.com","role":"owner"}]}`)
		case "GET /v1/organizations/org1/invitations":
			io.WriteString(w, `{"invitations":[]}`)
		case "GET /v1/organizations/org1/teams":
			if teamExists {
				io.WriteString(w, `{"teams":[{"id":"team1","name":"Engineering","path":"engineering"}]}`)
			} else {
				io.WriteString(w, `{"teams":[]}`)
			}
		case "GET /v1/organizations/org1/teams/team1/members":
			io.WriteString(w, `{"members":[{"user_id":"u1","email":"owner@acme.com","role":"admin"}]}`)
		case "PATCH /v1/organizations/org1":
			var input struct {
				Name string `json:"name"`
			}
			json.NewDecoder(r.Body).Decode(&input)
			name = input.Name
			orgWrites++
			io.WriteString(w, `{}`)
		case "POST /v1/organizations/org1/teams":
			if failTeam {
				w.WriteHeader(http.StatusConflict)
				io.WriteString(w, `{"error":"injected temporary team conflict"}`)
			} else {
				teamExists = true
				io.WriteString(w, `{"id":"team1"}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	repo := &memoryRepository{}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	path := "/v1/organizations/org1/workspace"
	document := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\norganization: {name: New Acme}\nteams: [{name: Engineering}]\nsafety: {guard: {holdForReview: true}}\n"
	plan := reviewedPlan(t, workspace, path, document)
	response := workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": document, "approved_plan": plan})
	var partial struct {
		Status    string                     `json:"status"`
		ApplyID   string                     `json:"apply_id"`
		Completed []store.CompletedOperation `json:"completed"`
	}
	if err := json.NewDecoder(response.Body).Decode(&partial); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || partial.Status != "partial" || len(partial.Completed) != 1 || partial.Completed[0].Method != "PATCH" || repo.attempts[partial.ApplyID].Status != "partial" || len(repo.versions) != 0 {
		t.Fatalf("partial progress or policy safety failed: status=%d %+v versions=%d", response.StatusCode, partial, len(repo.versions))
	}
	failTeam = false
	plan = reviewedPlan(t, workspace, path, document)
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": document, "approved_plan": plan})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 1 || orgWrites != 1 || !teamExists {
		t.Fatalf("resume failed or repeated write: status=%d versions=%d orgwrites=%d", response.StatusCode, len(repo.versions), orgWrites)
	}
}
