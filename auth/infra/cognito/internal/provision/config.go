package provision

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var domainPrefixPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type Config struct {
	Region             string
	PoolName           string
	AppClientName      string
	DomainPrefix       string
	GoogleClientID     string
	GoogleClientSecret string
	DemoPasswordOnly   bool
	CallbackURLs       []string
	LogoutURLs         []string
	// AgentCallbackURL is COGNITO_AGENT_CALLBACK_URL: {auth public
	// URL}/v1/agent-grant/callback. Optional; the agent resource server and
	// app client are provisioned only when it is set.
	AgentCallbackURL string
	// CLICallbackURL is a fixed loopback redirect for the public setup CLI.
	// Empty leaves the native app client unprovisioned.
	CLICallbackURL string
	// CLICallbackURLs includes registered fallback ports for concurrent sign-ins.
	CLICallbackURLs []string
}

func FromEnv() (Config, error) {
	config := Config{
		Region:             envOrDefault("AWS_REGION", "ap-south-1"),
		PoolName:           envOrDefault("COGNITO_POOL_NAME", "oryxa-users"),
		AppClientName:      envOrDefault("COGNITO_APP_CLIENT_NAME", "oryxa-web"),
		DomainPrefix:       strings.TrimSpace(os.Getenv("COGNITO_DOMAIN_PREFIX")),
		GoogleClientID:     strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_ID")),
		GoogleClientSecret: strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET")),
		DemoPasswordOnly:   strings.EqualFold(strings.TrimSpace(os.Getenv("COGNITO_DEMO_PASSWORD_ONLY")), "true"),
		CallbackURLs:       splitList(os.Getenv("COGNITO_CALLBACK_URLS")),
		LogoutURLs:         splitList(os.Getenv("COGNITO_LOGOUT_URLS")),
		AgentCallbackURL:   strings.TrimSpace(os.Getenv("COGNITO_AGENT_CALLBACK_URL")),
		CLICallbackURL:     strings.TrimSpace(os.Getenv("COGNITO_CLI_CALLBACK_URL")),
		CLICallbackURLs:    splitList(os.Getenv("COGNITO_CLI_CALLBACK_URLS")),
	}

	if config.DomainPrefix == "" {
		return Config{}, errors.New("COGNITO_DOMAIN_PREFIX is required")
	}
	if config.DemoPasswordOnly {
		if config.GoogleClientID != "" || config.GoogleClientSecret != "" {
			return Config{}, errors.New("COGNITO_DEMO_PASSWORD_ONLY cannot be used with Google OAuth credentials")
		}
	} else if config.GoogleClientID == "" || config.GoogleClientSecret == "" {
		return Config{}, errors.New("GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET are required unless COGNITO_DEMO_PASSWORD_ONLY=true")
	}
	if !domainPrefixPattern.MatchString(config.DomainPrefix) {
		return Config{}, errors.New("COGNITO_DOMAIN_PREFIX must use 1-63 lowercase letters, numbers, or interior hyphens")
	}
	if len(config.CallbackURLs) == 0 {
		return Config{}, errors.New("COGNITO_CALLBACK_URLS must contain at least one URL")
	}
	for _, value := range append(append([]string{}, config.CallbackURLs...), config.LogoutURLs...) {
		if err := validateRedirectURL(value); err != nil {
			return Config{}, err
		}
	}
	if config.AgentCallbackURL != "" {
		if err := validateRedirectURL(config.AgentCallbackURL); err != nil {
			return Config{}, err
		}
	}
	if config.CLICallbackURL != "" && !slices.Contains(config.CLICallbackURLs, config.CLICallbackURL) {
		config.CLICallbackURLs = append([]string{config.CLICallbackURL}, config.CLICallbackURLs...)
	}
	for _, callback := range config.CLICallbackURLs {
		parsed, err := url.Parse(callback)
		if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.RawPath != "" || parsed.Path != "/callback" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Config{}, errors.New("CLI callback URLs must be http://127.0.0.1:<port>/callback")
		}
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return Config{}, errors.New("CLI callback URLs must use a valid fixed port")
		}
	}
	if config.CLICallbackURL == "" && len(config.CLICallbackURLs) > 0 {
		config.CLICallbackURL = config.CLICallbackURLs[0]
	}

	return config, nil
}

func validateRedirectURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid Cognito redirect URL %q", value)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1") {
		return nil
	}
	return fmt.Errorf("Cognito redirect URL %q must use HTTPS, except on localhost", value)
}

func splitList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
