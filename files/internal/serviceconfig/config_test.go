package serviceconfig

import (
	"slices"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"AUTH_URL":          "http://localhost:8080/",
		"FILES_BUCKET_NAME": "oryxa-files",
	}))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.AuthURL != "http://localhost:8080" || config.TeamsURL != "" || config.EventsQueueURL != "" || config.BucketName != "oryxa-files" || len(config.AllowedOrigins) != 0 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"AUTH_URL":               "http://localhost:8080",
		"FILES_BUCKET_NAME":      "oryxa-files",
		"FILES_ALLOWED_ORIGINS":  "http://localhost:3000, https://app.example.com",
		"FILES_EVENTS_QUEUE_URL": " http://localhost:4566/000000000000/files-events ",
		"TEAMS_URL":              "http://localhost:8083/",
		"AWS_ENDPOINT_URL":       "http://localhost:4566/",
	}))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.EventsQueueURL != "http://localhost:4566/000000000000/files-events" || config.S3Endpoint != "http://localhost:4566" || config.TeamsURL != "http://localhost:8083" {
		t.Fatalf("config = %+v", config)
	}
	if !slices.Equal(config.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.com"}) {
		t.Fatalf("allowed origins = %v", config.AllowedOrigins)
	}
}

func TestFromEnvOverridesS3EndpointAloneOverGeneric(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"AUTH_URL":            "http://localhost:8080",
		"FILES_BUCKET_NAME":   "oryxa-files",
		"AWS_ENDPOINT_URL":    "http://localhost:4566/",
		"AWS_ENDPOINT_URL_S3": "http://localhost:9000/",
	}))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.S3Endpoint != "http://localhost:9000" {
		t.Fatalf("S3Endpoint = %q", config.S3Endpoint)
	}
}

func TestFromEnvRejectsInvalidValues(t *testing.T) {
	valid := map[string]string{"AUTH_URL": "http://localhost:8080", "FILES_BUCKET_NAME": "oryxa-files"}
	tests := map[string]map[string]string{
		"missing bucket":     {"AUTH_URL": valid["AUTH_URL"]},
		"missing auth url":   {"FILES_BUCKET_NAME": valid["FILES_BUCKET_NAME"]},
		"relative auth url":  {"AUTH_URL": "/auth", "FILES_BUCKET_NAME": valid["FILES_BUCKET_NAME"]},
		"relative teams url": {"AUTH_URL": valid["AUTH_URL"], "FILES_BUCKET_NAME": valid["FILES_BUCKET_NAME"], "TEAMS_URL": "/teams"},
	}
	for name, values := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := FromEnv(env(values)); err == nil {
				t.Fatalf("FromEnv() error = nil with %v", values)
			}
		})
	}
}
