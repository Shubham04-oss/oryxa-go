package serviceconfig

import (
	"errors"
	"os"
	"strings"
)

const defaultRegion = "ap-south-1"

type Config struct {
	Address         string
	AWSRegion       string
	CognitoIssuer   string
	CognitoClientID string
	CognitoDomain   string
	EventBusName    string
}

func FromEnv() (Config, error) {
	config := Config{
		Address:         valueOrDefault("AUTH_ADDRESS", ":8080"),
		AWSRegion:       valueOrDefault("AWS_REGION", defaultRegion),
		CognitoIssuer:   strings.TrimSpace(os.Getenv("COGNITO_ISSUER")),
		CognitoClientID: strings.TrimSpace(os.Getenv("COGNITO_CLIENT_ID")),
		CognitoDomain:   strings.TrimRight(strings.TrimSpace(os.Getenv("COGNITO_DOMAIN")), "/"),
		EventBusName:    valueOrDefault("EVENT_BUS_NAME", "default"),
	}
	if config.CognitoIssuer == "" || config.CognitoClientID == "" || config.CognitoDomain == "" {
		return Config{}, errors.New("COGNITO_ISSUER, COGNITO_CLIENT_ID, and COGNITO_DOMAIN are required")
	}
	return config, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
