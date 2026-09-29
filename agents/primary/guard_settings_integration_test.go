package primary

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// mountedAgents migrates and mounts the agents service on database, with auth
// standing in for the sign-in service, the way the test of a service with no
// teams does: every organization answers the same member, user_1. values are
// the settings beyond the ones that make Mount reach its assembly.
func mountedAgents(t *testing.T, database *postgreskit.Database, values map[string]string) host.Service {
	t.Helper()
	ctx := context.Background()
	auth := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"membership": map[string]string{"user_id": "user_1", "role": "member"},
			"agent":      false,
		})
	}))
	t.Cleanup(auth.Close)
	google := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"fake-access-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(google.Close)
	fakeGoogleADC(t, google.URL)

	configured := map[string]string{"GOOGLE_CLOUD_PROJECT": "oryxa-test-project", "AUTH_URL": auth.URL}
	for name, value := range values {
		configured[name] = value
	}
	plat := platform(configured, database)
	plat.AWS = aws.Config{Region: "us-east-1", Credentials: awscredentials.NewStaticCredentialsProvider("test", "test", "")}
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
	return service
}

// TestAMountedServiceKeepsAMembersAnswerGuardLevels needs the shared local
// PostgreSQL, as the test of a service with no teams does:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/teams_nil_fix?sslmode=disable' go test ./agents/primary -run AnswerGuardLevels
//
// It goes from the host's migration, through Mount, to a member's own request:
// the levels they are told at first, the change they make, and what is kept
// and logged for it. The deployment here has a ceiling of off, which no
// organization gets by default, and exempts one organization from it, so what
// each is told of the relevance check is where Mount hands the settings the
// ceiling.
func TestAMountedServiceKeepsAMembersAnswerGuardLevels(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agents-primary-guard-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	// Cleanups run last in, first out, so the service closes before the pool
	// it was mounted on.
	t.Cleanup(database.Close)

	organizationID := fmt.Sprintf("org_guard_settings_%d", time.Now().UnixNano())
	exemptID := organizationID + "_exempt"
	defer func() {
		for _, statement := range []string{
			`DELETE FROM agents_primary.member_agents WHERE organization_id=$1 OR organization_id=$2`,
			`DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1 OR detail->>'organization_id'=$2`,
			`DELETE FROM agents_primary.guard_preferences WHERE organization_id=$1 OR organization_id=$2`,
			`DELETE FROM agents_primary.guard_preference_changes WHERE organization_id=$1 OR organization_id=$2`,
		} {
			_, _ = database.Pool.Exec(context.Background(), statement, organizationID, exemptID)
		}
	}()
	service := mountedAgents(t, database, map[string]string{
		"AGENTS_PRIMARY_GUARD_RELEVANCE_CAP": "OFF",
		"AGENTS_PRIMARY_GUARD_HOLD_ORGS":     "org_elsewhere, " + exemptID,
	})
	call := func(organization, method, body string) (int, string) {
		t.Helper()
		request := httptest.NewRequest(method, "/v1/organizations/"+organization+"/answer-guard/preferences", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer member-token")
		recorder := httptest.NewRecorder()
		service.Handler.ServeHTTP(recorder, request)
		return recorder.Code, strings.TrimSpace(recorder.Body.String())
	}
	view := func(chosenOwner, effectiveOwner, relevance string) string {
		return `{"owner":{"chosen":` + chosenOwner + `,"default":"secrets","floor":"secrets","effective":"` + effectiveOwner + `"},` +
			`"peer":{"chosen":null,"default":"strict","floor":"secrets","effective":"strict"},"relevance":"` + relevance + `","regulated":false}`
	}

	if code, body := call(organizationID, http.MethodGet, ""); code != http.StatusOK || body != view("null", "secrets", "off") {
		t.Fatalf("before any choice = %d %s\nwant                 %s", code, body, view("null", "secrets", "off"))
	}
	if code, body := call(exemptID, http.MethodGet, ""); code != http.StatusOK || body != view("null", "secrets", "hold") {
		t.Fatalf("the organization the ceiling does not apply to = %d %s\nwant                                            %s", code, body, view("null", "secrets", "hold"))
	}
	if code, body := call(organizationID, http.MethodPut, `{"owner":"strict"}`); code != http.StatusOK || body != view(`"strict"`, "strict", "off") {
		t.Fatalf("choosing strict = %d %s\nwant             %s", code, body, view(`"strict"`, "strict", "off"))
	}
	if code, body := call(organizationID, http.MethodGet, ""); code != http.StatusOK || body != view(`"strict"`, "strict", "off") {
		t.Fatalf("read back = %d %s", code, body)
	}
	// Another organization's view of the same member is their own.
	if code, body := call(exemptID, http.MethodGet, ""); code != http.StatusOK || body != view("null", "secrets", "hold") {
		t.Fatalf("the same member elsewhere = %d %s, want nothing chosen", code, body)
	}
	var kept string
	if err := database.Pool.QueryRow(ctx,
		`SELECT owner_level FROM agents_primary.guard_preferences WHERE organization_id=$1 AND user_id='user_1'`, organizationID).Scan(&kept); err != nil || kept != "strict" {
		t.Fatalf("kept owner level = %q, %v; want strict", kept, err)
	}
	if code, body := call(organizationID, http.MethodPut, `{"owner":null}`); code != http.StatusOK || body != view("null", "secrets", "off") {
		t.Fatalf("clearing it = %d %s", code, body)
	}
	var logged int
	if err := database.Pool.QueryRow(ctx,
		`SELECT count(*) FROM agents_primary.guard_preference_changes WHERE organization_id=$1 AND user_id='user_1'`, organizationID).Scan(&logged); err != nil || logged != 2 {
		t.Fatalf("logged %d changes, %v; want the two made", logged, err)
	}
}
