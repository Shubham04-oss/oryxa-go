package serviceconfig

import "testing"

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestFromEnvDefaults(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"EMAIL_FROM_ADDRESS": "no-reply@example.com",
		"EMAIL_QUEUE_URL":    "http://localhost:4566/000000000000/email-events",
	}))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	want := Config{
		From:              `"Oryxa" <no-reply@example.com>`,
		ConfigurationSet:  "oryxa-email",
		DeliveriesTable:   "email-deliveries",
		SuppressionsTable: "email-suppressions",
		QueueURL:          "http://localhost:4566/000000000000/email-events",
	}
	if config != want {
		t.Fatalf("FromEnv() = %+v, want %+v", config, want)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"EMAIL_FROM_ADDRESS":       "no-reply@example.com",
		"EMAIL_FROM_NAME":          "Oryxa Notifications",
		"EMAIL_QUEUE_URL":          " http://localhost:4566/000000000000/email-events ",
		"SES_CONFIGURATION_SET":    "custom-set",
		"EMAIL_DELIVERIES_TABLE":   "custom-deliveries",
		"EMAIL_SUPPRESSIONS_TABLE": "custom-suppressions",
		"EMAIL_SMTP_ADDR":          "localhost:1025",
	}))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	want := Config{
		From:              `"Oryxa Notifications" <no-reply@example.com>`,
		ConfigurationSet:  "custom-set",
		DeliveriesTable:   "custom-deliveries",
		SuppressionsTable: "custom-suppressions",
		QueueURL:          "http://localhost:4566/000000000000/email-events",
		SMTPAddr:          "localhost:1025",
	}
	if config != want {
		t.Fatalf("FromEnv() = %+v, want %+v", config, want)
	}
}

// TestFromEnvSendsFromItsOwnSESRegion: SES production access belongs to one
// region, and a deployment elsewhere still sends through it.
func TestFromEnvSendsFromItsOwnSESRegion(t *testing.T) {
	env := map[string]string{
		"EMAIL_FROM_ADDRESS": "no-reply@oryxa.in",
		"EMAIL_QUEUE_URL":    "https://sqs.us-west-2.amazonaws.com/123456789012/email-events",
		"SES_REGION":         " ap-south-1 ",
	}
	config, err := FromEnv(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	if config.SESRegion != "ap-south-1" {
		t.Fatalf("SESRegion = %q, want ap-south-1", config.SESRegion)
	}
	delete(env, "SES_REGION")
	if config, err = FromEnv(func(name string) string { return env[name] }); err != nil || config.SESRegion != "" {
		t.Fatalf("unset SES_REGION = %q, %v; want empty, meaning the deployment's region", config.SESRegion, err)
	}
}

func TestFromEnvRejectsInvalidSender(t *testing.T) {
	tests := []struct {
		name    string
		address string
	}{
		{name: "missing", address: ""},
		{name: "not an address", address: "no-reply"},
		{name: "display name", address: "Oryxa <no-reply@example.com>"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := FromEnv(env(map[string]string{
				"EMAIL_FROM_ADDRESS": test.address,
				"EMAIL_QUEUE_URL":    "http://localhost:4566/000000000000/email-events",
			}))
			if err == nil {
				t.Fatalf("FromEnv() error = nil for %q", test.address)
			}
		})
	}
}

func TestFromEnvRequiresQueueURL(t *testing.T) {
	_, err := FromEnv(env(map[string]string{"EMAIL_FROM_ADDRESS": "no-reply@example.com"}))
	if err == nil {
		t.Fatal("FromEnv() error = nil for a missing EMAIL_QUEUE_URL")
	}
}
