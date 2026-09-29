package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

func TestNewerStoredFieldsDoNotBlockPlanApplyPullStatusOrRollback(t *testing.T) {
	api := fakeOrganizationAPI(false)
	defer api.Close()
	current := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nsafety: {guard: {holdForReview: true}}\n"
	future := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nfutureFeature: {enabled: true}\nsafety: {guard: {holdForReview: true, futureGuard: enabled}}\n"
	repo := &memoryRepository{versions: []store.Version{{Number: 1, Document: []byte(future)}}}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	path := "/v1/organizations/org1/workspace"
	for _, suffix := range []string{"", "/status", "/context"} {
		response := workspaceRequest(t, workspace, http.MethodGet, path+suffix, "owner-token", nil)
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("read %s = %d %s", suffix, response.StatusCode, body)
		}
		if suffix == "" {
			if _, err := spec.Parse(body); err != nil {
				t.Fatalf("pull emitted invalid manifest: %v", err)
			}
		}
	}
	plan := reviewedPlan(t, workspace, path, current)
	response := workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": current, "approved_plan": plan})
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 2 {
		t.Fatalf("apply = %d %s", response.StatusCode, body)
	}
	response = workspaceRequest(t, workspace, http.MethodPost, path+":rollback-plan", "owner-token", map[string]int{"to": 1})
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || plan.PlanID == "" {
		t.Fatalf("rollback plan = %d %+v", response.StatusCode, plan)
	}
	response = workspaceRequest(t, workspace, http.MethodPost, path+":rollback", "owner-token", map[string]any{"to": 1, "approved_plan": plan})
	defer response.Body.Close()
	var applied runtime.WorkspaceApplyResult
	if err := json.NewDecoder(response.Body).Decode(&applied); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || applied.Status != "done" {
		t.Fatalf("rollback = %d %+v", response.StatusCode, applied)
	}
	// Submitted manifests still reject the same unknown fields.
	response = workspaceRequest(t, workspace, http.MethodPost, path+":validate", "owner-token", map[string]string{"document": future})
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("submitted future fields were accepted: %d", response.StatusCode)
	}
}
