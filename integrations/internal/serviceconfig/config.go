// Package serviceconfig reads integrations' configuration from the
// environment.
package serviceconfig

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const defaultRegion = "ap-south-1"

type Config struct {
	Address            string
	AWSRegion          string
	DatabaseURL        string
	PostgresExtensions []string
	AutoMigrate        bool
	AuthURL            string
	ConnectionsURL     string
	// PublicBaseURL is the address providers reach integrations at, in front
	// of which Cloudflare sits in production. Webhook URLs are built on it.
	PublicBaseURL  string
	AllowedOrigins []string
	// KMSKeyID envelope-encrypts webhook secrets.
	KMSKeyID            string
	KMSEndpoint         string
	EventBridgeEndpoint string
	SQSEndpoint         string
	EventBusName        string
	EventsQueueURL      string
	// ServiceName and ServiceKeyID are the identity integrations signs its
	// calls to connections with.
	ServiceName  string
	ServiceKeyID string
}

func FromEnv() (Config, error) {
	autoMigrate, err := boolValue("INTEGRATIONS_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	genericEndpoint := endpoint("AWS_ENDPOINT_URL")
	config := Config{
		Address:            valueOrDefault("INTEGRATIONS_ADDRESS", ":8080"),
		AWSRegion:          valueOrDefault("AWS_REGION", defaultRegion),
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		PostgresExtensions: splitList(os.Getenv("POSTGRES_REQUIRED_EXTENSIONS")),
		AutoMigrate:        autoMigrate,
		AuthURL:            endpoint("AUTH_URL"),
		ConnectionsURL:     endpoint("CONNECTIONS_URL"),
		PublicBaseURL:      endpoint("INTEGRATIONS_PUBLIC_BASE_URL"),
		AllowedOrigins:     splitList(os.Getenv("INTEGRATIONS_ALLOWED_ORIGINS")),
		KMSKeyID:           strings.TrimSpace(os.Getenv("INTEGRATIONS_KMS_KEY_ID")),
		// The local stack runs no KMS, so KMS never follows AWS_ENDPOINT_URL.
		KMSEndpoint:         endpoint("AWS_ENDPOINT_URL_KMS"),
		EventBridgeEndpoint: endpointOr("EVENTBRIDGE_ENDPOINT", genericEndpoint),
		SQSEndpoint:         endpointOr("AWS_ENDPOINT_URL_SQS", genericEndpoint),
		EventBusName:        valueOrDefault("EVENT_BUS_NAME", "default"),
		EventsQueueURL:      strings.TrimSpace(os.Getenv("INTEGRATIONS_EVENTS_QUEUE_URL")),
		ServiceName:         valueOrDefault("INTEGRATIONS_SERVICE_NAME", "integrations"),
		ServiceKeyID:        strings.TrimSpace(os.Getenv("INTEGRATIONS_SERVICE_KEY_ID")),
	}
	if config.DatabaseURL == "" || config.AuthURL == "" || config.ConnectionsURL == "" || config.PublicBaseURL == "" ||
		config.KMSKeyID == "" || config.ServiceKeyID == "" {
		return Config{}, errors.New("DATABASE_URL, AUTH_URL, CONNECTIONS_URL, INTEGRATIONS_PUBLIC_BASE_URL, INTEGRATIONS_KMS_KEY_ID and INTEGRATIONS_SERVICE_KEY_ID are required")
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

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func endpoint(name string) string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(name)), "/")
}

func endpointOr(name, fallback string) string {
	if value := endpoint(name); value != "" {
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

func absoluteURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}
