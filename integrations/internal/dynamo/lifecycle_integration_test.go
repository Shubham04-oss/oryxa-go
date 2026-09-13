package dynamo

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestOAuthRoutesAndTriggerStateWithDynamoDB(t *testing.T) {
	endpoint := os.Getenv("INTEGRATIONS_TEST_AWS_ENDPOINT")
	if endpoint == "" {
		t.Skip("INTEGRATIONS_TEST_AWS_ENDPOINT is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("ap-south-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	client := dynamodb.NewFromConfig(config, func(options *dynamodb.Options) { options.BaseEndpoint = aws.String(endpoint) })
	tableName := "oryxa-integrations-lifecycle-" + time.Now().UTC().Format("20060102150405000000000")
	_, err = client.CreateTable(ctx, &dynamodb.CreateTableInput{TableName: aws.String(tableName), AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("PK"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("SK"), AttributeType: types.ScalarAttributeTypeS}}, KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: types.KeyTypeHash}, {AttributeName: aws.String("SK"), KeyType: types.KeyTypeRange}}, BillingMode: types.BillingModePayPerRequest})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})
	}()

	hash := sha256.Sum256([]byte("one-use-state"))
	stateStore := OAuthStates{Client: client, TableName: tableName}
	wantState := domain.OAuthAuthorizationState{StateHash: hash, OrganizationID: "org-1", MemberID: "member-1", ProfileID: "app-1", Provider: "slack", RedirectURI: "https://connect.example/callback", ReturnURI: "https://app.example/integrations", Scopes: []string{"channels:history"}, EncryptedVerifier: domain.SealedData{Version: 1, Nonce: []byte{1}, Ciphertext: []byte{2}, EncryptedDataKey: []byte{3}}, CreatedAt: time.Now().UTC().Truncate(time.Nanosecond), ExpiresAt: time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)}
	if err := stateStore.CreateOAuthState(ctx, wantState); err != nil {
		t.Fatal(err)
	}
	gotState, err := stateStore.ConsumeOAuthState(ctx, hash, time.Now())
	if err != nil || gotState.OrganizationID != "org-1" || gotState.EncryptedVerifier.Version != 1 {
		t.Fatalf("state/error = %+v/%v", gotState, err)
	}
	if _, err := stateStore.ConsumeOAuthState(ctx, hash, time.Now()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second state consume = %v", err)
	}

	routes := Routes{Client: client, TableName: tableName}
	route := domain.WebhookRoute{OpaqueRoute: "opaque", OrganizationID: "org-1", ProfileID: "app-1", Provider: "slack", PieceName: "piece", PieceVersion: "1", TriggerName: "hook", Mode: domain.WebhookRouteAppLevel, SigningSecret: domain.SealedData{Version: 1, Nonce: []byte{1}, Ciphertext: []byte{2}, EncryptedDataKey: []byte{3}}, TimestampHeader: "x-timestamp", TimestampWindow: 5 * time.Minute, MaxBodyBytes: 1234, Active: true, CredentialRevision: 1}
	if err := routes.CreateWebhookRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	byOpaque, err := routes.WebhookRoute(ctx, "opaque")
	if err != nil || byOpaque.OrganizationID != "org-1" || byOpaque.MaxBodyBytes != 1234 {
		t.Fatalf("route/error = %+v/%v", byOpaque, err)
	}
	byProfile, err := routes.ActiveWebhookRoute(ctx, "org-1", "app-1", "hook")
	if err != nil || byProfile.OpaqueRoute != "opaque" {
		t.Fatalf("route index/error = %+v/%v", byProfile, err)
	}
	if _, err := routes.ActiveWebhookRoute(ctx, "another-org", "app-1", "hook"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-org route = %v", err)
	}
	if err := routes.CreateWebhookRoute(ctx, route); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate route = %v", err)
	}

	runtime := TriggerState{Client: client, TableName: tableName}
	if err := runtime.ApplyTriggerState(ctx, "org-1", "trg-1", 0, []piece.StoreMutation{{Operation: "put", Key: "cursor", Value: "1"}}); err != nil {
		t.Fatal(err)
	}
	values, version, err := runtime.LoadTriggerState(ctx, "org-1", "trg-1")
	if err != nil || version != 1 || values["\x00cursor"] != "1" {
		t.Fatalf("runtime state = %+v/%d/%v", values, version, err)
	}
	if err := runtime.ApplyTriggerState(ctx, "org-1", "trg-1", 0, []piece.StoreMutation{{Operation: "put", Key: "cursor", Value: "stale"}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("stale state write = %v", err)
	}
}
