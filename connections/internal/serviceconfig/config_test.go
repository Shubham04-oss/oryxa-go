package serviceconfig

import (
	"slices"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// requiredValues is the smallest environment FromEnv accepts.
func requiredValues() map[string]string {
	return map[string]string{
		"AUTH_URL":                    "http://localhost:8080/",
		"CONNECTIONS_PUBLIC_BASE_URL": "https://connect.example.test/",
		"CONNECTIONS_KMS_KEY_ID":      " alias/oryxa-connections ",
	}
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(requiredValues()))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.AuthURL != "http://localhost:8080" || config.PublicBaseURL != "https://connect.example.test" ||
		config.TeamsURL != "" || config.EventsQueueURL != "" ||
		config.KMSKeyID != "alias/oryxa-connections" || config.KMSEndpoint != "" || config.EventBridgeEndpoint != "" ||
		config.SQSEndpoint != "" || len(config.ServiceCallers) != 0 || len(config.AllowedOrigins) != 0 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	values := requiredValues()
	values["TEAMS_URL"] = "http://localhost:8083/"
	values["CONNECTIONS_ALLOWED_ORIGINS"] = "http://localhost:3000, https://app.example.test"
	values["CONNECTIONS_EVENTS_QUEUE_URL"] = " https://sqs.example.test/connections-events "
	values["AWS_ENDPOINT_URL"] = "http://localhost:4566/"

	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.KMSEndpoint != "" || config.EventBridgeEndpoint != "http://localhost:4566" || config.SQSEndpoint != "http://localhost:4566" {
		t.Fatalf("AWS endpoints = %q %q %q", config.KMSEndpoint, config.EventBridgeEndpoint, config.SQSEndpoint)
	}

	values["AWS_ENDPOINT_URL_KMS"] = "http://localhost:4599"
	if config, err := FromEnv(env(values)); err != nil || config.KMSEndpoint != "http://localhost:4599" {
		t.Fatalf("KMS endpoint = %q %v", config.KMSEndpoint, err)
	}

	config, err = FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.TeamsURL != "http://localhost:8083" || config.EventsQueueURL != "https://sqs.example.test/connections-events" ||
		!slices.Equal(config.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.test"}) {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvRejects(t *testing.T) {
	for name, change := range map[string]func(map[string]string){
		"missing auth url":        func(v map[string]string) { v["AUTH_URL"] = "" },
		"missing public base url": func(v map[string]string) { v["CONNECTIONS_PUBLIC_BASE_URL"] = "" },
		"missing KMS key":         func(v map[string]string) { v["CONNECTIONS_KMS_KEY_ID"] = " " },
		"unparseable callers":     func(v map[string]string) { v["CONNECTIONS_SERVICE_CALLERS"] = "integrations" },
		"unknown caller purpose": func(v map[string]string) {
			v["CONNECTIONS_SERVICE_CALLERS"] = `[{"service":"integrations","key_id":"alias/k","purposes":["export"]}]`
		},
		"relative KMS endpoint": func(v map[string]string) { v["AWS_ENDPOINT_URL_KMS"] = "localhost:4566" },
		"relative auth URL":     func(v map[string]string) { v["AUTH_URL"] = "auth.local" },
		"relative teams URL":    func(v map[string]string) { v["TEAMS_URL"] = "/teams" },
		"relative origin":       func(v map[string]string) { v["CONNECTIONS_ALLOWED_ORIGINS"] = "localhost:3000" },
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

func TestFromEnvServiceCallers(t *testing.T) {
	values := requiredValues()
	values["CONNECTIONS_SERVICE_CALLERS"] = `[{"service":"integrations","key_id":"arn:aws:kms:ap-south-1:123456789012:key/abc","purposes":["action","trigger","cleanup"]}]`
	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if len(config.ServiceCallers) != 1 || config.ServiceCallers[0].Service != "integrations" ||
		!slices.Equal(config.ServiceCallers[0].Purposes, []string{"action", "trigger", "cleanup"}) {
		t.Fatalf("service callers = %+v", config.ServiceCallers)
	}
}
