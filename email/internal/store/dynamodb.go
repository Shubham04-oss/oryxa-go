package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
)

// timeLayout is fixed-width so timestamps sort as strings, which the
// deliveries table's recipient index relies on.
const timeLayout = "2006-01-02T15:04:05.000Z"

// lastErrorLimit keeps a runaway error message from pushing an item toward
// DynamoDB's 400 KB limit.
const lastErrorLimit = 1000

type dynamoAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

type DynamoDB struct {
	client            dynamoAPI
	deliveriesTable   string
	suppressionsTable string
}

func NewDynamoDB(client dynamoAPI, deliveriesTable, suppressionsTable string) *DynamoDB {
	return &DynamoDB{client: client, deliveriesTable: deliveriesTable, suppressionsTable: suppressionsTable}
}

type deliveryItem struct {
	DeliveryID      string `dynamodbav:"delivery_id"`
	EventSource     string `dynamodbav:"event_source"`
	EventDetailType string `dynamodbav:"event_detail_type"`
	EventID         string `dynamodbav:"event_id"`
	Template        string `dynamodbav:"template"`
	Recipient       string `dynamodbav:"recipient"`
	RecipientKey    string `dynamodbav:"recipient_key"`
	Status          string `dynamodbav:"status"`
	StatusRank      int    `dynamodbav:"status_rank"`
	LeaseExpiresAt  int64  `dynamodbav:"lease_expires_at,omitempty"`
	Attempts        int    `dynamodbav:"attempts"`
	SESMessageID    string `dynamodbav:"ses_message_id,omitempty"`
	LastError       string `dynamodbav:"last_error,omitempty"`
	FeedbackDetail  string `dynamodbav:"feedback_detail,omitempty"`
	FeedbackAt      string `dynamodbav:"feedback_at,omitempty"`
	CreatedAt       string `dynamodbav:"created_at"`
	UpdatedAt       string `dynamodbav:"updated_at"`
	SentAt          string `dynamodbav:"sent_at,omitempty"`
	ExpiresAt       int64  `dynamodbav:"expires_at"`
}

type suppressionItem struct {
	Address      string `dynamodbav:"address"`
	Reason       string `dynamodbav:"reason"`
	Detail       string `dynamodbav:"detail,omitempty"`
	DeliveryID   string `dynamodbav:"delivery_id,omitempty"`
	SESMessageID string `dynamodbav:"ses_message_id,omitempty"`
	CreatedAt    string `dynamodbav:"created_at"`
}

func (s *DynamoDB) Suppression(ctx context.Context, address string) (delivery.Suppression, bool, error) {
	output, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.suppressionsTable),
		Key:            map[string]types.AttributeValue{"address": &types.AttributeValueMemberS{Value: address}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return delivery.Suppression{}, false, fmt.Errorf("look up suppression: %w", err)
	}
	if output.Item == nil {
		return delivery.Suppression{}, false, nil
	}

	var item suppressionItem
	if err := attributevalue.UnmarshalMap(output.Item, &item); err != nil {
		return delivery.Suppression{}, false, fmt.Errorf("decode suppression: %w", err)
	}
	return delivery.Suppression{
		Address:      item.Address,
		Reason:       item.Reason,
		Detail:       item.Detail,
		DeliveryID:   item.DeliveryID,
		SESMessageID: item.SESMessageID,
		CreatedAt:    parseTime(item.CreatedAt),
	}, true, nil
}

// Suppress keeps the first reason an address was suppressed for.
func (s *DynamoDB) Suppress(ctx context.Context, suppression delivery.Suppression) error {
	item, err := attributevalue.MarshalMap(suppressionItem{
		Address:      suppression.Address,
		Reason:       suppression.Reason,
		Detail:       suppression.Detail,
		DeliveryID:   suppression.DeliveryID,
		SESMessageID: suppression.SESMessageID,
		CreatedAt:    formatTime(suppression.CreatedAt),
	})
	if err != nil {
		return fmt.Errorf("encode suppression: %w", err)
	}
	condition, err := expression.NewBuilder().
		WithCondition(expression.AttributeNotExists(expression.Name("address"))).
		Build()
	if err != nil {
		return fmt.Errorf("build suppression condition: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                aws.String(s.suppressionsTable),
		Item:                     item,
		ConditionExpression:      condition.Condition(),
		ExpressionAttributeNames: condition.Names(),
	})
	if isConditionFailure(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("suppress address: %w", err)
	}
	return nil
}

