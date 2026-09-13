package serviceconfig

import (
	"slices"
	"testing"
)

func setRequired(t *testing.T) {
	t.Helper()
	for _, name := range []string{"TEAMS_ADDRESS", "AWS_REGION", "DATABASE_URL", "POSTGRES_REQUIRED_EXTENSIONS", "TEAMS_AUTO_MIGRATE", "AUTH_URL", "TEAMS_ALLOWED_ORIGINS", "EVENT_BUS_NAME", "TEAMS_EVENTS_QUEUE_URL"} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", "postgres://oryxa@localhost:5435/oryxa")
	t.Setenv("AUTH_URL", "http://localhost:8080/")
}

func TestFromEnvDefaults(t *testing.T) {
	setRequired(t)

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8080" || config.AWSRegion != "ap-south-1" || !config.AutoMigrate || config.AuthURL != "http://localhost:8080" ||
		config.EventBusName != "default" || config.EventsQueueURL != "" {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	setRequired(t)
	t.Setenv("TEAMS_ADDRESS", " :8083 ")
	t.Setenv("TEAMS_ALLOWED_ORIGINS", "http://localhost:3000, https://app.example.com")
	t.Setenv("TEAMS_EVENTS_QUEUE_URL", " http://localhost:4566/000000000000/teams-events ")
	t.Setenv("POSTGRES_REQUIRED_EXTENSIONS", "ltree, pg_trgm, pgvector")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8083" || config.EventsQueueURL != "http://localhost:4566/000000000000/teams-events" {
		t.Fatalf("config = %+v", config)
	}
	if !slices.Equal(config.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.com"}) {
		t.Fatalf("allowed origins = %v", config.AllowedOrigins)
	}
	if !slices.Equal(config.PostgresExtensions, []string{"ltree", "pg_trgm", "pgvector"}) {
		t.Fatalf("postgres extensions = %v", config.PostgresExtensions)
	}
}

func TestFromEnvRejectsInvalidValues(t *testing.T) {
	tests := map[string]map[string]string{
		"missing database":         {"DATABASE_URL": ""},
		"relative auth url":        {"AUTH_URL": "/auth"},
		"auto migrate not boolean": {"TEAMS_AUTO_MIGRATE": "sometimes"},
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
