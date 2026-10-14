package assembly

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"cloud.google.com/go/auth"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
)

const unguardedAnswer = `{"status":"completed","summary":"Asha's salary is 90k.","tools_used":[],"artifacts":[],"handoffs":[],"decisions":[],"risks":[],"open_questions":[]}`

// guardedKrude is Krude as Build assembles it, its models answered by a
// stand-in for Vertex AI and, when guardrail is set, screened by a stand-in
// for Bedrock that intervenes on every answer. screens counts the screens.
func guardedKrude(t *testing.T, guardrail string) (krude *Krude, screens *atomic.Int32) {
	t.Helper()
	vertex := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		text := strings.ReplaceAll(unguardedAnswer, `"`, `\"`)
		_, _ = io.WriteString(writer, `{"candidates":[{"content":{"role":"model","parts":[{"text":"`+text+`"}]},"finishReason":"STOP"}]}`)
	}))
	t.Cleanup(vertex.Close)
	screens = &atomic.Int32{}
	bedrock := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		screens.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		action := "NONE"
		if strings.Contains(string(body), `"OUTPUT"`) {
			action = "GUARDRAIL_INTERVENED"
		}
		_, _ = io.WriteString(writer, `{"action":"`+action+`","outputs":[],"assessments":[]}`)
	}))
	t.Cleanup(bedrock.Close)
	for _, name := range []string{"GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_GENAI_USE_VERTEXAI"} {
		t.Setenv(name, "")
	}
	t.Setenv("GOOGLE_VERTEX_BASE_URL", vertex.URL)
	awsConfig := validAWSConfig()
	awsConfig.BaseEndpoint = &bedrock.URL
	settings := serviceconfig.Config{
		GoogleCloudProject: "oryxa-test", GoogleCloudLocation: "global",
		Model: "gemini-test", ModelLite: "gemini-test-lite",
	}
	if guardrail != "" {
		settings.GuardrailIdentifier, settings.GuardrailVersion = guardrail, "1"
	}
	krude, err := Build(context.Background(), Options{
		Settings: settings, AWS: awsConfig,
		Google: auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: fixedToken("service-account-token")}),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	t.Cleanup(func() { _ = krude.Close(context.Background()) })
	return krude, screens
}

// answer runs one turn of built and returns the last text it answered.
func answer(t *testing.T, built agent.Agent) string {
	t.Helper()
	sessions := session.InMemoryService()
	turns, err := runner.New(runner.Config{AppName: "app", Agent: built, SessionService: sessions, ArtifactService: artifact.InMemoryService()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(context.Background(), &session.CreateRequest{AppName: "app", UserID: "user_a", State: map[string]any{agents.OrganizationIDKey: "org_1"}})
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for event, err := range turns.Run(context.Background(), "user_a", created.Session.ID(), genai.NewContentFromText("What does Asha earn?", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if event.Content != nil {
			for _, part := range event.Content.Parts {
				if part.Text != "" {
					last = part.Text
				}
			}
		}
	}
	return last
}

// L5: the guardrail screens every agent's model, not only the primary's main
// one. A specialist and the organization agent answer on the lite model.
func TestTheGuardrailScreensASpecialistAndTheOrganizationAgent(t *testing.T) {
	krude, screens := guardedKrude(t, "gr-1")
	specialist, err := krude.Managed(agents.ManagedSpec{ID: "agt_s", Name: "Scout", Instructions: "Answer."})
	if err != nil {
		t.Fatal(err)
	}
	if got := answer(t, specialist); got != "I can't help with that." {
		t.Errorf("the specialist answered %q unscreened (%d screens)", got, screens.Load())
	}
	organization, err := krude.OrganizationAgent(agents.OrganizationSpec{ID: "oag_1", Name: "Org", Instructions: "Synthesise."})
	if err != nil {
		t.Fatal(err)
	}
	before := screens.Load()
	if got := answer(t, organization); got != "I can't help with that." || screens.Load() == before {
		t.Errorf("the organization agent answered %q unscreened", got)
	}
}

// A call whose output only feeds other code, as compacting a room's brief, is
// not screened (the planner, helpers and sub-agents: modelrouter.Router.For).
func TestTheGuardrailLeavesCompactionAlone(t *testing.T) {
	krude, screens := guardedKrude(t, "gr-1")
	if got, err := krude.Compact(context.Background(), "Compact this room."); err != nil || got != unguardedAnswer || screens.Load() != 0 {
		t.Errorf("Compact() = %q, %v after %d screens, want the model's own answer and none", got, err, screens.Load())
	}
}

func TestWithoutAGuardrailEveryModelAnswersAsItIs(t *testing.T) {
	krude, screens := guardedKrude(t, "")
	specialist, err := krude.Managed(agents.ManagedSpec{ID: "agt_s", Name: "Scout", Instructions: "Answer."})
	if err != nil {
		t.Fatal(err)
	}
	if got := answer(t, specialist); got != unguardedAnswer || screens.Load() != 0 {
		t.Errorf("answer = %q after %d screens, want the model's own and none", got, screens.Load())
	}
}
