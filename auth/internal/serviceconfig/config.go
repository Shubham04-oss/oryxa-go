package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// Config is what auth reads for itself. The listener, the database, the AWS
// region and the event bus are the host's.
type Config struct {
	CognitoIssuer       string
	CognitoUserPoolID   string
	CognitoClientID     string
	CognitoDomain       string
	CognitoCallbackURL  string
	EventBridgeEndpoint string
	// EventsQueueURL is the queue billing's entitlements arrive on. Without
	// it no member limits are applied.
	EventsQueueURL         string
	SQSEndpoint            string
	InvitationAcceptURL    string
	EmailVerificationURL   string
	PasswordResetURL       string
	AllowedReturnURLs      []string
	CookieDomain           string
	CookieSecure           bool
	GoogleEnabled          bool
	CognitoCLIClientID     string
	CognitoCLICallbackURL  string
	CognitoCLICallbackURLs []string

	// Agent grants let an agent act for a member while the member is absent.
	// All four of the following are optional; AgentGrantsEnabled reports
	// whether every one of them is set. ServiceCallers gates only the
	// internal token-mint route, separately: a caller named there needs
	// purpose "agent" to use it.
	CognitoAgentClientID     string
	CognitoAgentClientSecret string
	CognitoAgentCallbackURL  string
	AuthKMSKeyID             string
	ServiceCallers           []serviceauth.Caller
}

// AgentGrantsEnabled reports whether every setting agent grants need to
// store and exchange a member's refresh token is present. Without all four,
// every agent grant route answers 404.
func (c Config) AgentGrantsEnabled() bool {
	return c.CognitoAgentClientID != "" && c.CognitoAgentClientSecret != "" && c.CognitoAgentCallbackURL != "" && c.AuthKMSKeyID != ""
}

// FromEnv reads auth's configuration through getenv, which a host layers its
// own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	cookieSecure, err := boolValue(getenv, "AUTH_COOKIE_SECURE", true)
	if err != nil {
		return Config{}, err
	}
	googleEnabled, err := boolValue(getenv, "AUTH_GOOGLE_ENABLED", true)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		CognitoIssuer:          strings.TrimSpace(getenv("COGNITO_ISSUER")),
		CognitoUserPoolID:      strings.TrimSpace(getenv("COGNITO_USER_POOL_ID")),
		CognitoClientID:        strings.TrimSpace(getenv("COGNITO_CLIENT_ID")),
		CognitoDomain:          strings.TrimRight(strings.TrimSpace(getenv("COGNITO_DOMAIN")), "/"),
		CognitoCallbackURL:     strings.TrimSpace(getenv("COGNITO_CALLBACK_URL")),
		EventBridgeEndpoint:    strings.TrimRight(strings.TrimSpace(getenv("EVENTBRIDGE_ENDPOINT")), "/"),
		EventsQueueURL:         strings.TrimSpace(getenv("AUTH_EVENTS_QUEUE_URL")),
		SQSEndpoint:            strings.TrimRight(strings.TrimSpace(getenv("SQS_ENDPOINT")), "/"),
		InvitationAcceptURL:    strings.TrimSpace(getenv("INVITATION_ACCEPT_URL")),
		EmailVerificationURL:   strings.TrimSpace(getenv("EMAIL_VERIFICATION_URL")),
		PasswordResetURL:       strings.TrimSpace(getenv("PASSWORD_RESET_URL")),
		AllowedReturnURLs:      splitList(getenv("AUTH_RETURN_URLS")),
		CookieDomain:           strings.TrimSpace(getenv("AUTH_COOKIE_DOMAIN")),
		CookieSecure:           cookieSecure,
		GoogleEnabled:          googleEnabled,
		CognitoCLIClientID:     strings.TrimSpace(getenv("COGNITO_CLI_CLIENT_ID")),
		CognitoCLICallbackURL:  strings.TrimSpace(getenv("COGNITO_CLI_CALLBACK_URL")),
		CognitoCLICallbackURLs: splitList(getenv("COGNITO_CLI_CALLBACK_URLS")),

		CognitoAgentClientID:     strings.TrimSpace(getenv("COGNITO_AGENT_CLIENT_ID")),
		CognitoAgentClientSecret: strings.TrimSpace(getenv("COGNITO_AGENT_CLIENT_SECRET")),
		CognitoAgentCallbackURL:  strings.TrimSpace(getenv("COGNITO_AGENT_CALLBACK_URL")),
		AuthKMSKeyID:             strings.TrimSpace(getenv("AUTH_KMS_KEY_ID")),
	}
	if config.CognitoIssuer == "" || config.CognitoUserPoolID == "" || config.CognitoClientID == "" || config.CognitoDomain == "" || config.CognitoCallbackURL == "" {
		return Config{}, errors.New("COGNITO_ISSUER, COGNITO_USER_POOL_ID, COGNITO_CLIENT_ID, COGNITO_DOMAIN, and COGNITO_CALLBACK_URL are required")
	}
	if config.CognitoCLIClientID != "" {
		if config.CognitoCLIClientID == config.CognitoClientID || config.CognitoCLIClientID == config.CognitoAgentClientID {
			return Config{}, errors.New("COGNITO_CLI_CLIENT_ID must be distinct from the web and agent app clients")
		}
		seen := map[string]bool{}
		callbacks := append([]string{config.CognitoCLICallbackURL}, config.CognitoCLICallbackURLs...)
		config.CognitoCLICallbackURLs = nil
		for _, callback := range callbacks {
			parsed, err := url.Parse(callback)
			if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.User != nil || parsed.Path != "/callback" || parsed.RawQuery != "" || parsed.Fragment != "" {
				return Config{}, errors.New("CLI callbacks must be http://127.0.0.1:<port>/callback")
			}
			port, err := strconv.Atoi(parsed.Port())
			if err != nil || port < 1 || port > 65535 {
				return Config{}, errors.New("CLI callback port must be between 1 and 65535")
			}
			if !seen[callback] {
				config.CognitoCLICallbackURLs = append(config.CognitoCLICallbackURLs, callback)
				seen[callback] = true
			}
		}

	}
	if config.InvitationAcceptURL == "" || config.EmailVerificationURL == "" || config.PasswordResetURL == "" || len(config.AllowedReturnURLs) == 0 {
		return Config{}, errors.New("INVITATION_ACCEPT_URL, EMAIL_VERIFICATION_URL, PASSWORD_RESET_URL, and AUTH_RETURN_URLS are required")
	}
	urls := append([]string{}, config.AllowedReturnURLs...)
	urls = append(urls, config.CognitoCallbackURL, config.InvitationAcceptURL, config.EmailVerificationURL, config.PasswordResetURL)
	for _, value := range urls {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Config{}, errors.New("auth callback, return, invitation, verification, and reset URLs must be absolute")
		}
	}
	if config.CognitoAgentCallbackURL != "" {
		parsed, err := url.Parse(config.CognitoAgentCallbackURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Config{}, errors.New("COGNITO_AGENT_CALLBACK_URL must be an absolute URL")
		}
	}
	if config.ServiceCallers, err = serviceauth.ParseCallers(getenv("AUTH_SERVICE_CALLERS")); err != nil {
		return Config{}, fmt.Errorf("AUTH_SERVICE_CALLERS: %w", err)
	}
	return config, nil
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

func boolValue(getenv func(string) string, name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.New(name + " must be true or false")
	}
	return parsed, nil
}
