package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
)

const defaultAWSRegion = "ap-south-1"

// Config is what primary reads for itself. The listener is the host's.
// DatabaseURL and AutoMigrate stay here, unlike a service the host migrates
// and mounts on the shared pool: cmd/devui is the only thing that opens
// PostgreSQL today, through adksession, and it needs a pool of its own for
// ADK's tables rather than the host's shared one.
type Config struct {
	// Model is the main chat model the agents run on, and ModelLite the
	// cheaper model planning and bulk work are routed to.
	Model     string
	ModelLite string
	// GoogleCloudProject and GoogleCloudLocation are where Vertex AI serves
	// the models.
	GoogleCloudProject  string
	GoogleCloudLocation string
	// DatabaseURL backs ADK sessions and state. AutoMigrate lets ADK create or
	// update its tables inside the agents_primary PostgreSQL schema.
	DatabaseURL string
	AutoMigrate bool
	// Cache turns Vertex explicit prompt caching on, with the TTL a cache
	// lives and the smallest prefix worth caching. Below MinTokens the request
	// is left to Vertex's implicit caching.
	Cache          bool
	CacheTTL       time.Duration
	CacheMinTokens int
	// AWSRegion serves the AgentCore sandbox and browser.
	// SandboxCodeInterpreter and BrowserIdentifier are either AWS's built-in
	// identifiers or provisioned resources with organization-specific network,
	// recording, profile and IAM controls.
	AWSRegion              string
	SandboxCodeInterpreter string
	SandboxSessionTimeout  time.Duration
	BrowserIdentifier      string
	BrowserSessionTimeout  time.Duration
	// EventBusName is the EventBridge bus agents primary publishes on.
	// EventBridgeEndpoint overrides the endpoint locally; when empty the AWS
	// SDK resolves AWS_ENDPOINT_URL itself.
	EventBusName        string
	EventBridgeEndpoint string
	// GuardrailIdentifier and GuardrailVersion screen input and output with
	// Amazon Bedrock Guardrails through the standalone ApplyGuardrail API. An
	// empty identifier disables screening; the version must be a numbered
	// snapshot, never DRAFT. GuardrailFailClosed blocks when the guardrail
	// itself fails instead of passing content through.
	GuardrailIdentifier string
	GuardrailVersion    string
	GuardrailFailClosed bool
	// ModelPrices overrides the price table used for run costs, as JSON of
	// per-million USD rates by model.
	ModelPrices string
	// AppsURL is the apps service's own URL. When set, devui builds the apps
	// toolbox (agents.AppsToolbox), which reaches every app a member has
	// connected through Composio; when empty, Krude loads no apps toolbox.
	AppsURL string
	// RagURL is rag's own URL. When set, Krude gets the knowledge toolbox,
	// which searches the member's and their organization's collections.
	RagURL string
	// TeamsURL is Teams' own URL. When set, primary and specialist agents get
	// a read-only organization directory toolbox.
	TeamsURL string
	// KnowledgeMinRelevance is the reranker relevance below which Krude's
	// knowledge search leaves a passage out; zero keeps every passage. It is
	// on the scale of the reranker rag runs (see agents.KnowledgeConfig).
	KnowledgeMinRelevance float64
	// MemoryAgentEngine is the Vertex AI agent engine whose memory bank keeps
	// what Krude remembers across a member's threads. Without it Krude has no
	// memory: nothing is written and nothing is recalled. Its bank must have
	// similarity search configured. MemoryRecalled bounds how many memories
	// one search returns; zero leaves the default. MemoryLocation is the
	// region the engine is in: agent engines are regional, while the models
	// are usually served from the global endpoint, so it defaults to
	// GoogleCloudLocation only for a deployment that runs both in one region.
	MemoryAgentEngine string
	MemoryRecalled    int
	MemoryLocation    string
	// ArtifactBucket is the S3 bucket artifacts are kept in. Without it they
	// are held in memory and go with the process.
	ArtifactBucket string
	// S3Endpoint overrides AWS for artifacts, for the LocalStack emulator.
	S3Endpoint string
	// AuthURL is auth's own URL: agent tokens for a member-absent run are
	// requested at {AuthURL}/v1/internal/agent-tokens.
	AuthURL string
	// ServiceKeyID is the KMS key agents signs its service tokens with
	// (serviceauth.Signer), naming itself to auth when it asks for an agent
	// token on a member's behalf.
	ServiceKeyID string
	// AllowedOrigins may send cookie-authenticated requests that change
	// something, such as starting a run, from a member's browser.
	AllowedOrigins []string
	// RunTimeout bounds one run of the run API.
	RunTimeout time.Duration
	// DatabaseMaxConns sizes the pool the service keeps for ADK's tables,
	// apart from the pool the other services share.
	DatabaseMaxConns int
	// EventsQueueURL is the queue batching's trigger batches arrive on, and
	// SQSEndpoint overrides where it is read from, for the emulator. Without
	// a queue, nothing wakes Krude for a member's apps.
	EventsQueueURL string
	SQSEndpoint    string
	// WorkflowQueueURL is the callback queue used by the Step Functions
	// Standard workflow. The remaining values let the API start executions
	// and create one EventBridge Scheduler schedule per workflow.
	WorkflowQueueURL         string
	WorkflowStateMachineARN  string
	WorkflowSchedulerRoleARN string
	WorkflowScheduleDLQARN   string
	WorkflowSchedulerGroup   string
	StepFunctionsEndpoint    string
	SchedulerEndpoint        string
	// WorkflowLocal is an explicit development-only in-process adapter used
	// to exercise the real API and model without AWS orchestration resources.
	WorkflowLocal bool
	// WakeupBatchSize and WakeupMaxWait decide when a member's waiting app
	// events wake Krude: once this many wait, or the oldest has waited this
	// long, whichever comes first. Batching already decided when they make a
	// batch, so the size defaults to one: a batch wakes Krude as it arrives.
	WakeupBatchSize int
	WakeupMaxWait   time.Duration
}

