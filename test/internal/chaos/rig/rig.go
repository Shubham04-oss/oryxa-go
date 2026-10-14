// Package rig boots an isolated copy of the whole Oryxa stack for chaos
// testing and tears it down again.
//
// Isolation is the point. The rig gives a run its own PostgreSQL database and
// its own event bus, queues, bucket and tables (all named with a run prefix),
// fronts auth with a stub, replaces Composio and Bedrock with fakes, and
// starts the process behind an egress proxy that refuses every call to the
// internet. So nothing a payload does can reach the development database,
// the staging Cognito pool, or a paid API.
package rig

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/test/internal/chaos/stubauth"
)

// Options say where the rig lives and what it runs.
type Options struct {
	Root     string // repository root
	WorkDir  string // scratch: binaries, logs, state
	RunID    string // becomes the resource prefix; lower-case letters and digits
	Services string // ORYXA_SERVICES
	Keep     bool   // leave the database and AWS resources behind on Down

	OryxaAddr    string
	StubAddr     string
	ComposioAddr string
	BedrockAddr  string
	// PipedreamAddr serves FakePipedream. Nothing in oryxa is pointed at it
	// yet: apps learns Pipedream only once its broker seam reads PIPEDREAM_*.
	PipedreamAddr string
	EgressAddr    string
	PprofAddr     string

	PostgresAdminURL string
	LocalStack       string
	SES              string
	SMTP             string
	Mailpit          string

	// Env is extra environment for the oryxa process, applied last.
	Env map[string]string

	// PostgresContainer is where the disk headroom is read, and MinFreeBytes
	// the least free space a run may start with. Zero takes the defaults, 3 GiB.
	PostgresContainer string
	MinFreeBytes      int64
}

// Defaults fills what was left empty.
func (opt *Options) Defaults() {
	set := func(target *string, value string) {
		if *target == "" {
			*target = value
		}
	}
	set(&opt.OryxaAddr, "127.0.0.1:18090")
	set(&opt.StubAddr, "127.0.0.1:18091")
	set(&opt.ComposioAddr, "127.0.0.1:18092")
	set(&opt.BedrockAddr, "127.0.0.1:18093")
	set(&opt.PipedreamAddr, "127.0.0.1:18094")
	set(&opt.EgressAddr, "127.0.0.1:18098")
	set(&opt.PprofAddr, "127.0.0.1:18099")
	set(&opt.PostgresAdminURL, "postgres://oryxa:oryxa-dev-password@localhost:5435/postgres?sslmode=disable")
	set(&opt.LocalStack, "http://localhost:4566")
	set(&opt.SES, "http://localhost:4567")
	set(&opt.SMTP, "127.0.0.1:1025")
	set(&opt.Mailpit, "http://127.0.0.1:8025")
	set(&opt.Services, "teams,billing,files,knowledge,rag,apps,batching,email,agents,realtime")
	set(&opt.PostgresContainer, DefaultPostgresContainer)
	if opt.MinFreeBytes == 0 {
		opt.MinFreeBytes = 3 << 30
	}
}

// State is what a probe needs to find a running rig.
type State struct {
	RunID          string            `json:"run_id"`
	Prefix         string            `json:"prefix"`
	Database       string            `json:"database"`
	DatabaseURL    string            `json:"database_url"`
	OryxaURL       string            `json:"oryxa_url"`
	PprofURL       string            `json:"pprof_url"`
	StubURL        string            `json:"stub_url"`
	ComposioURL    string            `json:"composio_url"`
	BedrockURL     string            `json:"bedrock_url"`
	PipedreamURL   string            `json:"pipedream_url"`
	LocalStackURL  string            `json:"localstack_url"`
	MailpitURL     string            `json:"mailpit_url"`
	LogPath        string            `json:"log_path"`
	PID            int               `json:"pid"`
	Services       []string          `json:"services"`
	Queues         map[string]string `json:"queues"`
	DeadLetters    map[string]string `json:"dead_letters"`
	Bucket         string            `json:"bucket"`
	Bus            string            `json:"bus"`
	ComposioSecret string            `json:"composio_webhook_secret"`
	DodoWebhookKey string            `json:"dodo_webhook_key"`
	RealtimeSecret string            `json:"realtime_purge_secret"`
	StartedAt      time.Time         `json:"started_at"`
}

