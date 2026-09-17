// Package serviceconfig reads knowledge's configuration from the
// environment.
package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Config is what knowledge reads for itself. The listener, the database, the
// AWS region and the event bus are the host's.
type Config struct {
	AuthURL        string
	ConnectionsURL string
	// AllowedOrigins may send cookie-authenticated mutations.
	AllowedOrigins []string
	// KMSEndpoint, EventBridgeEndpoint and SQSEndpoint override where those
	// AWS clients call, for a local emulator.
	KMSEndpoint         string
	EventBridgeEndpoint string
	SQSEndpoint         string
	// EventsQueueURL is the queue auth's and connections' events arrive on.
	// Without it knowledge doesn't learn that a connection, app, member or
	// organization is gone.
	EventsQueueURL string
	// ServiceName and ServiceKeyID are the identity knowledge signs its calls
	// to connections with.
	ServiceName  string
	ServiceKeyID string
}

// FromEnv reads knowledge's configuration through getenv, which a host layers
// its own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	genericEndpoint := endpoint(getenv, "AWS_ENDPOINT_URL")
	config := Config{
		AuthURL:        endpoint(getenv, "AUTH_URL"),
		ConnectionsURL: endpoint(getenv, "CONNECTIONS_URL"),
		AllowedOrigins: splitList(getenv("KNOWLEDGE_ALLOWED_ORIGINS")),
		// The local stack runs no KMS, so KMS never follows AWS_ENDPOINT_URL.
		KMSEndpoint:         endpoint(getenv, "AWS_ENDPOINT_URL_KMS"),
		EventBridgeEndpoint: endpointOr(getenv, "EVENTBRIDGE_ENDPOINT", genericEndpoint),
		SQSEndpoint:         endpointOr(getenv, "AWS_ENDPOINT_URL_SQS", genericEndpoint),
		EventsQueueURL:      strings.TrimSpace(getenv("KNOWLEDGE_EVENTS_QUEUE_URL")),
		ServiceName:         valueOr(getenv, "KNOWLEDGE_SERVICE_NAME", "knowledge"),
		ServiceKeyID:        strings.TrimSpace(getenv("KNOWLEDGE_SERVICE_KEY_ID")),
	}
	if config.AuthURL == "" || config.ConnectionsURL == "" || config.ServiceKeyID == "" {
		return Config{}, errors.New("AUTH_URL, CONNECTIONS_URL and KNOWLEDGE_SERVICE_KEY_ID are required")
	}
	for _, check := range []struct{ name, value string }{
		{"AUTH_URL", config.AuthURL},
		{"CONNECTIONS_URL", config.ConnectionsURL},
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
			return Config{}, errors.New("KNOWLEDGE_ALLOWED_ORIGINS must list absolute origins")
		}
	}
	return config, nil
}

func valueOr(getenv func(string) string, name, fallback string) string {
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