// FromEnv reads primary's configuration through getenv, which a host layers
// its own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	autoMigrate, err := boolValue(getenv, "AGENTS_PRIMARY_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	cache, err := boolValue(getenv, "AGENTS_PRIMARY_PROMPT_CACHE", true)
	if err != nil {
		return Config{}, err
	}
	cacheTTL, err := secondsValue(getenv, "AGENTS_PRIMARY_CACHE_TTL_SECONDS", time.Hour, time.Minute, 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cacheMinTokens, err := intValue(getenv, "AGENTS_PRIMARY_CACHE_MIN_TOKENS", 4096, 0, 1_000_000)
	if err != nil {
		return Config{}, err
	}
	sandboxTimeout, err := secondsValue(getenv, "AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS", 15*time.Minute, time.Minute, 8*time.Hour)
	if err != nil {
		return Config{}, err
	}
	browserTimeout, err := secondsValue(getenv, "AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS", 15*time.Minute, time.Minute, 8*time.Hour)
	if err != nil {
		return Config{}, err
	}
	guardrailFailClosed, err := boolValue(getenv, "AGENTS_PRIMARY_GUARDRAIL_FAIL_CLOSED", false)
	if err != nil {
		return Config{}, err
	}
	workflowLocal, err := boolValue(getenv, "AGENTS_WORKFLOW_LOCAL", false)
	if err != nil {
		return Config{}, err
	}
	guardrailIdentifier := strings.TrimSpace(getenv("AGENTS_PRIMARY_GUARDRAIL_ID"))
	guardrailVersion := strings.TrimSpace(getenv("AGENTS_PRIMARY_GUARDRAIL_VERSION"))
	if guardrailIdentifier != "" {
		if guardrailVersion == "" {
			return Config{}, errors.New("AGENTS_PRIMARY_GUARDRAIL_VERSION is required when AGENTS_PRIMARY_GUARDRAIL_ID is set")
		}
		if strings.EqualFold(guardrailVersion, "DRAFT") {
			return Config{}, errors.New("AGENTS_PRIMARY_GUARDRAIL_VERSION must be a numbered snapshot, not DRAFT")
		}
	}
	runTimeout, err := secondsValue(getenv, "AGENTS_PRIMARY_RUN_TIMEOUT_SECONDS", 10*time.Minute, time.Minute, time.Hour)
	if err != nil {
		return Config{}, err
	}
	databaseMaxConns, err := intValue(getenv, "AGENTS_PRIMARY_DATABASE_MAX_CONNS", 8, 1, 256)
	if err != nil {
		return Config{}, err
	}
	knowledgeMinRelevance, err := floatValue(getenv, "AGENTS_PRIMARY_KNOWLEDGE_MIN_RELEVANCE", 0, 0, 1)
	if err != nil {
		return Config{}, err
	}
	// Batching decides when a member's triggers make a batch, so by default
	// one that arrives wakes Krude at once rather than waiting on a second
	// rule of agents' own.
	wakeupBatchSize, err := intValue(getenv, "AGENTS_WAKEUP_BATCH_SIZE", 1, 1, 100)
	if err != nil {
		return Config{}, err
	}
	// Memory Bank coerces anything above 100 down to it, so the ceiling is
	// its own rather than one this service invents.
	memoryRecalled, err := intValue(getenv, "AGENTS_PRIMARY_MEMORY_RECALLED", 0, 0, 100)
	if err != nil {
		return Config{}, err
	}
	wakeupMaxWait, err := secondsValue(getenv, "AGENTS_WAKEUP_MAX_WAIT_SECONDS", 2*time.Minute, 10*time.Second, 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	allowedOrigins, err := originList(getenv, "AGENTS_ALLOWED_ORIGINS")
	if err != nil {
		return Config{}, err
	}
	googleCloudLocation := valueOrDefault(getenv, "GOOGLE_CLOUD_LOCATION", "global")
	modelPrices := strings.TrimSpace(getenv("AGENTS_PRIMARY_MODEL_PRICES"))
	if _, err := pricing.ParseOverrides(modelPrices); err != nil {
		return Config{}, err
	}
	return Config{
		Model:               valueOrDefault(getenv, "AGENTS_PRIMARY_MODEL", "gemini-3.5-flash-lite"),
		ModelLite:           valueOrDefault(getenv, "AGENTS_PRIMARY_MODEL_LITE", "gemini-3.5-flash-lite"),
		GoogleCloudProject:  strings.TrimSpace(getenv("GOOGLE_CLOUD_PROJECT")),
		GoogleCloudLocation: googleCloudLocation,
		DatabaseURL:         strings.TrimSpace(getenv("DATABASE_URL")),
		AutoMigrate:         autoMigrate,
		Cache:               cache,
		CacheTTL:            cacheTTL,
		CacheMinTokens:      cacheMinTokens,
		AWSRegion:           valueOrDefault(getenv, "AWS_REGION", defaultAWSRegion),
		SandboxCodeInterpreter: valueOrDefault(
			getenv, "AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID",
			"aws.codeinterpreter.v1",
		),
		SandboxSessionTimeout: sandboxTimeout,
		BrowserIdentifier: valueOrDefault(
			getenv, "AGENTS_PRIMARY_BROWSER_ID",
			"aws.browser.v1",
		),
		BrowserSessionTimeout:    browserTimeout,
		EventBusName:             valueOrDefault(getenv, "EVENT_BUS_NAME", "default"),
		EventBridgeEndpoint:      strings.TrimSpace(getenv("EVENTBRIDGE_ENDPOINT")),
		GuardrailIdentifier:      guardrailIdentifier,
		GuardrailVersion:         guardrailVersion,
		GuardrailFailClosed:      guardrailFailClosed,
		ModelPrices:              modelPrices,
		AppsURL:                  strings.TrimSpace(getenv("APPS_URL")),
		RagURL:                   strings.TrimSpace(getenv("RAG_URL")),
		TeamsURL:                 strings.TrimRight(strings.TrimSpace(getenv("TEAMS_URL")), "/"),
		KnowledgeMinRelevance:    knowledgeMinRelevance,
		MemoryAgentEngine:        strings.TrimSpace(getenv("AGENTS_PRIMARY_MEMORY_AGENT_ENGINE")),
		MemoryRecalled:           memoryRecalled,
		MemoryLocation:           valueOrDefault(getenv, "AGENTS_PRIMARY_MEMORY_LOCATION", googleCloudLocation),
		ArtifactBucket:           strings.TrimSpace(getenv("AGENTS_PRIMARY_ARTIFACT_BUCKET")),
		S3Endpoint:               artifactEndpoint(getenv),
		AuthURL:                  strings.TrimSpace(getenv("AUTH_URL")),
		ServiceKeyID:             strings.TrimSpace(getenv("AGENTS_PRIMARY_SERVICE_KEY_ID")),
		AllowedOrigins:           allowedOrigins,
		RunTimeout:               runTimeout,
		DatabaseMaxConns:         databaseMaxConns,
		EventsQueueURL:           strings.TrimSpace(getenv("AGENTS_EVENTS_QUEUE_URL")),
		SQSEndpoint:              sqsEndpoint(getenv),
		WorkflowQueueURL:         strings.TrimSpace(getenv("AGENTS_WORKFLOW_QUEUE_URL")),
		WorkflowStateMachineARN:  strings.TrimSpace(getenv("AGENTS_WORKFLOW_STATE_MACHINE_ARN")),
		WorkflowSchedulerRoleARN: strings.TrimSpace(getenv("AGENTS_WORKFLOW_SCHEDULER_ROLE_ARN")),
		WorkflowScheduleDLQARN:   strings.TrimSpace(getenv("AGENTS_WORKFLOW_SCHEDULE_DLQ_ARN")),
		WorkflowSchedulerGroup:   valueOrDefault(getenv, "AGENTS_WORKFLOW_SCHEDULER_GROUP", "default"),
		StepFunctionsEndpoint:    serviceEndpoint(getenv, "AWS_ENDPOINT_URL_SFN"),
		SchedulerEndpoint:        serviceEndpoint(getenv, "AWS_ENDPOINT_URL_SCHEDULER"),
		WorkflowLocal:            workflowLocal,
		WakeupBatchSize:          wakeupBatchSize,
		WakeupMaxWait:            wakeupMaxWait,
	}, nil
}

// originList reads a comma-separated list of absolute origins, such as
// https://app.oryxa.ai, refusing anything else rather than silently allowing
// less than was meant.
func originList(getenv func(string) string, name string) ([]string, error) {
	var origins []string
	for _, value := range strings.Split(getenv(name), ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("%s must list absolute origins such as https://app.example.com", name)
		}
		origins = append(origins, parsed.Scheme+"://"+parsed.Host)
	}
	return origins, nil
}