// Rig is a running stack.
type Rig struct {
	Options Options
	State   State

	Stub      *stubauth.Stub
	Composio  *FakeComposio
	Bedrock   *FakeBedrock
	Pipedream *FakePipedream
	Egress    *Egress
	Clients   *Clients

	servers []*http.Server
	oryxa   *exec.Cmd
	logFile *os.File
}

var idPattern = regexp.MustCompile(`^[a-z0-9]{4,24}$`)

// Up builds and starts everything. On error it tears down what it started.
func Up(ctx context.Context, opt Options) (*Rig, error) {
	opt.Defaults()
	if !idPattern.MatchString(opt.RunID) {
		return nil, fmt.Errorf("run id %q must be 4 to 24 lower-case letters or digits", opt.RunID)
	}
	rig := &Rig{Options: opt}
	if err := rig.up(ctx); err != nil {
		rig.Down(context.Background())
		return nil, err
	}
	return rig, nil
}

func (rig *Rig) up(ctx context.Context) error {
	opt := rig.Options
	prefix := "c" + opt.RunID
	for _, dir := range []string{"bin", "logs", "home", "tmp"} {
		if err := os.MkdirAll(filepath.Join(opt.WorkDir, dir), 0o755); err != nil {
			return err
		}
	}

	if free, total, err := Headroom(ctx, opt.PostgresContainer); err != nil {
		logf("WARNING: cannot check disk headroom (%v); make sure the Docker VM has room before a heavy run", err)
	} else if free < opt.MinFreeBytes {
		return fmt.Errorf("the Docker disk has %s free of %s, under the %s a run needs: a full disk stops every container on it, "+
			"including the development database. Free space first (docker system df shows what is reclaimable), or lower --min-free-gb at your own risk",
			GiB(free), GiB(total), GiB(opt.MinFreeBytes))
	} else {
		logf("disk headroom: %s free of %s", GiB(free), GiB(total))
	}

	logf("building binaries")
	oryxaBin, err := rig.build("oryxa-chaos", "./test/cmd/oryxa-chaos")
	if err != nil {
		return err
	}

	clients, err := NewClients(ctx, Emulators{LocalStack: opt.LocalStack, Region: "ap-south-1"})
	if err != nil {
		return err
	}
	rig.Clients = clients

	database := "chaos_" + opt.RunID
	logf("creating database %s", database)
	if err := createDatabase(ctx, opt.PostgresAdminURL, database); err != nil {
		return err
	}
	databaseURL := strings.Replace(opt.PostgresAdminURL, "/postgres?", "/"+database+"?", 1)

	logf("installing auth's schema (the agents service reads auth_users directly)")
	if err := rig.migrateAuth(ctx, databaseURL); err != nil {
		return err
	}

	rig.State = State{
		RunID: opt.RunID, Prefix: prefix, Database: database, DatabaseURL: databaseURL,
		OryxaURL: "http://" + opt.OryxaAddr, PprofURL: "http://" + opt.PprofAddr,
		StubURL: "http://" + opt.StubAddr, ComposioURL: "http://" + opt.ComposioAddr, BedrockURL: "http://" + opt.BedrockAddr,
		PipedreamURL:  "http://" + opt.PipedreamAddr,
		LocalStackURL: opt.LocalStack, MailpitURL: opt.Mailpit,
		LogPath:  filepath.Join(opt.WorkDir, "logs", "oryxa.log"),
		Services: strings.Split(opt.Services, ","),
		Queues:   map[string]string{}, DeadLetters: map[string]string{},
		Bus: prefix, Bucket: prefix + "-files",
		ComposioSecret: "chaos-composio-secret-" + opt.RunID,
		DodoWebhookKey: "whsec_" + base64.StdEncoding.EncodeToString([]byte("chaos-dodo-secret-"+opt.RunID+"-0123456789")),
		RealtimeSecret: "chaos-realtime-secret-" + opt.RunID + "-0123456789abcdef",
		StartedAt:      time.Now().UTC(),
	}

	logf("creating event bus %s and provisioning queues", rig.State.Bus)
	if err := clients.CreateBus(ctx, rig.State.Bus); err != nil {
		return fmt.Errorf("create bus: %w", err)
	}
	if err := rig.provision(ctx); err != nil {
		return err
	}

	logf("starting fakes: auth stub, Composio, Pipedream, Bedrock, egress guard")
	rig.Stub = stubauth.New()
	rig.Composio = NewFakeComposio()
	rig.Pipedream = NewFakePipedream()
	rig.Bedrock = NewFakeBedrock()
	if err := rig.serve(opt.StubAddr, rig.Stub); err != nil {
		return err
	}
	if err := rig.serve(opt.ComposioAddr, controlled(rig.Composio, rig.Composio.SetMode, rig.Composio.Requests)); err != nil {
		return err
	}
	if err := rig.serve(opt.PipedreamAddr, controlled(rig.Pipedream, rig.Pipedream.SetMode, rig.Pipedream.Requests)); err != nil {
		return err
	}
	if err := rig.serve(opt.BedrockAddr, controlled(rig.Bedrock, rig.Bedrock.SetMode, rig.Bedrock.Requests)); err != nil {
		return err
	}
	if rig.Egress, err = NewEgress(opt.EgressAddr); err != nil {
		return err
	}

	logf("starting oryxa (%s)", opt.Services)
	if err := rig.startOryxa(oryxaBin); err != nil {
		return err
	}
	if err := rig.waitHealthy(ctx, 90*time.Second); err != nil {
		return err
	}
	if err := rig.writeState(); err != nil {
		return err
	}
	logf("stack ready: %s (state in %s)", rig.State.OryxaURL, filepath.Join(opt.WorkDir, "state.json"))
	return nil
}

