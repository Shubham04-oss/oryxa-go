package serviceconfig

import (
	"slices"
	"testing"
)

func setRequired(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"INTEGRATIONS_ADDRESS", "AWS_REGION", "DATABASE_URL", "POSTGRES_REQUIRED_EXTENSIONS",
		"INTEGRATIONS_AUTO_MIGRATE", "AUTH_URL", "CONNECTIONS_URL", "INTEGRATIONS_PUBLIC_BASE_URL",
		"INTEGRATIONS_ALLOWED_ORIGINS", "INTEGRATIONS_KMS_KEY_ID", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_KMS",
		"EVENTBRIDGE_ENDPOINT", "AWS_ENDPOINT_URL_SQS", "EVENT_BUS_NAME", "INTEGRATIONS_EVENTS_QUEUE_URL",
		"INTEGRATIONS_SERVICE_NAME", "INTEGRATIONS_SERVICE_KEY_ID",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", "postgres://oryxa@localhost:5435/oryxa")
	t.Setenv("AUTH_URL", "http://localhost:8080/")
	t.Setenv("CONNECTIONS_URL", "http://localhost:8081/")
	t.Setenv("INTEGRATIONS_PUBLIC_BASE_URL", "https://integrations.example.test/")
	t.Setenv("INTEGRATIONS_KMS_KEY_ID", " alias/oryxa-integrations ")
	t.Setenv("INTEGRATIONS_SERVICE_KEY_ID", " alias/oryxa-integrations-svc ")
}

func TestFromEnvDefaults(t *testing.T) {
	setRequired(t)
	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8080" || config.AWSRegion != "ap-south-1" || config.EventBusName != "default" ||
		config.ServiceName != "integrations" || !config.AutoMigrate ||
		config.AuthURL != "http://localhost:8080" || config.ConnectionsURL != "http://localhost:8081" ||
		config.PublicBaseURL != "https://integrations.example.test" || config.KMSKeyID != "alias/oryxa-integrations" ||
		config.ServiceKeyID != "alias/oryxa-integrations-svc" || config.KMSEndpoint != "" || config.EventBridgeEndpoint != "" ||
		config.SQSEndpoint != "" || config.EventsQueueURL != "" ||
		len(config.AllowedOrigins) != 0 || len(config.PostgresExtensions) != 0 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("INTEGRATIONS_ADDRESS", " :9090 ")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("INTEGRATIONS_AUTO_MIGRATE", "false")
	t.Setenv("INTEGRATIONS_ALLOWED_ORIGINS", "http://localhost:3000, https://app.example.test")
	t.Setenv("EVENT_BUS_NAME", "oryxa")
	t.Setenv("INTEGRATIONS_SERVICE_NAME", "integrations-svc")
	t.Setenv("INTEGRATIONS_EVENTS_QUEUE_URL", " https://sqs.example.test/integrations-events ")
	t.Setenv("POSTGRES_REQUIRED_EXTENSIONS", "vector, ltree")
	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":9090" || config.AWSRegion != "us-east-1" || config.AutoMigrate ||
		config.EventBusName != "oryxa" || config.ServiceName != "integrations-svc" ||
		config.EventsQueueURL != "https://sqs.example.test/integrations-events" ||
		!slices.Equal(config.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.test"}) ||
		!slices.Equal(config.PostgresExtensions, []string{"vector", "ltree"}) {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvAWSEndpointURL(t *testing.T) {
	setRequired(t)
	t.Setenv("AWS_ENDPOINT_URL", "http://localhost:4566/")
	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	// The generic endpoint fills EventBridge and SQS, but never KMS.
	if config.EventBridgeEndpoint != "http://localhost:4566" || config.SQSEndpoint != "http://localhost:4566" || config.KMSEndpoint != "" {
		t.Fatalf("endpoints = eventbridge %q sqs %q kms %q", config.EventBridgeEndpoint, config.SQSEndpoint, config.KMSEndpoint)
	}

	t.Setenv("AWS_ENDPOINT_URL_KMS", "http://localhost:4599")
	if config, err := FromEnv(); err != nil || config.KMSEndpoint != "http://localhost:4599" {
		t.Fatalf("KMS endpoint = %q, err = %v", config.KMSEndpoint, err)
	}

	// A specific endpoint overrides the generic one.
	t.Setenv("EVENTBRIDGE_ENDPOINT", "http://localhost:4577")
	if config, err := FromEnv(); err != nil || config.EventBridgeEndpoint != "http://localhost:4577" {
		t.Fatalf("EventBridge endpoint = %q, err = %v", config.EventBridgeEndpoint, err)
	}
}

func TestFromEnvRejects(t *testing.T) {
	for name, change := range map[string]func(*testing.T){
		"missing database URL":     func(t *testing.T) { t.Setenv("DATABASE_URL", "") },
		"missing auth URL":         func(t *testing.T) { t.Setenv("AUTH_URL", "") },
		"missing connections URL":  func(t *testing.T) { t.Setenv("CONNECTIONS_URL", "") },
		"missing public base URL":  func(t *testing.T) { t.Setenv("INTEGRATIONS_PUBLIC_BASE_URL", "") },
		"missing KMS key":          func(t *testing.T) { t.Setenv("INTEGRATIONS_KMS_KEY_ID", " ") },
		"missing service key":      func(t *testing.T) { t.Setenv("INTEGRATIONS_SERVICE_KEY_ID", " ") },
		"relative auth URL":        func(t *testing.T) { t.Setenv("AUTH_URL", "auth.local") },
		"relative public base URL": func(t *testing.T) { t.Setenv("INTEGRATIONS_PUBLIC_BASE_URL", "/integrations") },
		"relative KMS endpoint":    func(t *testing.T) { t.Setenv("AWS_ENDPOINT_URL_KMS", "localhost:4566") },
		"relative allowed origin":  func(t *testing.T) { t.Setenv("INTEGRATIONS_ALLOWED_ORIGINS", "localhost:3000") },
		"unparseable auto migrate": func(t *testing.T) { t.Setenv("INTEGRATIONS_AUTO_MIGRATE", "maybe") },
	} {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			change(t)
			if _, err := FromEnv(); err == nil {
				t.Fatal("FromEnv() error = nil")
			}
		})
	}
}
