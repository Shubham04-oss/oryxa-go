package serviceconfig

import (
	"strings"
	"testing"
)

// environment answers like os.Getenv over a map.
func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// required is the least apps needs to start.
func required() map[string]string {
	return map[string]string{
		"AUTH_URL":             "http://auth:8080",
		"COMPOSIO_API_KEY":     "ak_test",
		"APPS_PUBLIC_BASE_URL": "https://api.oryxa.test",
	}
}

func TestFromEnvNeedsAuthComposioAndItsOwnURL(t *testing.T) {
	t.Parallel()
	for _, missing := range []string{"AUTH_URL", "COMPOSIO_API_KEY", "APPS_PUBLIC_BASE_URL"} {
		values := required()
		delete(values, missing)
		_, err := FromEnv(environment(values))
		if err == nil {
			t.Fatalf("started without %s", missing)
		}
		if !strings.Contains(err.Error(), missing) {
			t.Fatalf("error = %v, want it to name %s", err, missing)
		}
	}
	if _, err := FromEnv(environment(required())); err != nil {
		t.Fatalf("the least apps needs = %v", err)
	}
}

func TestFromEnvReadsWhatAppsIsGiven(t *testing.T) {
	t.Parallel()
	values := required()
	values["AUTH_URL"] = "http://auth:8080/"
	values["COMPOSIO_API_KEY"] = "  ak_test  "
	values["COMPOSIO_BASE_URL"] = "https://backend.composio.dev/api/v3.1/"
	values["COMPOSIO_WEBHOOK_SECRET"] = " whsec_test "
	values["APPS_PUBLIC_BASE_URL"] = "https://api.oryxa.test/"
	values["APPS_ALLOWED_ORIGINS"] = "https://app.oryxa.test, https://admin.oryxa.test ,"
	values["APPS_EVENTS_QUEUE_URL"] = " https://sqs.test/queue/apps-events "

	config, err := FromEnv(environment(values))
	if err != nil {
		t.Fatal(err)
	}
	// A trailing slash on a base URL would double up in every path built on
	// it, so it is trimmed once here rather than at each use.
	if config.AuthURL != "http://auth:8080" || config.PublicBaseURL != "https://api.oryxa.test" {
		t.Fatalf("config = %+v", config)
	}
	if config.ComposioBaseURL != "https://backend.composio.dev/api/v3.1" {
		t.Fatalf("Composio base URL = %q", config.ComposioBaseURL)
	}
	if config.ComposioAPIKey != "ak_test" || config.ComposioWebhookSecret != "whsec_test" {
		t.Fatalf("config = %+v", config)
	}
	if config.EventsQueueURL != "https://sqs.test/queue/apps-events" {
		t.Fatalf("queue = %q", config.EventsQueueURL)
	}
	if strings.Join(config.AllowedOrigins, "|") != "https://app.oryxa.test|https://admin.oryxa.test" {
		t.Fatalf("origins = %v", config.AllowedOrigins)
	}
}

func TestFromEnvFallsBackToTheGenericAWSEndpoint(t *testing.T) {
	t.Parallel()
	values := required()
	values["AWS_ENDPOINT_URL"] = "http://localstack:4566"

	config, err := FromEnv(environment(values))
	if err != nil {
		t.Fatal(err)
	}
	if config.EventBridgeEndpoint != "http://localstack:4566" || config.SQSEndpoint != "http://localstack:4566" {
		t.Fatalf("endpoints = %q and %q", config.EventBridgeEndpoint, config.SQSEndpoint)
	}

	// A specific endpoint wins over the generic one.
	values["AWS_ENDPOINT_URL_SQS"] = "http://sqs.local:4566"
	if config, err = FromEnv(environment(values)); err != nil {
		t.Fatal(err)
	}
	if config.SQSEndpoint != "http://sqs.local:4566" || config.EventBridgeEndpoint != "http://localstack:4566" {
		t.Fatalf("endpoints = %q and %q", config.SQSEndpoint, config.EventBridgeEndpoint)
	}
}

func TestFromEnvRefusesWhatIsNotAURL(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]string{
		"AUTH_URL":             "auth:8080",
		"COMPOSIO_BASE_URL":    "backend.composio.dev",
		"APPS_PUBLIC_BASE_URL": "api.oryxa.test",
		"EVENTBRIDGE_ENDPOINT": "localhost:4566",
		"AWS_ENDPOINT_URL_SQS": "localhost:4566",
	} {
		t.Run(name, func(t *testing.T) {
			values := required()
			values[name] = value
			_, err := FromEnv(environment(values))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("error = %v, want it to name %s", err, name)
			}
		})
	}
	t.Run("APPS_ALLOWED_ORIGINS", func(t *testing.T) {
		values := required()
		values["APPS_ALLOWED_ORIGINS"] = "app.oryxa.test"
		if _, err := FromEnv(environment(values)); err == nil {
			t.Fatal("accepted an origin that is not absolute")
		}
	})
}

func TestFromEnvWithoutAWebhookSecretStillStarts(t *testing.T) {
	t.Parallel()
	// Webhooks are refused without a secret, but the rest of apps works, so
	// a deployment can connect apps before its subscription exists.
	config, err := FromEnv(environment(required()))
	if err != nil {
		t.Fatal(err)
	}
	if config.ComposioWebhookSecret != "" || config.EventsQueueURL != "" || config.ComposioBaseURL != "" {
		t.Fatalf("config = %+v", config)
	}
}
