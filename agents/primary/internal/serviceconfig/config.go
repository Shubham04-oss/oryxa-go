package serviceconfig

import (
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
)

const defaultAWSRegion = "ap-south-1"

// The live backends AGENTS_PRIMARY_LIVE_BACKEND names.
const (
	LiveBackendAgentCore  = "agentcore"
	LiveBackendCloudflare = "cloudflare"
)

// ArtifactR2 is the R2 bucket artifacts are written to and its S3 API
// credentials.
type ArtifactR2 struct {
	Endpoint        string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

// LiveOnCloudflare reports whether an organization's sandbox and browser run
// on Cloudflare.
func (config Config) LiveOnCloudflare(organizationID string) bool {
	return config.LiveBackend == LiveBackendCloudflare || config.LiveCloudflareOrgs[organizationID]
}

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
	// DeliveryThinking is the Gemini 3 thinking level asked of the model on
	// every call of a run that delivers an agent request, and of no other
	// run. Empty leaves each model its own default, as before it was set.
	DeliveryThinking genai.ThinkingLevel
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
	// LiveBackend runs the sandbox and browser tools on "agentcore" (the
	// default) or "cloudflare", the oryxa-edge Worker at EdgeURL
	// (docs/design/live-layer). LiveCloudflareOrgs are organizations, by id,
	// that use Cloudflare while the default is still AgentCore: the rollout
	// is staging, then our organization, then everyone (A6, A11). Cloudflare
	// needs EdgeURL and ServiceKeyID, which signs the edge tokens.
	LiveBackend        string
	LiveCloudflareOrgs map[string]bool
	EdgeURL            string
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
	// AnswerGuardrailIdentifier and AnswerGuardrailVersion name the Bedrock
	// guardrail the answer guard checks every answer another member will
	// read with (internal/answerguard), before they read it. Both or
	// neither: without them no answer is checked, and one without the other
	// refuses to start.
	AnswerGuardrailIdentifier string
	AnswerGuardrailVersion    string
	// GuardRelevanceCap is the most the answer guard's relevance check may do
	// in this deployment, whatever an organization's workspace policy asks:
	// hold, shadow or off. It is shadow unless set, so the check holds nobody's
	// answers until a deployment says it may. GuardHoldOrgs are the
	// organizations, by id, the cap does not apply to.
	GuardRelevanceCap guardpolicy.RelevanceMode
	GuardHoldOrgs     map[string]bool
	// SlackConsent switches the Slack consent loop on (AGENTS_PRIMARY_SLACK_CONSENT
	// on|off, off unless set): the router that turns a mention into an ask,
	// the worker that says what became of it in Slack, the tickets on an
	// owner's buttons, and the client that reaches apps' Slack API. It is off
	// in a deployment that has not stored the Slack app's secrets, and it
	// refuses to start on without the ticket key, agents' service key and
	// apps' URL, rather than run half of a loop that decides nothing.
	SlackConsent bool
	// SlackLinkedOnly trusts nothing but a member's Slack account link to say
	// who a Slack user is (AGENTS_PRIMARY_SLACK_LINKED_ONLY, false unless
	// set): an account matched by email alone asks nothing, decides nothing
	// and is sent nothing in Slack. Off, the email still decides, and the
	// router logs who would not pass with links alone.
	SlackLinkedOnly bool
	// SlackTicketKey signs the buttons on an owner's Slack notice
	// (AGENTS_PRIMARY_SLACK_TICKET_KEY, 64 hexadecimal characters): 32 bytes,
	// decoded. It is read only when SlackConsent is on.
	SlackTicketKey []byte
	// SecretsKMSKeyID is the KMS key one-time secrets are sealed under
	// (AGENTS_PRIMARY_SECRETS_KMS_KEY_ID: an ID, an ARN or an alias). Without
	// it there is no secret store, and the routes that share a secret answer
	// that there is none.
	SecretsKMSKeyID string
	// ConsentGuardrailIdentifier and ConsentGuardrailVersion name the Bedrock
	// guardrail that checks whether an answer to a question asked in Slack
	// stands on what its run read, and whether it is about a topic that is
	// never let out (internal/consentguard). Both or neither: without them,
	// with SlackConsent on, every answer to a Slack question is held for its
	// owner's review as unchecked.
	ConsentGuardrailIdentifier string
	ConsentGuardrailVersion    string
	// ConsentPassagesOnly is an opt-in source-quality experiment.
	// It changes knowledge grounding to explicit returned passage text, keeps
	// full capture for copying and other tools, and defaults to false.
	ConsentPassagesOnly bool
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
	// KnowledgeURL is knowledge's own URL, for the read-only status of
	// indexing sources. The host defaults it to itself when knowledge runs.
	KnowledgeURL string
	// TeamsURL is Teams' own URL. When set, primary and specialist agents get
	// a read-only organization directory toolbox.
	TeamsURL string
	// WorkspaceURL exposes read-only ownership and org-chart context to
	// member agents through the workspace service.
	WorkspaceURL string
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
	// ArtifactR2 moves artifacts to Cloudflare R2 (live-layer A7) when its
	// endpoint is set: new versions are written there, under "artifacts/",
	// and ArtifactBucket, when set, is still read for what was saved before.
	ArtifactR2 ArtifactR2
	// Document editor endpoints and secrets for ONLYOFFICE Docs. The JWT
	// secret is Docs' own; the ticket secret signs the URLs Docs is handed for
	// a member's file, is never given to Docs, and must differ from it.
	DocumentServerURL         string
	DocumentServerInternalURL string
	DocumentAPIURL            string
	DocumentJWTSecret         string
	DocumentTicketSecret      string
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
	// TypeSafeAPIKey and TypeSafeBaseURL reach TypeSafe, which serves Jev:
	// the classifier a room asks, on every message, who should answer, what
	// is worth keeping, and whether its brief still describes it. Without a
	// key a room falls back to rules that answer less and keep less.
	TypeSafeAPIKey  string
	TypeSafeBaseURL string

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
	// Half an answer guard would look like one while checking nothing, so
	// it is both or neither.
	answerGuardrailIdentifier := strings.TrimSpace(getenv("AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID"))
	answerGuardrailVersion := strings.TrimSpace(getenv("AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION"))
	if (answerGuardrailIdentifier == "") != (answerGuardrailVersion == "") {
		return Config{}, errors.New("AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID and AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION are set together or not at all: one without the other would check no answer")
	}
	// The consent guardrail is the same: half of one would look like a check
	// and ground nothing.
	consentGuardrailIdentifier := strings.TrimSpace(getenv("AGENTS_PRIMARY_CONSENT_GUARDRAIL_ID"))
	consentGuardrailVersion := strings.TrimSpace(getenv("AGENTS_PRIMARY_CONSENT_GUARDRAIL_VERSION"))
	if (consentGuardrailIdentifier == "") != (consentGuardrailVersion == "") {
		return Config{}, errors.New("AGENTS_PRIMARY_CONSENT_GUARDRAIL_ID and AGENTS_PRIMARY_CONSENT_GUARDRAIL_VERSION are set together or not at all: one without the other would ground no answer")
	}
	consentKnowledgePassagesOnly, err := boolValue(getenv, "AGENTS_PRIMARY_CONSENT_KNOWLEDGE_PASSAGES_ONLY", false)
	if err != nil {
		return Config{}, err
	}
	slackConsent, slackTicketKey, err := slackConsentSettings(getenv)
	if err != nil {
		return Config{}, err
	}
	slackLinkedOnly, err := boolValue(getenv, "AGENTS_PRIMARY_SLACK_LINKED_ONLY", false)
	if err != nil {
		return Config{}, err
	}
	guardRelevanceCap, err := relevanceCap(getenv, "AGENTS_PRIMARY_GUARD_RELEVANCE_CAP")
	if err != nil {
		return Config{}, err
	}
	liveBackend := valueOrDefault(getenv, "AGENTS_PRIMARY_LIVE_BACKEND", LiveBackendAgentCore)
	if liveBackend != LiveBackendAgentCore && liveBackend != LiveBackendCloudflare {
		return Config{}, fmt.Errorf("AGENTS_PRIMARY_LIVE_BACKEND must be %s or %s", LiveBackendAgentCore, LiveBackendCloudflare)
	}
	liveCloudflareOrgs, err := organizationList(getenv, "AGENTS_PRIMARY_LIVE_CLOUDFLARE_ORGS")
	if err != nil {
		return Config{}, err
	}
	edgeURL := strings.TrimRight(strings.TrimSpace(getenv("AGENTS_PRIMARY_EDGE_URL")), "/")
	if liveBackend == LiveBackendCloudflare || len(liveCloudflareOrgs) > 0 {
		parsed, err := url.Parse(edgeURL)
		if edgeURL == "" || err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return Config{}, errors.New("AGENTS_PRIMARY_EDGE_URL must be the edge's absolute URL when any organization's sandbox and browser run on Cloudflare")
		}
		if strings.TrimSpace(getenv("AGENTS_PRIMARY_SERVICE_KEY_ID")) == "" {
			return Config{}, errors.New("AGENTS_PRIMARY_SERVICE_KEY_ID must be set when any organization's sandbox and browser run on Cloudflare: it signs the edge tokens")
		}
	}
	artifactR2 := ArtifactR2{
		Endpoint:        strings.TrimRight(strings.TrimSpace(getenv("AGENTS_PRIMARY_ARTIFACT_ENDPOINT")), "/"),
		Bucket:          valueOrDefault(getenv, "AGENTS_PRIMARY_ARTIFACT_R2_BUCKET", "oryxa-runs"),
		AccessKeyID:     strings.TrimSpace(getenv("AGENTS_PRIMARY_ARTIFACT_ACCESS_KEY_ID")),
		SecretAccessKey: strings.TrimSpace(getenv("AGENTS_PRIMARY_ARTIFACT_SECRET_ACCESS_KEY")),
	}
	if artifactR2.Endpoint != "" {
		parsed, err := url.Parse(artifactR2.Endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return Config{}, errors.New("AGENTS_PRIMARY_ARTIFACT_ENDPOINT must be the R2 account's https URL")
		}
		if artifactR2.AccessKeyID == "" || artifactR2.SecretAccessKey == "" {
			return Config{}, errors.New("AGENTS_PRIMARY_ARTIFACT_ENDPOINT needs AGENTS_PRIMARY_ARTIFACT_ACCESS_KEY_ID and AGENTS_PRIMARY_ARTIFACT_SECRET_ACCESS_KEY")
		}
	}
	guardHoldOrgs, err := organizationList(getenv, "AGENTS_PRIMARY_GUARD_HOLD_ORGS")
	if err != nil {
		return Config{}, err
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
	model := valueOrDefault(getenv, "AGENTS_PRIMARY_MODEL", "gemini-3.5-flash-lite")
	modelLite := valueOrDefault(getenv, "AGENTS_PRIMARY_MODEL_LITE", "gemini-3.5-flash-lite")
	// Krude answers a request on the main model and a specialist on the lite
	// one, so the level has to be one both take.
	deliveryThinking, err := thinkingLevel(getenv, "AGENTS_PRIMARY_DELIVERY_THINKING", map[string]string{
		"AGENTS_PRIMARY_MODEL": model, "AGENTS_PRIMARY_MODEL_LITE": modelLite,
	})
	if err != nil {
		return Config{}, err
	}
	return Config{
		Model:               model,
		ModelLite:           modelLite,
		DeliveryThinking:    deliveryThinking,
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
		BrowserSessionTimeout:      browserTimeout,
		LiveBackend:                liveBackend,
		LiveCloudflareOrgs:         liveCloudflareOrgs,
		EdgeURL:                    edgeURL,
		EventBusName:               valueOrDefault(getenv, "EVENT_BUS_NAME", "default"),
		EventBridgeEndpoint:        strings.TrimSpace(getenv("EVENTBRIDGE_ENDPOINT")),
		GuardrailIdentifier:        guardrailIdentifier,
		GuardrailVersion:           guardrailVersion,
		GuardrailFailClosed:        guardrailFailClosed,
		AnswerGuardrailIdentifier:  answerGuardrailIdentifier,
		AnswerGuardrailVersion:     answerGuardrailVersion,
		GuardRelevanceCap:          guardRelevanceCap,
		GuardHoldOrgs:              guardHoldOrgs,
		SlackConsent:               slackConsent,
		SlackTicketKey:             slackTicketKey,
		SlackLinkedOnly:            slackLinkedOnly,
		SecretsKMSKeyID:            strings.TrimSpace(getenv("AGENTS_PRIMARY_SECRETS_KMS_KEY_ID")),
		ConsentGuardrailIdentifier: consentGuardrailIdentifier,
		ConsentGuardrailVersion:    consentGuardrailVersion,
		ConsentPassagesOnly:        consentKnowledgePassagesOnly,
		ModelPrices:                modelPrices,
		AppsURL:                    strings.TrimSpace(getenv("APPS_URL")),
		RagURL:                     strings.TrimSpace(getenv("RAG_URL")),
		KnowledgeURL:               strings.TrimRight(strings.TrimSpace(getenv("KNOWLEDGE_URL")), "/"),
		TeamsURL:                   strings.TrimRight(strings.TrimSpace(getenv("TEAMS_URL")), "/"),
		WorkspaceURL:               strings.TrimRight(strings.TrimSpace(getenv("WORKSPACE_URL")), "/"),
		KnowledgeMinRelevance:      knowledgeMinRelevance,
		MemoryAgentEngine:          strings.TrimSpace(getenv("AGENTS_PRIMARY_MEMORY_AGENT_ENGINE")),
		MemoryRecalled:             memoryRecalled,
		MemoryLocation:             valueOrDefault(getenv, "AGENTS_PRIMARY_MEMORY_LOCATION", googleCloudLocation),
		ArtifactBucket:             strings.TrimSpace(getenv("AGENTS_PRIMARY_ARTIFACT_BUCKET")),
		S3Endpoint:                 artifactEndpoint(getenv),
		ArtifactR2:                 artifactR2,
		DocumentServerURL:          strings.TrimSpace(getenv("AGENTS_PRIMARY_DOCUMENT_SERVER_URL")),
		DocumentServerInternalURL:  strings.TrimSpace(getenv("AGENTS_PRIMARY_DOCUMENT_SERVER_INTERNAL_URL")),
		DocumentAPIURL:             strings.TrimSpace(getenv("AGENTS_PRIMARY_DOCUMENT_API_URL")),
		DocumentJWTSecret:          strings.TrimSpace(getenv("AGENTS_PRIMARY_DOCUMENT_JWT_SECRET")),
		DocumentTicketSecret:       strings.TrimSpace(getenv("AGENTS_PRIMARY_DOCUMENT_TICKET_SECRET")),
		TypeSafeAPIKey:             strings.TrimSpace(getenv("TYPESAFE_API_KEY")),
		TypeSafeBaseURL:            strings.TrimSpace(getenv("TYPESAFE_BASE_URL")),
		AuthURL:                    strings.TrimSpace(getenv("AUTH_URL")),
		ServiceKeyID:               strings.TrimSpace(getenv("AGENTS_PRIMARY_SERVICE_KEY_ID")),
		AllowedOrigins:             allowedOrigins,
		RunTimeout:                 runTimeout,
		DatabaseMaxConns:           databaseMaxConns,
		EventsQueueURL:             strings.TrimSpace(getenv("AGENTS_EVENTS_QUEUE_URL")),
		SQSEndpoint:                sqsEndpoint(getenv),
		WorkflowQueueURL:           strings.TrimSpace(getenv("AGENTS_WORKFLOW_QUEUE_URL")),
		WorkflowStateMachineARN:    strings.TrimSpace(getenv("AGENTS_WORKFLOW_STATE_MACHINE_ARN")),
		WorkflowSchedulerRoleARN:   strings.TrimSpace(getenv("AGENTS_WORKFLOW_SCHEDULER_ROLE_ARN")),
		WorkflowScheduleDLQARN:     strings.TrimSpace(getenv("AGENTS_WORKFLOW_SCHEDULE_DLQ_ARN")),
		WorkflowSchedulerGroup:     valueOrDefault(getenv, "AGENTS_WORKFLOW_SCHEDULER_GROUP", "default"),
		StepFunctionsEndpoint:      serviceEndpoint(getenv, "AWS_ENDPOINT_URL_SFN"),
		SchedulerEndpoint:          serviceEndpoint(getenv, "AWS_ENDPOINT_URL_SCHEDULER"),
		WorkflowLocal:              workflowLocal,
		WakeupBatchSize:            wakeupBatchSize,
		WakeupMaxWait:              wakeupMaxWait,
	}, nil
}

