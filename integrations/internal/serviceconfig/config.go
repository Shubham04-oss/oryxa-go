package serviceconfig

import (
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Address                 string
	AWSRegion               string
	DatabaseURL             string
	PostgresExtensions      []string
	CognitoIssuer           string
	CognitoClientID         string
	PublicBaseURL           string
	AllowedReturnURLs       []string
	DynamoTable             string
	WebhookQueueURL         string
	EventsQueueURL          string
	PieceRunnerFunction     string
	KMSKeyID                string
	EventBusName            string
	DynamoEndpoint          string
	SQSEndpoint             string
	LambdaEndpoint          string
	KMSEndpoint             string
	EventBridgeEndpoint     string
	AutoMigrate             bool
	GmailPubSubTopic        string
	GmailPushServiceAccount string
	FilesURL                string
}

func FromEnv() (Config, error) {
	autoMigrate, err := boolValue("INTEGRATIONS_AUTO_MIGRATE", true)
	if err != nil {
		return Config{}, err
	}
	genericEndpoint := endpoint("AWS_ENDPOINT_URL")
	config := Config{
		Address: value("INTEGRATIONS_ADDRESS", ":8082"), AWSRegion: value("AWS_REGION", "ap-south-1"),
		DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")), CognitoIssuer: endpoint("COGNITO_ISSUER"),
		PostgresExtensions: split(os.Getenv("POSTGRES_REQUIRED_EXTENSIONS")),
		CognitoClientID:    strings.TrimSpace(os.Getenv("COGNITO_CLIENT_ID")), PublicBaseURL: endpoint("INTEGRATIONS_PUBLIC_BASE_URL"),
		AllowedReturnURLs: split(os.Getenv("INTEGRATIONS_RETURN_URLS")), DynamoTable: value("INTEGRATIONS_DYNAMO_TABLE", "oryxa-integrations"),
		WebhookQueueURL: strings.TrimSpace(os.Getenv("INTEGRATIONS_WEBHOOK_QUEUE_URL")), EventsQueueURL: strings.TrimSpace(os.Getenv("INTEGRATIONS_EVENTS_QUEUE_URL")),
		PieceRunnerFunction: strings.TrimSpace(os.Getenv("INTEGRATIONS_PIECE_RUNNER_FUNCTION")),
		KMSKeyID:            strings.TrimSpace(os.Getenv("INTEGRATIONS_KMS_KEY_ID")), EventBusName: value("EVENT_BUS_NAME", "default"),
		DynamoEndpoint: endpointOr("AWS_ENDPOINT_URL_DYNAMODB", genericEndpoint), SQSEndpoint: endpointOr("AWS_ENDPOINT_URL_SQS", genericEndpoint),
		LambdaEndpoint: endpoint("AWS_ENDPOINT_URL_LAMBDA"), KMSEndpoint: endpoint("AWS_ENDPOINT_URL_KMS"),
		EventBridgeEndpoint: endpointOr("EVENTBRIDGE_ENDPOINT", genericEndpoint), AutoMigrate: autoMigrate,
		GmailPubSubTopic:        strings.TrimSpace(os.Getenv("GOOGLE_GMAIL_PUBSUB_TOPIC")),
		GmailPushServiceAccount: strings.TrimSpace(os.Getenv("GOOGLE_GMAIL_PUSH_SERVICE_ACCOUNT")),
		FilesURL:                endpoint("FILES_URL"),
	}
	if config.DatabaseURL == "" || config.CognitoIssuer == "" || config.CognitoClientID == "" || config.PublicBaseURL == "" || len(config.AllowedReturnURLs) == 0 || config.WebhookQueueURL == "" || config.PieceRunnerFunction == "" || config.KMSKeyID == "" {
		return Config{}, errors.New("DATABASE_URL, COGNITO_ISSUER, COGNITO_CLIENT_ID, INTEGRATIONS_PUBLIC_BASE_URL, INTEGRATIONS_RETURN_URLS, INTEGRATIONS_WEBHOOK_QUEUE_URL, INTEGRATIONS_PIECE_RUNNER_FUNCTION, and INTEGRATIONS_KMS_KEY_ID are required")
	}
	for _, raw := range append(append([]string{}, config.AllowedReturnURLs...), config.PublicBaseURL, config.CognitoIssuer) {
		parsed, err := url.Parse(raw)
		if err != nil || !parsed.IsAbs() || parsed.Host == "" {
			return Config{}, errors.New("integration public, issuer, and return URLs must be absolute")
		}
	}
	if config.FilesURL != "" {
		parsed, err := url.Parse(config.FilesURL)
		if err != nil || !parsed.IsAbs() || parsed.Host == "" {
			return Config{}, errors.New("FILES_URL must be an absolute URL")
		}
	}
	return config, nil
}

func value(name, fallback string) string {
	if result := strings.TrimSpace(os.Getenv(name)); result != "" {
		return result
	}
	return fallback
}
func endpoint(name string) string { return strings.TrimRight(strings.TrimSpace(os.Getenv(name)), "/") }
func endpointOr(name, fallback string) string {
	if result := endpoint(name); result != "" {
		return result
	}
	return fallback
}
func split(raw string) []string {
	var result []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
func boolValue(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	result, err := strconv.ParseBool(raw)
	if err != nil {
		return false, errors.New(name + " must be true or false")
	}
	return result, nil
}
