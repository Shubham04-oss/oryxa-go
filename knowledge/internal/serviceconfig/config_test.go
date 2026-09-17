package serviceconfig

import (
	"slices"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func required() map[string]string {
	return map[string]string{
		"AUTH_URL":                 "http://localhost:8080/",
		"CONNECTIONS_URL":          "http://localhost:8081/",
		"KNOWLEDGE_SERVICE_KEY_ID": " alias/oryxa-knowledge ",
	}
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(required()))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.AuthURL != "http://localhost:8080" || config.ConnectionsURL != "http://localhost:8081" ||
		config.ServiceName != "knowledge" || config.ServiceKeyID != "alias/oryxa-knowledge" ||
		config.KMSEndpoint != "" || config.EventBridgeEndpoint != "" || config.SQSEndpoint != "" ||
		config.EventsQueueURL != "" || len(config.AllowedOrigins) != 0 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	values := required()
	values["KNOWLEDGE_ALLOWED_ORIGINS"] = "http://localhost:3000, https://app.example.test"
	values["KNOWLEDGE_SERVICE_NAME"] = "knowledge-svc"
	values["KNOWLEDGE_EVENTS_QUEUE_URL"] = " https://sqs.example.test/knowledge-events "

	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.ServiceName != "knowledge-svc" || config.EventsQueueURL != "https://sqs.example.test/knowledge-events" {
		t.Fatalf("config = %+v", config)
	}
	if !slices.Equal(config.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.test"}) {
		t.Fatalf("allowed origins = %v", config.AllowedOrigins)
	}
}

func TestFromEnvAWSEndpointURL(t *testing.T) {
	values := required()
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
		"missing auth url":         func(values map[string]string) { values["AUTH_URL"] = "" },
		"missing connections url":  func(values map[string]string) { values["CONNECTIONS_URL"] = "" },
		"missing service key":      func(values map[string]string) { values["KNOWLEDGE_SERVICE_KEY_ID"] = " " },
		"relative auth url":        func(values map[string]string) { values["AUTH_URL"] = "auth.local" },
		"relative connections url": func(values map[string]string) { values["CONNECTIONS_URL"] = "/connections" },
		"relative KMS endpoint":    func(values map[string]string) { values["AWS_ENDPOINT_URL_KMS"] = "localhost:4566" },
		"relative allowed origin":  func(values map[string]string) { values["KNOWLEDGE_ALLOWED_ORIGINS"] = "localhost:3000" },
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
