package serviceconfig

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultAWSRegion = "ap-south-1"

type Config struct {
	Address string
	// Model is the Gemini model the agents run on.
	Model string
	// GoogleCloudProject and GoogleCloudLocation are where Vertex AI serves
	// the model.
	GoogleCloudProject  string
	GoogleCloudLocation string
	// DatabaseURL backs ADK sessions and state. AutoMigrate lets ADK create or
	// update its tables inside the agents_primary PostgreSQL schema.
	DatabaseURL string
	AutoMigrate bool
	// AWSRegion serves the AgentCore sandbox. SandboxCodeInterpreter is either
	// AWS's built-in identifier or a provisioned interpreter with organization
	// specific network and IAM controls.
	AWSRegion string
	// AWSRoleARN optionally makes local or cross-account runs assume the same
	// scoped role the agent uses in AWS. In an AWS deployment the default
	// credential chain should normally resolve the compute workload role.
	AWSRoleARN         string
	AWSRoleSessionName string
	AWSRoleExternalID  string
	// WorkloadIdentityName identifies this agent to AgentCore Identity when it
	// runs outside AgentCore Runtime. Runtime delivers its managed workload
	// access token instead, and its header is trusted only when explicitly set.
	WorkloadIdentityName       string
	TrustRuntimeIdentityHeader bool
	// The UserId token path is not cryptographically verified by AgentCore and
	// remains disabled unless a trusted upstream identity resolver requires it.
	AllowUnverifiedIdentityUserID bool

	SandboxCodeInterpreter string
	SandboxSessionTimeout  time.Duration
	// BrowserIdentifier is AWS's built-in AgentCore Browser identifier or a
	// provisioned custom browser. Each ADK conversation receives its own
	// BrowserSessionTimeout-scoped browser session.
	BrowserIdentifier     string
	BrowserSessionTimeout time.Duration
}

func FromEnv() (Config, error) {
	autoMigrate, err := boolValue("AGENTS_PRIMARY_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	sandboxTimeout, err := secondsValue("AGENTS_PRIMARY_SANDBOX_SESSION_TIMEOUT_SECONDS", 15*time.Minute, time.Minute, 8*time.Hour)
	if err != nil {
		return Config{}, err
	}
	browserTimeout, err := secondsValue("AGENTS_PRIMARY_BROWSER_SESSION_TIMEOUT_SECONDS", 15*time.Minute, time.Minute, 8*time.Hour)
	if err != nil {
		return Config{}, err
	}
	trustRuntimeIdentityHeader, err := boolValue("AGENTS_PRIMARY_TRUST_RUNTIME_IDENTITY_HEADER", false)
	if err != nil {
		return Config{}, err
	}
	allowUnverifiedIdentityUserID, err := boolValue("AGENTS_PRIMARY_ALLOW_UNVERIFIED_IDENTITY_USER_ID", false)
	if err != nil {
		return Config{}, err
	}
	return Config{
		Address:             valueOrDefault("AGENTS_PRIMARY_ADDRESS", ":8080"),
		Model:               valueOrDefault("AGENTS_PRIMARY_MODEL", "gemini-3.8-flash"),
		GoogleCloudProject:  strings.TrimSpace(os.Getenv("GOOGLE_CLOUD_PROJECT")),
		GoogleCloudLocation: valueOrDefault("GOOGLE_CLOUD_LOCATION", "global"),
		DatabaseURL:         strings.TrimSpace(os.Getenv("DATABASE_URL")),
		AutoMigrate:         autoMigrate,
		AWSRegion:           valueOrDefault("AWS_REGION", defaultAWSRegion),
		AWSRoleARN:          strings.TrimSpace(os.Getenv("AGENTS_PRIMARY_AWS_ROLE_ARN")),
		AWSRoleSessionName:  valueOrDefault("AGENTS_PRIMARY_AWS_ROLE_SESSION_NAME", "oryxa-agents-primary"),
		AWSRoleExternalID:   strings.TrimSpace(os.Getenv("AGENTS_PRIMARY_AWS_ROLE_EXTERNAL_ID")),
		WorkloadIdentityName: valueOrDefault(
			"AGENTS_PRIMARY_WORKLOAD_IDENTITY_NAME",
			"oryxa-primary",
		),
		TrustRuntimeIdentityHeader:    trustRuntimeIdentityHeader,
		AllowUnverifiedIdentityUserID: allowUnverifiedIdentityUserID,
		SandboxCodeInterpreter: valueOrDefault(
			"AGENTS_PRIMARY_SANDBOX_CODE_INTERPRETER_ID",
			"aws.codeinterpreter.v1",
		),
		SandboxSessionTimeout: sandboxTimeout,
		BrowserIdentifier: valueOrDefault(
			"AGENTS_PRIMARY_BROWSER_ID",
			"aws.browser.v1",
		),
		BrowserSessionTimeout: browserTimeout,
	}, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func boolValue(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func secondsValue(name string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number of seconds", name)
	}
	duration := time.Duration(seconds) * time.Second
	if duration < minimum || duration > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d seconds", name, int(minimum/time.Second), int(maximum/time.Second))
	}
	return duration, nil
}
