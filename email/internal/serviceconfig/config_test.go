package serviceconfig

import "testing"

func TestFromEnvDefaults(t *testing.T) {
	for _, name := range []string{"AWS_REGION", "EMAIL_FROM_NAME", "EMAIL_QUEUE_URL", "SES_CONFIGURATION_SET", "EMAIL_DELIVERIES_TABLE", "EMAIL_SUPPRESSIONS_TABLE"} {
		t.Setenv(name, "")
	}
	t.Setenv("EMAIL_FROM_ADDRESS", "no-reply@example.com")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	want := Config{
		AWSRegion:         "ap-south-1",
		From:              `"Oryxa" <no-reply@example.com>`,
		ConfigurationSet:  "oryxa-email",
		DeliveriesTable:   "email-deliveries",
		SuppressionsTable: "email-suppressions",
	}
	if config != want {
		t.Fatalf("FromEnv() = %+v, want %+v", config, want)
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
			t.Setenv("EMAIL_FROM_ADDRESS", test.address)
			if _, err := FromEnv(); err == nil {
				t.Fatalf("FromEnv() error = nil for %q", test.address)
			}
		})
	}
}
