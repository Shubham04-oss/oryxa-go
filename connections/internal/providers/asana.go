package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// asanaResourceScopes are Asana's specific API scopes, each of which its
// default scope covers.
var asanaResourceScopes = []string{
	"attachments:delete", "attachments:read", "attachments:write", "custom_fields:read", "custom_fields:write",
	"goals:read", "jobs:read", "portfolios:read", "portfolios:write", "project_templates:read",
	"projects:delete", "projects:read", "projects:write", "stories:read", "stories:write",
	"tags:read", "tags:write", "task_templates:read", "tasks:delete", "tasks:read", "tasks:write",
	"team_memberships:read", "teams:read", "users:read",
	"webhooks:delete", "webhooks:read", "webhooks:write", "workspaces.typeahead:read", "workspaces:read",
}

// Asana apps either have full permissions, requested as default on its own, or
// request specific scopes such as tasks:read.
func Asana() Provider {
	implications := domain.Implications{}
	for _, scope := range asanaResourceScopes {
		implications[scope] = []string{"default"}
	}
	return Provider{
		Name:            "asana",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialBearer},
		Scopes: ScopeRules{
			Ceiling: append(slices.Clone(asanaResourceScopes), "default", "email", "openid", "profile"),
			// The identity check reads the member at /users/me, which default
			// covers too.
			Required:     []string{"users:read"},
			Implications: implications,
			Exclusive:    []string{"default"},
		},
		OAuth2: &OAuth2{
			AuthorizationURL: "https://app.asana.com/-/oauth_authorize",
			TokenURL:         "https://app.asana.com/-/oauth_token",
			APIURL:           "https://app.asana.com/api/1.0",
			PKCE:             true,
			// Access tokens last an hour, so a grant needs its refresh token.
			RequireRefreshToken: true,
			PermanentErrors:     []string{"invalid_client", "invalid_grant", "unauthorized_client"},
			Identity:            asanaIdentity,
			RevokeURL:           "https://app.asana.com/-/oauth_revoke",
			Revoke:              asanaRevoke,
		},
		Keys: map[domain.CredentialType]KeyAuth{
			// A personal access token, sent as OAuth2 grants are.
			domain.CredentialBearer: {APIURL: "https://app.asana.com/api/1.0", Identity: asanaUser},
		},
	}
}

func asanaIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	return asanaUser(ctx, client, apiURL, bearerCredential(grant.AccessToken))
}

// asanaUser is asanaIdentity generalized to any way of applying a credential,
// so a personal access token (KeyAuth) can share it.
func asanaUser(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error) {
	var result struct {
		Data struct {
			GID   string `json:"gid"`
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"data"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/users/me", apply, nil, nil, &result); err != nil {
		return domain.ProviderAccount{}, err
	}
	if result.Data.GID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Asana account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: result.Data.GID, Name: firstNonEmpty(result.Data.Email, result.Data.Name)}, nil
}

// asanaRevoke deauthorizes a grant at Asana's revoke endpoint. Asana takes
// only the refresh token, which also ends the access tokens issued from it. A
// token Asana reports as invalid_grant or invalid_token is already gone, which
// is not a failure.
func asanaRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	if request.Grant.RefreshToken == "" {
		return false, nil
	}
	values := url.Values{"client_id": {request.ClientID}, "client_secret": {request.ClientSecret}, "token": {request.Grant.RefreshToken}}
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
	if response.StatusCode == http.StatusBadRequest && json.Unmarshal(payload, &errorBody) == nil &&
		(errorBody.Error == "invalid_grant" || errorBody.Error == "invalid_token") {
		return true, nil
	}
	return false, fmt.Errorf("%w: Asana returned %d", domain.ErrProviderUnavailable, response.StatusCode)
}