// migrateAuth installs auth's tables without running auth. The agents service
// joins auth_users and auth_memberships to read a member, so they must exist
// even though auth itself is a stub here.
func (rig *Rig) migrateAuth(ctx context.Context, databaseURL string) error {
	bin, err := rig.build("oryxa", "./cmd/oryxa")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, "migrate")
	cmd.Dir = rig.Options.Root
	cmd.Env = append(isolatedEnv(rig.Options.WorkDir), "DATABASE_URL="+databaseURL, "ORYXA_SERVICES=auth", "AWS_REGION=ap-south-1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("migrate auth: %w\n%s", err, out)
	}
	return nil
}

func (rig *Rig) build(name, pkg string) (string, error) {
	bin := filepath.Join(rig.Options.WorkDir, "bin", name)
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	cmd.Dir = rig.Options.Root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build %s: %w\n%s", pkg, err, out)
	}
	return bin, nil
}

// provisionSpec is one provisioner and how to feed and read it.
type provisionSpec struct {
	name, pkg string
	env       func(prefix string) map[string]string
	queueKey  string // key under which the queue URL is exposed to the process
	queueName string
}

func (rig *Rig) provisioners() []provisionSpec {
	events := func(name, queueVar string) func(string) map[string]string {
		return func(prefix string) map[string]string {
			return map[string]string{queueVar: prefix + "-" + name + "-events"}
		}
	}
	return []provisionSpec{
		{"workspace-events", "./internal/workspace/infra/events/cmd/provision", events("workspace", "WORKSPACE_EVENTS_QUEUE_NAME"), "WORKSPACE_EVENTS_QUEUE_URL", "workspace"},
		{"teams-events", "./teams/infra/events/cmd/provision", events("teams", "TEAMS_EVENTS_QUEUE_NAME"), "TEAMS_EVENTS_QUEUE_URL", "teams"},
		{"billing-events", "./billing/infra/events/cmd/provision", events("billing", "BILLING_EVENTS_QUEUE_NAME"), "BILLING_EVENTS_QUEUE_URL", "billing"},
		{"knowledge-events", "./knowledge/infra/events/cmd/provision", events("knowledge", "KNOWLEDGE_EVENTS_QUEUE_NAME"), "KNOWLEDGE_EVENTS_QUEUE_URL", "knowledge"},
		{"apps-events", "./apps/infra/events/cmd/provision", events("apps", "APPS_EVENTS_QUEUE_NAME"), "APPS_EVENTS_QUEUE_URL", "apps"},
		{"batching-events", "./batching/infra/events/cmd/provision", events("batching", "BATCHING_EVENTS_QUEUE_NAME"), "BATCHING_EVENTS_QUEUE_URL", "batching"},
		{"agents-events", "./agents/primary/infra/events/cmd/provision", events("agents", "AGENTS_EVENTS_QUEUE_NAME"), "AGENTS_EVENTS_QUEUE_URL", "agents"},
		{"realtime-events", "./realtime/infra/events/cmd/provision", events("realtime", "REALTIME_EVENTS_QUEUE_NAME"), "REALTIME_EVENTS_QUEUE_URL", "realtime"},
		{"rag-events", "./rag/infra/vectors/cmd/provision", events("rag", "RAG_EVENTS_QUEUE_NAME"), "RAG_EVENTS_QUEUE_URL", "rag"},
		{"files-storage", "./files/infra/storage/cmd/provision", func(prefix string) map[string]string {
			return map[string]string{"FILES_BUCKET_NAME": prefix + "-files", "FILES_EVENTS_QUEUE_NAME": prefix + "-files-events"}
		}, "FILES_EVENTS_QUEUE_URL", "files"},
		{"email-delivery", "./email/infra/delivery/cmd/provision", func(prefix string) map[string]string {
			return map[string]string{
				"EMAIL_QUEUE_NAME": prefix + "-email-events", "EMAIL_DELIVERIES_TABLE": prefix + "-email-deliveries",
				"EMAIL_SUPPRESSIONS_TABLE": prefix + "-email-suppressions", "SES_EVENT_PUBLISHING": "false",
				"EMAIL_FROM_ADDRESS": "no-reply@example.com", "SES_CONFIGURATION_SET": prefix + "-email",
			}
		}, "EMAIL_QUEUE_URL", "email"},
		{"agents-artifacts", "./agents/primary/infra/artifacts/cmd/provision", func(prefix string) map[string]string {
			return map[string]string{"AGENTS_PRIMARY_ARTIFACT_BUCKET": prefix + "-artifacts"}
		}, "", ""},
	}
}

