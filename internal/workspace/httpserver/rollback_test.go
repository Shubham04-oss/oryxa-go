package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

func rollbackExpected(t *testing.T, document []byte) []byte {
	t.Helper()
	parsed, err := spec.Parse(document)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := rollbackOrganizationDefaults(parsed).YAML()
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestRollbackRestoresOmittedOrganizationDefaults(t *testing.T) {
	api := fakeOrganizationAPI(false)
	defer api.Close()
	first := []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\n")
	repo := &memoryRepository{versions: []store.Version{{Number: 1, Document: first}}}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	path := "/v1/organizations/org1/workspace"
	second := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme, domains: [example.com]}\norganization: {timezone: Asia/Kolkata}\npeople: {guests: true}\n"
	plan := reviewedPlan(t, workspace, path, second)
	response := workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": second, "approved_plan": plan})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 2 {
		t.Fatalf("v2 apply status=%d versions=%d", response.StatusCode, len(repo.versions))
	}
	response = workspaceRequest(t, workspace, http.MethodPost, path+":rollback-plan", "owner-token", map[string]int64{"to": 1})
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("rollback plan status=%d", response.StatusCode)
	}
	response = workspaceRequest(t, workspace, http.MethodPost, path+":rollback", "owner-token", map[string]any{"to": 1, "approved_plan": plan})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 3 {
		t.Fatalf("rollback apply status=%d versions=%d", response.StatusCode, len(repo.versions))
	}
	response = workspaceRequest(t, api, http.MethodGet, "/v1/organizations/org1", "owner-token", nil)
	defer response.Body.Close()
	var result struct {
		Organization runtime.Organization `json:"organization"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Organization.Domains) != 0 || result.Organization.Timezone != "UTC" || result.Organization.GuestsAllowed {
		t.Fatalf("rollback retained later settings: %+v", result.Organization)
	}
}
