package serviceconfig

import (
	"testing"
	"time"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AGENTS_PRIMARY_ADDRESS", "AGENTS_PRIMARY_MODEL", "AGENTS_PRIMARY_MODEL_LITE", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "DATABASE_URL", "AGENTS_PRIMARY_AUTO_MIGRATE",
		"AGENTS_PRIMARY_PROMPT_CACHE", "AGENTS_PRIMARY_CACHE_TTL_SECONDS", "AGENTS_PRIMARY_CACHE_MIN_TOKENS",
		"AWS_REGION", "AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID", "AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS",
		"AGENTS_PRIMARY_BROWSER_ID", "AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS",
		"EVENT_BUS_NAME", "EVENTBRIDGE_ENDPOINT",
		"AGENTS_PRIMARY_GUARDRAIL_ID", "AGENTS_PRIMARY_GUARDRAIL_VERSION", "AGENTS_PRIMARY_GUARDRAIL_FAIL_CLOSED", "AGENTS_PRIMARY_MODEL_PRICES",
	} {
		t.Setenv(name, "")
	}
}

func TestFromEnvDefaults(t *testing.T) {
	clearEnv(t)

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8080" || config.Model != "gemini-3.8-flash" || config.ModelLite != "gemini-3.8-flash-lite" || config.GoogleCloudProject != "" || config.GoogleCloudLocation != "global" || config.DatabaseURL != "" || !config.AutoMigrate {
		t.Fatalf("config = %+v", config)
	}
	if !config.Cache || config.CacheTTL != time.Hour || config.CacheMinTokens != 4096 {
		t.Fatalf("cache config = %+v", config)
	}
	if config.AWSRegion != "ap-south-1" || config.SandboxCodeInterpreter != "aws.codeinterpreter.v1" || config.SandboxSessionTimeout != 15*time.Minute {
		t.Fatalf("sandbox config = %+v", config)
	}
	if config.BrowserIdentifier != "aws.browser.v1" || config.BrowserSessionTimeout != 15*time.Minute {
		t.Fatalf("browser config = %+v", config)
	}
	if config.EventBusName != "default" || config.EventBridgeEndpoint != "" {
		t.Fatalf("events config = %+v", config)
	}
	if config.GuardrailIdentifier != "" || config.GuardrailVersion != "" || config.GuardrailFailClosed || config.ModelPrices != "" {
		t.Fatalf("guardrail/pricing config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("AGENTS_PRIMARY_ADDRESS", " :8086 ")
	t.Setenv("AGENTS_PRIMARY_MODEL", " gemini-3.5-flash ")
	t.Setenv("AGENTS_PRIMARY_MODEL_LITE", " gemini-3.5-flash-lite ")
	t.Setenv("GOOGLE_CLOUD_PROJECT", " oryxa-dev ")
	t.Setenv("GOOGLE_CLOUD_LOCATION", " eu ")
	t.Setenv("DATABASE_URL", " postgres://oryxa@localhost:5435/oryxa ")
	t.Setenv("AGENTS_PRIMARY_AUTO_MIGRATE", " false ")
	t.Setenv("AGENTS_PRIMARY_PROMPT_CACHE", " false ")
	t.Setenv("AGENTS_PRIMARY_CACHE_TTL_SECONDS", "7200")
	t.Setenv("AGENTS_PRIMARY_CACHE_MIN_TOKENS", "2048")
	t.Setenv("AWS_REGION", " us-east-1 ")
	t.Setenv("AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID", " interpreter-1 ")
	t.Setenv("AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS", "3600")
	t.Setenv("AGENTS_PRIMARY_BROWSER_ID", " browser-1 ")
	t.Setenv("AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS", "120")
	t.Setenv("EVENT_BUS_NAME", " oryxa-local ")
	t.Setenv("EVENTBRIDGE_ENDPOINT", " http://localhost:4566 ")
	t.Setenv("AGENTS_PRIMARY_GUARDRAIL_ID", " gr-123 ")
	t.Setenv("AGENTS_PRIMARY_GUARDRAIL_VERSION", " 7 ")
	t.Setenv("AGENTS_PRIMARY_GUARDRAIL_FAIL_CLOSED", " true ")
	t.Setenv("AGENTS_PRIMARY_MODEL_PRICES", ` {"gemini-3.8-flash":{"input":0.30,"output":2.50,"cached":0.075}} `)

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8086" || config.Model != "gemini-3.5-flash" || config.ModelLite != "gemini-3.5-flash-lite" || config.GoogleCloudProject != "oryxa-dev" || config.GoogleCloudLocation != "eu" || config.DatabaseURL != "postgres://oryxa@localhost:5435/oryxa" || config.AutoMigrate {
		t.Fatalf("config = %+v", config)
	}
	if config.Cache || config.CacheTTL != 2*time.Hour || config.CacheMinTokens != 2048 {
		t.Fatalf("cache config = %+v", config)
	}
	if config.AWSRegion != "us-east-1" || config.SandboxCodeInterpreter != "interpreter-1" || config.SandboxSessionTimeout != time.Hour {
		t.Fatalf("sandbox config = %+v", config)
	}
	if config.BrowserIdentifier != "browser-1" || config.BrowserSessionTimeout != 2*time.Minute {
		t.Fatalf("browser config = %+v", config)
	}
	if config.EventBusName != "oryxa-local" || config.EventBridgeEndpoint != "http://localhost:4566" {
		t.Fatalf("events config = %+v", config)
	}
	if config.GuardrailIdentifier != "gr-123" || config.GuardrailVersion != "7" || !config.GuardrailFailClosed {
		t.Fatalf("guardrail config = %+v", config)
	}
	if config.ModelPrices == "" {
		t.Fatalf("model prices were dropped: %+v", config)
	}
}

func TestFromEnvRejectsInvalidAutoMigrate(t *testing.T) {
	clearEnv(t)
	t.Setenv("AGENTS_PRIMARY_AUTO_MIGRATE", "sometimes")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil")
	}
}

func TestFromEnvRejectsInvalidCacheMinTokens(t *testing.T) {
	clearEnv(t)
	t.Setenv("AGENTS_PRIMARY_CACHE_MIN_TOKENS", "many")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil for a min-token count that is not a number")
	}
	t.Setenv("AGENTS_PRIMARY_CACHE_MIN_TOKENS", "-1")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil for a negative min-token count")
	}
}

func TestFromEnvRejectsInvalidGuardrail(t *testing.T) {
	clearEnv(t)
	t.Setenv("AGENTS_PRIMARY_GUARDRAIL_ID", "gr-123")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil when the guardrail version is missing")
	}
	t.Setenv("AGENTS_PRIMARY_GUARDRAIL_VERSION", "DRAFT")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil for the mutable DRAFT guardrail version")
	}
	t.Setenv("AGENTS_PRIMARY_GUARDRAIL_VERSION", "")
	t.Setenv("AGENTS_PRIMARY_GUARDRAIL_ID", "")
	t.Setenv("AGENTS_PRIMARY_MODEL_PRICES", "not json")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil for model prices that are not JSON")
	}
}

func TestFromEnvRejectsInvalidSessionTimeout(t *testing.T) {
	clearEnv(t)
	t.Setenv("AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS", "30")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil for a sandbox timeout below one minute")
	}
	t.Setenv("AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS", "")
	t.Setenv("AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS", "later")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil for a browser timeout that is not a number")
	}
}
