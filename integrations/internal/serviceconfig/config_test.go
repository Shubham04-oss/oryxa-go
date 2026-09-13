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
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_ENDPOINT_URL_DYNAMODB", "http://192.168.1.4:4566")
	config, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.AWSRegion != "ap-south-1" || config.DynamoEndpoint != "http://192.168.1.4:4566" || config.KMSEndpoint != "" {
		t.Fatalf("config = %+v", config)
	}
}
