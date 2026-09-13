package serviceconfig

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const defaultRegion = "ap-south-1"

type Config struct {
	Address            string
	AWSRegion          string
	DatabaseURL        string
	PostgresExtensions []string
	AutoMigrate        bool
	AuthURL            string
	AllowedOrigins     []string
	EventBusName       string
	// EventsQueueURL is the queue auth's organization and membership events
	// arrive on. Without it teams doesn't learn about new, renamed or
	// deleted organizations, or members leaving them.
	EventsQueueURL string
}

func FromEnv() (Config, error) {
	autoMigrate, err := boolValue("TEAMS_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		Address:            valueOrDefault("TEAMS_ADDRESS", ":8080"),
		AWSRegion:          valueOrDefault("AWS_REGION", defaultRegion),
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		PostgresExtensions: splitList(os.Getenv("POSTGRES_REQUIRED_EXTENSIONS")),
		AutoMigrate:        autoMigrate,
		AuthURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_URL")), "/"),
		AllowedOrigins:     splitList(os.Getenv("TEAMS_ALLOWED_ORIGINS")),
		EventBusName:       valueOrDefault("EVENT_BUS_NAME", "default"),
		EventsQueueURL:     strings.TrimSpace(os.Getenv("TEAMS_EVENTS_QUEUE_URL")),
	}
	if config.DatabaseURL == "" || config.AuthURL == "" {
		return Config{}, errors.New("DATABASE_URL and AUTH_URL are required")
	}
	if parsed, err := url.Parse(config.AuthURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Config{}, fmt.Errorf("AUTH_URL must be an absolute URL")
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
