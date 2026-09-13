package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/dodo"
)

const defaultRegion = "ap-south-1"

type Config struct {
	Address            string
	AWSRegion          string
	DatabaseURL        string
	PostgresExtensions []string
	AutoMigrate        bool
	// AuthURL is where billing asks auth who a caller is.
	AuthURL string
	// BillingURL is the app's billing page.
	BillingURL     string
	AllowedOrigins []string
	PlansFile      string
	EventBusName   string
	// EventsQueueURL is the queue auth's organization events arrive on.
	// Without it billing doesn't learn about new, renamed or deleted
	// organizations.
	EventsQueueURL      string
	RenewalReminderDays int
	Dodo                dodo.Config
}

func FromEnv() (Config, error) {
	autoMigrate, err := boolValue("BILLING_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	reminderDays, err := intValue("BILLING_RENEWAL_REMINDER_DAYS", 7)
	if err != nil {
		return Config{}, err
	}
	if reminderDays < 0 || reminderDays > 60 {
		return Config{}, errors.New("BILLING_RENEWAL_REMINDER_DAYS must be between 0 and 60")
	}

	config := Config{
		Address:             valueOrDefault("BILLING_ADDRESS", ":8080"),
		AWSRegion:           valueOrDefault("AWS_REGION", defaultRegion),
		DatabaseURL:         strings.TrimSpace(os.Getenv("DATABASE_URL")),
		PostgresExtensions:  splitList(os.Getenv("POSTGRES_REQUIRED_EXTENSIONS")),
		AutoMigrate:         autoMigrate,
		AuthURL:             strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_URL")), "/"),
		BillingURL:          strings.TrimSpace(os.Getenv("BILLING_URL")),
		AllowedOrigins:      splitList(os.Getenv("BILLING_ALLOWED_ORIGINS")),
		PlansFile:           strings.TrimSpace(os.Getenv("BILLING_PLANS_FILE")),
		EventBusName:        valueOrDefault("EVENT_BUS_NAME", "default"),
		EventsQueueURL:      strings.TrimSpace(os.Getenv("BILLING_EVENTS_QUEUE_URL")),
		RenewalReminderDays: reminderDays,
		Dodo: dodo.Config{
			APIKey:     strings.TrimSpace(os.Getenv("DODO_PAYMENTS_API_KEY")),
			WebhookKey: strings.TrimSpace(os.Getenv("DODO_PAYMENTS_WEBHOOK_KEY")),
			BaseURL:    strings.TrimSpace(os.Getenv("DODO_PAYMENTS_BASE_URL")),
		},
	}
	if config.DatabaseURL == "" || config.AuthURL == "" || config.BillingURL == "" {
		return Config{}, errors.New("DATABASE_URL, AUTH_URL, and BILLING_URL are required")
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

	switch environment := valueOrDefault("DODO_PAYMENTS_ENVIRONMENT", "test_mode"); environment {
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

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
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

func intValue(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, errors.New(name + " must be a whole number")
	}
	return parsed, nil
}
