package dynamo

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestClaimsWithDynamoDB(t *testing.T) {
	endpoint := os.Getenv("INTEGRATIONS_TEST_AWS_ENDPOINT")
	if endpoint == "" {
		t.Skip("INTEGRATIONS_TEST_AWS_ENDPOINT is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("ap-south-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := dynamodb.NewFromConfig(config, func(options *dynamodb.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})
	tableName := "oryxa-integrations-test-" + time.Now().UTC().Format("20060102150405000000000")
	_, err = client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: types.KeyTypeRange},
		},
		BillingMode: types.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer func() {
		_, _ = client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(tableName)})
	}()

	counter := 0
	claims := Claims{Client: client, TableName: tableName, IDs: func(string) (string, error) {
		counter++
		return fmt.Sprintf("token-%d", counter), nil
	}}
	now := time.Now().UTC()
	token, acquired, err := claims.Acquire(ctx, "event-one", now, now.Add(time.Minute), now.Add(time.Hour))
	if err != nil || !acquired {
		t.Fatalf("first acquire: token=%q acquired=%v error=%v", token, acquired, err)
	}
	if _, acquired, err := claims.Acquire(ctx, "event-one", now.Add(time.Second), now.Add(time.Minute), now.Add(time.Hour)); err != nil || acquired {
		t.Fatalf("concurrent acquire: acquired=%v error=%v", acquired, err)
	}
	if err := claims.Commit(ctx, "event-one", token); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, acquired, err := claims.Acquire(ctx, "event-one", now.Add(2*time.Hour), now.Add(2*time.Hour+time.Minute), now.Add(3*time.Hour)); err != nil || acquired {
		t.Fatalf("completed claim was reacquired: acquired=%v error=%v", acquired, err)
	}

	token, acquired, err = claims.Acquire(ctx, "event-two", now, now.Add(time.Minute), now.Add(time.Hour))
	if err != nil || !acquired {
		t.Fatalf("releasable acquire: acquired=%v error=%v", acquired, err)
	}
	if err := claims.Release(ctx, "event-two", token); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, acquired, err := claims.Acquire(ctx, "event-two", now, now.Add(time.Minute), now.Add(time.Hour)); err != nil || !acquired {
		t.Fatalf("acquire after release: acquired=%v error=%v", acquired, err)
	}

	_, acquired, err = claims.Acquire(ctx, "event-three", now, now.Add(time.Second), now.Add(time.Hour))
	if err != nil || !acquired {
		t.Fatalf("lease acquire: acquired=%v error=%v", acquired, err)
	}
	newToken, acquired, err := claims.Acquire(ctx, "event-three", now.Add(2*time.Second), now.Add(time.Minute), now.Add(time.Hour))
	if err != nil || !acquired || newToken == "" {
		t.Fatalf("expired lease acquire: token=%q acquired=%v error=%v", newToken, acquired, err)
	}
}
