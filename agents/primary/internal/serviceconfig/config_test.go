package serviceconfig

import (
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(map[string]string{}))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Model != "gemini-3.8-flash" || config.ModelLite != "gemini-3.8-flash-lite" || config.GoogleCloudProject != "" || config.GoogleCloudLocation != "global" || config.DatabaseURL != "" || !config.AutoMigrate {
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
	if config.AppsURL != "" || config.AuthURL != "" || config.ServiceKeyID != "" {
		t.Fatalf("apps config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"AGENTS_PRIMARY_MODEL":                           " gemini-3.5-flash ",
		"AGENTS_PRIMARY_MODEL_LITE":                      " gemini-3.5-flash-lite ",
		"GOOGLE_CLOUD_PROJECT":                           " oryxa-dev ",
		"GOOGLE_CLOUD_LOCATION":                          " eu ",
		"DATABASE_URL":                                   " postgres://oryxa@localhost:5435/oryxa ",
		"AGENTS_PRIMARY_AUTO_MIGRATE":                    " false ",
		"AGENTS_PRIMARY_PROMPT_CACHE":                    " false ",
		"AGENTS_PRIMARY_CACHE_TTL_SECONDS":               "7200",
		"AGENTS_PRIMARY_CACHE_MIN_TOKENS":                "2048",
		"AWS_REGION":                                     " us-east-1 ",
		"AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID":     " interpreter-1 ",
		"AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS": "3600",
		"AGENTS_PRIMARY_BROWSER_ID":                      " browser-1 ",
		"AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS": "120",
		"EVENT_BUS_NAME":                                 " oryxa-local ",
		"EVENTBRIDGE_ENDPOINT":                           " http://localhost:4566 ",
		"AGENTS_PRIMARY_GUARDRAIL_ID":                    " gr-123 ",
		"AGENTS_PRIMARY_GUARDRAIL_VERSION":               " 7 ",
		"AGENTS_PRIMARY_GUARDRAIL_FAIL_CLOSED":           " true ",
		"AGENTS_PRIMARY_MODEL_PRICES":                    ` {"gemini-3.8-flash":{"input":0.30,"output":2.50,"cached":0.075}} `,
		"APPS_URL":                                       " http://localhost:8091 ",
		"AUTH_URL":                                       " http://localhost:8081 ",
		"AGENTS_PRIMARY_SERVICE_KEY_ID":                  " arn:aws:kms:ap-south-1:1:key/agents ",
	}))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Model != "gemini-3.5-flash" || config.ModelLite != "gemini-3.5-flash-lite" || config.GoogleCloudProject != "oryxa-dev" || config.GoogleCloudLocation != "eu" || config.DatabaseURL != "postgres://oryxa@localhost:5435/oryxa" || config.AutoMigrate {
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
	if config.AppsURL != "http://localhost:8091" || config.AuthURL != "http://localhost:8081" || config.ServiceKeyID != "arn:aws:kms:ap-south-1:1:key/agents" {
		t.Fatalf("apps config = %+v", config)
	}
}

func TestFromEnvRejectsInvalidAutoMigrate(t *testing.T) {
	if _, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_AUTO_MIGRATE": "sometimes"})); err == nil {
		t.Fatal("FromEnv() error = nil")
	}
}

func TestFromEnvRejectsInvalidCacheMinTokens(t *testing.T) {
	if _, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_CACHE_MIN_TOKENS": "many"})); err == nil {
		t.Fatal("FromEnv() error = nil for a min-token count that is not a number")
	}
	if _, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_CACHE_MIN_TOKENS": "-1"})); err == nil {
		t.Fatal("FromEnv() error = nil for a negative min-token count")
	}
}

func TestFromEnvRejectsInvalidGuardrail(t *testing.T) {
	if _, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_GUARDRAIL_ID": "gr-123"})); err == nil {
		t.Fatal("FromEnv() error = nil when the guardrail version is missing")
	}
	if _, err := FromEnv(env(map[string]string{
		"AGENTS_PRIMARY_GUARDRAIL_ID": "gr-123", "AGENTS_PRIMARY_GUARDRAIL_VERSION": "DRAFT",
	})); err == nil {
		t.Fatal("FromEnv() error = nil for the mutable DRAFT guardrail version")
	}
	if _, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_MODEL_PRICES": "not json"})); err == nil {
		t.Fatal("FromEnv() error = nil for model prices that are not JSON")
	}
}

func TestFromEnvRejectsInvalidSessionTimeout(t *testing.T) {
	if _, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS": "30"})); err == nil {
		t.Fatal("FromEnv() error = nil for a sandbox timeout below one minute")
	}
	if _, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS": "later"})); err == nil {
		t.Fatal("FromEnv() error = nil for a browser timeout that is not a number")
	}
}
