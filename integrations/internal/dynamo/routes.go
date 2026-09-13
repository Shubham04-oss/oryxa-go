package dynamo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type RoutesAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

type Routes struct {
	Client    RoutesAPI
	TableName string
}

func (s Routes) CreateWebhookRoute(ctx context.Context, route domain.WebhookRoute) error {
	if s.Client == nil || s.TableName == "" || route.OpaqueRoute == "" || route.OrganizationID == "" || route.ProfileID == "" || route.TriggerName == "" {
		return errors.New("invalid webhook route")
	}
	sealed, err := json.Marshal(route.SigningSecret)
	if err != nil {
		return fmt.Errorf("encode webhook secret envelope: %w", err)
	}
	routeItem := map[string]types.AttributeValue{
		"PK":                  &types.AttributeValueMemberS{Value: routePK(route.OpaqueRoute)},
		"SK":                  &types.AttributeValueMemberS{Value: "ROUTE"},
		"opaque_route":        &types.AttributeValueMemberS{Value: route.OpaqueRoute},
		"organization_id":     &types.AttributeValueMemberS{Value: route.OrganizationID},
		"profile_id":          &types.AttributeValueMemberS{Value: route.ProfileID},
		"connection_id":       &types.AttributeValueMemberS{Value: route.ConnectionID},
		"provider":            &types.AttributeValueMemberS{Value: route.Provider},
		"piece_name":          &types.AttributeValueMemberS{Value: route.PieceName},
		"piece_version":       &types.AttributeValueMemberS{Value: route.PieceVersion},
		"trigger_name":        &types.AttributeValueMemberS{Value: route.TriggerName},
		"mode":                &types.AttributeValueMemberS{Value: string(route.Mode)},
		"signing_secret":      &types.AttributeValueMemberS{Value: string(sealed)},
		"timestamp_header":    &types.AttributeValueMemberS{Value: route.TimestampHeader},
		"timestamp_window_ms": &types.AttributeValueMemberN{Value: strconv.FormatInt(route.TimestampWindow.Milliseconds(), 10)},
		"max_body_bytes":      &types.AttributeValueMemberN{Value: strconv.FormatInt(route.MaxBodyBytes, 10)},
		"active":              &types.AttributeValueMemberBOOL{Value: route.Active},
		"credential_revision": &types.AttributeValueMemberN{Value: strconv.FormatInt(route.CredentialRevision, 10)},
	}
	indexItem := map[string]types.AttributeValue{
		"PK":           &types.AttributeValueMemberS{Value: routeIndexFor(route)},
		"SK":           &types.AttributeValueMemberS{Value: "ROUTE"},
		"opaque_route": &types.AttributeValueMemberS{Value: route.OpaqueRoute},
	}
	_, err = s.Client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Put: &types.Put{TableName: aws.String(s.TableName), Item: routeItem, ConditionExpression: aws.String("attribute_not_exists(PK)")}},
		{Put: &types.Put{TableName: aws.String(s.TableName), Item: indexItem, ConditionExpression: aws.String("attribute_not_exists(PK)")}},
	}})
	if err != nil {
		var canceled *types.TransactionCanceledException
		if errors.As(err, &canceled) {
			return domain.ErrConflict
		}
		return fmt.Errorf("create webhook route: %w", err)
	}
	return nil
}

func (s Routes) WebhookRoute(ctx context.Context, opaque string) (domain.WebhookRoute, error) {
	return s.webhookRoute(ctx, routePK(opaque))
}

func (s Routes) ActiveWebhookRoute(ctx context.Context, organizationID, profileID, triggerName string) (domain.WebhookRoute, error) {
	item, err := s.get(ctx, routeIndexPK(organizationID, profileID, triggerName))
	if err != nil {
		return domain.WebhookRoute{}, err
	}
	route, err := s.WebhookRoute(ctx, stringAttribute(item, "opaque_route"))
	if err != nil {
		return domain.WebhookRoute{}, err
	}
	if route.OrganizationID != organizationID || route.ProfileID != profileID || route.TriggerName != triggerName || !route.Active {
		return domain.WebhookRoute{}, domain.ErrNotFound
	}
	return route, nil
}

// ActiveWebhookRouteForConnection resolves a route that is dedicated to one
// connection. App-level Slack routes retain their existing profile-wide index.
func (s Routes) ActiveWebhookRouteForConnection(ctx context.Context, organizationID, profileID, triggerName, connectionID string) (domain.WebhookRoute, error) {
	if connectionID == "" {
		return domain.WebhookRoute{}, domain.ErrNotFound
	}
	item, err := s.get(ctx, routeConnectionIndexPK(organizationID, profileID, triggerName, connectionID))
	if err != nil {
		return domain.WebhookRoute{}, err
	}
	route, err := s.WebhookRoute(ctx, stringAttribute(item, "opaque_route"))
	if err != nil {
		return domain.WebhookRoute{}, err
	}
	if route.OrganizationID != organizationID || route.ProfileID != profileID || route.TriggerName != triggerName ||
		route.ConnectionID != connectionID || route.Mode != domain.WebhookRoutePerConnection || !route.Active {
		return domain.WebhookRoute{}, domain.ErrNotFound
	}
	return route, nil
}

