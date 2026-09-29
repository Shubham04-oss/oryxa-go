package slack

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
	"github.com/Shubham04-oss/oryxa-go/test/identities"
	"github.com/Shubham04-oss/oryxa-go/test/internal/authstub"
)

// The organization the test runs in, and the people in it. Each has a token,
// which the auth stand-in reads as the person's session, and a Slack user.
const organizationID = "org_e2e"

type person struct {
	UserID, Email, Name, Role, Token, SlackID string
}

var (
	// ada asks, in Slack.
	ada = person{UserID: "user-ada", Email: "ada@acme.test", Name: "Ada", Role: "member", Token: "ada", SlackID: "U0ADA"}
	// raya is the one whose agent knows.
	raya = person{UserID: "user-raya", Email: "raya@acme.test", Name: "Raya", Role: "member", Token: "raya", SlackID: "U0RAYA"}
	// root is an administrator, who installs the app.
	root = person{UserID: "user-root", Email: "root@acme.test", Name: "Root", Role: "admin", Token: "root", SlackID: "U0ROOT"}
	// gus is a guest in the Slack workspace: not a member of Oryxa in any way.
	gus = person{UserID: "user-gus", Email: "gus@acme.test", Name: "Gus", Role: "member", Token: "gus", SlackID: "U0GUS"}

	members = []person{ada, raya, root}
)

func init() {
	// The auth stand-in reads the roster in test/identities, and a bearer token
	// is the name of a person in it. This adds the people this test needs.
	for _, who := range append(append([]person(nil), members...), gus) {
		identities.People[who.Token] = identities.Person{
			UserID: who.UserID, Email: who.Email, DisplayName: who.Name, Role: who.Role, InOrg: who != gus,
		}
	}
}

const (
	// Both are a key that only this test knows, that the two services and the
	// stand-ins agree on.
	agentsKeyID  = "alias/e2e-agents-service"
	ticketKeyHex = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	webOrigin    = "https://app.oryxa.test"
	jevKey       = "test-typesafe-key"
)

// syncBuffer is a log two goroutines write.
type syncBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

// stack is the whole thing under test: the oryxa binary running apps and agents
// on a database of its own, and everything it reaches, faked.
type stack struct {
	t       testing.TB
	baseURL string

	aws    *fakeAWS
	slack  *slackfake.Fake
	jev    *fakeJev
	vertex *fakeVertex
	rag    *fakeRag
	logs   *syncBuffer
	// sensitive is what the process must never log, as a test notes it.
	sensitive []string
	// database is the run's own, read by the test where the process's tables are
	// the only place a fact can be seen.
	database *pgxpool.Pool
}

// repoRoot is the repository's root, found from this file.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// freePort finds a port nobody is listening on. It is closed again before it is
// used, which a process racing this test for ports could take, and none does.
func freePort(t testing.TB) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// createDatabase makes a database of its own for this run on the server the URL
// names, and drops it when the test ends, so that nothing of a run is left and
// nothing else's is touched.
func createDatabase(t testing.TB, serverURL string) string {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("SLACK_TEST_DATABASE_URL: %v", err)
	}
	var random [4]byte
	_, _ = rand.Read(random[:])
	name := fmt.Sprintf("slack_e2e_%d_%s", time.Now().Unix(), hex.EncodeToString(random[:]))
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, serverURL)
	if err != nil {
		t.Fatalf("connect to the database server: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create a database for the run: %v", err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		if err != nil {
			t.Logf("drop the run's database %s: %v", name, err)
		}
		admin.Close()
	})
	parsed.Path = "/" + name
	return parsed.String()
}

