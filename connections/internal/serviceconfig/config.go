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
	Address            string
	AWSRegion          string
	DatabaseURL        string
	PostgresExtensions []string
	AutoMigrate        bool
	AuthURL            string
	TeamsURL           string
	PublicBaseURL      string
	AllowedOrigins     []string
	// KMSKeyID is the key that envelope-encrypts client secrets, sessions and
	// grants.
	KMSKeyID            string
	KMSEndpoint         string
	EventBridgeEndpoint string
	SQSEndpoint         string
	EventBusName        string
	EventsQueueURL      string
	// ServiceCallers are the services that may call the internal API with
	// their own identity, their KMS keys, and their purposes.
	ServiceCallers []serviceauth.Caller
}

func FromEnv() (Config, error) {
	autoMigrate, err := boolValue("CONNECTIONS_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	genericEndpoint := endpoint("AWS_ENDPOINT_URL")
	config := Config{
		Address:            valueOrDefault("CONNECTIONS_ADDRESS", ":8080"),
		AWSRegion:          valueOrDefault("AWS_REGION", defaultRegion),
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		PostgresExtensions: splitList(os.Getenv("POSTGRES_REQUIRED_EXTENSIONS")),
		AutoMigrate:        autoMigrate,
		AuthURL:            endpoint("AUTH_URL"),
		TeamsURL:           endpoint("TEAMS_URL"),
		PublicBaseURL:      endpoint("CONNECTIONS_PUBLIC_BASE_URL"),
		AllowedOrigins:     splitList(os.Getenv("CONNECTIONS_ALLOWED_ORIGINS")),
		KMSKeyID:           strings.TrimSpace(os.Getenv("CONNECTIONS_KMS_KEY_ID")),
		// The local stack runs no KMS, so KMS never follows AWS_ENDPOINT_URL.
		KMSEndpoint:         endpoint("AWS_ENDPOINT_URL_KMS"),
		EventBridgeEndpoint: endpointOr("EVENTBRIDGE_ENDPOINT", genericEndpoint),
		SQSEndpoint:         endpointOr("AWS_ENDPOINT_URL_SQS", genericEndpoint),
		EventBusName:        valueOrDefault("EVENT_BUS_NAME", "default"),
		EventsQueueURL:      strings.TrimSpace(os.Getenv("CONNECTIONS_EVENTS_QUEUE_URL")),
	}
	if config.DatabaseURL == "" || config.AuthURL == "" || config.PublicBaseURL == "" || config.KMSKeyID == "" {
		return Config{}, errors.New("DATABASE_URL, AUTH_URL, CONNECTIONS_PUBLIC_BASE_URL and CONNECTIONS_KMS_KEY_ID are required")
	}
	if config.ServiceCallers, err = serviceauth.ParseCallers(os.Getenv("CONNECTIONS_SERVICE_CALLERS")); err != nil {
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
