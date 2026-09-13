package serviceconfig

import "testing"

func TestFromEnvLoadsRuntimeConfiguration(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AUTH_COOKIE_SECURE", "false")
	t.Setenv("AUTH_AUTO_MIGRATE", "true")
	t.Setenv("AUTH_RETURN_URLS", "https://app.example.com, https://admin.example.com")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.AWSRegion != "ap-south-1" || config.CookieSecure || len(config.AllowedReturnURLs) != 2 {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestFromEnvLoadsBillingEventsQueue(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AUTH_EVENTS_QUEUE_URL", " http://localhost:4566/000000000000/auth-events ")
	t.Setenv("SQS_ENDPOINT", "http://localhost:4566/")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.EventsQueueURL != "http://localhost:4566/000000000000/auth-events" || config.SQSEndpoint != "http://localhost:4566" {
		t.Fatalf("queue url = %q, sqs endpoint = %q", config.EventsQueueURL, config.SQSEndpoint)
	}
}

func TestFromEnvRejectsInvalidBoolean(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AUTH_COOKIE_SECURE", "sometimes")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil, want invalid boolean error")
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_REGION", "")
	t.Setenv("COGNITO_ISSUER", "https://cognito-idp.ap-south-1.amazonaws.com/pool")
	t.Setenv("COGNITO_USER_POOL_ID", "pool")
	t.Setenv("COGNITO_CLIENT_ID", "client-id")
	t.Setenv("COGNITO_DOMAIN", "https://identity.example.com")
	t.Setenv("COGNITO_CALLBACK_URL", "https://auth.example.com/v1/auth/callback")
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("INVITATION_ACCEPT_URL", "https://app.example.com/invitations/accept")
	t.Setenv("EMAIL_VERIFICATION_URL", "https://app.example.com/auth/verify-email")
	t.Setenv("PASSWORD_RESET_URL", "https://app.example.com/auth/reset-password")
	t.Setenv("AUTH_RETURN_URLS", "https://app.example.com")
	t.Setenv("AUTH_COOKIE_SECURE", "")
	t.Setenv("AUTH_AUTO_MIGRATE", "")
	t.Setenv("AUTH_EVENTS_QUEUE_URL", "")
	t.Setenv("SQS_ENDPOINT", "")
}
