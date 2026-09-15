package serviceconfig

import (
	"slices"
	"testing"
)

func setRequired(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"CONNECTIONS_ADDRESS", "AWS_REGION", "DATABASE_URL", "POSTGRES_REQUIRED_EXTENSIONS",
		"CONNECTIONS_AUTO_MIGRATE", "AUTH_URL", "TEAMS_URL", "CONNECTIONS_PUBLIC_BASE_URL",
		"CONNECTIONS_ALLOWED_ORIGINS", "EVENT_BUS_NAME", "CONNECTIONS_EVENTS_QUEUE_URL",
		"CONNECTIONS_KMS_KEY_ID", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_KMS", "EVENTBRIDGE_ENDPOINT",
		"AWS_ENDPOINT_URL_SQS", "CONNECTIONS_SERVICE_CALLERS",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", "postgres://oryxa@localhost:5435/oryxa")
	t.Setenv("AUTH_URL", "http://localhost:8080/")
	t.Setenv("CONNECTIONS_PUBLIC_BASE_URL", "https://connect.example.test/")
	t.Setenv("CONNECTIONS_KMS_KEY_ID", " alias/oryxa-connections ")
}

func TestFromEnvDefaults(t *testing.T) {
	setRequired(t)
	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8080" || config.AWSRegion != "ap-south-1" || !config.AutoMigrate ||
		config.AuthURL != "http://localhost:8080" || config.PublicBaseURL != "https://connect.example.test" ||
		config.TeamsURL != "" || config.EventBusName != "default" || config.EventsQueueURL != "" ||
		config.KMSKeyID != "alias/oryxa-connections" || config.KMSEndpoint != "" || config.EventBridgeEndpoint != "" ||
		config.SQSEndpoint != "" || len(config.ServiceCallers) != 0 ||
		len(config.AllowedOrigins) != 0 || len(config.PostgresExtensions) != 0 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("CONNECTIONS_ADDRESS", " :8086 ")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("CONNECTIONS_AUTO_MIGRATE", "false")
	t.Setenv("TEAMS_URL", "http://localhost:8083/")
	t.Setenv("CONNECTIONS_ALLOWED_ORIGINS", "http://localhost:3000, https://app.example.test")
	t.Setenv("EVENT_BUS_NAME", "oryxa")
	t.Setenv("CONNECTIONS_EVENTS_QUEUE_URL", " https://sqs.example.test/connections-events ")
	t.Setenv("POSTGRES_REQUIRED_EXTENSIONS", "vector, ltree")
	t.Setenv("AWS_ENDPOINT_URL", "http://localhost:4566/")
	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.KMSEndpoint != "" || config.EventBridgeEndpoint != "http://localhost:4566" || config.SQSEndpoint != "http://localhost:4566" {
		t.Fatalf("AWS endpoints = %q %q %q", config.KMSEndpoint, config.EventBridgeEndpoint, config.SQSEndpoint)
	}
	t.Setenv("AWS_ENDPOINT_URL_KMS", "http://localhost:4599")
	if config, err := FromEnv(); err != nil || config.KMSEndpoint != "http://localhost:4599" {
		t.Fatalf("KMS endpoint = %q %v", config.KMSEndpoint, err)
	}
	config, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8086" || config.AWSRegion != "us-east-1" || config.AutoMigrate ||
		config.TeamsURL != "http://localhost:8083" || config.EventBusName != "oryxa" ||
		config.EventsQueueURL != "https://sqs.example.test/connections-events" ||
		!slices.Equal(config.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.test"}) ||
		!slices.Equal(config.PostgresExtensions, []string{"vector", "ltree"}) {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvRejects(t *testing.T) {
	for name, change := range map[string]func(*testing.T){
		"missing database URL":    func(t *testing.T) { t.Setenv("DATABASE_URL", "") },
		"missing auth URL":        func(t *testing.T) { t.Setenv("AUTH_URL", "") },
		"missing public base URL": func(t *testing.T) { t.Setenv("CONNECTIONS_PUBLIC_BASE_URL", "") },
		"missing KMS key":         func(t *testing.T) { t.Setenv("CONNECTIONS_KMS_KEY_ID", " ") },
		"unparseable callers":     func(t *testing.T) { t.Setenv("CONNECTIONS_SERVICE_CALLERS", "integrations") },
		"unknown caller purpose": func(t *testing.T) {
			t.Setenv("CONNECTIONS_SERVICE_CALLERS", `[{"service":"integrations","key_id":"alias/k","purposes":["export"]}]`)
		},
		"relative KMS endpoint": func(t *testing.T) { t.Setenv("AWS_ENDPOINT_URL_KMS", "localhost:4566") },
		"relative auth URL":     func(t *testing.T) { t.Setenv("AUTH_URL", "auth.local") },
		"relative teams URL":    func(t *testing.T) { t.Setenv("TEAMS_URL", "/teams") },
		"relative origin":       func(t *testing.T) { t.Setenv("CONNECTIONS_ALLOWED_ORIGINS", "localhost:3000") },
		"unparseable auto migrate": func(t *testing.T) {
			t.Setenv("CONNECTIONS_AUTO_MIGRATE", "sometimes")
		},
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

func TestFromEnvServiceCallers(t *testing.T) {
	setRequired(t)
	t.Setenv("CONNECTIONS_SERVICE_CALLERS", `[{"service":"integrations","key_id":"arn:aws:kms:ap-south-1:123456789012:key/abc","purposes":["action","trigger","cleanup"]}]`)
	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if len(config.ServiceCallers) != 1 || config.ServiceCallers[0].Service != "integrations" ||
		!slices.Equal(config.ServiceCallers[0].Purposes, []string{"action", "trigger", "cleanup"}) {
		t.Fatalf("service callers = %+v", config.ServiceCallers)
	}
}
