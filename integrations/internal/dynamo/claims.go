package dynamo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type ClaimsAPI interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

type Claims struct {
	Client    ClaimsAPI
	TableName string
	IDs       func(string) (string, error)
}

func (c Claims) Acquire(ctx context.Context, key string, now, leaseUntil, expiresAt time.Time) (string, bool, error) {
	if c.Client == nil || c.TableName == "" || c.IDs == nil || key == "" || !leaseUntil.After(now) || !expiresAt.After(leaseUntil) {
		return "", false, errors.New("invalid webhook claim configuration")
	}
	token, err := c.IDs("clm")
	if err != nil {
		return "", false, fmt.Errorf("generate claim token: %w", err)
	}
	_, err = c.Client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(c.TableName),
		Item: map[string]types.AttributeValue{
			"PK":          &types.AttributeValueMemberS{Value: "DEDUPE#" + key},
			"SK":          &types.AttributeValueMemberS{Value: "CLAIM"},
			"status":      &types.AttributeValueMemberS{Value: "pending"},
			"claim_token": &types.AttributeValueMemberS{Value: token},
			"lease_until": &types.AttributeValueMemberN{Value: strconv.FormatInt(leaseUntil.UnixMilli(), 10)},
			"expires_at":  &types.AttributeValueMemberN{Value: strconv.FormatInt(expiresAt.Unix(), 10)},
		},
		ConditionExpression:      aws.String("attribute_not_exists(PK) OR (#status = :pending AND lease_until < :now)"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pending": &types.AttributeValueMemberS{Value: "pending"},
			":now":     &types.AttributeValueMemberN{Value: strconv.FormatInt(now.UnixMilli(), 10)},
		},
	})
	if err != nil {
		var conditional *types.ConditionalCheckFailedException
		if errors.As(err, &conditional) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("put webhook claim: %w", err)
	}
	return token, true, nil
}

func (c Claims) Commit(ctx context.Context, key, token string) error {
	_, err := c.Client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                aws.String(c.TableName),
		Key:                      claimKey(key),
		UpdateExpression:         aws.String("SET #status = :complete REMOVE lease_until"),
		ConditionExpression:      aws.String("claim_token = :token AND #status = :pending"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":complete": &types.AttributeValueMemberS{Value: "complete"},
			":pending":  &types.AttributeValueMemberS{Value: "pending"},
			":token":    &types.AttributeValueMemberS{Value: token},
		},
	})
	if err != nil {
		return fmt.Errorf("commit webhook claim: %w", err)
	}
	return nil
}

func (c Claims) Release(ctx context.Context, key, token string) error {
	_, err := c.Client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                aws.String(c.TableName),
		Key:                      claimKey(key),
		ConditionExpression:      aws.String("claim_token = :token AND #status = :pending"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pending": &types.AttributeValueMemberS{Value: "pending"},
			":token":   &types.AttributeValueMemberS{Value: token},
		},
	})
	if err != nil {
		return fmt.Errorf("release webhook claim: %w", err)
	}
	return nil
}

func claimKey(key string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "DEDUPE#" + key},
		"SK": &types.AttributeValueMemberS{Value: "CLAIM"},
	}
}
