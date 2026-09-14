package serviceconfig

import (
	"slices"
	"testing"
)

func setRequired(t *testing.T) {
	t.Helper()
	for _, name := range []string{"FILES_ADDRESS", "AWS_REGION", "DATABASE_URL", "AUTH_URL", "TEAMS_URL", "FILES_BUCKET_NAME", "FILES_AUTO_MIGRATE", "FILES_ALLOWED_ORIGINS", "EVENT_BUS_NAME", "FILES_EVENTS_QUEUE_URL"} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", "postgres://oryxa@localhost:5435/oryxa")
	t.Setenv("AUTH_URL", "http://localhost:8080/")
	t.Setenv("FILES_BUCKET_NAME", "oryxa-files")
}

func TestFromEnvDefaults(t *testing.T) {
	setRequired(t)

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8080" || config.AWSRegion != "ap-south-1" || !config.AutoMigrate || config.AuthURL != "http://localhost:8080" ||
		config.TeamsURL != "" || config.EventBusName != "default" || config.EventsQueueURL != "" || config.BucketName != "oryxa-files" {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("FILES_ADDRESS", " :8085 ")
	t.Setenv("FILES_ALLOWED_ORIGINS", "http://localhost:3000, https://app.example.com")
	t.Setenv("FILES_EVENTS_QUEUE_URL", " http://localhost:4566/000000000000/files-events ")
	t.Setenv("TEAMS_URL", "http://localhost:8083/")
	t.Setenv("AWS_ENDPOINT_URL", "http://localhost:4566/")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8085" || config.EventsQueueURL != "http://localhost:4566/000000000000/files-events" || config.S3Endpoint != "http://localhost:4566" || config.TeamsURL != "http://localhost:8083" {
		t.Fatalf("config = %+v", config)
	}
	if !slices.Equal(config.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.com"}) {
		t.Fatalf("allowed origins = %v", config.AllowedOrigins)
	}
}

func TestFromEnvRejectsInvalidValues(t *testing.T) {
	tests := map[string]map[string]string{
		"missing database":         {"DATABASE_URL": ""},
		"missing bucket":           {"FILES_BUCKET_NAME": ""},
		"relative auth url":        {"AUTH_URL": "/auth"},
		"relative teams url":       {"TEAMS_URL": "/teams"},
		"auto migrate not boolean": {"FILES_AUTO_MIGRATE": "sometimes"},
	}
	for name, values := range tests {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			for key, value := range values {
				t.Setenv(key, value)
			}
			if _, err := FromEnv(); err == nil {
				t.Fatalf("FromEnv() error = nil with %v", values)
			}
		})
	}
}