// ticketKeyHexLength is how long the ticket key is written: 32 bytes as 64
// hexadecimal characters, which is what `openssl rand -hex 32` makes and what
// deploy/scripts/put-secrets.sh stores.
const ticketKeyHexLength = 64

// slackConsentSettings reads the switch of the Slack consent loop, and with it
// on, what the loop cannot run without. Off is the default, and then nothing
// else is read: a deployment that has not stored the Slack app's secrets names
// none of them, and one that names them all but leaves the switch off has said
// so. On, each of the following that is missing stops the service starting,
// naming itself, since a loop with no ticket key would post buttons nobody's
// click decides, and one with no way to reach apps would ask owners for
// consent and never say so in Slack.
func slackConsentSettings(getenv func(string) string) (bool, []byte, error) {
	switch value := strings.ToLower(strings.TrimSpace(getenv("AGENTS_PRIMARY_SLACK_CONSENT"))); value {
	case "", "off":
		return false, nil, nil
	case "on":
	default:
		return false, nil, errors.New("AGENTS_PRIMARY_SLACK_CONSENT must be on or off")
	}
	encoded := strings.TrimSpace(getenv("AGENTS_PRIMARY_SLACK_TICKET_KEY"))
	if encoded == "" {
		return false, nil, errors.New("AGENTS_PRIMARY_SLACK_TICKET_KEY is required when AGENTS_PRIMARY_SLACK_CONSENT is on: it signs the buttons on an owner's notice (64 hexadecimal characters, `openssl rand -hex 32`)")
	}
	key, err := hex.DecodeString(encoded)
	if err != nil || len(encoded) != ticketKeyHexLength {
		// Neither the key nor any part of it goes in the message.
		return false, nil, fmt.Errorf("AGENTS_PRIMARY_SLACK_TICKET_KEY must be %d hexadecimal characters (32 bytes, `openssl rand -hex 32`)", ticketKeyHexLength)
	}
	for _, need := range []struct{ name, why string }{
		{"AGENTS_PRIMARY_SERVICE_KEY_ID", "agents signs its calls to apps' Slack API with it"},
		{"APPS_URL", "agents posts to Slack through apps, which is the process itself in one binary"},
	} {
		if strings.TrimSpace(getenv(need.name)) == "" {
			return false, nil, fmt.Errorf("%s is required when AGENTS_PRIMARY_SLACK_CONSENT is on: %s", need.name, need.why)
		}
	}
	return true, key, nil
}

