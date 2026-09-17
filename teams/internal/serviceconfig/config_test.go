package serviceconfig

import (
	"slices"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(map[string]string{"AUTH_URL": "http://localhost:8080/"}))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.AuthURL != "http://localhost:8080" || config.EventsQueueURL != "" || len(config.AllowedOrigins) != 0 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"AUTH_URL":               "http://localhost:8080",
		"TEAMS_ALLOWED_ORIGINS":  "http://localhost:3000, https://app.example.com",
		"TEAMS_EVENTS_QUEUE_URL": " http://localhost:4566/000000000000/teams-events ",
	}))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.EventsQueueURL != "http://localhost:4566/000000000000/teams-events" {
		t.Fatalf("config = %+v", config)
	}
	if !slices.Equal(config.AllowedOrigins, []string{"http://localhost:3000", "https://app.example.com"}) {
		t.Fatalf("allowed origins = %v", config.AllowedOrigins)
	}
}

func TestFromEnvRejectsInvalidValues(t *testing.T) {
	tests := map[string]map[string]string{
		"missing auth url":  {"AUTH_URL": ""},
		"relative auth url": {"AUTH_URL": "/auth"},
	}
	for name, values := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := FromEnv(env(values)); err == nil {
				t.Fatalf("FromEnv() error = nil with %v", values)
			}
		})
	}
}