// provisionEnv is the environment provisioners run in: emulators only.
func (rig *Rig) provisionEnv(extra map[string]string) []string {
	env := isolatedEnv(rig.Options.WorkDir)
	base := map[string]string{
		"AWS_REGION": "ap-south-1", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
		"AWS_EC2_METADATA_DISABLED": "true", "AWS_CONFIG_FILE": "/dev/null", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null",
		"AWS_ENDPOINT_URL": rig.Options.LocalStack, "AWS_ENDPOINT_URL_S3": rig.Options.LocalStack,
		"AWS_ENDPOINT_URL_SESV2": rig.Options.SES, "EVENT_BUS_NAME": rig.State.Bus,
	}
	for key, value := range extra {
		base[key] = value
	}
	for key, value := range base {
		env = append(env, key+"="+value)
	}
	return env
}

func (rig *Rig) provision(ctx context.Context) error {
	prefix := rig.State.Prefix
	for _, spec := range rig.provisioners() {
		bin, err := rig.build("prov-"+spec.name, spec.pkg)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, bin)
		cmd.Dir = rig.Options.Root
		cmd.Env = rig.provisionEnv(spec.env(prefix))
		out, err := cmd.Output()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return fmt.Errorf("provision %s: %w: %s", spec.name, err, strings.TrimSpace(string(exit.Stderr)))
			}
			return fmt.Errorf("provision %s: %w", spec.name, err)
		}
		if spec.queueKey == "" {
			continue
		}
		var result struct {
			QueueURL string `json:"queue_url"`
			DLQ      string `json:"dead_letter_queue_url"`
		}
		if err := json.Unmarshal(out, &result); err != nil || result.QueueURL == "" {
			return fmt.Errorf("provision %s: no queue_url in %q (%v)", spec.name, strings.TrimSpace(string(out)), err)
		}
		rig.State.Queues[spec.queueName] = result.QueueURL
		if result.DLQ != "" {
			rig.State.DeadLetters[spec.queueName] = result.DLQ
		}
	}
	return nil
}

func (rig *Rig) serve(address string, handler http.Handler) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	rig.servers = append(rig.servers, server)
	go func() { _ = server.Serve(listener) }()
	return nil
}

