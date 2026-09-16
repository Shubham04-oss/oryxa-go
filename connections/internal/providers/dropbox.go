package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// Dropbox issues a refresh token only for offline access, and keeps it: a
// refresh returns a new access token and no new refresh token.
func Dropbox() Provider {
	return Provider{
		Name:            "dropbox",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2},
		Scopes: ScopeRules{
			Ceiling: []string{
				"account_info.read", "account_info.write", "contacts.write", "email",
				"file_requests.read", "file_requests.write", "files.content.read", "files.content.write",
				"files.metadata.read", "files.metadata.write", "files.permanent_delete", "openid", "profile",
				"sharing.read", "sharing.write",
			},
			// The identity check reads the account with users/get_current_account.
			Required: []string{"account_info.read"},
		},
		OAuth2: &OAuth2{
			AuthorizationURL:    "https://www.dropbox.com/oauth2/authorize",
			TokenURL:            "https://api.dropboxapi.com/oauth2/token",
			APIURL:              "https://api.dropboxapi.com/2",
			Params:              map[string]string{"token_access_type": "offline"},
			RequireRefreshToken: true,
			// Dropbox documents no error codes for its token endpoint, so a
			// refresh it refuses outright means reconnecting.
			PermanentErrors: []string{"invalid_client", "invalid_grant", "oauth_request_rejected"},
			Identity:        dropboxIdentity,
			Revoke:          dropboxRevoke,
		},
	}
}

func dropboxIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	var account struct {
		AccountID string `json:"account_id"`
		Email     string `json:"email"`
		Name      struct {
			DisplayName string `json:"display_name"`
		} `json:"name"`
	}
	// An RPC endpoint that takes no arguments is called with no body.
	if err := callJSON(ctx, client, http.MethodPost, apiURL+"/users/get_current_account", bearerCredential(grant.AccessToken), nil, nil, &account); err != nil {
		return domain.ProviderAccount{}, err
	}
	if account.AccountID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Dropbox account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: account.AccountID, Name: firstNonEmpty(account.Email, account.Name.DisplayName)}, nil
}

// dropboxRevoke ends a grant with auth/token/revoke, which disables the access
// token it is called with, its refresh token, and every access token from that
// refresh token. The call needs a live access token and a stored one expires
// within hours, so the grant is refreshed first. A grant Dropbox already
// refuses is gone.
func dropboxRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	token := request.Grant.AccessToken
	if request.Grant.RefreshToken != "" {
		refreshed, err := Client{HTTP: client}.Refresh(ctx, provider, RefreshRequest{
			ClientID: request.ClientID, ClientSecret: request.ClientSecret, RefreshToken: request.Grant.RefreshToken,
		})
		switch {
		case errors.Is(err, domain.ErrCredentialRejected):
			return true, nil
		case err != nil:
			return false, err
		}
		token = refreshed.AccessToken
	}
	if token == "" {
		return false, nil
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.APIURL+"/auth/token/revoke", nil)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request could not be built", domain.ErrProviderUnavailable)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(httpRequest)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	switch response.StatusCode {
	case http.StatusOK, http.StatusUnauthorized:
		return true, nil
	default:
		return false, fmt.Errorf("%w: Dropbox returned %d", domain.ErrProviderUnavailable, response.StatusCode)
	}
}
