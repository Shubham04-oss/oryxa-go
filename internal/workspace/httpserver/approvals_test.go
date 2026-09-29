package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

func reviewedPlan(t *testing.T, server *httptest.Server, path, document string) runtime.Plan {
	t.Helper()
	response := workspaceRequest(t, server, http.MethodPost, path+":plan", "owner-token", map[string]string{"document": document})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plan returned %d", response.StatusCode)
	}
	var plan runtime.Plan
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if plan.PlanID == "" || plan.PlanHash == "" {
		t.Fatal("server did not issue a bound plan")
	}
	return plan
}

func TestWorkspaceApplyRequiresIssuedPlanBoundToActorDocumentVersionAndExpiry(t *testing.T) {
	for _, scenario := range []string{"missing", "fabricated", "document changed", "version changed", "actor changed", "expired", "sensitive state changed", "plan hash changed", "valid"} {
		t.Run(scenario, func(t *testing.T) {
			api := fakeOrganizationAPI(false)
			defer api.Close()
			repo := &memoryRepository{}
			server, err := New(repo, api.URL)
			if err != nil {
				t.Fatal(err)
			}
			workspace := httptest.NewServer(server)
			defer workspace.Close()
			path := "/v1/organizations/org1/workspace"
			document := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nsafety: {guard: {holdForReview: false}}\n"
			plan := reviewedPlan(t, workspace, path, document)
			record := repo.plans[plan.PlanID]
			switch scenario {
			case "missing":
				plan = runtime.Plan{}
			case "fabricated":
				plan.PlanID = "invented"
			case "document changed":
				document += "tools: {offer: [github]}\n"
			case "version changed":
				repo.versions = []store.Version{{Number: 1, Document: []byte(document)}}
			case "actor changed":
				record.ActorID = "other"
			case "expired":
				record.ExpiresAt = time.Now().Add(-time.Minute)
			case "sensitive state changed":
				var changed runtime.Plan
				if err := json.Unmarshal(record.Plan, &changed); err != nil {
					t.Fatal(err)
				}
				changed.Changes = append(changed.Changes, runtime.Change{Op: "delete", Kind: "member", Name: "replaced@acme.com", ResourceID: "old-user"})
				record.Plan, err = json.Marshal(changed)
				if err != nil {
					t.Fatal(err)
				}
			case "plan hash changed":
				plan.PlanHash = "invented"
			}
			if scenario != "missing" && scenario != "fabricated" {
				repo.plans[plan.PlanID] = record
			}
			response := workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": document, "approved_plan": plan})
			defer response.Body.Close()
			if scenario == "valid" {
				if response.StatusCode != http.StatusOK || len(repo.versions) != 1 {
					t.Fatalf("valid approval: status=%d versions=%d", response.StatusCode, len(repo.versions))
				}
				return
			}
			var failure struct {
				Code string `json:"code"`
			}
			if err := json.NewDecoder(response.Body).Decode(&failure); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusConflict || failure.Code != "stale_plan" {
				t.Fatalf("invalid plan status=%d code=%s", response.StatusCode, failure.Code)
			}
			if scenario != "version changed" && len(repo.versions) != 0 {
				t.Fatal("invalid approval mutated workspace")
			}
		})
	}
}

func TestSensitivePolicyChangesIncludeGuardAndToolPermissions(t *testing.T) {
	plan := runtime.Plan{Changes: []runtime.Change{
		{Op: "update", Kind: "policy", Name: "safety.guard.pii.minimum.owner"},
		{Op: "update", Kind: "policy", Name: "safety.guard.pii.minimum.peer"},
		{Op: "update", Kind: "policy", Name: "safety.guard.relevance"},
		{Op: "update", Kind: "policy", Name: "safety.guard.holdForReview"},
		{Op: "update", Kind: "policy", Name: "rules.consent.expireAfter"},
		{Op: "update", Kind: "policy", Name: "tools.offer"},
	}}
	if len(runtime.SensitiveChanges(plan)) != len(plan.Changes) {
		t.Fatal("policy changes bypassed approval")
	}
}