// oryxaEnv is the whole environment of the process under test.
func (rig *Rig) oryxaEnv() []string {
	opt, state := rig.Options, rig.State
	queues := func(name string) string { return state.Queues[name] }
	values := map[string]string{
		"ORYXA_ADDRESS": opt.OryxaAddr, "ORYXA_SERVICES": opt.Services, "ORYXA_AUTO_MIGRATE": "true",
		"ORYXA_PPROF_ADDRESS": opt.PprofAddr,
		"DATABASE_URL":        state.DatabaseURL, "AUTH_URL": state.StubURL,
		"DEPLOYMENT_ENVIRONMENT": "chaos", "LOG_LEVEL": "info",
		"EVENT_BUS_NAME": state.Bus, "AWS_REGION": "ap-south-1",
		// A run lives and dies with one process, so its minted keys cover every
		// event it sees, and a forged one must be refused rather than reported.
		"ORYXA_EVENT_SIGNATURES": "enforce",

		// Emulators only. Real credentials are unreachable: the config files are
		// /dev/null, HOME is empty, and the egress proxy refuses the rest.
		"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
		"AWS_EC2_METADATA_DISABLED": "true", "AWS_CONFIG_FILE": "/dev/null", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null",
		"AWS_ENDPOINT_URL_EVENTBRIDGE": opt.LocalStack, "AWS_ENDPOINT_URL_SQS": opt.LocalStack,
		"AWS_ENDPOINT_URL_S3": opt.LocalStack, "AWS_ENDPOINT_URL_DYNAMODB": opt.LocalStack,
		"AWS_ENDPOINT_URL_SESV2": opt.SES, "EVENTBRIDGE_ENDPOINT": opt.LocalStack, "SQS_ENDPOINT": opt.LocalStack,
		"AWS_ENDPOINT_URL_BEDROCK_RUNTIME": state.BedrockURL,
		"HTTPS_PROXY":                      "http://" + opt.EgressAddr, "HTTP_PROXY": "http://" + opt.EgressAddr,
		"NO_PROXY":          "127.0.0.1,localhost,::1,.localhost.localstack.cloud,localhost.localstack.cloud",
		"GCE_METADATA_HOST": "127.0.0.1:9", "NO_GCE_CHECK": "true",

		"TEAMS_ALLOWED_ORIGINS": "http://localhost:3000", "FILES_ALLOWED_ORIGINS": "http://localhost:3000",
		"BILLING_ALLOWED_ORIGINS": "http://localhost:3000", "KNOWLEDGE_ALLOWED_ORIGINS": "http://localhost:3000",
		"RAG_ALLOWED_ORIGINS": "http://localhost:3000", "APPS_ALLOWED_ORIGINS": "http://localhost:3000",
		"AGENTS_ALLOWED_ORIGINS": "http://localhost:3000",

		"WORKSPACE_EVENTS_QUEUE_URL": queues("workspace"),
		"TEAMS_EVENTS_QUEUE_URL":     queues("teams"), "BILLING_EVENTS_QUEUE_URL": queues("billing"),
		"KNOWLEDGE_EVENTS_QUEUE_URL": queues("knowledge"), "APPS_EVENTS_QUEUE_URL": queues("apps"),
		"BATCHING_EVENTS_QUEUE_URL": queues("batching"), "AGENTS_EVENTS_QUEUE_URL": queues("agents"),
		"RAG_EVENTS_QUEUE_URL": queues("rag"), "FILES_EVENTS_QUEUE_URL": queues("files"),
		"EMAIL_QUEUE_URL": queues("email"), "REALTIME_EVENTS_QUEUE_URL": queues("realtime"),

		// Realtime is on for its route, POST /v1/realtime/events, which takes
		// calls signed with this secret. It has no Worker to purge through:
		// nothing publishes auth's membership events here, and a purge would
		// only fail and wait on the queue.
		"REALTIME_WORKER_URL": "http://127.0.0.1:9", "REALTIME_PURGE_SECRET": state.RealtimeSecret,

		"BILLING_URL": "http://localhost:3000/settings/billing", "BILLING_PLANS_FILE": filepath.Join(opt.Root, "billing/plans.example.json"),
		"DODO_PAYMENTS_ENVIRONMENT": "test_mode", "DODO_PAYMENTS_API_KEY": "chaos-fake-dodo-key",
		"DODO_PAYMENTS_WEBHOOK_KEY": state.DodoWebhookKey,

		"FILES_BUCKET_NAME": state.Bucket,

		"COMPOSIO_API_KEY": "chaos-fake-composio-key", "COMPOSIO_BASE_URL": state.ComposioURL + "/api/v3.1",
		"COMPOSIO_WEBHOOK_SECRET": state.ComposioSecret, "APPS_PUBLIC_BASE_URL": state.OryxaURL,

		"EMAIL_FROM_ADDRESS": "no-reply@example.com", "EMAIL_FROM_NAME": "Oryxa", "EMAIL_SMTP_ADDR": opt.SMTP,
		"EMAIL_DELIVERIES_TABLE": state.Prefix + "-email-deliveries", "EMAIL_SUPPRESSIONS_TABLE": state.Prefix + "-email-suppressions",
		"SES_CONFIGURATION_SET": state.Prefix + "-email", "SES_EVENT_PUBLISHING": "false",

		"RAG_DEFAULT_EMBEDDING_PROVIDER": "bedrock", "RAG_DEFAULT_EMBEDDING_MODEL": "amazon.titan-embed-text-v2:0",
		"RAG_DEFAULT_METRIC": "cosine",

		"GOOGLE_CLOUD_PROJECT": "chaos-fake-project", "GOOGLE_CLOUD_LOCATION": "global",
		"AGENTS_PRIMARY_MODEL": "gemini-3.5-flash-lite", "AGENTS_PRIMARY_MODEL_LITE": "gemini-3.5-flash-lite",
		"AGENTS_PRIMARY_AUTO_MIGRATE": "true", "AGENTS_PRIMARY_RUN_TIMEOUT_SECONDS": "60",
		"AGENTS_PRIMARY_DATABASE_MAX_CONNS": "8", "AGENTS_PRIMARY_ARTIFACT_BUCKET": state.Prefix + "-artifacts",
		"AGENTS_WORKFLOW_LOCAL": "true",
	}
	for key, value := range opt.Env {
		values[key] = value
	}
	env := isolatedEnv(opt.WorkDir)
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

// isolatedEnv is the little of the caller's environment the rig passes on.
// HOME and TMPDIR are emptied so no credentials file or cache is found.
func isolatedEnv(workDir string) []string {
	env := []string{"HOME=" + filepath.Join(workDir, "home"), "TMPDIR=" + filepath.Join(workDir, "tmp")}
	for _, name := range []string{"PATH", "GOPATH", "GOROOT", "GOCACHE", "GOMODCACHE", "GOTOOLCHAIN", "GOFLAGS"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func (rig *Rig) startOryxa(bin string) error {
	logFile, err := os.OpenFile(rig.State.LogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	rig.logFile = logFile
	cmd := exec.Command(bin)
	cmd.Dir = rig.Options.Root
	cmd.Env = rig.oryxaEnv()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start oryxa: %w", err)
	}
	rig.oryxa = cmd
	rig.State.PID = cmd.Process.Pid
	return nil
}

func (rig *Rig) waitHealthy(ctx context.Context, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	exited := make(chan error, 1)
	go func() { exited <- rig.oryxa.Wait() }()
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return fmt.Errorf("oryxa exited during startup (%v):\n%s", err, tail(rig.State.LogPath, 40))
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		response, err := http.Get(rig.State.OryxaURL + "/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				go func() { <-exited }() // keep Wait reaped; the process is up
				return nil
			}
		}
	}
	return fmt.Errorf("oryxa was not healthy after %s:\n%s", limit, tail(rig.State.LogPath, 40))
}

func (rig *Rig) writeState() error {
	encoded, err := json.MarshalIndent(rig.State, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(rig.Options.WorkDir, "state.json"), encoded, 0o644)
}

// Down stops everything and, unless Keep is set, removes the run's database
// and AWS resources. Errors are logged, not returned: teardown must go on.
func (rig *Rig) Down(ctx context.Context) {
	if rig.oryxa != nil && rig.oryxa.Process != nil {
		_ = syscall.Kill(-rig.oryxa.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			for range 150 {
				if syscall.Kill(rig.oryxa.Process.Pid, 0) != nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
		}
		_ = syscall.Kill(-rig.oryxa.Process.Pid, syscall.SIGKILL)
	}
	if rig.Egress != nil {
		if lines := rig.Egress.Summary(); len(lines) > 0 {
			logf("outbound calls the system tried to make (all refused):")
			for _, line := range lines {
				logf("  %4d× %s", rig.Egress.Attempts()[line], line)
			}
		}
		rig.Egress.Close()
	}
	for _, server := range rig.servers {
		_ = server.Close()
	}
	if rig.logFile != nil {
		_ = rig.logFile.Close()
	}
	if rig.Options.Keep {
		logf("keeping database %s and resources prefixed %s", rig.State.Database, rig.State.Prefix)
		return
	}
	if rig.Clients != nil && rig.State.Prefix != "" {
		if removed, err := rig.Clients.Cleanup(ctx, rig.State.Prefix); err != nil {
			logf("cleanup: %v", err)
		} else {
			logf("removed %d emulator resources", len(removed))
		}
	}
	if rig.State.Database != "" {
		if err := dropDatabase(ctx, rig.Options.PostgresAdminURL, rig.State.Database); err != nil {
			logf("drop database: %v", err)
		} else {
			logf("dropped database %s", rig.State.Database)
		}
	}
}

// Wait blocks until the context ends or oryxa exits on its own.
func (rig *Rig) Wait(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
		if rig.oryxa != nil && rig.oryxa.Process != nil && syscall.Kill(rig.oryxa.Process.Pid, 0) != nil {
			return fmt.Errorf("oryxa exited on its own:\n%s", tail(rig.State.LogPath, 60))
		}
	}
}

func createDatabase(ctx context.Context, adminURL, name string) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	return nil
}

