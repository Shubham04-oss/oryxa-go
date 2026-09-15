package providers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// Figma refreshes at its own endpoint and returns no new refresh token. It
// keeps one access token per app and account, so a refresh ends the access
// token before it.
func Figma() Provider {
	return Provider{
		Name:            "figma",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialAPIKey},
		Scopes: ScopeRules{
			Ceiling: []string{
				"current_user:read", "file_comments:read", "file_comments:write", "file_content:read",
				"file_dev_resources:read", "file_dev_resources:write", "file_metadata:read",
				"file_variables:read", "file_variables:write", "file_versions:read", "folder_metadata:read", "folders:read",
				"library_analytics:read", "library_assets:read", "library_content:read", "selections:read",
				"team_library_content:read", "webhooks:read", "webhooks:write",
			},
			// The identity check reads the member at /v1/me.
			Required: []string{"current_user:read"},
		},
		OAuth2: &OAuth2{
			AuthorizationURL:    "https://www.figma.com/oauth",
			TokenURL:            "https://api.figma.com/v1/oauth/token",
			RefreshURL:          "https://api.figma.com/v1/oauth/refresh",
			APIURL:              "https://api.figma.com/v1",
			PKCE:                true,
			ClientSecretBasic:   true,
			RequireRefreshToken: true,
			// Figma documents no error codes for its token endpoints, so a refresh
			// it refuses outright means reconnecting.
			PermanentErrors: []string{"invalid_client", "invalid_grant", "oauth_request_rejected"},
			Identity:        figmaIdentity,
		},
		Keys: map[domain.CredentialType]KeyAuth{
			// Personal access tokens go in X-Figma-Token; OAuth access tokens are
			// bearer tokens.
			domain.CredentialAPIKey: {
				Placement: domain.TokenPlacement{In: "header", Name: "X-Figma-Token"},
				APIURL:    "https://api.figma.com/v1",
				Identity:  figmaUser,
			},
		},
	}
}

func figmaIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	return figmaUser(ctx, client, apiURL, bearerCredential(grant.AccessToken))
}

// figmaUser is figmaIdentity generalized to any way of applying a credential,
// so a personal access token (KeyAuth) can share it.
func figmaUser(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error) {
	var user struct {
		ID     string `json:"id"`
		Handle string `json:"handle"`
		Email  string `json:"email"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/me", apply, nil, nil, &user); err != nil {
		return domain.ProviderAccount{}, err
	}
	if user.ID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Figma account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: user.ID, Name: firstNonEmpty(user.Email, user.Handle)}, nil
}
