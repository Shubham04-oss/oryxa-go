package provision

import (
	"slices"
	"testing"
)

func TestCLIFallbackRedirectsAreValidatedAndRegistered(t *testing.T) {
	t.Setenv("COGNITO_DOMAIN_PREFIX", "oryxa-test")
	t.Setenv("COGNITO_CALLBACK_URLS", "http://localhost:8080/v1/auth/callback")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "")
	t.Setenv("COGNITO_DEMO_PASSWORD_ONLY", "true")
	t.Setenv("COGNITO_CLI_CALLBACK_URL", "http://127.0.0.1:8765/callback")
	t.Setenv("COGNITO_CLI_CALLBACK_URLS", "http://127.0.0.1:8766/callback,http://127.0.0.1:8765/callback")
	config, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	client := cliAppConfig(config)
	want := []string{"http://127.0.0.1:8765/callback", "http://127.0.0.1:8766/callback"}
	if !slices.Equal(client.CallbackURLs, want) {
		t.Fatalf("registered callbacks = %v, want %v", client.CallbackURLs, want)
	}
	for _, invalid := range []string{"http://user@127.0.0.1:8766/callback", "http://127.0.0.1:0/callback", "http://127.0.0.1:65536/callback", "http://127.0.0.1:8766/callback?next=outside", "http://outside.example:8766/callback"} {
		t.Setenv("COGNITO_CLI_CALLBACK_URLS", invalid)
		if _, err := FromEnv(); err == nil {
			t.Fatalf("accepted unsafe callback %q", invalid)
		}
	}
}
