package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

const defaultRegion = "ap-south-1"

type Config struct {
	Address             string
	AWSRegion           string
	CognitoIssuer       string
	CognitoUserPoolID   string
	CognitoClientID     string
	CognitoDomain       string
	CognitoCallbackURL  string
	EventBusName        string
	EventBridgeEndpoint string
	// EventsQueueURL is the queue billing's entitlements arrive on. Without
	// it no member limits are applied.
	EventsQueueURL       string
	SQSEndpoint          string
	DatabaseURL          string
	PostgresExtensions   []string
	InvitationAcceptURL  string
	EmailVerificationURL string
	PasswordResetURL     string
	AllowedReturnURLs    []string
	CookieDomain         string
	CookieSecure         bool
	AutoMigrate          bool

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

func FromEnv() (Config, error) {
	cookieSecure, err := boolValue("AUTH_COOKIE_SECURE", true)
	if err != nil {
		return Config{}, err
	}
	autoMigrate, err := boolValue("AUTH_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		Address:              valueOrDefault("AUTH_ADDRESS", ":8080"),
		AWSRegion:            valueOrDefault("AWS_REGION", defaultRegion),
		CognitoIssuer:        strings.TrimSpace(os.Getenv("COGNITO_ISSUER")),
		CognitoUserPoolID:    strings.TrimSpace(os.Getenv("COGNITO_USER_POOL_ID")),
		CognitoClientID:      strings.TrimSpace(os.Getenv("COGNITO_CLIENT_ID")),
		CognitoDomain:        strings.TrimRight(strings.TrimSpace(os.Getenv("COGNITO_DOMAIN")), "/"),
		CognitoCallbackURL:   strings.TrimSpace(os.Getenv("COGNITO_CALLBACK_URL")),
		EventBusName:         valueOrDefault("EVENT_BUS_NAME", "default"),
		EventBridgeEndpoint:  strings.TrimRight(strings.TrimSpace(os.Getenv("EVENTBRIDGE_ENDPOINT")), "/"),
		EventsQueueURL:       strings.TrimSpace(os.Getenv("AUTH_EVENTS_QUEUE_URL")),
		SQSEndpoint:          strings.TrimRight(strings.TrimSpace(os.Getenv("SQS_ENDPOINT")), "/"),
		DatabaseURL:          strings.TrimSpace(os.Getenv("DATABASE_URL")),
		PostgresExtensions:   splitList(os.Getenv("POSTGRES_REQUIRED_EXTENSIONS")),
		InvitationAcceptURL:  strings.TrimSpace(os.Getenv("INVITATION_ACCEPT_URL")),
		EmailVerificationURL: strings.TrimSpace(os.Getenv("EMAIL_VERIFICATION_URL")),
		PasswordResetURL:     strings.TrimSpace(os.Getenv("PASSWORD_RESET_URL")),
		AllowedReturnURLs:    splitList(os.Getenv("AUTH_RETURN_URLS")),
		CookieDomain:         strings.TrimSpace(os.Getenv("AUTH_COOKIE_DOMAIN")),
		CookieSecure:         cookieSecure,
		AutoMigrate:          autoMigrate,

		CognitoAgentClientID:     strings.TrimSpace(os.Getenv("COGNITO_AGENT_CLIENT_ID")),
		CognitoAgentClientSecret: strings.TrimSpace(os.Getenv("COGNITO_AGENT_CLIENT_SECRET")),
		CognitoAgentCallbackURL:  strings.TrimSpace(os.Getenv("COGNITO_AGENT_CALLBACK_URL")),
		AuthKMSKeyID:             strings.TrimSpace(os.Getenv("AUTH_KMS_KEY_ID")),
	}
	if config.CognitoIssuer == "" || config.CognitoUserPoolID == "" || config.CognitoClientID == "" || config.CognitoDomain == "" || config.CognitoCallbackURL == "" {
		return Config{}, errors.New("COGNITO_ISSUER, COGNITO_USER_POOL_ID, COGNITO_CLIENT_ID, COGNITO_DOMAIN, and COGNITO_CALLBACK_URL are required")
	}
	if config.DatabaseURL == "" || config.InvitationAcceptURL == "" || config.EmailVerificationURL == "" || config.PasswordResetURL == "" || len(config.AllowedReturnURLs) == 0 {
		return Config{}, errors.New("DATABASE_URL, INVITATION_ACCEPT_URL, EMAIL_VERIFICATION_URL, PASSWORD_RESET_URL, and AUTH_RETURN_URLS are required")
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
	if config.ServiceCallers, err = serviceauth.ParseCallers(os.Getenv("AUTH_SERVICE_CALLERS")); err != nil {
		return Config{}, fmt.Errorf("AUTH_SERVICE_CALLERS: %w", err)
	}
	return config, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
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

func boolValue(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.New(name + " must be true or false")
	}
	return parsed, nil
}
