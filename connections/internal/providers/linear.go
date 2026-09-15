package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func Linear() Provider {
	return Provider{
		Name:            "linear",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialAPIKey},
		Scopes: ScopeRules{
			Ceiling:  []string{"comments:create", "issues:create", "read", "write"},
			Required: []string{"read"},
			Implications: domain.Implications{
				"comments:create": {"write"},
				"issues:create":   {"write"},
			},
		},
		OAuth2: &OAuth2{
			AuthorizationURL: "https://linear.app/oauth/authorize",
			TokenURL:         "https://api.linear.app/oauth/token",
			APIURL:           "https://api.linear.app",
			ScopeSeparator:   ",",
			Params:           map[string]string{"actor": "user"},
			PKCE:             true,
			PermanentErrors:  []string{"invalid_client", "invalid_grant"},
			Identity:         linearIdentity,
			Revoke:           linearRevoke,
		},
		Keys: map[domain.CredentialType]KeyAuth{
			// Linear personal API keys are sent with no "Bearer " prefix.
			domain.CredentialAPIKey: {
				Placement: domain.TokenPlacement{In: "header", Name: "Authorization"},
				APIURL:    "https://api.linear.app",
				Identity:  linearViewer,
			},
		},
	}
}

func linearIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	return linearViewer(ctx, client, apiURL, bearerCredential(grant.AccessToken))
}

// linearViewer is linearIdentity generalized to any way of applying a
// credential, so a personal API key (KeyAuth) can share it.
func linearViewer(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error) {
	var result struct {
		Data struct {
			Viewer struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
			} `json:"viewer"`
			Organization struct {
				Name string `json:"name"`
			} `json:"organization"`
		} `json:"data"`
		Errors []struct {
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	query := strings.NewReader(`{"query":"query { viewer { id name displayName } organization { name } }"}`)
	header := http.Header{"Content-Type": {"application/json"}}
	if err := callJSON(ctx, client, http.MethodPost, apiURL+"/graphql", apply, header, query, &result); err != nil {
		return domain.ProviderAccount{}, err
	}
	for _, failure := range result.Errors {
		if code := strings.ToUpper(failure.Extensions.Code); code == "UNAUTHENTICATED" || code == "FORBIDDEN" {
			return domain.ProviderAccount{}, fmt.Errorf("%w: Linear refused the credential", domain.ErrCredentialRejected)
		}
	}
	if len(result.Errors) > 0 || result.Data.Viewer.ID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Linear account is incomplete", domain.ErrProviderUnavailable)
	}
	name := firstNonEmpty(result.Data.Viewer.DisplayName, result.Data.Viewer.Name)
	if organization := result.Data.Organization.Name; organization != "" {
		name = strings.TrimSpace(name + " (" + organization + ")")
	}
	return domain.ProviderAccount{ID: result.Data.Viewer.ID, Name: name}, nil
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
