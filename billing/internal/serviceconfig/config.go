package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/dodo"
)

// Config is what billing reads for itself. The listener, the database, the
// AWS region and the event bus are the host's.
type Config struct {
	// AuthURL is where billing asks auth who a caller is.
	AuthURL string
	// BillingURL is the app's billing page.
	BillingURL     string
	AllowedOrigins []string
	PlansFile      string
	// EventsQueueURL is the queue auth's organization events arrive on.
	// Without it billing doesn't learn about new, renamed or deleted
	// organizations.
	EventsQueueURL      string
	RenewalReminderDays int
	Dodo                dodo.Config
}

// FromEnv reads billing's configuration through getenv, which a host layers
// its own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	reminderDays, err := intValue(getenv, "BILLING_RENEWAL_REMINDER_DAYS", 7)
	if err != nil {
		return Config{}, err
	}
	if reminderDays < 0 || reminderDays > 60 {
		return Config{}, errors.New("BILLING_RENEWAL_REMINDER_DAYS must be between 0 and 60")
	}

	config := Config{
		AuthURL:             strings.TrimRight(strings.TrimSpace(getenv("AUTH_URL")), "/"),
		BillingURL:          strings.TrimSpace(getenv("BILLING_URL")),
		AllowedOrigins:      splitList(getenv("BILLING_ALLOWED_ORIGINS")),
		PlansFile:           strings.TrimSpace(getenv("BILLING_PLANS_FILE")),
		EventsQueueURL:      strings.TrimSpace(getenv("BILLING_EVENTS_QUEUE_URL")),
		RenewalReminderDays: reminderDays,
		Dodo: dodo.Config{
			APIKey:     strings.TrimSpace(getenv("DODO_PAYMENTS_API_KEY")),
			WebhookKey: strings.TrimSpace(getenv("DODO_PAYMENTS_WEBHOOK_KEY")),
			BaseURL:    strings.TrimSpace(getenv("DODO_PAYMENTS_BASE_URL")),
		},
	}
	if config.AuthURL == "" || config.BillingURL == "" {
		return Config{}, errors.New("AUTH_URL and BILLING_URL are required")
	}
	for name, value := range map[string]string{"AUTH_URL": config.AuthURL, "BILLING_URL": config.BillingURL} {
		if parsed, err := url.Parse(value); err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Config{}, fmt.Errorf("%s must be an absolute URL", name)
		}
	}
	if len(config.AllowedOrigins) == 0 {
		parsed, _ := url.Parse(config.BillingURL)
		config.AllowedOrigins = []string{parsed.Scheme + "://" + parsed.Host}
	}

	switch environment := valueOrDefault(getenv, "DODO_PAYMENTS_ENVIRONMENT", "test_mode"); environment {
	case "test_mode":
	case "live_mode":
		config.Dodo.LiveMode = true
	default:
		return Config{}, fmt.Errorf("DODO_PAYMENTS_ENVIRONMENT must be test_mode or live_mode, not %q", environment)
	}
	if config.Dodo.WebhookKey != "" && config.Dodo.APIKey == "" {
		return Config{}, errors.New("DODO_PAYMENTS_WEBHOOK_KEY is set without DODO_PAYMENTS_API_KEY")
	}
	return config, nil
}

func valueOrDefault(getenv func(string) string, name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
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

func intValue(getenv func(string) string, name string, fallback int) (int, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, errors.New(name + " must be a whole number")
	}
	return parsed, nil
}
