package provision

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Region              string
	EventBusName        string
	QueueName           string
	DeadLetterQueueName string
	DeliveriesTable     string
	SuppressionsTable   string
	SESIdentity         string
	ConfigurationSet    string
	// SESEventPublishing sets up the SES half of feedback: the configuration
	// set's suppression and its EventBridge destination. It is off against an
	// emulator that can't do either, where SES events are simulated instead.
	SESEventPublishing bool
}

func FromEnv() (Config, error) {
	identity, err := sendingDomain(strings.TrimSpace(os.Getenv("EMAIL_FROM_ADDRESS")))
	if err != nil {
		return Config{}, err
	}
	publishing, err := strconv.ParseBool(envOrDefault("SES_EVENT_PUBLISHING", "true"))
	if err != nil {
		return Config{}, errors.New("SES_EVENT_PUBLISHING must be true or false")
	}
	queueName := envOrDefault("EMAIL_QUEUE_NAME", "email-events")

	return Config{
		Region:              envOrDefault("AWS_REGION", "ap-south-1"),
		EventBusName:        envOrDefault("EVENT_BUS_NAME", defaultBus),
		QueueName:           queueName,
		DeadLetterQueueName: queueName + "-dlq",
		DeliveriesTable:     envOrDefault("EMAIL_DELIVERIES_TABLE", "email-deliveries"),
		SuppressionsTable:   envOrDefault("EMAIL_SUPPRESSIONS_TABLE", "email-suppressions"),
		SESIdentity:         identity,
		ConfigurationSet:    envOrDefault("SES_CONFIGURATION_SET", "oryxa-email"),
		SESEventPublishing:  publishing,
	}, nil
}

// sendingDomain is the SES identity to verify for an address: the whole domain,
// signed with DKIM, rather than the single mailbox mail is sent from.
func sendingDomain(address string) (string, error) {
	if address == "" {
		return "", errors.New("EMAIL_FROM_ADDRESS is required")
	}
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Name != "" {
		return "", fmt.Errorf("EMAIL_FROM_ADDRESS %q must be a bare email address", address)
	}
	return strings.ToLower(parsed.Address[strings.LastIndex(parsed.Address, "@")+1:]), nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
