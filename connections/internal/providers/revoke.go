package providers

// This file ends a grant at the provider, the last step before its
// credentials are deleted. Revoking is best-effort from the caller's view:
// "already gone" counts as revoked, and a provider with no revoke endpoint
// simply reports it revoked nothing.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

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

// githubRevoke deletes a single OAuth App token. It targets
// /applications/{client_id}/token, not .../grant: the grant endpoint would
// revoke every token GitHub has issued this account for the app, which would
// disconnect every other connection sharing that authorization. GitHub
// answers 404 or 422 when the token is already gone, which is not a failure.
func githubRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	if request.Grant.AccessToken == "" {
		return false, nil
	}
	if request.ClientID == "" || request.ClientSecret == "" {
		return false, fmt.Errorf("client ID and client secret are required: %w", domain.ErrInvalid)
	}
	body, err := json.Marshal(struct {
		AccessToken string `json:"access_token"`
	}{request.Grant.AccessToken})
	if err != nil {
		return false, fmt.Errorf("%w: could not build revoke request", domain.ErrProviderUnavailable)
	}
	endpoint := strings.TrimRight(provider.APIURL, "/") + "/applications/" + url.PathEscape(request.ClientID) + "/token"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("%w: revoke request could not be built", domain.ErrProviderUnavailable)
	}
	httpRequest.SetBasicAuth(request.ClientID, request.ClientSecret)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/vnd.github+json")
	response, err := client.Do(httpRequest)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	switch response.StatusCode {
	case http.StatusNoContent, http.StatusNotFound, http.StatusUnprocessableEntity:
		return true, nil
	default:
		return false, fmt.Errorf("%w: GitHub returned %d", domain.ErrProviderUnavailable, response.StatusCode)
	}
}

// googleRevoke revokes a grant at Google's token revocation endpoint,
// preferring the refresh token since revoking it also invalidates the access
// token issued with it. Google answers 400 with error=invalid_token when the
// token is already gone, which is not a failure.
func googleRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	token := request.Grant.RefreshToken
	if token == "" {
		token = request.Grant.AccessToken
	}
	if token == "" {
		return false, nil
	}
	values := url.Values{"token": {token}}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.RevokeURL, strings.NewReader(values.Encode()))
	if err != nil {
		return false, fmt.Errorf("%w: revoke request could not be built", domain.ErrProviderUnavailable)
	}
	httpRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(httpRequest)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(payload) > maxResponseBytes {
		return false, fmt.Errorf("%w: revoke response could not be read", domain.ErrProviderUnavailable)
	}
	if response.StatusCode == http.StatusOK {
		return true, nil
	}
	var errorBody struct {
		Error string `json:"error"`
	}
	if response.StatusCode == http.StatusBadRequest && json.Unmarshal(payload, &errorBody) == nil && errorBody.Error == "invalid_token" {
		return true, nil
	}
	return false, fmt.Errorf("%w: Google returned %d", domain.ErrProviderUnavailable, response.StatusCode)
}

// slackRevoke revokes only the connecting member's own user token. It never
// revokes grant.AccessToken, the bot token: Slack issues one bot token per
// workspace and app, shared by every member who connected that workspace, so
// revoking it on one member's disconnect would break the others. A grant
// without a user token has nothing this connection alone may revoke.
func slackRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	userToken := request.Grant.Tokens["user"]
	if userToken == "" {
		return false, nil
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(provider.APIURL, "/")+"/auth.revoke", nil)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request could not be built", domain.ErrProviderUnavailable)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+userToken)
	response, err := client.Do(httpRequest)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(payload) > maxResponseBytes {
		return false, fmt.Errorf("%w: revoke response could not be read", domain.ErrProviderUnavailable)
	}
	var result struct {
		OK      bool   `json:"ok"`
		Revoked bool   `json:"revoked"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(payload, &result) != nil {
		return false, fmt.Errorf("%w: revoke response is not valid", domain.ErrProviderUnavailable)
	}
	if result.OK {
		return result.Revoked, nil
	}
	switch safeCode(result.Error) {
	case "invalid_auth", "token_revoked", "account_inactive", "not_authed":
		return true, nil
	default:
		return false, fmt.Errorf("%w: Slack returned %s", domain.ErrProviderUnavailable, safeCode(result.Error))
	}
}

// linearRevoke revokes a grant at Linear's token revocation endpoint. Linear
// answers 400 or 401 when the token is already gone, which is not a failure.
func linearRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	if request.Grant.AccessToken == "" {
		return false, nil
	}
	endpoint := strings.TrimRight(provider.APIURL, "/") + "/oauth/revoke"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request could not be built", domain.ErrProviderUnavailable)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+request.Grant.AccessToken)
	response, err := client.Do(httpRequest)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	switch response.StatusCode {
	case http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized:
		return true, nil
	default:
		return false, fmt.Errorf("%w: Linear returned %d", domain.ErrProviderUnavailable, response.StatusCode)
	}
}
