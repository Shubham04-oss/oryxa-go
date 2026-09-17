package serviceconfig

import (
	"slices"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func requiredValues() map[string]string {
	return map[string]string{
		"AUTH_URL":                     "http://localhost:8080/",
		"CONNECTIONS_URL":              "http://localhost:8081/",
		"INTEGRATIONS_PUBLIC_BASE_URL": "https://integrations.example.test/",
		"INTEGRATIONS_KMS_KEY_ID":      " alias/oryxa-integrations ",
		"INTEGRATIONS_SERVICE_KEY_ID":  " alias/oryxa-integrations-svc ",
	}
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(requiredValues()))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.ServiceName != "integrations" ||
		config.AuthURL != "http://localhost:8080" || config.ConnectionsURL != "http://localhost:8081" ||
		config.PublicBaseURL != "https://integrations.example.test" || config.KMSKeyID != "alias/oryxa-integrations" ||
		config.ServiceKeyID != "alias/oryxa-integrations-svc" || config.KMSEndpoint != "" || config.EventBridgeEndpoint != "" ||
		config.SQSEndpoint != "" || config.EventsQueueURL != "" || len(config.AllowedOrigins) != 0 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	values := requiredValues()
	values["INTEGRATIONS_ALLOWED_ORIGINS"] = "http://localhost:3000, https://app.example.test"
	values["INTEGRATIONS_SERVICE_NAME"] = "integrations-svc"
	values["INTEGRATIONS_EVENTS_QUEUE_URL"] = " https://sqs.example.test/integrations-events "
	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.ServiceName != "integrations-svc" || config.EventsQueueURL != "https://sqs.example.test/integrations-events" ||
		!slices.Equal(config.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.test"}) {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvAWSEndpointURL(t *testing.T) {
	values := requiredValues()
	values["AWS_ENDPOINT_URL"] = "http://localhost:4566/"
	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	// The generic endpoint fills EventBridge and SQS, but never KMS.
	if config.EventBridgeEndpoint != "http://localhost:4566" || config.SQSEndpoint != "http://localhost:4566" || config.KMSEndpoint != "" {
		t.Fatalf("endpoints = eventbridge %q sqs %q kms %q", config.EventBridgeEndpoint, config.SQSEndpoint, config.KMSEndpoint)
	}

	values["AWS_ENDPOINT_URL_KMS"] = "http://localhost:4599"
	if config, err := FromEnv(env(values)); err != nil || config.KMSEndpoint != "http://localhost:4599" {
		t.Fatalf("KMS endpoint = %q, err = %v", config.KMSEndpoint, err)
	}

	// A specific endpoint overrides the generic one.
	values["EVENTBRIDGE_ENDPOINT"] = "http://localhost:4577"
	if config, err := FromEnv(env(values)); err != nil || config.EventBridgeEndpoint != "http://localhost:4577" {
		t.Fatalf("EventBridge endpoint = %q, err = %v", config.EventBridgeEndpoint, err)
	}
}

func TestFromEnvRejects(t *testing.T) {
	for name, change := range map[string]func(map[string]string){
		"missing auth URL":         func(values map[string]string) { values["AUTH_URL"] = "" },
		"missing connections URL":  func(values map[string]string) { values["CONNECTIONS_URL"] = "" },
		"missing public base URL":  func(values map[string]string) { values["INTEGRATIONS_PUBLIC_BASE_URL"] = "" },
		"missing KMS key":          func(values map[string]string) { values["INTEGRATIONS_KMS_KEY_ID"] = " " },
		"missing service key":      func(values map[string]string) { values["INTEGRATIONS_SERVICE_KEY_ID"] = " " },
		"relative auth URL":        func(values map[string]string) { values["AUTH_URL"] = "auth.local" },
		"relative public base URL": func(values map[string]string) { values["INTEGRATIONS_PUBLIC_BASE_URL"] = "/integrations" },
		"relative KMS endpoint":    func(values map[string]string) { values["AWS_ENDPOINT_URL_KMS"] = "localhost:4566" },
		"relative allowed origin":  func(values map[string]string) { values["INTEGRATIONS_ALLOWED_ORIGINS"] = "localhost:3000" },
	} {
		t.Run(name, func(t *testing.T) {
			values := requiredValues()
			change(values)
			if _, err := FromEnv(env(values)); err == nil {
				t.Fatal("FromEnv() error = nil")
			}
		})
	}
}
