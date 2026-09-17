package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Config is what teams reads for itself. The listener, the database, the AWS
// region and the event bus are the host's.
type Config struct {
	AuthURL        string
	AllowedOrigins []string
	EventsQueueURL string
}

// FromEnv reads teams' configuration through getenv, which a host layers its
// own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	config := Config{
		AuthURL:        strings.TrimRight(strings.TrimSpace(getenv("AUTH_URL")), "/"),
		AllowedOrigins: splitList(getenv("TEAMS_ALLOWED_ORIGINS")),
		EventsQueueURL: strings.TrimSpace(getenv("TEAMS_EVENTS_QUEUE_URL")),
	}
	if config.AuthURL == "" {
		return Config{}, errors.New("AUTH_URL is required")
	}
	if parsed, err := url.Parse(config.AuthURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Config{}, fmt.Errorf("AUTH_URL must be an absolute URL")
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
