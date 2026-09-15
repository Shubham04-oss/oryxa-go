package providers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// Airtable requires PKCE and client credentials as HTTP Basic. Refresh tokens
// rotate: a refresh ends the access and refresh tokens before it. The member
// picks the bases and workspaces an app may reach while consenting.
func Airtable() Provider {
	return Provider{
		Name:            "airtable",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialBearer},
		Scopes: ScopeRules{
			Ceiling: []string{
				"block:manage", "data.recordComments:read", "data.recordComments:write", "data.records:read",
				"data.records:write", "enterprise.groups:read", "schema.bases:read", "schema.bases:write",
				"user.email:read", "webhook:manage", "workspacesAndBases.shares:manage", "workspacesAndBases:read",
				"workspacesAndBases:write",
			},
			// Airtable refuses an empty scope, and the identity check names the
			// account by its email.
			Required: []string{"user.email:read"},
		},
		OAuth2: &OAuth2{
			AuthorizationURL:    "https://airtable.com/oauth2/v1/authorize",
			TokenURL:            "https://airtable.com/oauth2/v1/token",
			APIURL:              "https://api.airtable.com/v0",
			PKCE:                true,
			ClientSecretBasic:   true,
			RequireRefreshToken: true,
			PermanentErrors:     []string{"invalid_client", "invalid_grant"},
			Identity:            airtableIdentity,
		},
		Keys: map[domain.CredentialType]KeyAuth{
			// A personal access token, sent as OAuth2 grants are.
			domain.CredentialBearer: {APIURL: "https://api.airtable.com/v0", Identity: airtableUser},
		},
	}
}

func airtableIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	return airtableUser(ctx, client, apiURL, bearerCredential(grant.AccessToken))
}

// airtableUser is airtableIdentity generalized to any way of applying a
// credential, so a personal access token (KeyAuth) can share it. The email is
// there only with user.email:read.
func airtableUser(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error) {
	var user struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/meta/whoami", apply, nil, nil, &user); err != nil {
		return domain.ProviderAccount{}, err
	}
	if user.ID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Airtable account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: user.ID, Name: firstNonEmpty(user.Email, user.ID)}, nil
}
