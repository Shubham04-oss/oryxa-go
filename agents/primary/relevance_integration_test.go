package primary

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestAMountedServiceJudgesRelevanceAsTheDeploymentAndOrganizationsSay needs
// the shared local PostgreSQL, as the test of the answer guard's levels does:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/teams_nil_fix?sslmode=disable' go test ./agents/primary -run JudgesRelevance
//
// It mounts the service with an answer guardrail (a stand-in for Bedrock that
// finds nothing) and a stand-in for Vertex AI that says every answer carries
// health details nobody asked for, and drives the check route as a member:
// with the deployment's default ceiling, shadow, the answer is let through and
// the block says it would have been held; in the one organization the ceiling
// does not apply to, it is held, for what the model found, and shares nothing;
// and an answer from no private source never reaches the model. It is where
// Mount hands the guard its judge.
func TestAMountedServiceJudgesRelevanceAsTheDeploymentAndOrganizationsSay(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agents-primary-relevance-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)

	// auth stands in for the sign-in service: every organization answers the
	// same member, user_1.
	auth := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"membership": map[string]string{"user_id": "user_1", "role": "member"}, "agent": false})
	}))
	t.Cleanup(auth.Close)
	// Google's token endpoint, and Vertex AI itself.
	google := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"fake-access-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(google.Close)
	fakeGoogleADC(t, google.URL)
	vertex := &fakeVertex{}
	vertexServer := httptest.NewServer(vertex)
	t.Cleanup(vertexServer.Close)
	t.Setenv("GOOGLE_VERTEX_BASE_URL", vertexServer.URL)
	// Bedrock finds nothing in anything.
	bedrock := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"action":"NONE","outputs":[],"assessments":[],"usage":{}}`))
	}))
	t.Cleanup(bedrock.Close)

	const exemptOrganization = "org_relevance_exempt"
	plat := platform(map[string]string{
		"GOOGLE_CLOUD_PROJECT": "oryxa-test-project", "AUTH_URL": auth.URL,
		"AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID": "gr_answers", "AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION": "1",
		"AGENTS_PRIMARY_GUARD_HOLD_ORGS": exemptOrganization,
	}, database)
	plat.AWS = aws.Config{
		Region: "us-east-1", BaseEndpoint: aws.String(bedrock.URL),
		Credentials: awscredentials.NewStaticCredentialsProvider("test", "test", ""),
	}
	if err := Migrate(ctx, plat); err != nil {
		t.Fatal(err)
	}
	service, err := Mount(ctx, plat)
	if err != nil {
		t.Fatal(err)
	}
	if service.Close != nil {
		t.Cleanup(service.Close)
	}

	check := func(organization string, sources []string) map[string]json.RawMessage {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"texts": []string{"No, she has a doctor's appointment."}, "request": "Is Priya free on Thursday?", "sources": sources,
		})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/organizations/"+organization+"/answer-guard/check", strings.NewReader(string(body)))
		request.Header.Set("Authorization", "Bearer member-token")
		recorder := httptest.NewRecorder()
		service.Handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("check in %s = %d %s", organization, recorder.Code, recorder.Body.String())
		}
		var answer map[string]json.RawMessage
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatal(err)
		}
		return answer
	}
	private := []string{"apps_gmail_search_messages"}

	// The deployment's ceiling is shadow: what the model found is recorded.
	watched := check("org_relevance_watched", private)
	if string(watched["state"]) != `"clear"` || string(watched["shareable"]) != `["No, she has a doctor's appointment."]` ||
		string(watched["relevance"]) != `{"judged":true,"would_hold":true,"held":false}` {
		t.Fatalf("under the default ceiling the check answered %v, want the answer let through and the would-hold recorded", watched)
	}
	// The organization the ceiling does not apply to has it held.
	held := check(exemptOrganization, private)
	var kinds []string
	_ = json.Unmarshal(held["kinds"], &kinds)
	if string(held["state"]) != `"held"` || string(held["shareable"]) != `null` || !slices.Equal(kinds, []string{"OFF_TOPIC", "health"}) ||
		string(held["relevance"]) != `{"judged":true,"would_hold":true,"held":true}` {
		t.Fatalf("in the organization the ceiling does not apply to the check answered %v, want it held for health with nothing to share", held)
	}
	if vertex.asked() != 2 {
		t.Fatalf("the model was asked %d times, want twice", vertex.asked())
	}
	// No private source, nothing judged, and the model is not asked.
	for _, organization := range []string{"org_relevance_watched", exemptOrganization} {
		unjudged := check(organization, []string{"directory_find_people"})
		if string(unjudged["state"]) != `"clear"` || string(unjudged["relevance"]) != `{"judged":false,"would_hold":false,"held":false}` {
			t.Fatalf("an answer from a directory lookup in %s = %v, want it clear and not judged", organization, unjudged)
		}
	}
	if vertex.asked() != 2 {
		t.Fatalf("the model was asked %d times after answers from a directory lookup, want it still 2", vertex.asked())
	}
}
