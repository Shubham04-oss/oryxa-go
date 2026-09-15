package providers

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// ClickUp has no OAuth scopes: the member picks the workspaces an app may
// reach while consenting. Its access tokens do not expire, and it issues no
// refresh token.
func ClickUp() Provider {
	return Provider{
		Name:            "clickup",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialAPIKey},
		OAuth2: &OAuth2{
			AuthorizationURL: "https://app.clickup.com/api",
			TokenURL:         "https://api.clickup.com/api/v2/oauth/token",
			APIURL:           "https://api.clickup.com/api/v2",
			Identity:         clickUpIdentity,
		},
		Keys: map[domain.CredentialType]KeyAuth{
			// Personal API tokens are sent with no "Bearer " prefix; OAuth
			// access tokens take one.
			domain.CredentialAPIKey: {
				Placement: domain.TokenPlacement{In: "header", Name: "Authorization"},
				APIURL:    "https://api.clickup.com/api/v2",
				Identity:  clickUpUser,
			},
		},
	}
}

func clickUpIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	return clickUpUser(ctx, client, apiURL, bearerCredential(grant.AccessToken))
}

// clickUpUser is clickUpIdentity generalized to any way of applying a
// credential, so a personal API token (KeyAuth) can share it.
func clickUpUser(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error) {
	var result struct {
		User struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/user", apply, nil, nil, &result); err != nil {
		return domain.ProviderAccount{}, err
	}
	if result.User.ID < 1 {
		return domain.ProviderAccount{}, fmt.Errorf("%w: ClickUp account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: strconv.FormatInt(result.User.ID, 10), Name: firstNonEmpty(result.User.Email, result.User.Username)}, nil
}