// thinkingLevels are the thinking levels a setting may name: Gemini 3's, as
// genai.ThinkingLevel has them.
var thinkingLevels = map[string]genai.ThinkingLevel{
	"minimal": genai.ThinkingLevelMinimal,
	"low":     genai.ThinkingLevelLow,
	"medium":  genai.ThinkingLevelMedium,
	"high":    genai.ThinkingLevelHigh,
}

// thinkingLevelsTaken are the levels Google documents the Gemini 3 models
// that do not take all four as taking (ai.google.dev/gemini-api/docs/thinking,
// 2026-09-26). Vertex refuses any other with 400 INVALID_ARGUMENT, "Thinking
// level is unsupported", so every run asking it would fail. A model not
// listed is not checked.
var thinkingLevelsTaken = map[string][]string{
	"gemini-3.8-flash":       {"low", "medium", "high"},
	"gemini-3.7-flash":       {"low", "medium", "high"},
	"gemini-3.1-pro-preview": {"low", "medium", "high"},
	"gemini-3-pro-preview":   {"low", "high"},
}

// thinkingLevel reads a thinking level: minimal, low, medium or high, in any
// case, or nothing. It refuses a level one of the models it is for, named by
// the variables that set them, does not take, rather than let every run on
// that model fail.
func thinkingLevel(getenv func(string) string, name string, models map[string]string) (genai.ThinkingLevel, error) {
	value := strings.ToLower(strings.TrimSpace(getenv(name)))
	if value == "" {
		return "", nil
	}
	level, ok := thinkingLevels[value]
	if !ok {
		return "", fmt.Errorf("%s must be minimal, low, medium or high, a Gemini 3 thinking level, or unset", name)
	}
	variables := slices.Sorted(maps.Keys(models))
	for _, variable := range variables {
		taken, known := thinkingLevelsTaken[models[variable]]
		if known && !slices.Contains(taken, value) {
			return "", fmt.Errorf("%s is %s, which %s, the model %s names, does not take: set %s", name, value, models[variable], variable, orList(taken))
		}
	}
	return level, nil
}

