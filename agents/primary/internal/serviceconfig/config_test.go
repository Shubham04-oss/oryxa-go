package serviceconfig

import (
	"testing"
	"time"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"AGENTS_PRIMARY_ADDRESS", "AGENTS_PRIMARY_MODEL", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "DATABASE_URL", "AGENTS_PRIMARY_AUTO_MIGRATE", "AWS_REGION", "AGENTS_PRIMARY_AWS_ROLE_ARN", "AGENTS_PRIMARY_AWS_ROLE_SESSION_NAME", "AGENTS_PRIMARY_AWS_ROLE_EXTERNAL_ID", "AGENTS_PRIMARY_WORKLOAD_IDENTITY_NAME", "AGENTS_PRIMARY_TRUST_RUNTIME_IDENTITY_HEADER", "AGENTS_PRIMARY_ALLOW_UNVERIFIED_IDENTITY_USER_ID", "AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID", "AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS", "AGENTS_PRIMARY_BROWSER_ID", "AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS"} {
		t.Setenv(name, "")
	}
}

func TestFromEnvDefaults(t *testing.T) {
	clearEnv(t)

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8080" || config.Model != "gemini-3.8-flash" || config.GoogleCloudProject != "" || config.GoogleCloudLocation != "global" || config.DatabaseURL != "" || !config.AutoMigrate || config.AWSRegion != defaultAWSRegion || config.AWSRoleARN != "" || config.AWSRoleSessionName != "oryxa-agents-primary" || config.AWSRoleExternalID != "" || config.WorkloadIdentityName != "oryxa-primary" || config.TrustRuntimeIdentityHeader || config.AllowUnverifiedIdentityUserID || config.SandboxCodeInterpreter != "aws.codeinterpreter.v1" || config.SandboxSessionTimeout != 15*time.Minute || config.BrowserIdentifier != "aws.browser.v1" || config.BrowserSessionTimeout != 15*time.Minute {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("AGENTS_PRIMARY_ADDRESS", " :8086 ")
	t.Setenv("AGENTS_PRIMARY_MODEL", " gemini-3.5-flash-lite ")
	t.Setenv("GOOGLE_CLOUD_PROJECT", " oryxa-dev ")
	t.Setenv("GOOGLE_CLOUD_LOCATION", " eu ")
	t.Setenv("DATABASE_URL", " postgres://oryxa@localhost:5435/oryxa ")
	t.Setenv("AGENTS_PRIMARY_AUTO_MIGRATE", " false ")
	t.Setenv("AWS_REGION", " us-east-1 ")
	t.Setenv("AGENTS_PRIMARY_AWS_ROLE_ARN", " arn:aws:iam::123456789012:role/oryxa-agent ")
	t.Setenv("AGENTS_PRIMARY_AWS_ROLE_SESSION_NAME", " local-primary ")
	t.Setenv("AGENTS_PRIMARY_AWS_ROLE_EXTERNAL_ID", " tenant-secret ")
	t.Setenv("AGENTS_PRIMARY_WORKLOAD_IDENTITY_NAME", " oryxa-primary-dev ")
	t.Setenv("AGENTS_PRIMARY_TRUST_RUNTIME_IDENTITY_HEADER", " true ")
	t.Setenv("AGENTS_PRIMARY_ALLOW_UNVERIFIED_IDENTITY_USER_ID", " true ")
	t.Setenv("AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID", " ci-custom ")
	t.Setenv("AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS", " 3600 ")
	t.Setenv("AGENTS_PRIMARY_BROWSER_ID", " browser-custom ")
	t.Setenv("AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS", " 1800 ")

	config, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if config.Address != ":8086" || config.Model != "gemini-3.5-flash-lite" || config.GoogleCloudProject != "oryxa-dev" || config.GoogleCloudLocation != "eu" || config.DatabaseURL != "postgres://oryxa@localhost:5435/oryxa" || config.AutoMigrate || config.AWSRegion != "us-east-1" || config.AWSRoleARN != "arn:aws:iam::123456789012:role/oryxa-agent" || config.AWSRoleSessionName != "local-primary" || config.AWSRoleExternalID != "tenant-secret" || config.WorkloadIdentityName != "oryxa-primary-dev" || !config.TrustRuntimeIdentityHeader || !config.AllowUnverifiedIdentityUserID || config.SandboxCodeInterpreter != "ci-custom" || config.SandboxSessionTimeout != time.Hour || config.BrowserIdentifier != "browser-custom" || config.BrowserSessionTimeout != 30*time.Minute {
		t.Fatalf("config = %+v", config)
	}
}

func TestFromEnvRejectsInvalidSandboxTimeout(t *testing.T) {
	clearEnv(t)
	for _, value := range []string{"sometimes", "59", "28801"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS", value)
			if _, err := FromEnv(); err == nil {
				t.Fatal("FromEnv() error = nil")
			}
		})
	}
}

func TestFromEnvRejectsInvalidBrowserTimeout(t *testing.T) {
	clearEnv(t)
	for _, value := range []string{"sometimes", "59", "28801"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS", value)
			if _, err := FromEnv(); err == nil {
				t.Fatal("FromEnv() error = nil")
			}
		})
	}
}

func TestFromEnvRejectsInvalidAutoMigrate(t *testing.T) {
	clearEnv(t)
	t.Setenv("AGENTS_PRIMARY_AUTO_MIGRATE", "sometimes")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv() error = nil")
	}
}

func TestFromEnvRejectsInvalidIdentityBooleans(t *testing.T) {
	clearEnv(t)
	for _, name := range []string{"AGENTS_PRIMARY_TRUST_RUNTIME_IDENTITY_HEADER", "AGENTS_PRIMARY_ALLOW_UNVERIFIED_IDENTITY_USER_ID"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "sometimes")
			if _, err := FromEnv(); err == nil {
				t.Fatal("FromEnv() error = nil")
			}
		})
	}
}
