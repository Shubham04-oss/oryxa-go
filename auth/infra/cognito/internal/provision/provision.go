package provision

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

const googleProviderName = "Google"

type Output struct {
	Region            string `json:"region"`
	UserPoolID        string `json:"user_pool_id"`
	AppClientID       string `json:"app_client_id"`
	CognitoDomain     string `json:"cognito_domain"`
	Issuer            string `json:"issuer"`
	GoogleRedirectURI string `json:"google_redirect_uri"`
}

func Run(ctx context.Context, client *cognitoidentityprovider.Client, config Config) (Output, error) {
	poolID, err := ensureUserPool(ctx, client, config)
	if err != nil {
		return Output{}, err
	}
	if err := ensureDomain(ctx, client, poolID, config.DomainPrefix); err != nil {
		return Output{}, err
	}
	if err := ensureGoogleProvider(ctx, client, poolID, config); err != nil {
		return Output{}, err
	}
	clientID, err := ensureAppClient(ctx, client, poolID, config)
	if err != nil {
		return Output{}, err
	}
	if err := ensureManagedLoginBranding(ctx, client, poolID, clientID); err != nil {
		return Output{}, err
	}

	domain := fmt.Sprintf("https://%s.auth.%s.amazoncognito.com", config.DomainPrefix, config.Region)
	return Output{
		Region:            config.Region,
		UserPoolID:        poolID,
		AppClientID:       clientID,
		CognitoDomain:     domain,
		Issuer:            fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s", config.Region, poolID),
		GoogleRedirectURI: domain + "/oauth2/idpresponse",
	}, nil
}

func ensureUserPool(ctx context.Context, client *cognitoidentityprovider.Client, config Config) (string, error) {
	var nextToken *string
	for {
		output, err := client.ListUserPools(ctx, &cognitoidentityprovider.ListUserPoolsInput{
			MaxResults: aws.Int32(60),
			NextToken:  nextToken,
		})
		if err != nil {
			return "", fmt.Errorf("list Cognito user pools: %w", err)
		}
		for _, pool := range output.UserPools {
			if aws.ToString(pool.Name) == config.PoolName {
				return aws.ToString(pool.Id), nil
			}
		}
		if output.NextToken == nil {
			break
		}
		nextToken = output.NextToken
	}

	output, err := client.CreateUserPool(ctx, &cognitoidentityprovider.CreateUserPoolInput{
		PoolName:              aws.String(config.PoolName),
		AdminCreateUserConfig: &types.AdminCreateUserConfigType{AllowAdminCreateUserOnly: true},
		AutoVerifiedAttributes: []types.VerifiedAttributeType{
			types.VerifiedAttributeTypeEmail,
		},
		DeletionProtection: types.DeletionProtectionTypeActive,
		Schema: []types.SchemaAttributeType{{
			AttributeDataType: types.AttributeDataTypeString,
			Mutable:           aws.Bool(true),
			Name:              aws.String("email"),
			Required:          aws.Bool(true),
		}},
		UsernameAttributes: []types.UsernameAttributeType{
			types.UsernameAttributeTypeEmail,
		},
		UsernameConfiguration: &types.UsernameConfigurationType{CaseSensitive: aws.Bool(false)},
		UserPoolTags: map[string]string{
			"application": "oryxa",
			"managed-by":  "oryxa-go",
		},
		UserPoolTier: types.UserPoolTierTypeEssentials,
	})
	if err != nil {
		return "", fmt.Errorf("create Cognito user pool: %w", err)
	}
	return aws.ToString(output.UserPool.Id), nil
}

func ensureDomain(ctx context.Context, client *cognitoidentityprovider.Client, poolID, prefix string) error {
	description, err := client.DescribeUserPoolDomain(ctx, &cognitoidentityprovider.DescribeUserPoolDomainInput{
		Domain: aws.String(prefix),
	})
	if err != nil {
		return fmt.Errorf("describe Cognito domain: %w", err)
	}
	if description.DomainDescription != nil && description.DomainDescription.UserPoolId != nil {
		owner := aws.ToString(description.DomainDescription.UserPoolId)
		if owner != poolID {
			return fmt.Errorf("Cognito domain prefix %q belongs to another user pool", prefix)
		}
		return nil
	}

	_, err = client.CreateUserPoolDomain(ctx, &cognitoidentityprovider.CreateUserPoolDomainInput{
		Domain:              aws.String(prefix),
		ManagedLoginVersion: aws.Int32(2),
		UserPoolId:          aws.String(poolID),
	})
	if err != nil {
		return fmt.Errorf("create Cognito domain: %w", err)
	}
	return nil
}

func ensureGoogleProvider(ctx context.Context, client *cognitoidentityprovider.Client, poolID string, config Config) error {
	details := map[string]string{
		"authorize_scopes": "email profile openid",
		"client_id":        config.GoogleClientID,
		"client_secret":    config.GoogleClientSecret,
	}
	attributes := map[string]string{
		"email":       "email",
		"family_name": "family_name",
		"given_name":  "given_name",
		"name":        "name",
		"picture":     "picture",
	}

	_, err := client.DescribeIdentityProvider(ctx, &cognitoidentityprovider.DescribeIdentityProviderInput{
		ProviderName: aws.String(googleProviderName),
		UserPoolId:   aws.String(poolID),
	})
	if err == nil {
		_, err = client.UpdateIdentityProvider(ctx, &cognitoidentityprovider.UpdateIdentityProviderInput{
			AttributeMapping: attributes,
			ProviderDetails:  details,
			ProviderName:     aws.String(googleProviderName),
			UserPoolId:       aws.String(poolID),
		})
		if err != nil {
			return fmt.Errorf("update Google identity provider: %w", err)
		}
		return nil
	}

	var notFound *types.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("describe Google identity provider: %w", err)
	}
	_, err = client.CreateIdentityProvider(ctx, &cognitoidentityprovider.CreateIdentityProviderInput{
		AttributeMapping: attributes,
		ProviderDetails:  details,
		ProviderName:     aws.String(googleProviderName),
		ProviderType:     types.IdentityProviderTypeTypeGoogle,
		UserPoolId:       aws.String(poolID),
	})
	if err != nil {
		return fmt.Errorf("create Google identity provider: %w", err)
	}
	return nil
}

