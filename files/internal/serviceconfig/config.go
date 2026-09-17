package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Config is what files reads for itself. The listener, the database, the AWS
// region and the event bus are the host's.
type Config struct {
	S3Endpoint     string
	BucketName     string
	AuthURL        string
	TeamsURL       string
	AllowedOrigins []string
	EventsQueueURL string
}

// FromEnv reads files' configuration through getenv, which a host layers its
// own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	genericEndpoint := endpoint(getenv, "AWS_ENDPOINT_URL")
	config := Config{
		S3Endpoint:     endpointOr(getenv, "AWS_ENDPOINT_URL_S3", genericEndpoint),
		BucketName:     strings.TrimSpace(getenv("FILES_BUCKET_NAME")),
		AuthURL:        strings.TrimRight(strings.TrimSpace(getenv("AUTH_URL")), "/"),
		TeamsURL:       strings.TrimRight(strings.TrimSpace(getenv("TEAMS_URL")), "/"),
		AllowedOrigins: splitList(getenv("FILES_ALLOWED_ORIGINS")),
		EventsQueueURL: strings.TrimSpace(getenv("FILES_EVENTS_QUEUE_URL")),
	}
	if config.AuthURL == "" || config.BucketName == "" {
		return Config{}, errors.New("AUTH_URL and FILES_BUCKET_NAME are required")
	}
	if parsed, err := url.Parse(config.AuthURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Config{}, fmt.Errorf("AUTH_URL must be an absolute URL")
	}
	if config.TeamsURL != "" {
		if parsed, err := url.Parse(config.TeamsURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Config{}, fmt.Errorf("TEAMS_URL must be an absolute URL")
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