// seedAuth writes what auth would hold of the organization, which agents and
// the router read as they do in production: its members and their roles. The
// tables are the ones auth's migrations make, and the columns the two read.
func seedAuth(t testing.TB, databaseURL string) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS auth_users (
		    id text PRIMARY KEY,
		    email text NOT NULL,
		    display_name text NOT NULL DEFAULT '',
		    avatar_url text NOT NULL DEFAULT '',
		    created_at timestamptz NOT NULL,
		    updated_at timestamptz NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS auth_users_email_unique ON auth_users (lower(email));
		CREATE TABLE IF NOT EXISTS auth_organizations (
		    id text PRIMARY KEY,
		    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
		    slug text NOT NULL CHECK (length(slug) BETWEEN 2 AND 63),
		    created_by text NOT NULL REFERENCES auth_users(id),
		    created_at timestamptz NOT NULL,
		    updated_at timestamptz NOT NULL
		);
		CREATE UNIQUE INDEX IF NOT EXISTS auth_organizations_slug_unique ON auth_organizations (lower(slug));
		CREATE TABLE IF NOT EXISTS auth_memberships (
		    organization_id text NOT NULL REFERENCES auth_organizations(id) ON DELETE CASCADE,
		    user_id text NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
		    role text NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
		    joined_at timestamptz NOT NULL,
		    PRIMARY KEY (organization_id, user_id)
		)`); err != nil {
		t.Fatalf("ensure auth tables: %v", err)
	}
	now := time.Now().UTC()
	for _, who := range members {
		if _, err := pool.Exec(ctx, `INSERT INTO auth_users (id, email, display_name, created_at, updated_at) VALUES ($1, $2, $3, $4, $4)`,
			who.UserID, who.Email, who.Name, now); err != nil {
			t.Fatalf("seed %s: %v", who.Name, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auth_organizations (id, name, slug, created_by, created_at, updated_at) VALUES ($1, 'Acme', 'acme-e2e', $2, $3, $3)`,
		organizationID, root.UserID, now); err != nil {
		t.Fatalf("seed the organization: %v", err)
	}
	for _, who := range members {
		if _, err := pool.Exec(ctx, `INSERT INTO auth_memberships (organization_id, user_id, role, joined_at) VALUES ($1, $2, $3, $4)`,
			organizationID, who.UserID, who.Role, now); err != nil {
			t.Fatalf("seed %s's place in the organization: %v", who.Name, err)
		}
	}
}

// build compiles the oryxa binary, which is the thing under test, into dir.
func build(t testing.TB, dir string) string {
	t.Helper()
	binary := filepath.Join(dir, "oryxa")
	command := exec.Command("go", "build", "-o", binary, "./cmd/oryxa")
	command.Dir = repoRoot()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build oryxa: %v\n%s", err, output)
	}
	return binary
}

