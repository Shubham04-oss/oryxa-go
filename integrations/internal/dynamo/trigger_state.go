package dynamo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type TriggerStateAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
}

type TriggerState struct {
	Client    TriggerStateAPI
	TableName string
}

func (s TriggerState) LoadTriggerState(ctx context.Context, organizationID, triggerID string) (map[string]any, int64, error) {
	if s.Client == nil || s.TableName == "" || organizationID == "" || triggerID == "" {
		return nil, 0, errors.New("trigger state store is not configured")
	}
	output, err := s.Client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.TableName), Key: triggerStateKey(organizationID, triggerID), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, 0, fmt.Errorf("get trigger state: %w", err)
	}
	if output == nil || len(output.Item) == 0 {
		return map[string]any{}, 0, nil
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(stringAttribute(output.Item, "state_json")), &values); err != nil {
		return nil, 0, fmt.Errorf("decode trigger state: %w", err)
	}
	return values, numberAttribute(output.Item, "version"), nil
}

func (s TriggerState) ApplyTriggerState(ctx context.Context, organizationID, triggerID string, expectedVersion int64, mutations []piece.StoreMutation) error {
	if len(mutations) == 0 {
		return nil
	}
	values, actualVersion, err := s.LoadTriggerState(ctx, organizationID, triggerID)
	if err != nil {
		return err
	}
	if actualVersion != expectedVersion {
		return domain.ErrConflict
	}
	for _, mutation := range mutations {
		if mutation.Key == "" || (mutation.Operation != "put" && mutation.Operation != "delete") {
			return errors.New("invalid trigger state mutation")
		}
		key := mutation.Scope + "\x00" + mutation.Key
		if mutation.Operation == "delete" {
			delete(values, key)
		} else {
			values[key] = mutation.Value
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("encode trigger state: %w", err)
	}
	if len(encoded) > 128*1024 {
		return errors.New("trigger state exceeds 128 KiB")
	}
	condition := "attribute_not_exists(PK)"
	var expressionNames map[string]string
	var expressionValues map[string]types.AttributeValue
	if expectedVersion > 0 {
		condition = "#version = :expected"
		expressionNames = map[string]string{"#version": "version"}
		expressionValues = map[string]types.AttributeValue{}
		expressionValues[":expected"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(expectedVersion, 10)}
	}
	_, err = s.Client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.TableName),
		Item: map[string]types.AttributeValue{
			"PK":         &types.AttributeValueMemberS{Value: "TRIGGER_STATE#" + organizationID + "#" + triggerID},
			"SK":         &types.AttributeValueMemberS{Value: "STATE"},
			"state_json": &types.AttributeValueMemberS{Value: string(encoded)},
			"version":    &types.AttributeValueMemberN{Value: strconv.FormatInt(expectedVersion+1, 10)},
		},
		ConditionExpression:       aws.String(condition),
		ExpressionAttributeNames:  expressionNames,
		ExpressionAttributeValues: expressionValues,
	})
	if err != nil {
		var conditional *types.ConditionalCheckFailedException
		if errors.As(err, &conditional) {
			return domain.ErrConflict
		}
		return fmt.Errorf("put trigger state: %w", err)
	}
	return nil
}

func triggerStateKey(org, trigger string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "TRIGGER_STATE#" + org + "#" + trigger},
		"SK": &types.AttributeValueMemberS{Value: "STATE"},
	}
}
