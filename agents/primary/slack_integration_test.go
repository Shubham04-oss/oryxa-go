package primary

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// TestMountWithTheSlackLoopOnNeedsTeams needs the shared local PostgreSQL,
// pointed at a throwaway database, as the other Mount tests do:
//
//	AGENTS_PRIMARY_TEST_DATABASE_URL='postgres://oryxa:oryxa-dev-password@localhost:5435/teams_nil_fix?sslmode=disable' go test ./agents/primary -run SlackLoop
//
// A request is between two members, whom agents finds through Teams, so the
// loop cannot be on where there is no ledger of requests: the service refuses to
// start, naming TEAMS_URL, rather than run a router that can ask nobody. It
// still starts with the loop off, on the same settings.
func TestMountWithTheSlackLoopOnNeedsTeams(t *testing.T) {
	databaseURL := os.Getenv("AGENTS_PRIMARY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AGENTS_PRIMARY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: databaseURL, ApplicationName: "oryxa-agents-primary-slack-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	auth := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"membership":{"user_id":"user_1","role":"member"},"agent":false}`))
	}))
	t.Cleanup(auth.Close)
	google := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"fake-access-token","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(google.Close)
	fakeGoogleADC(t, google.URL)

	settings := slackOnSettings()
	settings["AUTH_URL"] = auth.URL
	plat := platform(settings, database)
	plat.AWS = aws.Config{Region: "us-east-1", Credentials: awscredentials.NewStaticCredentialsProvider("test", "test", "")}
	if err := Migrate(ctx, plat); err != nil {
		t.Fatal(err)
	}
	if _, err := Mount(ctx, plat); err == nil || !strings.Contains(err.Error(), "TEAMS_URL") {
		t.Fatalf("Mount() with the loop on and no TEAMS_URL = %v, want a refusal naming TEAMS_URL", err)
	}

	// With the loop off, the same settings mount: nothing of it runs.
	settings["AGENTS_PRIMARY_SLACK_CONSENT"] = "off"
	off := platform(settings, database)
	off.AWS = plat.AWS
	service, err := Mount(ctx, off)
	if err != nil {
		t.Fatalf("Mount() with the loop off = %v", err)
	}
	if service.Close != nil {
		t.Cleanup(service.Close)
	}
}
