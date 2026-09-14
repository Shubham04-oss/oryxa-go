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
	S3Endpoint         string
	BucketName         string
	DatabaseURL        string
	PostgresExtensions []string
	AutoMigrate        bool
	AuthURL            string
	TeamsURL           string
	AllowedOrigins     []string
	EventBusName       string
	EventsQueueURL     string
}

func FromEnv() (Config, error) {
	autoMigrate, err := boolValue("FILES_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	genericEndpoint := endpoint("AWS_ENDPOINT_URL")
	config := Config{
		Address:            valueOrDefault("FILES_ADDRESS", ":8080"),
		AWSRegion:          valueOrDefault("AWS_REGION", defaultRegion),
		S3Endpoint:         endpointOr("AWS_ENDPOINT_URL_S3", genericEndpoint),
		BucketName:         strings.TrimSpace(os.Getenv("FILES_BUCKET_NAME")),
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		PostgresExtensions: splitList(os.Getenv("POSTGRES_REQUIRED_EXTENSIONS")),
		AutoMigrate:        autoMigrate,
		AuthURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_URL")), "/"),
		TeamsURL:           strings.TrimRight(strings.TrimSpace(os.Getenv("TEAMS_URL")), "/"),
		AllowedOrigins:     splitList(os.Getenv("FILES_ALLOWED_ORIGINS")),
		EventBusName:       valueOrDefault("EVENT_BUS_NAME", "default"),
		EventsQueueURL:     strings.TrimSpace(os.Getenv("FILES_EVENTS_QUEUE_URL")),
	}
	if config.DatabaseURL == "" || config.AuthURL == "" || config.BucketName == "" {
		return Config{}, errors.New("DATABASE_URL, AUTH_URL and FILES_BUCKET_NAME are required")
	}
	if parsed, err := url.Parse(config.AuthURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return Config{}, fmt.Errorf("AUTH_URL must be an absolute URL")
	}
	if config.TeamsURL != "" {
		if parsed, err := url.Parse(config.TeamsURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return Config{}, fmt.Errorf("TEAMS_URL must be an absolute URL")
		}
	}
	return config, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func endpoint(name string) string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(name)), "/")
}

func endpointOr(name, fallback string) string {
	if value := endpoint(name); value != "" {
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
