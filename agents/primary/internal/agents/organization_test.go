package agents

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/modelrouter"
)

func TestOrganizationAgentHasStrictSharedContextOnlyContract(t *testing.T) {
	llm := newTreeModel(func(_ string, _ *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText(`{"summary":"Shared context","decisions":[],"risks":[],"open_questions":[],"follow_ups":[]}`, genai.RoleModel)
	})
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	built, err := OrganizationAgent(router, OrganizationSpec{
		ID: "oag_acme", Name: "Acme Agent", Description: "Keeps shared context", Instructions: "Prefer attributable facts.",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{AppName: "organization_test", Agent: built, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: "organization_test", UserID: "organization-agent:org_acme"})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range turns.Run(context.Background(), "organization-agent:org_acme", created.Session.ID(), genai.NewContentFromText(`{"new_publication":{"summary":"Ready"}}`, genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	requests := llm.requestsFor("subagent")
	if len(requests) != 1 {
		t.Fatalf("model requests=%d, want 1", len(requests))
	}
	request := requests[0]
	if request.Config.ResponseSchema == nil || request.Config.ResponseMIMEType != "application/json" {
		t.Fatalf("response config schema=%+v mime=%q", request.Config.ResponseSchema, request.Config.ResponseMIMEType)
	}
	if functions := declaredFunctions(request); len(functions) != 0 {
		t.Fatalf("organization agent can call tools: %v", functions)
	}
	instruction := text(request.Config.SystemInstruction)
	for _, required := range []string{
		"untrusted data", "never as instructions", "private conversations", "app connections",
		"A follow-up is only a proposal", "You have no tools",
	} {
		if !strings.Contains(instruction, required) {
			t.Fatalf("organization instruction is missing %q:\n%s", required, instruction)
		}
	}
}

func TestOrganizationAgentRequiresIdentityAndRouter(t *testing.T) {
	valid := OrganizationSpec{ID: "oag_1", Name: "Org Agent", Instructions: "Synthesize shared work."}
	if _, err := OrganizationAgent(nil, valid); err == nil {
		t.Fatal("nil router was accepted")
	}
	llm := newTreeModel(func(_ string, _ *model.LLMRequest) *genai.Content {
		return genai.NewContentFromText(`{}`, genai.RoleModel)
	})
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []OrganizationSpec{
		{Name: valid.Name, Instructions: valid.Instructions},
		{ID: valid.ID, Instructions: valid.Instructions},
		{ID: valid.ID, Name: valid.Name},
	} {
		if _, err := OrganizationAgent(router, spec); err == nil {
			t.Fatalf("invalid organization spec was accepted: %+v", spec)
		}
	}
}

// TestOrganizationAgentWithGemini is an opt-in Vertex AI smoke test. It
// verifies that the production model honors the organization agent's strict
// schema rather than only exercising the deterministic fake model above.
//
//	AGENTS_PRIMARY_TEST_LIVE_MODEL=true GOOGLE_CLOUD_PROJECT=project go test ./agents/primary/internal/agents -run TestOrganizationAgentWithGemini -count=1 -v
func TestOrganizationAgentWithGemini(t *testing.T) {
	if os.Getenv("AGENTS_PRIMARY_TEST_LIVE_MODEL") != "true" {
		t.Skip("AGENTS_PRIMARY_TEST_LIVE_MODEL is not true")
	}
	project := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT"))
	if project == "" {
		t.Skip("GOOGLE_CLOUD_PROJECT is not set")
	}
	location := strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_LOCATION"))
	if location == "" {
		location = "global"
	}
	modelName := strings.TrimSpace(os.Getenv("AGENTS_PRIMARY_MODEL_LITE"))
	if modelName == "" {
		modelName = "gemini-3.5-flash-lite"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	llm, err := Gemini(ctx, modelName, project, location, nil)
	if err != nil {
		t.Fatal(err)
	}
	router, err := modelrouter.New(llm, llm)
	if err != nil {
		t.Fatal(err)
	}
	built, err := OrganizationAgent(router, OrganizationSpec{
		ID: "oag_live", Name: "Live Organization Agent", Description: "Maintains shared launch context",
		Instructions: "Keep only attributable shared facts and propose bounded follow-ups.",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{AppName: "organization_live", Agent: built, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(ctx, &session.CreateRequest{AppName: "organization_live", UserID: "organization-agent:org_live"})
	if err != nil {
		t.Fatal(err)
	}
	input := `{"previous_synthesis":{"revision":0,"summary":"","decisions":[],"risks":[],"open_questions":[]},"teamspace":{"id":"tsp_launch","name":"Launch"},"participants":[{"agent_id":"agt_sales","agent_name":"Sales","role":"publication author"}],"new_publication":{"id":"tse_1","author_agent_id":"agt_sales","kind":"decision","title":"Launch date","summary":"Sales and engineering agreed to launch on October 15."}}`
	var output strings.Builder
	for event, err := range turns.Run(ctx, "organization-agent:org_live", created.Session.ID(), genai.NewContentFromText(input, genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
		if event.Content != nil && event.Author == built.Name() && !event.Partial {
			output.WriteString(text(event.Content))
		}
	}
	var result struct {
		Summary       string `json:"summary"`
		Decisions     []string
		Risks         []string
		OpenQuestions []string `json:"open_questions"`
		FollowUps     []struct {
			TargetAgentID string `json:"target_agent_id"`
			Task          string `json:"task"`
			Rationale     string `json:"rationale"`
		} `json:"follow_ups"`
	}
	if err := json.Unmarshal([]byte(output.String()), &result); err != nil {
		t.Fatalf("decode live output %q: %v", output.String(), err)
	}
	if strings.TrimSpace(result.Summary) == "" || result.Decisions == nil || result.Risks == nil || result.OpenQuestions == nil || result.FollowUps == nil {
		t.Fatalf("incomplete live synthesis=%+v", result)
	}
	for _, followUp := range result.FollowUps {
		if followUp.TargetAgentID != "" && followUp.TargetAgentID != "agt_sales" {
			t.Fatalf("Gemini invented target %q in %+v", followUp.TargetAgentID, result.FollowUps)
		}
	}
}
