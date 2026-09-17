// Package serviceconfig reads integrations' own configuration from the
// environment.
package serviceconfig

import (
	"errors"
	"net/url"
	"strings"
)

// Config is what integrations reads for itself. The listener, the database,
// auto-migration, the AWS region and the event bus are the host's.
type Config struct {
	AuthURL        string
	ConnectionsURL string
	// PublicBaseURL is the address providers reach integrations at, in front
	// of which Cloudflare sits in production. Webhook URLs are built on it.
	PublicBaseURL  string
	AllowedOrigins []string
	// KMSKeyID envelope-encrypts webhook secrets.
	KMSKeyID            string
	KMSEndpoint         string
	EventBridgeEndpoint string
	SQSEndpoint         string
	EventsQueueURL      string
	// ServiceName and ServiceKeyID are the identity integrations signs its
	// calls to connections with.
	ServiceName  string
	ServiceKeyID string
}

// FromEnv reads integrations' configuration through getenv, which a host
// layers its own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	genericEndpoint := endpoint(getenv, "AWS_ENDPOINT_URL")
	config := Config{
		AuthURL:        endpoint(getenv, "AUTH_URL"),
		ConnectionsURL: endpoint(getenv, "CONNECTIONS_URL"),
		PublicBaseURL:  endpoint(getenv, "INTEGRATIONS_PUBLIC_BASE_URL"),
		AllowedOrigins: splitList(getenv("INTEGRATIONS_ALLOWED_ORIGINS")),
		KMSKeyID:       strings.TrimSpace(getenv("INTEGRATIONS_KMS_KEY_ID")),
		// The local stack runs no KMS, so KMS never follows AWS_ENDPOINT_URL.
		KMSEndpoint:         endpoint(getenv, "AWS_ENDPOINT_URL_KMS"),
		EventBridgeEndpoint: endpointOr(getenv, "EVENTBRIDGE_ENDPOINT", genericEndpoint),
		SQSEndpoint:         endpointOr(getenv, "AWS_ENDPOINT_URL_SQS", genericEndpoint),
		EventsQueueURL:      strings.TrimSpace(getenv("INTEGRATIONS_EVENTS_QUEUE_URL")),
		ServiceName:         valueOrDefault(getenv, "INTEGRATIONS_SERVICE_NAME", "integrations"),
		ServiceKeyID:        strings.TrimSpace(getenv("INTEGRATIONS_SERVICE_KEY_ID")),
	}
	if config.AuthURL == "" || config.ConnectionsURL == "" || config.PublicBaseURL == "" ||
		config.KMSKeyID == "" || config.ServiceKeyID == "" {
		return Config{}, errors.New("AUTH_URL, CONNECTIONS_URL, INTEGRATIONS_PUBLIC_BASE_URL, INTEGRATIONS_KMS_KEY_ID and INTEGRATIONS_SERVICE_KEY_ID are required")
	}
	for _, check := range []struct{ name, value string }{
		{"AUTH_URL", config.AuthURL},
		{"CONNECTIONS_URL", config.ConnectionsURL},
		{"INTEGRATIONS_PUBLIC_BASE_URL", config.PublicBaseURL},
		{"AWS_ENDPOINT_URL_KMS", config.KMSEndpoint},
		{"EVENTBRIDGE_ENDPOINT", config.EventBridgeEndpoint},
		{"AWS_ENDPOINT_URL_SQS", config.SQSEndpoint},
	} {
		if check.value != "" && !absoluteURL(check.value) {
			return Config{}, errors.New(check.name + " must be an absolute URL")
		}
	}
	for _, origin := range config.AllowedOrigins {
		if !absoluteURL(origin) {
			return Config{}, errors.New("INTEGRATIONS_ALLOWED_ORIGINS must list absolute origins")
		}
	}
	return config, nil
}

func valueOrDefault(getenv func(string) string, name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
		return value
	}
	return fallback
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
