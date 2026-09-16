package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// Box refresh tokens work once: each refresh returns a new one, which Refresh
// keeps. An enterprise's admin may have to authorize the app before its members
// can connect.
func Box() Provider {
	return Provider{
		Name:            "box",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2},
		Scopes: ScopeRules{
			Ceiling: []string{
				"ai.readwrite", "enterprise_content", "manage_data_retention", "manage_enterprise_properties",
				"manage_groups", "manage_legal_holds", "manage_managed_users", "manage_triggers", "manage_webhook",
				"root_readonly", "root_readwrite", "sign_requests.readwrite",
			},
			// Reading and writing everything covers reading it.
			Implications: domain.Implications{"root_readonly": {"root_readwrite"}},
		},
		OAuth2: &OAuth2{
			AuthorizationURL:    "https://account.box.com/api/oauth2/authorize",
			TokenURL:            "https://api.box.com/oauth2/token",
			APIURL:              "https://api.box.com/2.0",
			RevokeURL:           "https://api.box.com/oauth2/revoke",
			RequireRefreshToken: true,
			PermanentErrors:     []string{"invalid_client", "invalid_grant", "unauthorized_client"},
			Identity:            boxIdentity,
			Revoke:              boxRevoke,
		},
	}
}

func boxIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	var user struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Login string `json:"login"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/users/me", bearerCredential(grant.AccessToken), nil, nil, &user); err != nil {
		return domain.ProviderAccount{}, err
	}
	if user.ID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Box account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: user.ID, Name: firstNonEmpty(user.Login, user.Name)}, nil
}

// boxRevoke revokes a grant with the app's client. Box takes either token and
// the refresh token outlives the access token, so it goes first. A token Box
// reports as invalid_grant or invalid_token is already gone.
func boxRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	token := firstNonEmpty(request.Grant.RefreshToken, request.Grant.AccessToken)
	if token == "" {
		return false, nil
	}
	values := url.Values{"client_id": {request.ClientID}, "client_secret": {request.ClientSecret}, "token": {token}}
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
	var failure struct {
		Error string `json:"error"`
	}
	if response.StatusCode == http.StatusBadRequest && json.Unmarshal(payload, &failure) == nil &&
		(failure.Error == "invalid_grant" || failure.Error == "invalid_token") {
		return true, nil
	}
	return false, fmt.Errorf("%w: Box returned %d", domain.ErrProviderUnavailable, response.StatusCode)
}
