// Package serviceconfig reads realtime's configuration from the environment.
package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// MinSecretLength is the shortest REALTIME_PURGE_SECRET accepted. The secret
// lets whoever holds it delete conversations, so it should be at least as
// long as the SHA-256 it keys; openssl rand -hex 32 makes one.
const MinSecretLength = 32

// Config is what realtime reads for itself. The AWS region and the event bus
// are the host's.
type Config struct {
	// WorkerURL is the web app's origin, where its Worker takes
	// POST /internal/realtime/purge. It has no path: the Worker matches the
	// route's path exactly, so one under another path would never reach it.
	WorkerURL string
	// PurgeSecret signs every purge. The Worker holds the same secret.
	PurgeSecret string
	// EventsQueueURL is the queue auth's membership endings arrive on.
	EventsQueueURL string
}

// FromEnv reads realtime's configuration through getenv, which a host layers
// its own defaults under. Every setting may be unset, which Missing reports;
// one that is set but malformed is an error.
func FromEnv(getenv func(string) string) (Config, error) {
	config := Config{
		WorkerURL:      strings.TrimRight(strings.TrimSpace(getenv("REALTIME_WORKER_URL")), "/"),
		PurgeSecret:    strings.TrimSpace(getenv("REALTIME_PURGE_SECRET")),
		EventsQueueURL: strings.TrimSpace(getenv("REALTIME_EVENTS_QUEUE_URL")),
	}
	if config.WorkerURL != "" && !origin(config.WorkerURL) {
		return Config{}, errors.New("REALTIME_WORKER_URL must be the web app's origin, an absolute http or https URL with no path or query, such as https://app.oryxa.in")
	}
	if config.PurgeSecret != "" && len(config.PurgeSecret) < MinSecretLength {
		return Config{}, fmt.Errorf("REALTIME_PURGE_SECRET must be at least %d characters; openssl rand -hex 32 makes one", MinSecretLength)
	}
	return config, nil
}

// Missing names the settings realtime cannot run without that are unset.
func (config Config) Missing() []string {
	var missing []string
	for _, setting := range []struct{ name, value string }{
		{"REALTIME_WORKER_URL", config.WorkerURL},
		{"REALTIME_PURGE_SECRET", config.PurgeSecret},
		{"REALTIME_EVENTS_QUEUE_URL", config.EventsQueueURL},
	} {
		if setting.value == "" {
			missing = append(missing, setting.name)
		}
	}
	return missing
}

// origin reports whether value is an origin, with nothing after its host for
// the route to be appended to but the trailing slash FromEnv trims.
func origin(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" &&
		parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" && !parsed.ForceQuery
}
