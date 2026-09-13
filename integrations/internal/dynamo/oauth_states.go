package dynamo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type OAuthStatesAPI interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

type OAuthStates struct {
	Client    OAuthStatesAPI
	TableName string
}

func (s OAuthStates) CreateOAuthState(ctx context.Context, state domain.OAuthAuthorizationState) error {
	if s.Client == nil || s.TableName == "" || state.OrganizationID == "" || state.MemberID == "" || state.ExpiresAt.IsZero() {
		return errors.New("invalid OAuth state configuration")
	}
	verifier, err := json.Marshal(state.EncryptedVerifier)
	if err != nil {
		return fmt.Errorf("encode OAuth verifier: %w", err)
	}
	scopes, err := attributevalue.Marshal(state.Scopes)
	if err != nil {
		return fmt.Errorf("encode OAuth scopes: %w", err)
	}
	_, err = s.Client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.TableName),
		Item: map[string]types.AttributeValue{
			"PK":                 &types.AttributeValueMemberS{Value: oauthStatePK(state.StateHash)},
			"SK":                 &types.AttributeValueMemberS{Value: "STATE"},
			"organization_id":    &types.AttributeValueMemberS{Value: state.OrganizationID},
			"member_id":          &types.AttributeValueMemberS{Value: state.MemberID},
			"profile_id":         &types.AttributeValueMemberS{Value: state.ProfileID},
			"provider":           &types.AttributeValueMemberS{Value: state.Provider},
			"redirect_uri":       &types.AttributeValueMemberS{Value: state.RedirectURI},
			"return_uri":         &types.AttributeValueMemberS{Value: state.ReturnURI},
			"scopes":             scopes,
			"encrypted_verifier": &types.AttributeValueMemberS{Value: string(verifier)},
			"created_at":         &types.AttributeValueMemberS{Value: state.CreatedAt.UTC().Format(time.RFC3339Nano)},
			"expires_at":         &types.AttributeValueMemberN{Value: fmt.Sprint(state.ExpiresAt.Unix())},
		},
		ConditionExpression: aws.String("attribute_not_exists(PK)"),
	})
	if err != nil {
		return fmt.Errorf("put OAuth state: %w", err)
	}
	return nil
}

func (s OAuthStates) ConsumeOAuthState(ctx context.Context, hash [32]byte, _ time.Time) (domain.OAuthAuthorizationState, error) {
	if s.Client == nil || s.TableName == "" {
		return domain.OAuthAuthorizationState{}, errors.New("OAuth state store is not configured")
	}
	output, err := s.Client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.TableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: oauthStatePK(hash)},
			"SK": &types.AttributeValueMemberS{Value: "STATE"},
		},
		ReturnValues: types.ReturnValueAllOld,
	})
	if err != nil {
		return domain.OAuthAuthorizationState{}, fmt.Errorf("consume OAuth state: %w", err)
	}
	if output == nil || len(output.Attributes) == 0 {
		return domain.OAuthAuthorizationState{}, domain.ErrNotFound
	}
	state := domain.OAuthAuthorizationState{StateHash: hash}
	state.OrganizationID = stringAttribute(output.Attributes, "organization_id")
	state.MemberID = stringAttribute(output.Attributes, "member_id")
	state.ProfileID = stringAttribute(output.Attributes, "profile_id")
	state.Provider = stringAttribute(output.Attributes, "provider")
	state.RedirectURI = stringAttribute(output.Attributes, "redirect_uri")
	state.ReturnURI = stringAttribute(output.Attributes, "return_uri")
	if err := attributevalue.Unmarshal(output.Attributes["scopes"], &state.Scopes); err != nil {
		return domain.OAuthAuthorizationState{}, fmt.Errorf("decode OAuth scopes: %w", err)
	}
	if err := json.Unmarshal([]byte(stringAttribute(output.Attributes, "encrypted_verifier")), &state.EncryptedVerifier); err != nil {
		return domain.OAuthAuthorizationState{}, fmt.Errorf("decode OAuth verifier: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, stringAttribute(output.Attributes, "created_at"))
	if err != nil {
		return domain.OAuthAuthorizationState{}, fmt.Errorf("decode OAuth creation time: %w", err)
	}
	state.CreatedAt = createdAt
	var expiry int64
	if err := attributevalue.Unmarshal(output.Attributes["expires_at"], &expiry); err != nil {
		return domain.OAuthAuthorizationState{}, fmt.Errorf("decode OAuth expiry: %w", err)
	}
	state.ExpiresAt = time.Unix(expiry, 0).UTC()
	return state, nil
}

func oauthStatePK(hash [32]byte) string { return "OAUTH_STATE#" + hex.EncodeToString(hash[:]) }

func stringAttribute(item map[string]types.AttributeValue, key string) string {
	value, _ := item[key].(*types.AttributeValueMemberS)
	if value == nil {
		return ""
	}
	return value.Value
}
