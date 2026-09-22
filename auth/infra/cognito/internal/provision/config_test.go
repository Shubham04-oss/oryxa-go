package provision

import "testing"

func TestFromEnvDemoPasswordOnly(t *testing.T) {
	t.Setenv("COGNITO_DOMAIN_PREFIX", "oryxa-demo-test")
	t.Setenv("COGNITO_CALLBACK_URLS", "http://localhost:8080/v1/auth/callback")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "")
	t.Setenv("COGNITO_DEMO_PASSWORD_ONLY", "true")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if !config.DemoPasswordOnly {
		t.Fatal("FromEnv() did not enable demo password-only mode")
	}
}

func TestFromEnvRequiresGoogleOutsideDemo(t *testing.T) {
	t.Setenv("COGNITO_DOMAIN_PREFIX", "oryxa-test")
	t.Setenv("COGNITO_CALLBACK_URLS", "http://localhost:8080/v1/auth/callback")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "")
	t.Setenv("COGNITO_DEMO_PASSWORD_ONLY", "")

	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() accepted missing Google credentials outside demo mode")
	}
}

func TestValidateRedirectURL(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "https", value: "https://app.example.com/auth/callback"},
		{name: "localhost", value: "http://localhost:3000/auth/callback"},
		{name: "insecure remote", value: "http://app.example.com/auth/callback", wantErr: true},
		{name: "fragment", value: "https://app.example.com/auth/callback#token", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRedirectURL(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateRedirectURL() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
