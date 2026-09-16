package serviceconfig

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
)

const defaultAWSRegion = "ap-south-1"

type Config struct {
	Address string
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
}

func FromEnv() (Config, error) {
	autoMigrate, err := boolValue("AGENTS_PRIMARY_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	cache, err := boolValue("AGENTS_PRIMARY_PROMPT_CACHE", true)
	if err != nil {
		return Config{}, err
	}
	cacheTTL, err := secondsValue("AGENTS_PRIMARY_CACHE_TTL_SECONDS", time.Hour, time.Minute, 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cacheMinTokens, err := intValue("AGENTS_PRIMARY_CACHE_MIN_TOKENS", 4096, 0, 1_000_000)
	if err != nil {
		return Config{}, err
	}
	sandboxTimeout, err := secondsValue("AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS", 15*time.Minute, time.Minute, 8*time.Hour)
	if err != nil {
		return Config{}, err
	}
	browserTimeout, err := secondsValue("AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS", 15*time.Minute, time.Minute, 8*time.Hour)
	if err != nil {
		return Config{}, err
	}
	guardrailFailClosed, err := boolValue("AGENTS_PRIMARY_GUARDRAIL_FAIL_CLOSED", false)
	if err != nil {
		return Config{}, err
	}
	guardrailIdentifier := strings.TrimSpace(os.Getenv("AGENTS_PRIMARY_GUARDRAIL_ID"))
	guardrailVersion := strings.TrimSpace(os.Getenv("AGENTS_PRIMARY_GUARDRAIL_VERSION"))
	if guardrailIdentifier != "" {
		if guardrailVersion == "" {
			return Config{}, errors.New("AGENTS_PRIMARY_GUARDRAIL_VERSION is required when AGENTS_PRIMARY_GUARDRAIL_ID is set")
		}
		if strings.EqualFold(guardrailVersion, "DRAFT") {
			return Config{}, errors.New("AGENTS_PRIMARY_GUARDRAIL_VERSION must be a numbered snapshot, not DRAFT")
		}
	}
	modelPrices := strings.TrimSpace(os.Getenv("AGENTS_PRIMARY_MODEL_PRICES"))
	if _, err := pricing.ParseOverrides(modelPrices); err != nil {
		return Config{}, err
	}
	return Config{
		Address:             valueOrDefault("AGENTS_PRIMARY_ADDRESS", ":8080"),
		Model:               valueOrDefault("AGENTS_PRIMARY_MODEL", "gemini-3.8-flash"),
		ModelLite:           valueOrDefault("AGENTS_PRIMARY_MODEL_LITE", "gemini-3.8-flash-lite"),
		GoogleCloudProject:  strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT")),
		GoogleCloudLocation: valueOrDefault("GOOGLE_CLOUD_LOCATION", "global"),
		DatabaseURL:         strings.TrimSpace(os.Getenv("DATABASE_URL")),
		AutoMigrate:         autoMigrate,
		Cache:               cache,
		CacheTTL:            cacheTTL,
		CacheMinTokens:      cacheMinTokens,
		AWSRegion:           valueOrDefault("AWS_REGION", defaultAWSRegion),
		SandboxCodeInterpreter: valueOrDefault(
			"AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID",
			"aws.codeinterpreter.v1",
		),
		SandboxSessionTimeout: sandboxTimeout,
		BrowserIdentifier: valueOrDefault(
			"AGENTS_PRIMARY_BROWSER_ID",
			"aws.browser.v1",
		),
		BrowserSessionTimeout: browserTimeout,
		EventBusName:          valueOrDefault("EVENT_BUS_NAME", "default"),
		EventBridgeEndpoint:   strings.TrimSpace(os.Getenv("EVENTBRIDGE_ENDPOINT")),
		GuardrailIdentifier:   guardrailIdentifier,
		GuardrailVersion:      guardrailVersion,
		GuardrailFailClosed:   guardrailFailClosed,
		ModelPrices:           modelPrices,
	}, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func boolValue(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func secondsValue(name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
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

func intValue(name string, fallback, minimum, maximum int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
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