func dropDatabase(ctx context.Context, adminURL, name string) error {
	if !strings.HasPrefix(name, "chaos_") {
		return fmt.Errorf("refusing to drop %q: not a chaos database", name)
	}
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `DROP DATABASE IF EXISTS `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`)
	return err
}

// CleanupRun removes a run's leftovers from its run id alone, for a rig that
// died without tearing itself down.
func CleanupRun(ctx context.Context, opt Options) error {
	opt.Defaults()
	if !idPattern.MatchString(opt.RunID) {
		return fmt.Errorf("run id %q is not valid", opt.RunID)
	}
	clients, err := NewClients(ctx, Emulators{LocalStack: opt.LocalStack, Region: "ap-south-1"})
	if err != nil {
		return err
	}
	removed, err := clients.Cleanup(ctx, "c"+opt.RunID)
	if err != nil {
		return err
	}
	logf("removed %d emulator resources", len(removed))
	if err := dropDatabase(ctx, opt.PostgresAdminURL, "chaos_"+opt.RunID); err != nil {
		return err
	}
	logf("dropped database chaos_%s", opt.RunID)
	return nil
}

// LoadState reads the state file of a running rig.
func LoadState(workDir string) (State, error) {
	var state State
	raw, err := os.ReadFile(filepath.Join(workDir, "state.json"))
	if err != nil {
		return state, fmt.Errorf("no running rig here (%w); start one with: chaos rig up", err)
	}
	return state, json.Unmarshal(raw, &state)
}

// controlled adds /_fake/mode and /_fake/stats to a fake so a probe in another
// process can steer it.
func controlled(fake http.Handler, setMode func(string), requests func() int64) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/_fake/mode" && request.Method == http.MethodPut:
			setMode(request.URL.Query().Get("mode"))
			writer.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/_fake/stats":
			writeJSON(writer, http.StatusOK, map[string]int64{"requests": requests()})
		default:
			fake.ServeHTTP(writer, request)
		}
	})
}

func tail(path string, lines int) string {
	file, err := os.Open(path)
	if err != nil {
		return "(no log)"
	}
	defer file.Close()
	ring := make([]string, 0, lines)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 8<<20)
	for scanner.Scan() {
		if len(ring) == lines {
			ring = ring[1:]
		}
		ring = append(ring, scanner.Text())
	}
	return strings.Join(ring, "\n")
}

func logf(format string, args ...any) { log.Printf("rig: "+format, args...) }
