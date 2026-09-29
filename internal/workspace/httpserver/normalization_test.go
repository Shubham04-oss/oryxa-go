package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func TestNormalizedBlockTextSavesVersionOneAndConverges(t *testing.T) {
	for _, style := range []string{"|", ">"} {
		t.Run(style, func(t *testing.T) {
			fallback := fakeOrganizationAPI(false)
			defer fallback.Close()
			var mu sync.Mutex
			agent := runtime.OrganizationAgent{Name: "Default", Description: "Default", Instructions: "Default", Status: "active", CanManage: true}
			writes := 0
			var snapshotReads atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/organizations" {
					snapshotReads.Add(1)
				}
				if r.URL.Path != "/v1/organizations/org1/organization-agent" {
					fallback.Config.Handler.ServeHTTP(w, r)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPatch {
					var values map[string]string
					if err := json.NewDecoder(r.Body).Decode(&values); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					// The actual managedagents service collapses name whitespace and trims
					// descriptions/instructions before persistence. A fake must do the same.
					if value, ok := values["name"]; ok {
						agent.Name = strings.Join(strings.Fields(value), " ")
					}
					if value, ok := values["description"]; ok {
						agent.Description = strings.TrimSpace(value)
					}
					if value, ok := values["instructions"]; ok {
						agent.Instructions = strings.TrimSpace(value)
					}
					writes++
				}
				json.NewEncoder(w).Encode(agent)
			}))
			defer api.Close()
			repo := &memoryRepository{}
			server, err := New(repo, api.URL)
			if err != nil {
				t.Fatal(err)
			}
			workspace := httptest.NewServer(server)
			defer workspace.Close()
			document := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\norganization: {name: '  Acme  '}\nagents:\n  organization:\n    displayName: 'Acme  Assistant'\n    description: " + style + "\n      Helpful assistant.\n    instructions: " + style + "\n      Coordinate teams.\n"
			path := "/v1/organizations/org1/workspace"
			plan := reviewedPlan(t, workspace, path, document)
			snapshotReads.Store(0)
			response := workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": document, "approved_plan": plan})
			var result runtime.WorkspaceApplyResult
			err = json.NewDecoder(response.Body).Decode(&result)
			response.Body.Close()
			if err != nil || response.StatusCode != 200 || result.Status != "done" || result.Version != 1 || len(repo.versions) != 1 {
				t.Fatalf("first apply failed to persist v1: %+v status%d err%v", result, response.StatusCode, err)
			}
			if snapshotReads.Load() != 2 {
				t.Fatalf("apply must reuse initial and verified final state: snapshots=%d", snapshotReads.Load())
			}
			next := reviewedPlan(t, workspace, path, document)
			if len(next.Changes) != 0 || writes != 1 {
				t.Fatalf("normalized service values did not converge: %+v writes%d", next, writes)
			}
			snapshotReads.Store(0)
			response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": document, "approved_plan": next})
			response.Body.Close()
			if response.StatusCode != 200 || snapshotReads.Load() != 1 || writes != 1 {
				t.Fatalf("no-op apply repeated reads/writes: status%d snapshots%d writes%d", response.StatusCode, snapshotReads.Load(), writes)
			}
			saved, err := spec.Parse(repo.versions[0].Document)
			if err != nil {
				t.Fatal(err)
			}
			if *saved.Agents.Organization.Instructions != "Coordinate teams." || *saved.Agents.Organization.DisplayName != "Acme Assistant" {
				t.Fatal("saved version retained unnormalized text")
			}
		})
	}
}
