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
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestMountWithoutTeamsURLAnswersUnavailableNotPanic needs the shared local
// PostgreSQL, pointed at a throwaway database (never oryxa or oryxa_ui):
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/teams_nil_fix?sslmode=disable' go test ./agents/primary
//
// Without TEAMS_URL, Mount leaves teamspaceService, roomService and
// interactionService as nil *Service pointers. Boxing one of those directly
// into an interface-typed field (assembly.Options, httpserver.Config) makes
// the interface compare non-nil, so the routes below would call a method on
// a nil receiver and panic instead of answering 503 with the service's own
// error code. This reproduces the crash end to end: it fails (by panicking,
// or by answering the wrong status) without the fix in service.go, and
// passes with it.
func TestMountWithoutTeamsURLAnswersUnavailableNotPanic(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agents-primary-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// auth stands in for the auth service's membership lookup, so member()
	// resolves the caller without a real deployment: every organization
	// answers the same membership.
	auth := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"membership": map[string]string{"user_id": "user_1", "role": "member"},
			"agent":      false,
		})
	}))
	defer auth.Close()

	configured := map[string]string{
		"GOOGLE_CLOUD_PROJECT": "oryxa-test-project",
		"AUTH_URL":             auth.URL,
		// TEAMS_URL is deliberately left unset: it is what triggers the bug.
	}
	plat := platform(configured, database)
	// assembly.Build reaches AWS AgentCore's client constructors, which
	// validate a region and credential provider synchronously but never make
	// a network call building the session managers this test never uses.
	plat.AWS = aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}

	if err := Migrate(ctx, plat); err != nil {
		t.Fatal(err)
	}
	service, err := Mount(ctx, plat)
	if err != nil {
		t.Fatal(err)
	}
	if service.Close != nil {
		defer service.Close()
	}

	organizationID := fmt.Sprintf("org_teams_nil_%d", time.Now().UnixNano())
	defer func() {
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM agents_primary.member_agents WHERE organization_id=$1`, organizationID)
		_, _ = database.Pool.Exec(context.Background(), `DELETE FROM agents_primary.agents_outbox WHERE detail->>'organization_id'=$1`, organizationID)
	}()

	cases := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode string
	}{
		{
			name:     "teamspace room messages",
			method:   http.MethodGet,
			path:     "/v1/organizations/" + organizationID + "/teamspaces/tsp_1/messages",
			wantCode: "rooms_unavailable",
		},
		{
			name:     "teamspaces list",
			method:   http.MethodGet,
			path:     "/v1/organizations/" + organizationID + "/teamspaces",
			wantCode: "teamspaces_unavailable",
		},
		{
			name:     "agent requests with teamspace_id",
			method:   http.MethodPost,
			path:     "/v1/organizations/" + organizationID + "/agent-requests",
			body:     `{"target_agent_id":"agt_target","teamspace_id":"tsp_1","purpose":"test","task":"test"}`,
			wantCode: "interactions_unavailable",
		},
		{
			name:     "agent-request listings with teamspace_id",
			method:   http.MethodGet,
			path:     "/v1/organizations/" + organizationID + "/agent-requests?teamspace_id=tsp_1",
			wantCode: "interactions_unavailable",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(testCase.method, testCase.path, strings.NewReader(testCase.body))
			request.Header.Set("Authorization", "Bearer member-token")
			recorder := httptest.NewRecorder()
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("%s panicked instead of answering 503: %v", testCase.name, recovered)
					}
				}()
				service.Handler.ServeHTTP(recorder, request)
			}()
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s: status = %d, body = %s, want %d", testCase.name, recorder.Code, recorder.Body.String(), http.StatusServiceUnavailable)
			}
			var decoded struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("%s: decode body %q: %v", testCase.name, recorder.Body.String(), err)
			}
			if decoded.Error.Code != testCase.wantCode {
				t.Fatalf("%s: error code = %q, want %q", testCase.name, decoded.Error.Code, testCase.wantCode)
			}
		})
	}
}
