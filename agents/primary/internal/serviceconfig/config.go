package serviceconfig

import (
	"errors"
	"fmt"
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
	// AuthURL is auth's own URL: agent tokens for a member-absent run are
	// requested at {AuthURL}/v1/internal/agent-tokens.
	AuthURL string
	// ServiceKeyID is the KMS key agents signs its service tokens with
	// (serviceauth.Signer), naming itself to auth when it asks for an agent
	// token on a member's behalf.
	ServiceKeyID string
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
	modelPrices := strings.TrimSpace(getenv("AGENTS_PRIMARY_MODEL_PRICES"))
	if _, err := pricing.ParseOverrides(modelPrices); err != nil {
		return Config{}, err
	}
	return Config{
		Model:               valueOrDefault(getenv, "AGENTS_PRIMARY_MODEL", "gemini-3.8-flash"),
		ModelLite:           valueOrDefault(getenv, "AGENTS_PRIMARY_MODEL_LITE", "gemini-3.8-flash-lite"),
		GoogleCloudProject:  strings.TrimSpace(getenv("GOOGLE_CLOUD_PROJECT")),
		GoogleCloudLocation: valueOrDefault(getenv, "GOOGLE_CLOUD_LOCATION", "global"),
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
		BrowserSessionTimeout: browserTimeout,
		EventBusName:          valueOrDefault(getenv, "EVENT_BUS_NAME", "default"),
		EventBridgeEndpoint:   strings.TrimSpace(getenv("EVENTBRIDGE_ENDPOINT")),
		GuardrailIdentifier:   guardrailIdentifier,
		GuardrailVersion:      guardrailVersion,
		GuardrailFailClosed:   guardrailFailClosed,
		ModelPrices:           modelPrices,
		AppsURL:               strings.TrimSpace(getenv("APPS_URL")),
		AuthURL:               strings.TrimSpace(getenv("AUTH_URL")),
		ServiceKeyID:          strings.TrimSpace(getenv("AGENTS_PRIMARY_SERVICE_KEY_ID")),
	}, nil
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
