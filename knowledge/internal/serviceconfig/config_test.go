package serviceconfig

import (
	"slices"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func required() map[string]string {
	return map[string]string{
		"AUTH_URL": "http://localhost:8080/",
	}
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(required()))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.AuthURL != "http://localhost:8080" ||
		config.EventBridgeEndpoint != "" || config.SQSEndpoint != "" ||
		config.EventsQueueURL != "" || len(config.AllowedOrigins) != 0 {
		t.Fatalf("config = %+v", config)
	}
	if config.FullSweepInterval != 7*24*time.Hour {
		t.Fatalf("FullSweepInterval = %s, want a week", config.FullSweepInterval)
	}
	// Without a Composio key, app sources stay off and nothing names apps.
	if config.ComposioAPIKey != "" || config.AppsURL != "" || config.ComposioBaseURL != "" {
		t.Fatalf("config = %+v, want no Composio configuration", config)
	}
}

func TestFromEnvRequiresAppsURLOnlyWithAComposioKey(t *testing.T) {
	values := required()
	values["COMPOSIO_API_KEY"] = "sk_live_1"
	if _, err := FromEnv(env(values)); err == nil {
		t.Fatal("FromEnv() error = nil, want APPS_URL required alongside a Composio key")
	}

	values["APPS_URL"] = "http://localhost:8083"
	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.ComposioAPIKey != "sk_live_1" || config.AppsURL != "http://localhost:8083" {
		t.Fatalf("config = %+v", config)
	}

	// Without a key, apps stays optional: app sources are simply off.
	delete(values, "COMPOSIO_API_KEY")
	if _, err := FromEnv(env(values)); err != nil {
		t.Fatalf("FromEnv() error = %v, want APPS_URL alone to need no Composio key", err)
	}
}

// The files source reads as the member who registered it, so it needs both
// halves of that: somewhere to read files from, and a key to sign this
// service's own identity with when asking auth for the member's token.
// Neither is required — the source simply stays off — but a URL that is not
// one is still refused rather than failing on the first pass.
func TestFromEnvReadsWhatTheFilesSourceNeeds(t *testing.T) {
	values := required()
	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.FilesURL != "" || config.ServiceKeyID != "" {
		t.Fatalf("config = %+v, want both unset by default", config)
	}

	values["FILES_URL"] = "http://localhost:8085"
	values["KNOWLEDGE_SERVICE_KEY_ID"] = "alias/oryxa-knowledge"
	if config, err = FromEnv(env(values)); err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.FilesURL != "http://localhost:8085" || config.ServiceKeyID != "alias/oryxa-knowledge" {
		t.Fatalf("config = %+v", config)
	}

	values["FILES_URL"] = "not-a-url"
	if _, err := FromEnv(env(values)); err == nil {
		t.Fatal("FromEnv() error = nil, want FILES_URL checked as a URL")
	}
}

