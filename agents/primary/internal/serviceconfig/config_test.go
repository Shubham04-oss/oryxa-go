package serviceconfig

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(map[string]string{}))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Model != "gemini-3.5-flash-lite" || config.ModelLite != "gemini-3.5-flash-lite" || config.GoogleCloudProject != "" || config.GoogleCloudLocation != "global" || config.DatabaseURL != "" || !config.AutoMigrate {
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
	if config.AppsURL != "" || config.RagURL != "" || config.KnowledgeURL != "" || config.TeamsURL != "" || config.AuthURL != "" || config.ServiceKeyID != "" {
		t.Fatalf("apps config = %+v", config)
	}
	if len(config.AllowedOrigins) != 0 || config.RunTimeout != 10*time.Minute || config.DatabaseMaxConns != 8 {
		t.Fatalf("run API config = %+v", config)
	}
	// Batching decides when triggers make a batch; one that arrives wakes
	// Krude at once.
	if config.EventsQueueURL != "" || config.WakeupBatchSize != 1 || config.WakeupMaxWait != 2*time.Minute {
		t.Fatalf("wake-up config = %+v", config)
	}
	if config.WorkflowQueueURL != "" || config.WorkflowStateMachineARN != "" || config.WorkflowSchedulerRoleARN != "" || config.WorkflowSchedulerGroup != "default" || config.WorkflowLocal {
		t.Fatalf("workflow config = %+v", config)
	}
	if config.KnowledgeMinRelevance != 0 {
		t.Fatalf("knowledge min relevance = %v, want off", config.KnowledgeMinRelevance)
	}
	if config.DeliveryThinking != "" {
		t.Fatalf("delivery thinking = %q, want none: every model thinks as it does by default", config.DeliveryThinking)
	}
}

// TestFromEnvReadsTheDeliveryThinkingLevel: a delivery's thinking level is
// one of Gemini 3's four, named in any case.
func TestFromEnvReadsTheDeliveryThinkingLevel(t *testing.T) {
	for value, want := range map[string]genai.ThinkingLevel{
		" Low ":   genai.ThinkingLevelLow,
		"minimal": genai.ThinkingLevelMinimal,
		"MEDIUM":  genai.ThinkingLevelMedium,
		"high":    genai.ThinkingLevelHigh,
		"":        "",
		"   ":     "",
	} {
		config, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_DELIVERY_THINKING": value}))
		if err != nil || config.DeliveryThinking != want {
			t.Errorf("AGENTS_PRIMARY_DELIVERY_THINKING=%q: %q, %v, want %q", value, config.DeliveryThinking, err, want)
		}
	}
}

// TestFromEnvRefusesADeliveryThinkingLevelItCannotUse: anything but a
// thinking level refuses to start, a token budget included, and so does a
// level either model a delivery runs on does not take, which Vertex would
// refuse on every delivery; a model whose levels are not documented here is
// not second-guessed.
func TestFromEnvRefusesADeliveryThinkingLevelItCannotUse(t *testing.T) {
	for _, value := range []string{"extreme", "none", "off", "0", "1024", "-1", "low,high"} {
		_, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_DELIVERY_THINKING": value}))
		if err == nil || !strings.Contains(err.Error(), "AGENTS_PRIMARY_DELIVERY_THINKING must be minimal, low, medium or high") {
			t.Errorf("AGENTS_PRIMARY_DELIVERY_THINKING=%q: error = %v, want one naming the levels", value, err)
		}
	}
	for name, values := range map[string]map[string]string{
		"the main model": {"AGENTS_PRIMARY_MODEL": "gemini-3.8-flash"},
		"the lite model": {"AGENTS_PRIMARY_MODEL_LITE": "gemini-3.7-flash"},
		"a pro model":    {"AGENTS_PRIMARY_MODEL": "gemini-3-pro-preview"},
	} {
		level := "minimal"
		if name == "a pro model" {
			level = "medium"
		}
		values["AGENTS_PRIMARY_DELIVERY_THINKING"] = level
		_, err := FromEnv(env(values))
		if err == nil || !strings.Contains(err.Error(), "does not take") {
			t.Errorf("%s with %s: error = %v, want a refusal to start", name, level, err)
		}
	}
	for _, values := range []map[string]string{
		{"AGENTS_PRIMARY_MODEL": "gemini-3.8-flash", "AGENTS_PRIMARY_DELIVERY_THINKING": "low"},
		{"AGENTS_PRIMARY_MODEL": "gemini-9-flash", "AGENTS_PRIMARY_DELIVERY_THINKING": "minimal"},
	} {
		if _, err := FromEnv(env(values)); err != nil {
			t.Errorf("%v: error = %v, want it accepted", values, err)
		}
	}
}