func (s *DynamoDB) Claim(ctx context.Context, claim delivery.Delivery, now time.Time) (bool, error) {
	update := expression.
		Set(expression.Name("status"), expression.Value(string(claim.Status))).
		Set(expression.Name("status_rank"), expression.Value(claim.Status.Rank())).
		Set(expression.Name("event_source"), expression.Value(claim.Event.Source)).
		Set(expression.Name("event_detail_type"), expression.Value(claim.Event.DetailType)).
		Set(expression.Name("event_id"), expression.Value(claim.Event.ID)).
		Set(expression.Name("template"), expression.Value(claim.Template)).
		Set(expression.Name("recipient"), expression.Value(claim.Recipient)).
		Set(expression.Name("recipient_key"), expression.Value(delivery.AddressKey(claim.Recipient))).
		Set(expression.Name("attempts"), expression.Plus(expression.IfNotExists(expression.Name("attempts"), expression.Value(0)), expression.Value(1))).
		Set(expression.Name("created_at"), expression.IfNotExists(expression.Name("created_at"), expression.Value(formatTime(claim.CreatedAt)))).
		Set(expression.Name("expires_at"), expression.IfNotExists(expression.Name("expires_at"), expression.Value(claim.ExpiresAt.Unix()))).
		Set(expression.Name("updated_at"), expression.Value(formatTime(now)))
	if claim.Status == delivery.StatusSending {
		update = update.Set(expression.Name("lease_expires_at"), expression.Value(claim.LeaseExpiresAt.UnixMilli()))
	} else {
		update = update.Remove(expression.Name("lease_expires_at"))
	}

	// Claimable when new, when the last attempt failed, or when the attempt
	// holding it has let its lease run out.
	condition := expression.Or(
		expression.AttributeNotExists(expression.Name("delivery_id")),
		expression.Name("status").Equal(expression.Value(string(delivery.StatusFailed))),
		expression.And(
			expression.Name("status").Equal(expression.Value(string(delivery.StatusSending))),
			expression.Name("lease_expires_at").LessThan(expression.Value(now.UnixMilli())),
		),
	)

	updated, err := s.updateDelivery(ctx, claim.ID, update, condition)
	if err != nil {
		return false, fmt.Errorf("claim delivery %s: %w", claim.ID, err)
	}
	return updated, nil
}

func (s *DynamoDB) Get(ctx context.Context, id string) (delivery.Delivery, error) {
	output, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.deliveriesTable),
		Key:            deliveryKey(id),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return delivery.Delivery{}, fmt.Errorf("get delivery %s: %w", id, err)
	}
	if output.Item == nil {
		return delivery.Delivery{}, fmt.Errorf("delivery %s not found", id)
	}

	var item deliveryItem
	if err := attributevalue.UnmarshalMap(output.Item, &item); err != nil {
		return delivery.Delivery{}, fmt.Errorf("decode delivery %s: %w", id, err)
	}
	result := delivery.Delivery{
		ID:           item.DeliveryID,
		Event:        delivery.EventRef{Source: item.EventSource, DetailType: item.EventDetailType, ID: item.EventID},
		Template:     item.Template,
		Recipient:    item.Recipient,
		Status:       delivery.Status(item.Status),
		SESMessageID: item.SESMessageID,
		CreatedAt:    parseTime(item.CreatedAt),
		ExpiresAt:    time.Unix(item.ExpiresAt, 0).UTC(),
	}
	if item.LeaseExpiresAt != 0 {
		result.LeaseExpiresAt = time.UnixMilli(item.LeaseExpiresAt).UTC()
	}
	return result, nil
}