func ensureAppClient(ctx context.Context, client *cognitoidentityprovider.Client, poolID string, config Config) (string, error) {
	var existingID string
	var nextToken *string
	for {
		output, err := client.ListUserPoolClients(ctx, &cognitoidentityprovider.ListUserPoolClientsInput{
			MaxResults: aws.Int32(60),
			NextToken:  nextToken,
			UserPoolId: aws.String(poolID),
		})
		if err != nil {
			return "", fmt.Errorf("list Cognito app clients: %w", err)
		}
		for _, appClient := range output.UserPoolClients {
			if aws.ToString(appClient.ClientName) == config.AppClientName {
				existingID = aws.ToString(appClient.ClientId)
				break
			}
		}
		if existingID != "" || output.NextToken == nil {
			break
		}
		nextToken = output.NextToken
	}

	base := appClientSettings(config)
	if existingID == "" {
		output, err := client.CreateUserPoolClient(ctx, &cognitoidentityprovider.CreateUserPoolClientInput{
			UserPoolId:                      aws.String(poolID),
			ClientName:                      aws.String(config.AppClientName),
			GenerateSecret:                  false,
			AllowedOAuthFlowsUserPoolClient: true,
			AllowedOAuthFlows:               base.allowedFlows,
			AllowedOAuthScopes:              base.allowedScopes,
			CallbackURLs:                    config.CallbackURLs,
			DefaultRedirectURI:              aws.String(config.CallbackURLs[0]),
			EnableTokenRevocation:           aws.Bool(true),
			LogoutURLs:                      config.LogoutURLs,
			PreventUserExistenceErrors:      types.PreventUserExistenceErrorTypesEnabled,
			ReadAttributes:                  base.attributes,
			RefreshTokenRotation:            base.refreshTokenRotation,
			RefreshTokenValidity:            30,
			SupportedIdentityProviders:      []string{googleProviderName},
			WriteAttributes:                 base.attributes,
		})
		if err != nil {
			return "", fmt.Errorf("create Cognito app client: %w", err)
		}
		return aws.ToString(output.UserPoolClient.ClientId), nil
	}

	output, err := client.UpdateUserPoolClient(ctx, &cognitoidentityprovider.UpdateUserPoolClientInput{
		UserPoolId:                      aws.String(poolID),
		ClientId:                        aws.String(existingID),
		ClientName:                      aws.String(config.AppClientName),
		AllowedOAuthFlowsUserPoolClient: true,
		AllowedOAuthFlows:               base.allowedFlows,
		AllowedOAuthScopes:              base.allowedScopes,
		CallbackURLs:                    config.CallbackURLs,
		DefaultRedirectURI:              aws.String(config.CallbackURLs[0]),
		EnableTokenRevocation:           aws.Bool(true),
		LogoutURLs:                      config.LogoutURLs,
		PreventUserExistenceErrors:      types.PreventUserExistenceErrorTypesEnabled,
		ReadAttributes:                  base.attributes,
		RefreshTokenRotation:            base.refreshTokenRotation,
		RefreshTokenValidity:            30,
		SupportedIdentityProviders:      []string{googleProviderName},
		WriteAttributes:                 base.attributes,
	})
	if err != nil {
		return "", fmt.Errorf("update Cognito app client: %w", err)
	}
	return aws.ToString(output.UserPoolClient.ClientId), nil
}

type clientSettings struct {
	allowedFlows         []types.OAuthFlowType
	allowedScopes        []string
	attributes           []string
	refreshTokenRotation *types.RefreshTokenRotationType
}

func appClientSettings(_ Config) clientSettings {
	return clientSettings{
		allowedFlows:  []types.OAuthFlowType{types.OAuthFlowTypeCode},
		allowedScopes: []string{"openid", "email", "profile"},
		attributes:    []string{"email", "family_name", "given_name", "name", "picture"},
		refreshTokenRotation: &types.RefreshTokenRotationType{
			Feature:                 types.FeatureTypeEnabled,
			RetryGracePeriodSeconds: aws.Int32(10),
		},
	}
}

func ensureManagedLoginBranding(ctx context.Context, client *cognitoidentityprovider.Client, poolID, clientID string) error {
	_, err := client.DescribeManagedLoginBrandingByClient(ctx, &cognitoidentityprovider.DescribeManagedLoginBrandingByClientInput{
		ClientId:   aws.String(clientID),
		UserPoolId: aws.String(poolID),
	})
	if err == nil {
		return nil
	}
	var notFound *types.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("describe managed login branding: %w", err)
	}
	_, err = client.CreateManagedLoginBranding(ctx, &cognitoidentityprovider.CreateManagedLoginBrandingInput{
		ClientId:                 aws.String(clientID),
		UserPoolId:               aws.String(poolID),
		UseCognitoProvidedValues: true,
	})
	if err != nil {
		return fmt.Errorf("create managed login branding: %w", err)
	}
	return nil
}