// bootStack starts everything: the stand-ins, the database, the process, and
// installs the Slack app in the organization the way an administrator does.
func bootStack(t *testing.T, databaseServerURL string) *stack {
	t.Helper()
	binary := build(t, t.TempDir())
	databaseURL := createDatabase(t, databaseServerURL)
	seedAuth(t, databaseURL)

	s := &stack{t: t, logs: &syncBuffer{}}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s.database = pool
	s.aws = newFakeAWS(t)
	s.jev = newFakeJev(t)
	s.vertex = newFakeVertex(t)
	s.rag = newFakeRag(t)
	s.slack = slackfake.New(t)
	s.slack.AddUser(slackfake.User{ID: ada.SlackID, Name: "ada", RealName: "Ada", Email: ada.Email, Title: "Account executive"})
	s.slack.AddUser(slackfake.User{ID: raya.SlackID, Name: "raya", RealName: "Raya", Email: raya.Email, Title: "Head of finance"})
	s.slack.AddUser(slackfake.User{ID: root.SlackID, Name: "root", RealName: "Root", Email: root.Email})
	s.slack.AddUser(slackfake.User{ID: gus.SlackID, Name: "gus", RealName: "Gus", Email: gus.Email, Guest: true})
	s.slack.AddChannel(slackfake.Channel{ID: channelGeneral, Name: "general"})
	teams := newFakeTeams(t, organizationID, members)

	// auth, as far as the two services ask it: who a token is, and the agent
	// token an agent exchanges a member's standing grant for, which it proves
	// itself to with a signed service token that KMS verifies.
	awsConfig := aws.Config{
		Region: "us-west-2", BaseEndpoint: aws.String(s.aws.URL()),
		Credentials: awscredentials.NewStaticCredentialsProvider("test", "test", ""),
	}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{
		Keys: kms.NewFromConfig(awsConfig), Audience: "auth",
		Callers: []serviceauth.Caller{{Service: "agents", KeyID: agentsKeyID, Purposes: []string{"agent"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := httptest.NewServer(authstub.NewHandler(authstub.Config{Services: verifier}))
	t.Cleanup(auth.Close)

	port := freePort(t)
	s.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	credentials := writeGoogleCredentials(t, s.vertex.URL())
	slackApp, err := json.Marshal(map[string]string{
		"client_id": s.slack.ClientID(), "client_secret": s.slack.ClientSecret(), "signing_secret": s.slack.SigningSecret(),
	})
	if err != nil {
		t.Fatal(err)
	}
	callers, err := json.Marshal([]serviceauth.Caller{{Service: "agents", KeyID: agentsKeyID, Purposes: []string{"slack"}}})
	if err != nil {
		t.Fatal(err)
	}
	environment := map[string]string{
		"PATH": os.Getenv("PATH"), "HOME": t.TempDir(), "TMPDIR": os.TempDir(),
		"ORYXA_ADDRESS": fmt.Sprintf("127.0.0.1:%d", port), "ORYXA_SERVICES": "apps,agents",
		"DATABASE_URL": databaseURL, "DEPLOYMENT_ENVIRONMENT": "development", "LOG_LEVEL": "info",
		"ORYXA_SHUTDOWN_TIMEOUT": "10s",
		// Nothing reaches AWS but the fake, and nothing is exported.
		"AWS_REGION": "us-west-2", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
		"AWS_ENDPOINT_URL": s.aws.URL(), "AWS_CONFIG_FILE": os.DevNull, "AWS_SHARED_CREDENTIALS_FILE": os.DevNull,
		"AWS_EC2_METADATA_DISABLED": "true", "EVENT_BUS_NAME": "default",
		"OTEL_SDK_DISABLED": "true", "OTEL_TRACES_EXPORTER": "none", "OTEL_METRICS_EXPORTER": "none", "OTEL_LOGS_EXPORTER": "none",
		"AUTH_URL": auth.URL, "TEAMS_URL": teams.URL(), "RAG_URL": s.rag.URL(),

		// apps: the Slack surface, on, against the fake Slack.
		"APPS_PUBLIC_BASE_URL": s.baseURL, "COMPOSIO_API_KEY": "test-composio-key", "APPS_ALLOWED_ORIGINS": webOrigin,
		"APPS_SIGNIN_KMS_KEY_ID": "alias/e2e-apps-signin", "APPS_SIGNIN_REDIRECT_URI": s.baseURL + "/v1/apps/oauth/callback", "APPS_SLACK_SURFACE": "true", "APPS_SLACK_APP": string(slackApp),
		"APPS_SLACK_API_BASE_URL": s.slack.URL(), "APPS_SERVICE_CALLERS": string(callers),

		// agents: a model, the guardrails, the queue, and the loop on.
		"GOOGLE_CLOUD_PROJECT": "oryxa-e2e", "GOOGLE_CLOUD_LOCATION": "global",
		"GOOGLE_APPLICATION_CREDENTIALS": credentials, "GOOGLE_VERTEX_BASE_URL": s.vertex.URL(),
		"AGENTS_ALLOWED_ORIGINS":                   webOrigin,
		"AGENTS_PRIMARY_SERVICE_KEY_ID":            agentsKeyID,
		"AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID":       "gr_answers",
		"AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION":  "1",
		"AGENTS_PRIMARY_CONSENT_GUARDRAIL_ID":      "gr_consent",
		"AGENTS_PRIMARY_CONSENT_GUARDRAIL_VERSION": "1",
		"AGENTS_PRIMARY_SECRETS_KMS_KEY_ID":        "alias/e2e-agents-secrets",
		"AGENTS_PRIMARY_SLACK_CONSENT":             "on",
		// The fake model has no cached contents to offer.
		"AGENTS_PRIMARY_PROMPT_CACHE":     "false",
		"AGENTS_PRIMARY_SLACK_TICKET_KEY": ticketKeyHex,
		"AGENTS_EVENTS_QUEUE_URL":         s.aws.QueueURL(),
		"TYPESAFE_API_KEY":                jevKey, "TYPESAFE_BASE_URL": s.jev.URL(),
	}
	command := exec.Command(binary)
	command.Dir = t.TempDir()
	for name, value := range environment {
		command.Env = append(command.Env, name+"="+value)
	}
	command.Stdout, command.Stderr = s.logs, s.logs
	if err := command.Start(); err != nil {
		t.Fatalf("start oryxa: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(20 * time.Second):
			_ = command.Process.Kill()
			<-exited
		}
		if t.Failed() {
			t.Logf("what oryxa logged:\n%s", tail(s.logs.String(), 24_000))
		}
	})
	s.waitUntilServing(exited)
	s.install()
	return s
}

// tail is the last max bytes of text.
func tail(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return "…" + text[len(text)-max:]
}

func (s *stack) waitUntilServing(exited <-chan error) {
	s.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			s.t.Fatalf("oryxa stopped before it served: %v\n%s", err, tail(s.logs.String(), 24_000))
		default:
		}
		response, err := http.Get(s.baseURL + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("oryxa did not serve in time\n%s", tail(s.logs.String(), 24_000))
}

// call is a call to the process as a person, who is named by their token, and
// its answer, decoded as JSON when it is. It never stops the test, so that a
// goroutine may make it.
func (s *stack) call(who person, method, path string, body any) (int, map[string]any, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, s.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+who.Token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	return response.StatusCode, decoded, nil
}

// api is call for the test's own goroutine: a call that could not be made stops
// the test.
func (s *stack) api(who person, method, path string, body any) (int, map[string]any) {
	s.t.Helper()
	status, decoded, err := s.call(who, method, path, body)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	return status, decoded
}

// install has the administrator install the Slack app in the organization, as
// the Add to Slack link does: the link's state, Slack's code, the callback.
func (s *stack) install() {
	s.t.Helper()
	status, view := s.api(root, http.MethodGet, "/v1/organizations/"+organizationID+"/apps/slack/install", nil)
	if status != http.StatusOK {
		s.t.Fatalf("the install link: %d %v\n%s", status, view, tail(s.logs.String(), 8000))
	}
	link, _ := view["add_to_slack_url"].(string)
	parsed, err := url.Parse(link)
	if err != nil || parsed.Query().Get("state") == "" {
		s.t.Fatalf("the install link %q carries no state", link)
	}
	callback := s.baseURL + "/v1/apps/slack/install/callback?" + url.Values{
		"code": {s.slack.IssueCode(slackfake.DefaultTeamID)}, "state": {parsed.Query().Get("state")},
	}.Encode()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(callback)
	if err != nil {
		s.t.Fatalf("the install callback: %v", err)
	}
	response.Body.Close()
	if response.StatusCode >= 400 {
		s.t.Fatalf("the install callback answered %d\n%s", response.StatusCode, tail(s.logs.String(), 8000))
	}
	status, view = s.api(root, http.MethodGet, "/v1/organizations/"+organizationID+"/apps/slack/install", nil)
	if view["status"] != "active" {
		s.t.Fatalf("the install is %v (%d), want it active", view, status)
	}
}

// eventually waits for what a test expects to come to pass, which takes a few
// hops over the bus, and says what it was waiting for if it does not.
func (s *stack) eventually(what string, within time.Duration, ready func() bool) {
	s.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("gave up waiting for %s\n%s", what, tail(s.logs.String(), 12_000))
}