// sqsEndpoint is the SQS endpoint override, the service-specific one ahead of
// the generic, as apps reads it.
func sqsEndpoint(getenv func(string) string) string {
	for _, name := range []string{"AWS_ENDPOINT_URL_SQS", "AWS_ENDPOINT_URL"} {
		if value := strings.TrimRight(strings.TrimSpace(getenv(name)), "/"); value != "" {
			return value
		}
	}
	return ""
}

func serviceEndpoint(getenv func(string) string, specific string) string {
	for _, name := range []string{specific, "AWS_ENDPOINT_URL"} {
		if value := strings.TrimRight(strings.TrimSpace(getenv(name)), "/"); value != "" {
			return value
		}
	}
	return ""
}

// artifactEndpoint is the S3 endpoint override, the service-specific one
// ahead of the generic, exactly as files reads it.
func artifactEndpoint(getenv func(string) string) string {
	for _, name := range []string{"AWS_ENDPOINT_URL_S3", "AWS_ENDPOINT_URL"} {
		if value := strings.TrimRight(strings.TrimSpace(getenv(name)), "/"); value != "" {
			return value
		}
	}
	return ""
}

func valueOrDefault(getenv func(string) string, name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
		return value
	}
	return fallback
}

func boolValue(getenv func(string) string, name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func secondsValue(getenv func(string) string, name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number of seconds", name)
	}
	duration := time.Duration(seconds) * time.Second
	if duration < minimum || duration > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d seconds", name, int(minimum/time.Second), int(maximum/time.Second))
	}
	return duration, nil
}

func floatValue(getenv func(string) string, name string, fallback, minimum, maximum float64) (float64, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a number", name)
	}
	if parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %g and %g", name, minimum, maximum)
	}
	return parsed, nil
}

func intValue(getenv func(string) string, name string, fallback, minimum, maximum int) (int, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number", name)
	}
	if parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return parsed, nil
}
