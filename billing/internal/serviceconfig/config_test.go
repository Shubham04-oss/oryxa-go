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
		"AUTH_URL":    "http://localhost:8080/",
		"BILLING_URL": "https://app.example.com/settings/billing",
	}
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(required()))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.AuthURL != "http://localhost:8080" || config.RenewalReminderDays != 7 || config.Dodo.LiveMode || config.Dodo.APIKey != "" {
		t.Fatalf("config = %+v", config)
	}
	if !slices.Equal(config.AllowedOrigins, []string{"https://app.example.com"}) {
		t.Fatalf("allowed origins = %v, want the billing page's origin", config.AllowedOrigins)
	}
}

func TestFromEnvDodo(t *testing.T) {
	values := required()
	values["DODO_PAYMENTS_API_KEY"] = "key"
	values["DODO_PAYMENTS_WEBHOOK_KEY"] = "whsec_abc"
	values["DODO_PAYMENTS_ENVIRONMENT"] = "live_mode"
	values["BILLING_ALLOWED_ORIGINS"] = "https://app.example.com, https://admin.example.com"

	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if !config.Dodo.LiveMode || config.Dodo.APIKey != "key" || config.Dodo.WebhookKey != "whsec_abc" || len(config.AllowedOrigins) != 2 {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvRejectsInvalidValues(t *testing.T) {
	tests := map[string]map[string]string{
		"missing auth url":          {"AUTH_URL": ""},
		"relative auth url":         {"AUTH_URL": "/auth"},
		"missing billing url":       {"BILLING_URL": ""},
		"relative billing url":      {"BILLING_URL": "settings/billing"},
		"unknown dodo environment":  {"DODO_PAYMENTS_ENVIRONMENT": "sandbox"},
		"webhook key without api":   {"DODO_PAYMENTS_WEBHOOK_KEY": "whsec_abc"},
		"reminder days too many":    {"BILLING_RENEWAL_REMINDER_DAYS": "90"},
		"reminder days not numeric": {"BILLING_RENEWAL_REMINDER_DAYS": "a week"},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			values := required()
			for key, value := range overrides {
				values[key] = value
			}
			if _, err := FromEnv(env(values)); err == nil {
				t.Fatalf("FromEnv() error = nil with %v", values)
			}
		})
	}
}