func (s *DynamoDB) MarkSent(ctx context.Context, id, sesMessageID string, at time.Time) (bool, error) {
	update := expression.
		Set(expression.Name("status"), expression.Value(string(delivery.StatusSent))).
		Set(expression.Name("status_rank"), expression.Value(delivery.StatusSent.Rank())).
		Set(expression.Name("ses_message_id"), expression.Value(sesMessageID)).
		Set(expression.Name("sent_at"), expression.Value(formatTime(at))).
		Set(expression.Name("updated_at"), expression.Value(formatTime(at))).
		Remove(expression.Name("lease_expires_at"))
	condition := expression.Name("status").Equal(expression.Value(string(delivery.StatusSending)))

	updated, err := s.updateDelivery(ctx, id, update, condition)
	if err != nil {
		return false, fmt.Errorf("mark delivery %s sent: %w", id, err)
	}
	return updated, nil
}

func (s *DynamoDB) MarkFailed(ctx context.Context, id, reason string, at time.Time) error {
	if len(reason) > lastErrorLimit {
		reason = reason[:lastErrorLimit]
	}
	update := expression.
		Set(expression.Name("status"), expression.Value(string(delivery.StatusFailed))).
		Set(expression.Name("status_rank"), expression.Value(delivery.StatusFailed.Rank())).
		Set(expression.Name("last_error"), expression.Value(reason)).
		Set(expression.Name("updated_at"), expression.Value(formatTime(at))).
		Remove(expression.Name("lease_expires_at"))
	condition := expression.Name("status").Equal(expression.Value(string(delivery.StatusSending)))

	if _, err := s.updateDelivery(ctx, id, update, condition); err != nil {
		return fmt.Errorf("mark delivery %s failed: %w", id, err)
	}
	return nil
}

func (s *DynamoDB) RecordFeedback(ctx context.Context, feedback delivery.Feedback, at time.Time) (bool, error) {
	rank := feedback.Status.Rank()
	update := expression.
		Set(expression.Name("status"), expression.Value(string(feedback.Status))).
		Set(expression.Name("status_rank"), expression.Value(rank)).
		Set(expression.Name("feedback_detail"), expression.Value(feedback.Detail)).
		Set(expression.Name("feedback_at"), expression.Value(formatTime(feedback.OccurredAt))).
		Set(expression.Name("updated_at"), expression.Value(formatTime(at))).
		Remove(expression.Name("lease_expires_at"))
	if feedback.SESMessageID != "" {
		update = update.Set(expression.Name("ses_message_id"),
			expression.IfNotExists(expression.Name("ses_message_id"), expression.Value(feedback.SESMessageID)))
	}
	condition := expression.And(
		expression.AttributeExists(expression.Name("delivery_id")),
		expression.Name("status_rank").LessThanEqual(expression.Value(rank)),
	)

	updated, err := s.updateDelivery(ctx, feedback.DeliveryID, update, condition)
	if err != nil {
		return false, fmt.Errorf("record feedback for delivery %s: %w", feedback.DeliveryID, err)
	}
	return updated, nil
}

// updateDelivery applies update when condition holds, and reports whether it did.
func (s *DynamoDB) updateDelivery(ctx context.Context, id string, update expression.UpdateBuilder, condition expression.ConditionBuilder) (bool, error) {
	built, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return false, fmt.Errorf("build expression: %w", err)
	}

	_, err = s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(s.deliveriesTable),
		Key:                       deliveryKey(id),
		UpdateExpression:          built.Update(),
		ConditionExpression:       built.Condition(),
		ExpressionAttributeNames:  built.Names(),
		ExpressionAttributeValues: built.Values(),
	})
	if isConditionFailure(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func deliveryKey(id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"delivery_id": &types.AttributeValueMemberS{Value: id}}
}

func isConditionFailure(err error) bool {
	var conditional *types.ConditionalCheckFailedException
	return errors.As(err, &conditional)
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(timeLayout)
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(timeLayout, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
