package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/specialistonboarding"
)

func TestSpecialistDraftIsAuthenticatedAndDoesNotCreateAnAgent(t *testing.T) {
	registry := &fakeManagedAgents{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl"}}}
	planner := specialistonboarding.New(func(_ context.Context, prompt string) (string, error) {
		return `{"purpose":"Investigate evidence","instructions":"Cite sources","cycle":[{"title":"Ask","detail":"Clarify"},{"title":"Read","detail":"Find evidence"},{"title":"Report","detail":"Summarize"}],"integrations":[],"first_message":"What should I research?"}`, nil
	})
	server := New(member(), &fakeThreads{}, Config{Agents: registry, SpecialistPlanner: planner}, nil)
	body := `{"name":"Researcher","context":{"member_name":"Shubham","organization_name":"Odd Works","knowledge_sources":[],"connected_apps":[]}}`
	response := serve(server, http.MethodPost, "/v1/organizations/org_1/agents/onboarding-draft", body, bearer)
	if response.Code != http.StatusOK {
		t.Fatalf("draft = %d %s", response.Code, response.Body.String())
	}
	var plan specialistonboarding.Plan
	if err := json.Unmarshal(response.Body.Bytes(), &plan); err != nil || plan.Purpose != "Investigate evidence" {
		t.Fatalf("draft plan = %+v, %v", plan, err)
	}
	if len(registry.definitions) != 1 {
		t.Fatal("draft must not create a specialist")
	}
}
