// Package serviceconfig reads batching's runtime configuration.
package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
)

// MaxJevTimeout is the longest BATCHING_JEV_TIMEOUT may be. A message is
// handled within one classification and a few statements, and the queue must
// hide it from other replicas for longer than that or it is handled twice;
// the provisioner's visibility timeout is checked against this.
const MaxJevTimeout = 10 * time.Second

type Config struct {
	EventsQueueURL      string
	EventBridgeEndpoint string
	SQSEndpoint         string
	TypeSafeAPIKey      string
	TypeSafeBaseURL     string
	JevModel            string
	JevTimeout          time.Duration
	JevImmediate        float64
	JevMinConfidence    float64
	BatchSize           int
	MaxWait             time.Duration
	LargestBatch        int
	SweepInterval       time.Duration
	ClaimLease          time.Duration
	GroupsPerSweep      int
}

func FromEnv(getenv func(string) string) (Config, error) {
	genericEndpoint := endpoint(getenv, "AWS_ENDPOINT_URL")
	batchSize, err := integer(getenv, "BATCHING_BATCH_SIZE", 5, 1, 100)
	if err != nil {
		return Config{}, err
	}
	largest, err := integer(getenv, "BATCHING_LARGEST_BATCH", 20, batchSize, 100)
	if err != nil {
		return Config{}, err
	}
	groups, err := integer(getenv, "BATCHING_GROUPS_PER_SWEEP", 8, 2, 100)
	if err != nil {
		return Config{}, err
	}
	maxWait, err := duration(getenv, "BATCHING_MAX_WAIT", time.Hour)
	if err != nil {
		return Config{}, err
	}
	if maxWait > domain.MaximumDeliveryDelay {
		return Config{}, fmt.Errorf("BATCHING_MAX_WAIT must not exceed %s", domain.MaximumDeliveryDelay)
	}
	interval, err := duration(getenv, "BATCHING_SWEEP_INTERVAL", time.Second)
	if err != nil {
		return Config{}, err
	}
	lease, err := duration(getenv, "BATCHING_CLAIM_LEASE", time.Minute)
	if err != nil {
		return Config{}, err
	}
	if lease <= interval {
		return Config{}, fmt.Errorf("BATCHING_CLAIM_LEASE must be longer than BATCHING_SWEEP_INTERVAL")
	}
	jevTimeout, err := duration(getenv, "BATCHING_JEV_TIMEOUT", 3*time.Second)
	if err != nil {
		return Config{}, err
	}
	if jevTimeout > MaxJevTimeout {
		return Config{}, fmt.Errorf("BATCHING_JEV_TIMEOUT must not exceed %s", MaxJevTimeout)
	}
	immediate, err := decimal(getenv, "BATCHING_JEV_IMMEDIATE_PROBABILITY", .85)
	if err != nil {
		return Config{}, err
	}
	confidence, err := decimal(getenv, "BATCHING_JEV_MIN_CONFIDENCE", .60)
	if err != nil {
		return Config{}, err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(getenv("TYPESAFE_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = "https://api.typesafe.ai"
	}
	model := strings.TrimSpace(getenv("BATCHING_JEV_MODEL"))
	if model == "" {
		model = "jev-latest"
	}
	config := Config{
		EventsQueueURL:      strings.TrimSpace(getenv("BATCHING_EVENTS_QUEUE_URL")),
		EventBridgeEndpoint: endpointOr(getenv, "EVENTBRIDGE_ENDPOINT", genericEndpoint),
		SQSEndpoint:         endpointOr(getenv, "AWS_ENDPOINT_URL_SQS", genericEndpoint),
		TypeSafeAPIKey:      strings.TrimSpace(getenv("TYPESAFE_API_KEY")),
		TypeSafeBaseURL:     baseURL,
		JevModel:            model,
		JevTimeout:          jevTimeout,
		JevImmediate:        immediate,
		JevMinConfidence:    confidence,
		BatchSize:           batchSize,
		MaxWait:             maxWait,
		LargestBatch:        largest,
		SweepInterval:       interval,
		ClaimLease:          lease,
		GroupsPerSweep:      groups,
	}
	for _, check := range []struct{ name, value string }{
		{"EVENTBRIDGE_ENDPOINT", config.EventBridgeEndpoint},
		{"AWS_ENDPOINT_URL_SQS", config.SQSEndpoint},
		{"TYPESAFE_BASE_URL", config.TypeSafeBaseURL},
	} {
		if check.value != "" && !absoluteURL(check.value) {
			return Config{}, fmt.Errorf("%s must be an absolute URL", check.name)
		}
	}
	if !httpsOrLocal(config.TypeSafeBaseURL) {
		return Config{}, errors.New("TYPESAFE_BASE_URL must use HTTPS, except on localhost, since every request carries the API key")
	}
	return config, nil
}

func decimal(getenv func(string) string, name string, fallback float64) (float64, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 || parsed > 1 {
		return 0, fmt.Errorf("%s must be a number between zero and one", name)
	}
	return parsed, nil
}

// integer reads a whole number, and checks its bounds after defaulting too:
// a bound can follow another setting, as the largest batch follows the
// cold-start target, and a default does not.
func integer(getenv func(string) string, name string, fallback, minimum, maximum int) (int, error) {
	value := strings.TrimSpace(getenv(name))
	parsed := fallback
	if value != "" {
		var err error
		if parsed, err = strconv.Atoi(value); err != nil {
			return 0, fmt.Errorf("%s must be a whole number between %d and %d", name, minimum, maximum)
		}
	}
	if parsed < minimum || parsed > maximum {
		if value == "" {
			return 0, fmt.Errorf("%s must be set to a whole number between %d and %d; its default, %d, is not", name, minimum, maximum, fallback)
		}
		return 0, fmt.Errorf("%s must be a whole number between %d and %d", name, minimum, maximum)
	}
	return parsed, nil
}

func duration(getenv func(string) string, name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
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

func absoluteURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

// httpsOrLocal says a URL uses HTTPS, or HTTP on this machine as tests do.
// Anywhere else plain HTTP would send the TypeSafe API key in cleartext.
func httpsOrLocal(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	hostname := parsed.Hostname()
	return parsed.Scheme == "https" ||
		parsed.Scheme == "http" && (hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1")
}
