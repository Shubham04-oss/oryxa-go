// Package serviceconfig reads apps' configuration from the environment.
package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Config is what apps reads for itself. The listener, the database, the AWS
// region and the event bus are the host's.
type Config struct {
	AuthURL string
	// ComposioAPIKey is the project API key. It reaches every user in the
	// project, so it lives only here.
	ComposioAPIKey  string
	ComposioBaseURL string
	// ComposioWebhookSecret verifies Composio's deliveries. Without it every
	// delivery is refused.
	ComposioWebhookSecret string
	// PublicBaseURL is where a member's browser and Composio reach this
	// process; the connect callback and the webhook URL are built on it.
	PublicBaseURL string
	// AllowedOrigins may send cookie-authenticated mutations, and are where a
	// member's browser may be sent back to after connecting an app.
	AllowedOrigins []string
	// EventBridgeEndpoint and SQSEndpoint override where those AWS clients
	// call, for a local emulator.
	EventBridgeEndpoint string
	SQSEndpoint         string
	// EventsQueueURL is the queue auth's events arrive on. Without it apps
	// does not learn that a member left or an organization was deleted.
	EventsQueueURL string
	// NoAutoSubscribe stops a newly connected app from being subscribed to
	// everything it can deliver. Connecting subscribes by default; set
	// APPS_AUTO_SUBSCRIBE=false for a deployment that would rather members
	// chose their own events. Parsed as a boolean (strconv.ParseBool, so
	// true/false/1/0/T/F and case-insensitive variants); unset means on, and
	// anything else refuses to start rather than silently keep the default.
	NoAutoSubscribe bool
}

// FromEnv reads apps' configuration through getenv, which a host layers its
// own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	genericEndpoint := endpoint(getenv, "AWS_ENDPOINT_URL")
	autoSubscribe, err := boolValue(getenv, "APPS_AUTO_SUBSCRIBE", true)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		AuthURL:               endpoint(getenv, "AUTH_URL"),
		ComposioAPIKey:        strings.TrimSpace(getenv("COMPOSIO_API_KEY")),
		ComposioBaseURL:       endpoint(getenv, "COMPOSIO_BASE_URL"),
		ComposioWebhookSecret: strings.TrimSpace(getenv("COMPOSIO_WEBHOOK_SECRET")),
		PublicBaseURL:         endpoint(getenv, "APPS_PUBLIC_BASE_URL"),
		AllowedOrigins:        splitList(getenv("APPS_ALLOWED_ORIGINS")),
		EventBridgeEndpoint:   endpointOr(getenv, "EVENTBRIDGE_ENDPOINT", genericEndpoint),
		SQSEndpoint:           endpointOr(getenv, "AWS_ENDPOINT_URL_SQS", genericEndpoint),
		EventsQueueURL:        strings.TrimSpace(getenv("APPS_EVENTS_QUEUE_URL")),
		NoAutoSubscribe:       !autoSubscribe,
	}
	if config.AuthURL == "" || config.ComposioAPIKey == "" || config.PublicBaseURL == "" {
		return Config{}, errors.New("AUTH_URL, COMPOSIO_API_KEY and APPS_PUBLIC_BASE_URL are required")
	}
	for _, check := range []struct{ name, value string }{
		{"AUTH_URL", config.AuthURL},
		{"COMPOSIO_BASE_URL", config.ComposioBaseURL},
		{"APPS_PUBLIC_BASE_URL", config.PublicBaseURL},
		{"EVENTBRIDGE_ENDPOINT", config.EventBridgeEndpoint},
		{"AWS_ENDPOINT_URL_SQS", config.SQSEndpoint},
	} {
		if check.value != "" && !absoluteURL(check.value) {
			return Config{}, fmt.Errorf("%s must be an absolute URL", check.name)
		}
	}
	for _, origin := range config.AllowedOrigins {
		if !absoluteURL(origin) {
			return Config{}, errors.New("APPS_ALLOWED_ORIGINS must list absolute origins")
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

// boolValue reads an environment variable as a boolean (strconv.ParseBool:
// true/false/1/0/T/F, case-insensitive). Unset answers fallback; anything
// else is a configuration error naming the variable, since silently keeping
// the default on a typo is worse than refusing to start.
func boolValue(getenv func(string) string, name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func absoluteURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}
