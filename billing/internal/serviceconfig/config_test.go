package serviceconfig

import (
	"slices"
	"testing"
)

var names = []string{
	"BILLING_ADDRESS", "AWS_REGION", "DATABASE_URL", "BILLING_AUTO_MIGRATE", "AUTH_URL", "BILLING_URL",
	"BILLING_ALLOWED_ORIGINS", "BILLING_PLANS_FILE", "EVENT_BUS_NAME", "BILLING_EVENTS_QUEUE_URL",
	"BILLING_RENEWAL_REMINDER_DAYS", "DODO_PAYMENTS_API_KEY", "DODO_PAYMENTS_WEBHOOK_KEY",
	"DODO_PAYMENTS_BASE_URL", "DODO_PAYMENTS_ENVIRONMENT",
}

func setRequired(t *testing.T) {
	t.Helper()
	for _, name := range names {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", "postgres://oryxa@localhost:5435/oryxa")
	t.Setenv("AUTH_URL", "http://localhost:8080/")
	t.Setenv("BILLING_URL", "https://app.example.com/settings/billing")
}

func TestFromEnvDefaults(t *testing.T) {
	setRequired(t)

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8080" || config.AWSRegion != "ap-south-1" || !config.AutoMigrate || config.AuthURL != "http://localhost:8080" ||
		config.EventBusName != "default" || config.RenewalReminderDays != 7 || config.Dodo.LiveMode || config.Dodo.APIKey != "" {
		t.Fatalf("config = %+v", config)
	}
	if !slices.Equal(config.AllowedOrigins, []string{"https://app.example.com"}) {
		t.Fatalf("allowed origins = %v, want the billing page's origin", config.AllowedOrigins)
	}
}

func TestFromEnvDodo(t *testing.T) {
	setRequired(t)
	t.Setenv("DODO_PAYMENTS_API_KEY", "key")
	t.Setenv("DODO_PAYMENTS_WEBHOOK_KEY", "whsec_abc")
	t.Setenv("DODO_PAYMENTS_ENVIRONMENT", "live_mode")
	t.Setenv("BILLING_ALLOWED_ORIGINS", "https://app.example.com, https://admin.example.com")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if !config.Dodo.LiveMode || config.Dodo.APIKey != "key" || config.Dodo.WebhookKey != "whsec_abc" || len(config.AllowedOrigins) != 2 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvRejectsInvalidValues(t *testing.T) {
	tests := map[string]map[string]string{
		"missing database":          {"DATABASE_URL": ""},
		"relative auth url":         {"AUTH_URL": "/auth"},
		"relative billing url":      {"BILLING_URL": "settings/billing"},
		"unknown dodo environment":  {"DODO_PAYMENTS_ENVIRONMENT": "sandbox"},
		"webhook key without api":   {"DODO_PAYMENTS_WEBHOOK_KEY": "whsec_abc"},
		"reminder days too many":    {"BILLING_RENEWAL_REMINDER_DAYS": "90"},
		"reminder days not numeric": {"BILLING_RENEWAL_REMINDER_DAYS": "a week"},
		"auto migrate not boolean":  {"BILLING_AUTO_MIGRATE": "sometimes"},
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
