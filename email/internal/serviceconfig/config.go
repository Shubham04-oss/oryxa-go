package serviceconfig

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

// Config is what email reads for itself. The AWS region is the host's.
type Config struct {
	// From is the complete From header, display name included.
	From              string
	ConfigurationSet  string
	DeliveriesTable   string
	SuppressionsTable string
	// QueueURL is the only queue email ever reads: polling it is all this
	// service does, so it is required.
	QueueURL string
	// SMTPAddr, when set, sends through SMTP instead of SES. Local runs
	// point this at Mailpit.
	SMTPAddr string
}

// FromEnv reads email's configuration through getenv, which a host layers its
// own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	address := strings.TrimSpace(getenv("EMAIL_FROM_ADDRESS"))
	if address == "" {
		return Config{}, errors.New("EMAIL_FROM_ADDRESS is required")
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Name != "" {
		return Config{}, fmt.Errorf("EMAIL_FROM_ADDRESS %q must be a bare email address; set the display name with EMAIL_FROM_NAME", address)
	}
	from := mail.Address{Name: valueOrDefault(getenv, "EMAIL_FROM_NAME", "Oryxa"), Address: parsed.Address}

	queueURL := strings.TrimSpace(getenv("EMAIL_QUEUE_URL"))
	if queueURL == "" {
		return Config{}, errors.New("EMAIL_QUEUE_URL is required; leave email out of ORYXA_SERVICES to run without it")
	}

	return Config{
		From:              from.String(),
		ConfigurationSet:  valueOrDefault(getenv, "SES_CONFIGURATION_SET", "oryxa-email"),
		DeliveriesTable:   valueOrDefault(getenv, "EMAIL_DELIVERIES_TABLE", "email-deliveries"),
		SuppressionsTable: valueOrDefault(getenv, "EMAIL_SUPPRESSIONS_TABLE", "email-suppressions"),
		QueueURL:          queueURL,
		SMTPAddr:          strings.TrimSpace(getenv("EMAIL_SMTP_ADDR")),
	}, nil
}

func valueOrDefault(getenv func(string) string, name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
		return value
	}
	return fallback
}
