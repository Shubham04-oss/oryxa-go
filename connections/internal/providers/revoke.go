package providers

// This file ends a grant at the provider, the last step before its
// credentials are deleted. Revoking is best-effort from the caller's view:
// "already gone" counts as revoked, and a provider with no revoke endpoint
// simply reports it revoked nothing.

import "context"

// Revoke revokes a grant at the provider. It reports false, with no error,
// when the provider has no way to revoke it, or the grant carries no token.
func (client Client) Revoke(ctx context.Context, provider Provider, request RevokeRequest) (bool, error) {
	if provider.OAuth2 == nil || provider.OAuth2.Revoke == nil {
		return false, nil
	}
	grant := request.Grant
	if grant.AccessToken == "" && grant.RefreshToken == "" && len(grant.Tokens) == 0 {
		return false, nil
	}
	return provider.OAuth2.Revoke(ctx, client.httpClient(), *provider.OAuth2, request)
}
