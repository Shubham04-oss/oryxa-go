package dynamo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/filebroker"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/filesclient"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type FileSessionsAPI interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

type FileSessions struct {
	Client    FileSessionsAPI
	TableName string
}

func (s FileSessions) Put(ctx context.Context, session filebroker.Session) error {
	if s.Client == nil || s.TableName == "" || session.Token == "" || session.OrganizationID == "" ||
		session.MemberID == "" || session.Authorization == "" || session.ExpiresAt.IsZero() {
		return errors.New("invalid file broker session")
	}
	_, err := s.Client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.TableName),
		Item: map[string]types.AttributeValue{
			"PK":              &types.AttributeValueMemberS{Value: fileBrokerPK(session.Token)},
			"SK":              &types.AttributeValueMemberS{Value: "SESSION"},
			"organization_id": &types.AttributeValueMemberS{Value: session.OrganizationID},
			"member_id":       &types.AttributeValueMemberS{Value: session.MemberID},
			"authorization":   &types.AttributeValueMemberS{Value: session.Authorization},
			"expires_at":      &types.AttributeValueMemberN{Value: strconv.FormatInt(session.ExpiresAt.Unix(), 10)},
		},
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		return fmt.Errorf("put file broker session: %w", err)
	}
	return nil
}

func (s FileSessions) Get(ctx context.Context, token string) (filebroker.Session, error) {
	if s.Client == nil || s.TableName == "" || token == "" {
		return filebroker.Session{}, filesclient.ErrNotFound
	}
	output, err := s.Client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.TableName),
		Key:            fileBrokerKey(token),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return filebroker.Session{}, fmt.Errorf("get file broker session: %w", err)
	}
	if output == nil || len(output.Item) == 0 {
		return filebroker.Session{}, filesclient.ErrNotFound
	}
	session := filebroker.Session{
		Token:          token,
		OrganizationID: stringAttribute(output.Item, "organization_id"),
		MemberID:       stringAttribute(output.Item, "member_id"),
		Authorization:  stringAttribute(output.Item, "authorization"),
	}
	if raw, ok := output.Item["expires_at"].(*types.AttributeValueMemberN); ok {
		unix, _ := strconv.ParseInt(raw.Value, 10, 64)
		session.ExpiresAt = time.Unix(unix, 0).UTC()
	}
	if session.OrganizationID == "" || session.Authorization == "" || session.ExpiresAt.Before(time.Now().UTC()) {
		return filebroker.Session{}, filesclient.ErrNotFound
	}
	return session, nil
}

func (s FileSessions) Delete(ctx context.Context, token string) error {
	if s.Client == nil || s.TableName == "" || token == "" {
		return nil
	}
	_, err := s.Client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.TableName),
		Key:       fileBrokerKey(token),
	})
	if err != nil {
		return fmt.Errorf("delete file broker session: %w", err)
	}
	return nil
}

func fileBrokerPK(token string) string { return "FILEBROKER#" + token }

func fileBrokerKey(token string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: fileBrokerPK(token)},
		"SK": &types.AttributeValueMemberS{Value: "SESSION"},
	}
}
