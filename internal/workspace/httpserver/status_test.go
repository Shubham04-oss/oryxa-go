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

func TestAdminStatusOfOwnerPruneConfigurationIsReadableAndNonDestructive(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/me":
			io.WriteString(w, `{"user":{"id":"admin1","email":"admin@acme.com"}}`)
		case "/v1/organizations":
			io.WriteString(w, `{"organizations":[{"id":"org1","slug":"acme","name":"Acme"}]}`)
		case "/v1/organizations/org1":
			io.WriteString(w, `{"organization":{"id":"org1","slug":"acme"},"membership":{"organization_id":"org1","user_id":"admin1","role":"admin"}}`)
		case "/v1/organizations/org1/members":
			io.WriteString(w, `{"members":[{"user_id":"owner1","email":"owner@acme.com","role":"owner"},{"user_id":"admin1","email":"admin@acme.com","role":"admin"},{"user_id":"joined1","email":"joined@acme.com","role":"member"}]}`)
		case "/v1/organizations/org1/invitations":
			io.WriteString(w, `{"invitations":[]}`)
		case "/v1/organizations/org1/teams":
			io.WriteString(w, `{"teams":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	document := []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\npeople: {prune: true, members: [{email: owner@acme.com, role: owner}, {email: admin@acme.com, role: admin}]}\nteams: {prune: true}\n")
	repo := &memoryRepository{versions: []store.Version{{Number: 1, Document: document}}}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	response := workspaceRequest(t, workspace, http.MethodGet, "/v1/organizations/org1/workspace/status", "admin-token", nil)
	defer response.Body.Close()
	var result struct {
		Status string       `json:"status"`
		Plan   runtime.Plan `json:"plan"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || result.Status != "needs_human" || len(result.Plan.NeedsHuman) == 0 {
		t.Fatalf("admin status=%d %+v", response.StatusCode, result)
	}
	for _, change := range result.Plan.Changes {
		if change.Op == "delete" {
			t.Fatalf("admin received executable deletion: %+v", change)
		}
	}
}
