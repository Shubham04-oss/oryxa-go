package provision

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
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
	CallbackURLs       []string
	LogoutURLs         []string
}

func FromEnv() (Config, error) {
	config := Config{
		Region:             envOrDefault("AWS_REGION", "ap-south-1"),
		PoolName:           envOrDefault("COGNITO_POOL_NAME", "oryxa-users"),
		AppClientName:      envOrDefault("COGNITO_APP_CLIENT_NAME", "oryxa-web"),
		DomainPrefix:       strings.TrimSpace(os.Getenv("COGNITO_DOMAIN_PREFIX")),
		GoogleClientID:     strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_ID")),
		GoogleClientSecret: strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET")),
		CallbackURLs:       splitList(os.Getenv("COGNITO_CALLBACK_URLS")),
		LogoutURLs:         splitList(os.Getenv("COGNITO_LOGOUT_URLS")),
	}

	if config.DomainPrefix == "" || config.GoogleClientID == "" || config.GoogleClientSecret == "" {
		return Config{}, errors.New("COGNITO_DOMAIN_PREFIX, GOOGLE_OAUTH_CLIENT_ID, and GOOGLE_OAUTH_CLIENT_SECRET are required")
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
