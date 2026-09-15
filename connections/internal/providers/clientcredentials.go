package providers

// This file is the client-credentials grant: an app mints its own access
// token with no per-user consent, after an organization admin has granted it
// tenant access at AdminConsentURL.

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// Mint issues an app-only token with the client-credentials grant. A
// permanent refusal wraps domain.ErrCredentialRejected; anything else wraps
// domain.ErrProviderUnavailable, so the caller can retry.
func (client Client) Mint(ctx context.Context, provider Provider, request MintRequest) (Grant, error) {
	cc := provider.ClientCredentials
	if cc == nil {
		return Grant{}, fmt.Errorf("%s does not support client-credentials grants: %w", provider.Name, domain.ErrInvalid)
	}
	// CredentialInput.Validate already enforces the tenant pattern
	// (^[A-Za-z0-9][A-Za-z0-9.-]{0,99}$) and trims it; reuse it rather than
	// keeping a second copy of that rule in this package.
	trimmed, err := domain.CredentialInput{Tenant: request.Tenant}.Validate(domain.CredentialOAuth2ClientCredentials)
	if err != nil {
		return Grant{}, err
	}
	if request.ClientID == "" || request.ClientSecret == "" {
		return Grant{}, fmt.Errorf("client ID and client secret are required: %w", domain.ErrInvalid)
	}
	tokenURL := strings.ReplaceAll(cc.TokenURL, "{tenant}", url.PathEscape(trimmed.Tenant))
	values := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {request.ClientID},
		"client_secret": {request.ClientSecret},
		"scope":         {strings.Join(cc.Scopes, " ")},
	}
	// The client-credentials token endpoint follows the same RFC 6749 shape
	// as an authorization-code one, so client.token's response cap, 429/5xx
	// handling and decoding all apply unchanged.
	token, err := client.token(ctx, OAuth2{TokenURL: tokenURL}, values)
	if err != nil {
		return Grant{}, err
	}
	if token.Error != "" || token.AccessToken == "" {
		code := safeCode(token.Error)
		if slices.Contains(cc.PermanentErrors, code) {
			return Grant{}, fmt.Errorf("%w: %s", domain.ErrCredentialRejected, code)
		}
		return Grant{}, fmt.Errorf("%w: %s", domain.ErrProviderUnavailable, code)
	}
	grant := client.grant(OAuth2{}, token, cc.Scopes)
	// A client-credentials grant's scopes are exactly what the app is
	// configured to request, not whatever the token response echoes back.
	grant.Scopes = domain.NormalizeScopes(cc.Scopes)
	return grant, nil
}

// AdminConsentURL is where an admin grants a client-credentials app access.
func (client Client) AdminConsentURL(provider Provider, request AdminConsentRequest) (string, error) {
	cc := provider.ClientCredentials
	if cc == nil {
		return "", fmt.Errorf("%s does not support client-credentials grants: %w", provider.Name, domain.ErrInvalid)
	}
	if request.ClientID == "" || request.RedirectURI == "" || request.State == "" {
		return "", fmt.Errorf("client ID, redirect URI and state are required: %w", domain.ErrInvalid)
	}
	parsed, err := url.Parse(cc.AdminConsentURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%w: admin consent URL is invalid", domain.ErrInvalid)
	}
	query := parsed.Query()
	query.Set("client_id", request.ClientID)
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("state", request.State)
	if len(cc.Scopes) > 0 {
		query.Set("scope", strings.Join(cc.Scopes, " "))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
