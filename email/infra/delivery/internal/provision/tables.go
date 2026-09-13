package provision

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	// deliveryExpiryAttribute holds when DynamoDB's TTL may delete a delivery.
	deliveryExpiryAttribute = "expires_at"
	// recipientIndex answers "what have we sent this address?", oldest first.
	recipientIndex = "recipient"
)

func deliveriesTable(name string) *dynamodb.CreateTableInput {
	return &dynamodb.CreateTableInput{
		TableName:   aws.String(name),
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("delivery_id"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("recipient_key"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("created_at"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("delivery_id"), KeyType: types.KeyTypeHash},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{
			IndexName: aws.String(recipientIndex),
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String("recipient_key"), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String("created_at"), KeyType: types.KeyTypeRange},
			},
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}},
		DeletionProtectionEnabled: aws.Bool(true),
		Tags:                      tableTags(),
	}
}

func suppressionsTable(name string) *dynamodb.CreateTableInput {
	return &dynamodb.CreateTableInput{
		TableName:   aws.String(name),
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("address"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("address"), KeyType: types.KeyTypeHash},
		},
		DeletionProtectionEnabled: aws.Bool(true),
		Tags:                      tableTags(),
	}
}

// ensureTable creates the table when it is missing and turns on TTL for
// ttlAttribute. An existing table's keys and indexes are left as they are:
// DynamoDB can't change a key, so a changed schema needs a new table.
func ensureTable(ctx context.Context, client *dynamodb.Client, input *dynamodb.CreateTableInput, ttlAttribute string) error {
	name := aws.ToString(input.TableName)
	_, err := client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: input.TableName})
	if err != nil {
		var notFound *types.ResourceNotFoundException
		if !errors.As(err, &notFound) {
			return fmt.Errorf("describe DynamoDB table %s: %w", name, err)
		}
		if _, err := client.CreateTable(ctx, input); err != nil {
			return fmt.Errorf("create DynamoDB table %s: %w", name, err)
		}
		waiter := dynamodb.NewTableExistsWaiter(client)
		if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: input.TableName}, 5*time.Minute); err != nil {
			return fmt.Errorf("wait for DynamoDB table %s: %w", name, err)
		}
	}
	if ttlAttribute == "" {
		return nil
	}

	ttl, err := client.DescribeTimeToLive(ctx, &dynamodb.DescribeTimeToLiveInput{TableName: input.TableName})
	if err != nil {
		return fmt.Errorf("describe TTL of DynamoDB table %s: %w", name, err)
	}
	if description := ttl.TimeToLiveDescription; description != nil &&
		(description.TimeToLiveStatus == types.TimeToLiveStatusEnabled || description.TimeToLiveStatus == types.TimeToLiveStatusEnabling) {
		return nil
	}
	_, err = client.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: input.TableName,
		TimeToLiveSpecification: &types.TimeToLiveSpecification{
			AttributeName: aws.String(ttlAttribute),
			Enabled:       aws.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("enable TTL on DynamoDB table %s: %w", name, err)
	}
	return nil
}

func tableTags() []types.Tag {
	var result []types.Tag
	for _, key := range sortedTagKeys() {
		result = append(result, types.Tag{Key: aws.String(key), Value: aws.String(tags[key])})
	}
	return result
}