func TestFromEnvReadsTheComposioBaseURLOverride(t *testing.T) {
	values := required()
	values["COMPOSIO_API_KEY"] = "sk_live_1"
	values["APPS_URL"] = "http://localhost:8083"
	values["COMPOSIO_BASE_URL"] = "http://localhost:4010/composio/"

	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.ComposioBaseURL != "http://localhost:4010/composio" {
		t.Fatalf("ComposioBaseURL = %q, want the trailing slash trimmed", config.ComposioBaseURL)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	values := required()
	values["KNOWLEDGE_ALLOWED_ORIGINS"] = "http://localhost:3000, https://app.example.test"
	values["KNOWLEDGE_EVENTS_QUEUE_URL"] = " https://sqs.example.test/knowledge-events "
	values["KNOWLEDGE_FULL_SWEEP_INTERVAL"] = " 72h "

	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.EventsQueueURL != "https://sqs.example.test/knowledge-events" {
		t.Fatalf("config = %+v", config)
	}
	if !slices.Equal(config.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.test"}) {
		t.Fatalf("allowed origins = %v", config.AllowedOrigins)
	}
	if config.FullSweepInterval != 72*time.Hour {
		t.Fatalf("FullSweepInterval = %s, want 72h", config.FullSweepInterval)
	}
}

func TestFromEnvAWSEndpointURL(t *testing.T) {
	values := required()
	values["AWS_ENDPOINT_URL"] = "http://localhost:4566/"
	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	// The generic endpoint fills EventBridge, SQS and S3.
	if config.EventBridgeEndpoint != "http://localhost:4566" || config.SQSEndpoint != "http://localhost:4566" ||
		config.S3Endpoint != "http://localhost:4566" {
		t.Fatalf("endpoints = eventbridge %q sqs %q s3 %q", config.EventBridgeEndpoint, config.SQSEndpoint, config.S3Endpoint)
	}

	// A specific endpoint overrides the generic one.
	values["EVENTBRIDGE_ENDPOINT"] = "http://localhost:4577"
	if config, err := FromEnv(env(values)); err != nil || config.EventBridgeEndpoint != "http://localhost:4577" {
		t.Fatalf("EventBridge endpoint = %q, err = %v", config.EventBridgeEndpoint, err)
	}
}

// TestFromEnvReadsTheOriginalsBucket covers the one setting that turns on
// keeping a PDF or an Office file's own bytes instead of skipping them:
// unset by default, so nothing about this changes a deployment that has not
// opted in.
func TestFromEnvReadsTheOriginalsBucket(t *testing.T) {
	config, err := FromEnv(env(required()))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.OriginalsBucket != "" {
		t.Fatalf("OriginalsBucket = %q, want empty by default", config.OriginalsBucket)
	}

	values := required()
	values["KNOWLEDGE_ORIGINALS_BUCKET"] = " oryxa-knowledge-originals "
	config, err = FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.OriginalsBucket != "oryxa-knowledge-originals" {
		t.Fatalf("OriginalsBucket = %q, want the trimmed bucket name", config.OriginalsBucket)
	}
}

func TestFromEnvRejects(t *testing.T) {
	for name, change := range map[string]func(map[string]string){
		"missing auth url":              func(values map[string]string) { values["AUTH_URL"] = "" },
		"relative auth url":             func(values map[string]string) { values["AUTH_URL"] = "auth.local" },
		"relative EventBridge endpoint": func(values map[string]string) { values["EVENTBRIDGE_ENDPOINT"] = "localhost:4566" },
		"relative S3 endpoint":          func(values map[string]string) { values["AWS_ENDPOINT_URL_S3"] = "localhost:4566" },
		"relative apps url": func(values map[string]string) {
			values["COMPOSIO_API_KEY"] = "sk_live_1"
			values["APPS_URL"] = "apps.local"
		},
		"relative composio base url": func(values map[string]string) {
			values["COMPOSIO_API_KEY"] = "sk_live_1"
			values["APPS_URL"] = "http://localhost:8083"
			values["COMPOSIO_BASE_URL"] = "composio.local"
		},
		"composio key without apps url": func(values map[string]string) { values["COMPOSIO_API_KEY"] = "sk_live_1" },
		"relative allowed origin":       func(values map[string]string) { values["KNOWLEDGE_ALLOWED_ORIGINS"] = "localhost:3000" },
		"zero full sweep interval":      func(values map[string]string) { values["KNOWLEDGE_FULL_SWEEP_INTERVAL"] = "0h" },
		"negative full sweep interval": func(values map[string]string) {
			values["KNOWLEDGE_FULL_SWEEP_INTERVAL"] = "-24h"
		},
		"unparsable full sweep interval": func(values map[string]string) {
			values["KNOWLEDGE_FULL_SWEEP_INTERVAL"] = "a week"
		},
	} {
		t.Run(name, func(t *testing.T) {
			values := required()
			change(values)
			if _, err := FromEnv(env(values)); err == nil {
				t.Fatalf("FromEnv() error = nil with %v", values)
			}
		})
	}
}
