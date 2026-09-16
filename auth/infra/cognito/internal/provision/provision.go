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

// Agent app client constants. See the "Auth" section of the agent identity
// contract: a resource server identifier "oryxa" with scope "agent.actions",
// and an app client named "<pool name>-agents".
const (
	agentResourceServerIdentifier = "oryxa"
	agentActionsScopeName         = "agent.actions"
	agentAppClientSuffix          = "-agents"
)

type Output struct {
	Region            string `json:"region"`
	UserPoolID        string `json:"user_pool_id"`
	AppClientID       string `json:"app_client_id"`
	CognitoDomain     string `json:"cognito_domain"`
	Issuer            string `json:"issuer"`
	GoogleRedirectURI string `json:"google_redirect_uri"`
	// AgentAppClientID is the agent app client's ID, set only when
	// config.AgentCallbackURL provisioned one. Its secret is never returned
	// or logged.
	AgentAppClientID string `json:"agent_app_client_id,omitempty"`
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
	output := Output{
		Region:            config.Region,
		UserPoolID:        poolID,
		AppClientID:       clientID,
		CognitoDomain:     domain,
		Issuer:            fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s", config.Region, poolID),
		GoogleRedirectURI: domain + "/oauth2/idpresponse",
	}

	if config.AgentCallbackURL == "" {
		return output, nil
	}
	if err := ensureAgentResourceServer(ctx, client, poolID); err != nil {
		return Output{}, err
	}
	agentClientID, err := ensureAgentAppClient(ctx, client, poolID, config)
	if err != nil {
		return Output{}, err
	}
	output.AgentAppClientID = agentClientID
	return output, nil
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
				poolID := aws.ToString(pool.Id)
				if err := updateUserPool(ctx, client, poolID, config.PoolName); err != nil {
					return "", err
				}
				return poolID, nil
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
		AccountRecoverySetting: &types.AccountRecoverySettingType{RecoveryMechanisms: []types.RecoveryOptionType{{
			Name:     types.RecoveryOptionNameTypeVerifiedEmail,
			Priority: aws.Int32(1),
		}}},
		AutoVerifiedAttributes: []types.VerifiedAttributeType{
			types.VerifiedAttributeTypeEmail,
		},
		DeletionProtection: types.DeletionProtectionTypeActive,
		Policies: &types.UserPoolPolicyType{PasswordPolicy: &types.PasswordPolicyType{
			MinimumLength:                 aws.Int32(12),
			RequireLowercase:              true,
			RequireNumbers:                true,
			RequireSymbols:                true,
			RequireUppercase:              true,
			TemporaryPasswordValidityDays: 1,
		}},
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

func updateUserPool(ctx context.Context, client *cognitoidentityprovider.Client, poolID, poolName string) error {
	_, err := client.UpdateUserPool(ctx, &cognitoidentityprovider.UpdateUserPoolInput{
		UserPoolId:            aws.String(poolID),
		PoolName:              aws.String(poolName),
		AdminCreateUserConfig: &types.AdminCreateUserConfigType{AllowAdminCreateUserOnly: true},
		AccountRecoverySetting: &types.AccountRecoverySettingType{RecoveryMechanisms: []types.RecoveryOptionType{{
			Name:     types.RecoveryOptionNameTypeVerifiedEmail,
			Priority: aws.Int32(1),
		}}},
		AutoVerifiedAttributes: []types.VerifiedAttributeType{types.VerifiedAttributeTypeEmail},
		DeletionProtection:     types.DeletionProtectionTypeActive,
		Policies: &types.UserPoolPolicyType{PasswordPolicy: &types.PasswordPolicyType{
			MinimumLength:                 aws.Int32(12),
			RequireLowercase:              true,
			RequireNumbers:                true,
			RequireSymbols:                true,
			RequireUppercase:              true,
			TemporaryPasswordValidityDays: 1,
		}},
		UserPoolTier: types.UserPoolTierTypeEssentials,
	})
	if err != nil {
		return fmt.Errorf("update Cognito user pool: %w", err)
	}
	return nil
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
			AccessTokenValidity:             aws.Int32(15),
			IdTokenValidity:                 aws.Int32(15),
			AllowedOAuthFlowsUserPoolClient: true,
			AllowedOAuthFlows:               base.allowedFlows,
			AllowedOAuthScopes:              base.allowedScopes,
			CallbackURLs:                    config.CallbackURLs,
			DefaultRedirectURI:              aws.String(config.CallbackURLs[0]),
			EnableTokenRevocation:           aws.Bool(true),
			LogoutURLs:                      config.LogoutURLs,
			PreventUserExistenceErrors:      types.PreventUserExistenceErrorTypesEnabled,
			ExplicitAuthFlows:               base.explicitAuthFlows,
			ReadAttributes:                  base.attributes,
			RefreshTokenRotation:            base.refreshTokenRotation,
			RefreshTokenValidity:            30,
			TokenValidityUnits: &types.TokenValidityUnitsType{
				AccessToken:  types.TimeUnitsTypeMinutes,
				IdToken:      types.TimeUnitsTypeMinutes,
				RefreshToken: types.TimeUnitsTypeDays,
			},
			SupportedIdentityProviders: []string{"COGNITO", googleProviderName},
			WriteAttributes:            base.attributes,
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
		AccessTokenValidity:             aws.Int32(15),
		IdTokenValidity:                 aws.Int32(15),
		AllowedOAuthFlowsUserPoolClient: true,
		AllowedOAuthFlows:               base.allowedFlows,
		AllowedOAuthScopes:              base.allowedScopes,
		CallbackURLs:                    config.CallbackURLs,
		DefaultRedirectURI:              aws.String(config.CallbackURLs[0]),
		EnableTokenRevocation:           aws.Bool(true),
		LogoutURLs:                      config.LogoutURLs,
		PreventUserExistenceErrors:      types.PreventUserExistenceErrorTypesEnabled,
		ExplicitAuthFlows:               base.explicitAuthFlows,
		ReadAttributes:                  base.attributes,
		RefreshTokenRotation:            base.refreshTokenRotation,
		RefreshTokenValidity:            30,
		TokenValidityUnits: &types.TokenValidityUnitsType{
			AccessToken:  types.TimeUnitsTypeMinutes,
			IdToken:      types.TimeUnitsTypeMinutes,
			RefreshToken: types.TimeUnitsTypeDays,
		},
		SupportedIdentityProviders: []string{"COGNITO", googleProviderName},
		WriteAttributes:            base.attributes,
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
	explicitAuthFlows    []types.ExplicitAuthFlowsType
	refreshTokenRotation *types.RefreshTokenRotationType
}

func appClientSettings(_ Config) clientSettings {
	return clientSettings{
		allowedFlows:  []types.OAuthFlowType{types.OAuthFlowTypeCode},
		allowedScopes: []string{"openid", "email", "profile"},
		attributes:    []string{"email", "family_name", "given_name", "name", "picture"},
		explicitAuthFlows: []types.ExplicitAuthFlowsType{
			types.ExplicitAuthFlowsTypeAllowUserPasswordAuth,
		},
		refreshTokenRotation: &types.RefreshTokenRotationType{
			Feature:                 types.FeatureTypeEnabled,
			RetryGracePeriodSeconds: aws.Int32(10),
		},
	}
}

// ensureAgentResourceServer creates or updates the "oryxa" resource server
// and its "agent.actions" scope, idempotent like the rest of provisioning.
func ensureAgentResourceServer(ctx context.Context, client *cognitoidentityprovider.Client, poolID string) error {
	scopes := []types.ResourceServerScopeType{{
		ScopeName:        aws.String(agentActionsScopeName),
		ScopeDescription: aws.String("Act for the member who granted this, while they are absent."),
	}}

	_, err := client.DescribeResourceServer(ctx, &cognitoidentityprovider.DescribeResourceServerInput{
		Identifier: aws.String(agentResourceServerIdentifier),
		UserPoolId: aws.String(poolID),
	})
	if err == nil {
		_, err = client.UpdateResourceServer(ctx, &cognitoidentityprovider.UpdateResourceServerInput{
			Identifier: aws.String(agentResourceServerIdentifier),
			Name:       aws.String(agentResourceServerIdentifier),
			UserPoolId: aws.String(poolID),
			Scopes:     scopes,
		})
		if err != nil {
			return fmt.Errorf("update agent resource server: %w", err)
		}
		return nil
	}

	var notFound *types.ResourceNotFoundException
	if !errors.As(err, &notFound) {
		return fmt.Errorf("describe agent resource server: %w", err)
	}
	_, err = client.CreateResourceServer(ctx, &cognitoidentityprovider.CreateResourceServerInput{
		Identifier: aws.String(agentResourceServerIdentifier),
		Name:       aws.String(agentResourceServerIdentifier),
		UserPoolId: aws.String(poolID),
		Scopes:     scopes,
	})
	if err != nil {
		return fmt.Errorf("create agent resource server: %w", err)
	}
	return nil
}

// agentAppClientName is the agent app client's name: "<pool name>-agents".
func agentAppClientName(poolName string) string {
	return poolName + agentAppClientSuffix
}

// agentAppClientSettings is the pure, testable part of the agent app
// client's configuration: a confidential, code-flow-only client scoped to
// OpenID Connect and the custom "oryxa/agent.actions" scope, with refresh
// token rotation on so a mint can save the rotated token the contract
// describes.
func agentAppClientSettings() clientSettings {
	return clientSettings{
		allowedFlows:  []types.OAuthFlowType{types.OAuthFlowTypeCode},
		allowedScopes: []string{"openid", agentResourceServerIdentifier + "/" + agentActionsScopeName},
		refreshTokenRotation: &types.RefreshTokenRotationType{
			Feature:                 types.FeatureTypeEnabled,
			RetryGracePeriodSeconds: aws.Int32(10),
		},
	}
}

// ensureAgentAppClient creates or updates the confidential "<pool
// name>-agents" app client, idempotent like ensureAppClient. It returns only
// the client ID: CreateUserPoolClient's response also carries the new
// client's secret, and this function never reads or returns that field, so
// it can never be logged.
func ensureAgentAppClient(ctx context.Context, client *cognitoidentityprovider.Client, poolID string, config Config) (string, error) {
	name := agentAppClientName(config.PoolName)
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
			if aws.ToString(appClient.ClientName) == name {
				existingID = aws.ToString(appClient.ClientId)
				break
			}
		}
		if existingID != "" || output.NextToken == nil {
			break
		}
		nextToken = output.NextToken
	}

	settings := agentAppClientSettings()
	tokenValidityUnits := &types.TokenValidityUnitsType{
		AccessToken:  types.TimeUnitsTypeMinutes,
		RefreshToken: types.TimeUnitsTypeDays,
	}
	if existingID == "" {
		output, err := client.CreateUserPoolClient(ctx, &cognitoidentityprovider.CreateUserPoolClientInput{
			UserPoolId:                      aws.String(poolID),
			ClientName:                      aws.String(name),
			GenerateSecret:                  true,
			AccessTokenValidity:             aws.Int32(60),
			RefreshTokenValidity:            365,
			AllowedOAuthFlowsUserPoolClient: true,
			AllowedOAuthFlows:               settings.allowedFlows,
			AllowedOAuthScopes:              settings.allowedScopes,
			CallbackURLs:                    []string{config.AgentCallbackURL},
			DefaultRedirectURI:              aws.String(config.AgentCallbackURL),
			EnableTokenRevocation:           aws.Bool(true),
			PreventUserExistenceErrors:      types.PreventUserExistenceErrorTypesEnabled,
			RefreshTokenRotation:            settings.refreshTokenRotation,
			TokenValidityUnits:              tokenValidityUnits,
			SupportedIdentityProviders:      []string{"COGNITO", googleProviderName},
		})
		if err != nil {
			return "", fmt.Errorf("create Cognito agent app client: %w", err)
		}
		return aws.ToString(output.UserPoolClient.ClientId), nil
	}

	_, err := client.UpdateUserPoolClient(ctx, &cognitoidentityprovider.UpdateUserPoolClientInput{
		UserPoolId:                      aws.String(poolID),
		ClientId:                        aws.String(existingID),
		ClientName:                      aws.String(name),
		AccessTokenValidity:             aws.Int32(60),
		RefreshTokenValidity:            365,
		AllowedOAuthFlowsUserPoolClient: true,
		AllowedOAuthFlows:               settings.allowedFlows,
		AllowedOAuthScopes:              settings.allowedScopes,
		CallbackURLs:                    []string{config.AgentCallbackURL},
		DefaultRedirectURI:              aws.String(config.AgentCallbackURL),
		EnableTokenRevocation:           aws.Bool(true),
		PreventUserExistenceErrors:      types.PreventUserExistenceErrorTypesEnabled,
		RefreshTokenRotation:            settings.refreshTokenRotation,
		TokenValidityUnits:              tokenValidityUnits,
		SupportedIdentityProviders:      []string{"COGNITO", googleProviderName},
	})
	if err != nil {
		return "", fmt.Errorf("update Cognito agent app client: %w", err)
	}
	return existingID, nil
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
