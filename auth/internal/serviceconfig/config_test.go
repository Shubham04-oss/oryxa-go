package serviceconfig

import "testing"

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// requiredEnv is the minimum set of variables FromEnv needs, so each test
// starts from a valid configuration and overrides only what it means to
// exercise.
func requiredEnv() map[string]string {
	return map[string]string{
		"COGNITO_ISSUER":         "https://cognito-idp.ap-south-1.amazonaws.com/pool",
		"COGNITO_USER_POOL_ID":   "pool",
		"COGNITO_CLIENT_ID":      "client-id",
		"COGNITO_DOMAIN":         "https://identity.example.com",
		"COGNITO_CALLBACK_URL":   "https://auth.example.com/v1/auth/callback",
		"INVITATION_ACCEPT_URL":  "https://app.example.com/invitations/accept",
		"EMAIL_VERIFICATION_URL": "https://app.example.com/auth/verify-email",
		"PASSWORD_RESET_URL":     "https://app.example.com/auth/reset-password",
		"AUTH_RETURN_URLS":       "https://app.example.com",
	}
}

func TestFromEnvLoadsRuntimeConfiguration(t *testing.T) {
	values := requiredEnv()
	values["AUTH_COOKIE_SECURE"] = "false"
	values["AUTH_RETURN_URLS"] = "https://app.example.com, https://admin.example.com"

	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.CookieSecure || len(config.AllowedReturnURLs) != 2 {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestFromEnvLoadsBillingEventsQueue(t *testing.T) {
	values := requiredEnv()
	values["AUTH_EVENTS_QUEUE_URL"] = " http://localhost:4566/000000000000/auth-events "
	values["SQS_ENDPOINT"] = "http://localhost:4566/"

	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.EventsQueueURL != "http://localhost:4566/000000000000/auth-events" || config.SQSEndpoint != "http://localhost:4566" {
		t.Fatalf("queue url = %q, sqs endpoint = %q", config.EventsQueueURL, config.SQSEndpoint)
	}
}

func TestFromEnvRejectsInvalidBoolean(t *testing.T) {
	values := requiredEnv()
	values["AUTH_COOKIE_SECURE"] = "sometimes"

	if _, err := FromEnv(env(values)); err == nil {
		t.Fatal("FromEnv() error = nil, want invalid boolean error")
	}
}

func TestAgentGrantsDisabledUnlessAllFourAreSet(t *testing.T) {
	config, err := FromEnv(env(requiredEnv()))
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
		values := requiredEnv()
		for name, value := range agentEnv {
			if name != missing {
				values[name] = value
			}
		}
		config, err := FromEnv(env(values))
		if err != nil {
			t.Fatalf("FromEnv() error = %v (missing %s)", err, missing)
		}
		if config.AgentGrantsEnabled() {
			t.Fatalf("AgentGrantsEnabled() = true without %s", missing)
		}
	}

	values := requiredEnv()
	for name, value := range agentEnv {
		values[name] = value
	}
	config, err = FromEnv(env(values))
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
	values := requiredEnv()
	values["COGNITO_AGENT_CALLBACK_URL"] = "not-a-url"

	if _, err := FromEnv(env(values)); err == nil {
		t.Fatal("FromEnv() error = nil, want an absolute URL error")
	}
}

func TestFromEnvParsesServiceCallers(t *testing.T) {
	values := requiredEnv()
	values["AUTH_SERVICE_CALLERS"] = `[{"service":"agents","key_id":"alias/agents","purposes":["agent"]}]`

	config, err := FromEnv(env(values))
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if len(config.ServiceCallers) != 1 || config.ServiceCallers[0].Service != "agents" || config.ServiceCallers[0].Purposes[0] != "agent" {
		t.Fatalf("service callers = %+v", config.ServiceCallers)
	}
}

func TestFromEnvRejectsInvalidServiceCallersJSON(t *testing.T) {
	values := requiredEnv()
	values["AUTH_SERVICE_CALLERS"] = "not-json"

	if _, err := FromEnv(env(values)); err == nil {
		t.Fatal("FromEnv() error = nil, want invalid callers JSON error")
	}
}
