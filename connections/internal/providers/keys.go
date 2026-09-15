package providers

// This file is key-based credentials: api_key, bearer and basic. They skip
// the OAuth dance entirely -- their owner supplies the secret directly, and
// KeyIdentity confirms it works before a connection is saved.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// KeyIdentity returns the account a key-based credential belongs to.
func (client Client) KeyIdentity(ctx context.Context, provider Provider, credentialType domain.CredentialType, credential domain.CredentialInput) (domain.ProviderAccount, error) {
	value, placement, err := KeyToken(provider, credentialType, credential)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	auth := provider.Keys[credentialType]
	if auth.Identity == nil {
		return domain.ProviderAccount{}, fmt.Errorf("%s has no identity check for %s credentials: %w", provider.Name, credentialType, domain.ErrInvalid)
	}
	account, err := auth.Identity(ctx, client.httpClient(), strings.TrimRight(auth.APIURL, "/"), applyPlacement(placement, value))
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	if !safeValue(account.ID) || (account.Name != "" && !safeValue(account.Name)) {
		return domain.ProviderAccount{}, fmt.Errorf("%w: provider account is incomplete", domain.ErrProviderUnavailable)
	}
	return account, nil
}

// KeyToken returns the value a key-based credential puts on requests, and
// where it goes. The placement for bearer and basic comes from the
// provider's Keys entry when it sets one, else header Authorization with the
// type's usual prefix; api_key always uses the provider's placement, since
// there is no universal default for it.
func KeyToken(provider Provider, credentialType domain.CredentialType, credential domain.CredentialInput) (string, domain.TokenPlacement, error) {
	auth, ok := provider.Keys[credentialType]
	if !ok {
		return "", domain.TokenPlacement{}, fmt.Errorf("%s does not support %s credentials: %w", provider.Name, credentialType, domain.ErrInvalid)
	}
	switch credentialType {
	case domain.CredentialAPIKey:
		return credential.APIKey, auth.Placement, nil
	case domain.CredentialBearer:
		return credential.Token, placementOrDefault(auth.Placement, "Bearer "), nil
	case domain.CredentialBasic:
		value := base64.StdEncoding.EncodeToString([]byte(credential.Username + ":" + credential.Password))
		return value, placementOrDefault(auth.Placement, "Basic "), nil
	default:
		return "", domain.TokenPlacement{}, fmt.Errorf("%s is not a key-based credential type: %w", credentialType, domain.ErrInvalid)
	}
}

// placementOrDefault returns configured when it is set, else an Authorization
// header with prefix.
func placementOrDefault(configured domain.TokenPlacement, prefix string) domain.TokenPlacement {
	if configured != (domain.TokenPlacement{}) {
		return configured
	}
	return domain.TokenPlacement{In: "header", Name: "Authorization", Prefix: prefix}
}

// applyPlacement returns a function that puts a credential's value on a
// request the way its TokenPlacement says: a header, or a query parameter.
func applyPlacement(placement domain.TokenPlacement, value string) func(*http.Request) {
	return func(request *http.Request) {
		if placement.In == "query" {
			query := request.URL.Query()
			query.Set(placement.Name, placement.Prefix+value)
			request.URL.RawQuery = query.Encode()
			return
		}
		request.Header.Set(placement.Name, placement.Prefix+value)
	}
}
