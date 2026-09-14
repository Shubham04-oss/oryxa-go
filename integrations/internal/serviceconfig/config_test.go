package serviceconfig

import "testing"

func TestFromEnvDefaultsToMumbaiAndSeparatesLocalEndpoints(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("COGNITO_ISSUER", "https://issuer.example")
	t.Setenv("COGNITO_CLIENT_ID", "client")
	t.Setenv("INTEGRATIONS_PUBLIC_BASE_URL", "https://connect.example")
	t.Setenv("INTEGRATIONS_RETURN_URLS", "https://app.example")
	t.Setenv("INTEGRATIONS_WEBHOOK_QUEUE_URL", "https://sqs.example/queue")
	t.Setenv("INTEGRATIONS_PIECE_RUNNER_FUNCTION", "runner")
	t.Setenv("INTEGRATIONS_KMS_KEY_ID", "alias/key")
	t.Setenv("POSTGRES_REQUIRED_EXTENSIONS", "ltree,pg_trgm,pgvector,age")
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_ENDPOINT_URL_DYNAMODB", "http://192.168.1.4:4566")
	config, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.AWSRegion != "ap-south-1" || config.DynamoEndpoint != "http://192.168.1.4:4566" || config.KMSEndpoint != "" ||
		len(config.PostgresExtensions) != 4 || config.PostgresExtensions[3] != "age" {
		t.Fatalf("config = %+v", config)
	}
	if config.GmailPubSubTopic != "" || config.GmailPushServiceAccount != "" || config.FilesURL != "" || config.EventsQueueURL != "" {
		t.Fatalf("optional Gmail push, files, and events settings should default empty: %+v", config)
	}
}

func TestFromEnvReadsOptionalGmailPushSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("COGNITO_ISSUER", "https://issuer.example")
	t.Setenv("COGNITO_CLIENT_ID", "client")
	t.Setenv("INTEGRATIONS_PUBLIC_BASE_URL", "https://connect.example")
	t.Setenv("INTEGRATIONS_RETURN_URLS", "https://app.example")
	t.Setenv("INTEGRATIONS_WEBHOOK_QUEUE_URL", "https://sqs.example/queue")
	t.Setenv("INTEGRATIONS_PIECE_RUNNER_FUNCTION", "runner")
	t.Setenv("INTEGRATIONS_KMS_KEY_ID", "alias/key")
	t.Setenv("GOOGLE_GMAIL_PUBSUB_TOPIC", "projects/oryxa/topics/gmail-watch")
	t.Setenv("GOOGLE_GMAIL_PUSH_SERVICE_ACCOUNT", "gmail-push@oryxa.iam.gserviceaccount.com")
	config, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.GmailPubSubTopic != "projects/oryxa/topics/gmail-watch" ||
		config.GmailPushServiceAccount != "gmail-push@oryxa.iam.gserviceaccount.com" {
		t.Fatalf("gmail push config = %+v", config)
	}
}

func TestFromEnvReadsOptionalFilesURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("COGNITO_ISSUER", "https://issuer.example")
	t.Setenv("COGNITO_CLIENT_ID", "client")
	t.Setenv("INTEGRATIONS_PUBLIC_BASE_URL", "https://connect.example")
	t.Setenv("INTEGRATIONS_RETURN_URLS", "https://app.example")
	t.Setenv("INTEGRATIONS_WEBHOOK_QUEUE_URL", "https://sqs.example/queue")
	t.Setenv("INTEGRATIONS_PIECE_RUNNER_FUNCTION", "runner")
	t.Setenv("INTEGRATIONS_KMS_KEY_ID", "alias/key")
	t.Setenv("FILES_URL", "https://files.example")
	config, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.FilesURL != "https://files.example" {
		t.Fatalf("files URL = %q", config.FilesURL)
	}
}

func TestFromEnvReadsOptionalEventsQueueURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("COGNITO_ISSUER", "https://issuer.example")
	t.Setenv("COGNITO_CLIENT_ID", "client")
	t.Setenv("INTEGRATIONS_PUBLIC_BASE_URL", "https://connect.example")
	t.Setenv("INTEGRATIONS_RETURN_URLS", "https://app.example")
	t.Setenv("INTEGRATIONS_WEBHOOK_QUEUE_URL", "https://sqs.example/queue")
	t.Setenv("INTEGRATIONS_PIECE_RUNNER_FUNCTION", "runner")
	t.Setenv("INTEGRATIONS_KMS_KEY_ID", "alias/key")
	t.Setenv("INTEGRATIONS_EVENTS_QUEUE_URL", "http://localhost:4566/000000000000/integrations-events")
	config, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.EventsQueueURL != "http://localhost:4566/000000000000/integrations-events" {
		t.Fatalf("events queue URL = %q", config.EventsQueueURL)
	}
}
