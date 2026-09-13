package serviceconfig

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"
)

const defaultRegion = "ap-south-1"

type Config struct {
	AWSRegion string
	// From is the complete From header, display name included.
	From              string
	ConfigurationSet  string
	DeliveriesTable   string
	SuppressionsTable string
	QueueURL          string
}

func FromEnv() (Config, error) {
	address := strings.TrimSpace(os.Getenv("EMAIL_FROM_ADDRESS"))
	if address == "" {
		return Config{}, errors.New("EMAIL_FROM_ADDRESS is required")
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Name != "" {
		return Config{}, fmt.Errorf("EMAIL_FROM_ADDRESS %q must be a bare email address; set the display name with EMAIL_FROM_NAME", address)
	}
	from := mail.Address{Name: valueOrDefault("EMAIL_FROM_NAME", "Oryxa"), Address: parsed.Address}

	return Config{
		AWSRegion:         valueOrDefault("AWS_REGION", defaultRegion),
		From:              from.String(),
		ConfigurationSet:  valueOrDefault("SES_CONFIGURATION_SET", "oryxa-email"),
		DeliveriesTable:   valueOrDefault("EMAIL_DELIVERIES_TABLE", "email-deliveries"),
		SuppressionsTable: valueOrDefault("EMAIL_SUPPRESSIONS_TABLE", "email-suppressions"),
		QueueURL:          strings.TrimSpace(os.Getenv("EMAIL_QUEUE_URL")),
	}, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