// DeactivateWebhookRoute removes the lookup index and closes the opaque ingress
// route. Keeping the immutable route record supports safe in-flight reads while
// making a later subscription attempt allocate a fresh opaque path.
func (s Routes) DeactivateWebhookRoute(ctx context.Context, opaque string) error {
	route, err := s.WebhookRoute(ctx, opaque)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	_, err = s.Client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{
		{Update: &types.Update{TableName: aws.String(s.TableName), Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: routePK(opaque)}, "SK": &types.AttributeValueMemberS{Value: "ROUTE"},
		}, UpdateExpression: aws.String("SET active = :inactive"), ExpressionAttributeValues: map[string]types.AttributeValue{
			":inactive": &types.AttributeValueMemberBOOL{Value: false},
		}, ConditionExpression: aws.String("attribute_exists(PK)")}},
		{Delete: &types.Delete{TableName: aws.String(s.TableName), Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: routeIndexFor(route)}, "SK": &types.AttributeValueMemberS{Value: "ROUTE"},
		}}},
	}})
	if err != nil {
		var canceled *types.TransactionCanceledException
		if errors.As(err, &canceled) {
			return domain.ErrConflict
		}
		return fmt.Errorf("deactivate webhook route: %w", err)
	}
	return nil
}

func (s Routes) webhookRoute(ctx context.Context, pk string) (domain.WebhookRoute, error) {
	item, err := s.get(ctx, pk)
	if err != nil {
		return domain.WebhookRoute{}, err
	}
	var route domain.WebhookRoute
	route.OpaqueRoute = stringAttribute(item, "opaque_route")
	route.OrganizationID = stringAttribute(item, "organization_id")
	route.ProfileID = stringAttribute(item, "profile_id")
	route.ConnectionID = stringAttribute(item, "connection_id")
	route.Provider = stringAttribute(item, "provider")
	route.PieceName = stringAttribute(item, "piece_name")
	route.PieceVersion = stringAttribute(item, "piece_version")
	route.TriggerName = stringAttribute(item, "trigger_name")
	route.Mode = domain.WebhookRouteMode(stringAttribute(item, "mode"))
	route.TimestampHeader = stringAttribute(item, "timestamp_header")
	route.Active = boolAttribute(item, "active")
	route.TimestampWindow = time.Duration(numberAttribute(item, "timestamp_window_ms")) * time.Millisecond
	route.MaxBodyBytes = numberAttribute(item, "max_body_bytes")
	route.CredentialRevision = numberAttribute(item, "credential_revision")
	if err := json.Unmarshal([]byte(stringAttribute(item, "signing_secret")), &route.SigningSecret); err != nil {
		return domain.WebhookRoute{}, fmt.Errorf("decode webhook signing secret: %w", err)
	}
	return route, nil
}

func (s Routes) get(ctx context.Context, pk string) (map[string]types.AttributeValue, error) {
	if s.Client == nil || s.TableName == "" || pk == "" {
		return nil, errors.New("webhook route store is not configured")
	}
	output, err := s.Client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.TableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: pk},
			"SK": &types.AttributeValueMemberS{Value: "ROUTE"},
		},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("get webhook route: %w", err)
	}
	if output == nil || len(output.Item) == 0 {
		return nil, domain.ErrNotFound
	}
	return output.Item, nil
}

func routePK(opaque string) string { return "WEBHOOK#" + opaque }
func routeIndexPK(org, profile, trigger string) string {
	return "WEBHOOK_PROFILE#" + org + "#" + profile + "#" + trigger
}
func routeConnectionIndexPK(org, profile, trigger, connection string) string {
	return "WEBHOOK_CONNECTION#" + org + "#" + profile + "#" + trigger + "#" + connection
}
func routeIndexFor(route domain.WebhookRoute) string {
	if route.Mode == domain.WebhookRoutePerConnection && route.ConnectionID != "" {
		return routeConnectionIndexPK(route.OrganizationID, route.ProfileID, route.TriggerName, route.ConnectionID)
	}
	return routeIndexPK(route.OrganizationID, route.ProfileID, route.TriggerName)
}
func boolAttribute(item map[string]types.AttributeValue, key string) bool {
	value, _ := item[key].(*types.AttributeValueMemberBOOL)
	return value != nil && value.Value
}
func numberAttribute(item map[string]types.AttributeValue, key string) int64 {
	value, _ := item[key].(*types.AttributeValueMemberN)
	if value == nil {
		return 0
	}
	result, _ := strconv.ParseInt(value.Value, 10, 64)
	return result
}