// orList is values as "a, b or c".
func orList(values []string) string {
	if len(values) < 2 {
		return strings.Join(values, "")
	}
	return strings.Join(values[:len(values)-1], ", ") + " or " + values[len(values)-1]
}

// relevanceCap reads the most the answer guard's relevance check may do in
// this deployment: hold, shadow or off, in any case. Unset is shadow, so the
// check holds nobody's answers until a deployment says it may.
func relevanceCap(getenv func(string) string, name string) (guardpolicy.RelevanceMode, error) {
	value := strings.ToLower(strings.TrimSpace(getenv(name)))
	if value == "" {
		return guardpolicy.RelevanceShadow, nil
	}
	mode, err := guardpolicy.ParseRelevanceMode(value)
	if err != nil {
		return "", fmt.Errorf("%s must be hold, shadow or off, or unset for shadow", name)
	}
	return mode, nil
}

// organizationList reads a comma-separated list of organization ids. An id is
// made of letters, digits, "_" and "-", as every id auth makes is; anything
// else is refused, since a list separated by spaces or semicolons would
// otherwise be one id no organization has, and exempt nobody without saying so.
func organizationList(getenv func(string) string, name string) (map[string]bool, error) {
	var organizations map[string]bool
	for _, value := range strings.Split(getenv(name), ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !organizationID.MatchString(value) {
			return nil, fmt.Errorf("%s must list organization ids, which are letters, digits, _ and -, separated by commas", name)
		}
		if organizations == nil {
			organizations = map[string]bool{}
		}
		organizations[value] = true
	}
	return organizations, nil
}

var organizationID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

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