func TestFromEnvReadsTeamsURL(t *testing.T) {
	config, err := FromEnv(env(map[string]string{"TEAMS_URL": " https://teams.example.test/ "}))
	if err != nil {
		t.Fatal(err)
	}
	if config.TeamsURL != "https://teams.example.test" {
		t.Fatalf("TeamsURL = %q", config.TeamsURL)
	}
}

func TestFromEnvReadsKnowledgeURL(t *testing.T) {
	config, err := FromEnv(env(map[string]string{"KNOWLEDGE_URL": " https://knowledge.example.test/ "}))
	if err != nil || config.KnowledgeURL != "https://knowledge.example.test" {
		t.Fatalf("KnowledgeURL = %q, error = %v", config.KnowledgeURL, err)
	}
}

// A run on a model the price table does not know reports no cost, so the
// models a deployment gets without configuring any have to be priced.
func TestTheDefaultModelsArePriced(t *testing.T) {
	config, err := FromEnv(env(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	table, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{config.Model, config.ModelLite} {
		if _, priced := table.Rates(model); !priced {
			t.Errorf("the default model %q has no price", model)
		}
	}
}

func TestFromEnvReadsTheKnowledgeRelevanceFloor(t *testing.T) {
	config, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_KNOWLEDGE_MIN_RELEVANCE": " 0.2 "}))
	if err != nil || config.KnowledgeMinRelevance != 0.2 {
		t.Fatalf("FromEnv() = %v, %v", config.KnowledgeMinRelevance, err)
	}
	for _, value := range []string{"-0.1", "1.5", "high"} {
		if _, err := FromEnv(env(map[string]string{"AGENTS_PRIMARY_KNOWLEDGE_MIN_RELEVANCE": value})); err == nil {
			t.Errorf("FromEnv() error = nil for %q", value)
		}
	}
}

func TestFromEnvReadsWakeups(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"AGENTS_EVENTS_QUEUE_URL":        " http://localhost:4566/000000000000/agents-events ",
		"AWS_ENDPOINT_URL_SQS":           "http://localhost:4566/",
		"AGENTS_WAKEUP_BATCH_SIZE":       "3",
		"AGENTS_WAKEUP_MAX_WAIT_SECONDS": "30",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.EventsQueueURL != "http://localhost:4566/000000000000/agents-events" || config.SQSEndpoint != "http://localhost:4566" ||
		config.WakeupBatchSize != 3 || config.WakeupMaxWait != 30*time.Second {
		t.Fatalf("wake-up config = %+v", config)
	}
	for name, value := range map[string]string{"AGENTS_WAKEUP_BATCH_SIZE": "0", "AGENTS_WAKEUP_MAX_WAIT_SECONDS": "1"} {
		if _, err := FromEnv(env(map[string]string{name: value})); err == nil {
			t.Errorf("FromEnv() error = nil for %s=%q", name, value)
		}
	}
}

// Agent engines are regional, while the models are served from the global
// endpoint, so the engine's region is a setting of its own; a deployment that
// runs both in one region names it once.
func TestFromEnvPutsMemoryInItsEnginesRegion(t *testing.T) {
	config, err := FromEnv(env(map[string]string{"GOOGLE_CLOUD_LOCATION": "us-central1"}))
	if err != nil {
		t.Fatal(err)
	}
	if config.MemoryLocation != "us-central1" {
		t.Fatalf("MemoryLocation = %q, want GOOGLE_CLOUD_LOCATION's us-central1", config.MemoryLocation)
	}
	config, err = FromEnv(env(map[string]string{"AGENTS_PRIMARY_MEMORY_LOCATION": " us-west1 "}))
	if err != nil {
		t.Fatal(err)
	}
	if config.GoogleCloudLocation != "global" || config.MemoryLocation != "us-west1" {
		t.Fatalf("locations = %q for the models and %q for memory, want global and us-west1", config.GoogleCloudLocation, config.MemoryLocation)
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
		"AGENTS_ALLOWED_ORIGINS":                         " http://localhost:3000, https://app.oryxa.test/ ",
		"AGENTS_PRIMARY_RUN_TIMEOUT_SECONDS":             "300",
		"AGENTS_PRIMARY_DATABASE_MAX_CONNS":              "4",
		"AGENTS_WORKFLOW_QUEUE_URL":                      " https://sqs.example/workflows ",
		"AGENTS_WORKFLOW_STATE_MACHINE_ARN":              " arn:aws:states:ap-south-1:1:stateMachine:workflows ",
		"AGENTS_WORKFLOW_SCHEDULER_ROLE_ARN":             " arn:aws:iam::1:role/scheduler ",
		"AGENTS_WORKFLOW_SCHEDULE_DLQ_ARN":               " arn:aws:sqs:ap-south-1:1:workflow-dlq ",
		"AGENTS_WORKFLOW_SCHEDULER_GROUP":                " oryxa ",
		"AWS_ENDPOINT_URL_SFN":                           " http://localhost:4566/ ",
		"AWS_ENDPOINT_URL_SCHEDULER":                     " http://localhost:4567/ ",
		"AGENTS_WORKFLOW_LOCAL":                          " true ",
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
	if len(config.AllowedOrigins) != 2 || config.AllowedOrigins[0] != "http://localhost:3000" || config.AllowedOrigins[1] != "https://app.oryxa.test" {
		t.Fatalf("allowed origins = %v", config.AllowedOrigins)
	}
	if config.RunTimeout != 5*time.Minute || config.DatabaseMaxConns != 4 {
		t.Fatalf("run API config = %+v", config)
	}
	if config.WorkflowQueueURL != "https://sqs.example/workflows" || config.WorkflowSchedulerGroup != "oryxa" ||
		config.StepFunctionsEndpoint != "http://localhost:4566" || config.SchedulerEndpoint != "http://localhost:4567" || !config.WorkflowLocal {
		t.Fatalf("workflow config = %+v", config)
	}
}

func TestFromEnvRejectsInvalidRunAPISettings(t *testing.T) {
	for name, value := range map[string]string{
		"AGENTS_ALLOWED_ORIGINS":             "localhost:3000",
		"AGENTS_PRIMARY_RUN_TIMEOUT_SECONDS": "10",
		"AGENTS_PRIMARY_DATABASE_MAX_CONNS":  "0",
	} {
		if _, err := FromEnv(env(map[string]string{name: value})); err == nil {
			t.Errorf("FromEnv() error = nil for %s=%q", name, value)
		}
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

// TestTheAnswerGuardIsBothOrNeither: unset, no answer is checked; set, its
// guardrail and version are read; half set, the service refuses to start,
// naming both settings.
func TestTheAnswerGuardIsBothOrNeither(t *testing.T) {
	config, err := FromEnv(env(map[string]string{}))
	if err != nil || config.AnswerGuardrailIdentifier != "" || config.AnswerGuardrailVersion != "" {
		t.Fatalf("unset: config=%+v err=%v, want no answer guard", config, err)
	}
	config, err = FromEnv(env(map[string]string{
		"AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID": " gr-answers ", "AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION": " 2 ",
	}))
	if err != nil || config.AnswerGuardrailIdentifier != "gr-answers" || config.AnswerGuardrailVersion != "2" {
		t.Fatalf("set: config=%+v err=%v, want gr-answers version 2", config, err)
	}
	for _, half := range []map[string]string{
		{"AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID": "gr-answers"},
		{"AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION": "2"},
		{"AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID": "gr-answers", "AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION": "  "},
	} {
		if _, err := FromEnv(env(half)); err == nil ||
			!strings.Contains(err.Error(), "AGENTS_PRIMARY_ANSWER_GUARDRAIL_ID") || !strings.Contains(err.Error(), "AGENTS_PRIMARY_ANSWER_GUARDRAIL_VERSION") {
			t.Errorf("half set %v: error=%v, want a refusal naming both settings", half, err)
		}
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
