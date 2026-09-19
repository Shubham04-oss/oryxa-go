// Package serviceconfig reads knowledge's configuration from the
// environment.
package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// defaultFullSweepInterval is how often a source already indexed is read
// from the beginning again when KNOWLEDGE_FULL_SWEEP_INTERVAL is unset, so a
// document removed at the provider is eventually noticed even long after a
// source's first pass.
const defaultFullSweepInterval = 7 * 24 * time.Hour

// Config is what knowledge reads for itself. The listener, the database, the
// AWS region and the event bus are the host's.
type Config struct {
	AuthURL string
	// AppsURL is where a provider source's connection is read, required only
	// once COMPOSIO_API_KEY says provider sources are on. It is a host URL
	// variable: when apps runs in this process, the host defaults it there.
	AppsURL string
	// FilesURL is where the organization's own stored files are read, and
	// ServiceKeyID is the KMS key knowledge signs its own identity with when
	// asking auth for a member's token. The files source is mounted only
	// when both are set, since it reads as the member who registered it and
	// has no other way in.
	FilesURL     string
	ServiceKeyID string
	// ServiceCallers are the services allowed to read a document's body
	// through the internal API, which is how a retrieval pipeline reads back
	// what an event was too large to carry. Empty means the internal API
	// refuses every call.
	ServiceCallers []serviceauth.Caller
	// ComposioAPIKey turns on GitHub, Notion, Slack, Google Drive and Gmail:
	// without it knowledge mounts no extractor for any of them, only files.
	// ComposioBaseURL overrides Composio's own API, for a test double.
	ComposioAPIKey  string
	ComposioBaseURL string
	// OriginalsBucket is where knowledge keeps the bytes of a document it does
	// not read itself — a PDF, an Office file from Drive or the
	// organization's own files. Empty means such a document is skipped
	// instead, the same as before this existed.
	OriginalsBucket string
	// JudgeModel is the Gemini model that judges whether a document from a
	// connected app is worth indexing, on Vertex AI in GoogleCloudProject at
	// GoogleCloudLocation. Empty indexes what the rules alone do not exclude,
	// unjudged.
	JudgeModel          string
	GoogleCloudProject  string
	GoogleCloudLocation string
	// AllowedOrigins may send cookie-authenticated mutations.
	AllowedOrigins []string
	// EventBridgeEndpoint, SQSEndpoint and S3Endpoint override where those AWS
	// clients call, for a local emulator.
	EventBridgeEndpoint string
	SQSEndpoint         string
	S3Endpoint          string
	// EventsQueueURL is the queue auth's and apps' events arrive on. Without
	// it knowledge doesn't learn that a connection, member or organization is
	// gone.
	EventsQueueURL string
	// FullSweepInterval is how long a full sweep's word about what a source
	// holds is trusted before a pass starts over from the beginning.
	FullSweepInterval time.Duration
}

// FromEnv reads knowledge's configuration through getenv, which a host layers
// its own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	genericEndpoint := endpoint(getenv, "AWS_ENDPOINT_URL")
	fullSweepInterval, err := duration(getenv, "KNOWLEDGE_FULL_SWEEP_INTERVAL", defaultFullSweepInterval)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		AuthURL:             endpoint(getenv, "AUTH_URL"),
		AppsURL:             endpoint(getenv, "APPS_URL"),
		FilesURL:            endpoint(getenv, "FILES_URL"),
		ServiceKeyID:        strings.TrimSpace(getenv("KNOWLEDGE_SERVICE_KEY_ID")),
		ComposioAPIKey:      strings.TrimSpace(getenv("COMPOSIO_API_KEY")),
		ComposioBaseURL:     endpoint(getenv, "COMPOSIO_BASE_URL"),
		OriginalsBucket:     strings.TrimSpace(getenv("KNOWLEDGE_ORIGINALS_BUCKET")),
		JudgeModel:          strings.TrimSpace(getenv("KNOWLEDGE_JUDGE_MODEL")),
		GoogleCloudProject:  strings.TrimSpace(getenv("GOOGLE_CLOUD_PROJECT")),
		GoogleCloudLocation: strings.TrimSpace(getenv("GOOGLE_CLOUD_LOCATION")),
		AllowedOrigins:      splitList(getenv("KNOWLEDGE_ALLOWED_ORIGINS")),
		EventBridgeEndpoint: endpointOr(getenv, "EVENTBRIDGE_ENDPOINT", genericEndpoint),
		SQSEndpoint:         endpointOr(getenv, "AWS_ENDPOINT_URL_SQS", genericEndpoint),
		S3Endpoint:          endpointOr(getenv, "AWS_ENDPOINT_URL_S3", genericEndpoint),
		EventsQueueURL:      strings.TrimSpace(getenv("KNOWLEDGE_EVENTS_QUEUE_URL")),
		FullSweepInterval:   fullSweepInterval,
	}
	if config.ServiceCallers, err = serviceauth.ParseCallers(getenv("KNOWLEDGE_SERVICE_CALLERS")); err != nil {
		return Config{}, fmt.Errorf("KNOWLEDGE_SERVICE_CALLERS: %w", err)
	}
	if config.AuthURL == "" {
		return Config{}, errors.New("AUTH_URL is required")
	}
	if config.ComposioAPIKey != "" && config.AppsURL == "" {
		return Config{}, errors.New("APPS_URL is required when COMPOSIO_API_KEY is set")
	}
	if config.GoogleCloudLocation == "" {
		// Gemini 3 is served from the global endpoint only.
		config.GoogleCloudLocation = "global"
	}
	if config.JudgeModel != "" && config.GoogleCloudProject == "" {
		return Config{}, errors.New("GOOGLE_CLOUD_PROJECT is required when KNOWLEDGE_JUDGE_MODEL is set: Vertex AI serves the judge")
	}
	for _, check := range []struct{ name, value string }{
		{"AUTH_URL", config.AuthURL},
		{"APPS_URL", config.AppsURL},
		{"FILES_URL", config.FilesURL},
		{"COMPOSIO_BASE_URL", config.ComposioBaseURL},
		{"EVENTBRIDGE_ENDPOINT", config.EventBridgeEndpoint},
		{"AWS_ENDPOINT_URL_SQS", config.SQSEndpoint},
		{"AWS_ENDPOINT_URL_S3", config.S3Endpoint},
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

// duration reads a Go duration, defaulting an unset value. What is set must
// be positive: zero or negative says nothing about how often, and a sweep
// interval that is never due, or always due, is a config mistake worth
// refusing rather than running with.
func duration(getenv func(string) string, name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", name, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return parsed, nil
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
