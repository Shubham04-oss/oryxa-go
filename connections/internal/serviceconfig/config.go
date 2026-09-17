package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// Config is what connections reads for itself. The listener, the database,
// the AWS region and the event bus are the host's.
type Config struct {
	AuthURL        string
	TeamsURL       string
	PublicBaseURL  string
	AllowedOrigins []string
	// KMSKeyID is the key that envelope-encrypts client secrets, sessions and
	// grants.
	KMSKeyID            string
	KMSEndpoint         string
	EventBridgeEndpoint string
	SQSEndpoint         string
	EventsQueueURL      string
	// ServiceCallers are the services that may call the internal API with
	// their own identity, their KMS keys, and their purposes.
	ServiceCallers []serviceauth.Caller
}

// FromEnv reads connections' configuration through getenv, which a host
// layers its own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	genericEndpoint := endpoint(getenv, "AWS_ENDPOINT_URL")
	config := Config{
		AuthURL:        endpoint(getenv, "AUTH_URL"),
		TeamsURL:       endpoint(getenv, "TEAMS_URL"),
		PublicBaseURL:  endpoint(getenv, "CONNECTIONS_PUBLIC_BASE_URL"),
		AllowedOrigins: splitList(getenv("CONNECTIONS_ALLOWED_ORIGINS")),
		KMSKeyID:       strings.TrimSpace(getenv("CONNECTIONS_KMS_KEY_ID")),
		// The local stack runs no KMS, so KMS never follows AWS_ENDPOINT_URL.
		KMSEndpoint:         endpoint(getenv, "AWS_ENDPOINT_URL_KMS"),
		EventBridgeEndpoint: endpointOr(getenv, "EVENTBRIDGE_ENDPOINT", genericEndpoint),
		SQSEndpoint:         endpointOr(getenv, "AWS_ENDPOINT_URL_SQS", genericEndpoint),
		EventsQueueURL:      strings.TrimSpace(getenv("CONNECTIONS_EVENTS_QUEUE_URL")),
	}
	if config.AuthURL == "" || config.PublicBaseURL == "" || config.KMSKeyID == "" {
		return Config{}, errors.New("AUTH_URL, CONNECTIONS_PUBLIC_BASE_URL and CONNECTIONS_KMS_KEY_ID are required")
	}
	var err error
	if config.ServiceCallers, err = serviceauth.ParseCallers(getenv("CONNECTIONS_SERVICE_CALLERS")); err != nil {
		return Config{}, fmt.Errorf("CONNECTIONS_SERVICE_CALLERS: %w", err)
	}
	for _, caller := range config.ServiceCallers {
		for _, purpose := range caller.Purposes {
			if purpose != "action" && purpose != "trigger" && purpose != "cleanup" && purpose != "heartbeat" {
				return Config{}, fmt.Errorf("CONNECTIONS_SERVICE_CALLERS: %s names purpose %q, which is not action, trigger, cleanup or heartbeat", caller.Service, purpose)
			}
		}
	}
	for _, check := range []struct{ name, value string }{
		{"AUTH_URL", config.AuthURL},
		{"CONNECTIONS_PUBLIC_BASE_URL", config.PublicBaseURL},
		{"TEAMS_URL", config.TeamsURL},
		{"AWS_ENDPOINT_URL_KMS", config.KMSEndpoint},
		{"EVENTBRIDGE_ENDPOINT", config.EventBridgeEndpoint},
		{"AWS_ENDPOINT_URL_SQS", config.SQSEndpoint},
	} {
		if check.value != "" && !absoluteURL(check.value) {
			return Config{}, fmt.Errorf("%s must be an absolute URL", check.name)
		}
	}
	for _, origin := range config.AllowedOrigins {
		if !absoluteURL(origin) {
			return Config{}, errors.New("CONNECTIONS_ALLOWED_ORIGINS must list absolute origins")
		}
	}
	return config, nil
}

func endpoint(getenv func(string) string, name string) string {
	return strings.TrimRight(strings.TrimSpace(getenv(name)), "/")
}

func endpointOr(getenv func(string) string, name, fallback string) string {
	if value := endpoint(getenv, name); value != "" {
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

func absoluteURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}
