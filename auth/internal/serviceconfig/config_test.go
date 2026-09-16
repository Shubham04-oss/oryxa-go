package serviceconfig

import "testing"

func TestFromEnvLoadsRuntimeConfiguration(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AUTH_COOKIE_SECURE", "false")
	t.Setenv("AUTH_AUTO_MIGRATE", "true")
	t.Setenv("AUTH_RETURN_URLS", "https://app.example.com, https://admin.example.com")
	t.Setenv("POSTGRES_REQUIRED_EXTENSIONS", "ltree, pg_trgm, pgvector, age")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.AWSRegion != "ap-south-1" || config.CookieSecure || len(config.AllowedReturnURLs) != 2 ||
		len(config.PostgresExtensions) != 4 || config.PostgresExtensions[2] != "pgvector" {
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

func TestAgentGrantsDisabledUnlessAllFourAreSet(t *testing.T) {
	setRequiredEnv(t)

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.AgentGrantsEnabled() {
		t.Fatal("AgentGrantsEnabled() = true with nothing set")
	}

	agentEnv := map[string]string{
		"COGNITO_AGENT_CLIENT_ID":     "agent-client-id",
		"COGNITO_AGENT_CLIENT_SECRET": "agent-client-secret",
		"COGNITO_AGENT_CALLBACK_URL":  "https://auth.example.com/v1/agent-grant/callback",
		"AUTH_KMS_KEY_ID":             "alias/oryxa-auth",
	}
	// Setting any three of the four still leaves grants disabled.
	for missing := range agentEnv {
		setRequiredEnv(t)
		for name, value := range agentEnv {
			if name != missing {
				t.Setenv(name, value)
			}
		}
		config, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv() error = %v (missing %s)", err, missing)
		}
		if config.AgentGrantsEnabled() {
			t.Fatalf("AgentGrantsEnabled() = true without %s", missing)
		}
	}

	setRequiredEnv(t)
	for name, value := range agentEnv {
		t.Setenv(name, value)
	}
	config, err = FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if !config.AgentGrantsEnabled() {
		t.Fatalf("AgentGrantsEnabled() = false with all four set: %+v", config)
	}
	if config.CognitoAgentClientID != "agent-client-id" || config.CognitoAgentClientSecret != "agent-client-secret" ||
		config.CognitoAgentCallbackURL != "https://auth.example.com/v1/agent-grant/callback" || config.AuthKMSKeyID != "alias/oryxa-auth" {
		t.Fatalf("unexpected agent config: %+v", config)
	}
}

func TestFromEnvRejectsInvalidAgentCallbackURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("COGNITO_AGENT_CALLBACK_URL", "not-a-url")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil, want an absolute URL error")
	}
}

func TestFromEnvParsesServiceCallers(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AUTH_SERVICE_CALLERS", `[{"service":"agents","key_id":"alias/agents","purposes":["agent"]}]`)

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if len(config.ServiceCallers) != 1 || config.ServiceCallers[0].Service != "agents" || config.ServiceCallers[0].Purposes[0] != "agent" {
		t.Fatalf("service callers = %+v", config.ServiceCallers)
	}
}

func TestFromEnvRejectsInvalidServiceCallersJSON(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AUTH_SERVICE_CALLERS", "not-json")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil, want invalid callers JSON error")
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
	t.Setenv("POSTGRES_REQUIRED_EXTENSIONS", "")
	t.Setenv("COGNITO_AGENT_CLIENT_ID", "")
	t.Setenv("COGNITO_AGENT_CLIENT_SECRET", "")
	t.Setenv("COGNITO_AGENT_CALLBACK_URL", "")
	t.Setenv("AUTH_KMS_KEY_ID", "")
	t.Setenv("AUTH_SERVICE_CALLERS", "")
}
