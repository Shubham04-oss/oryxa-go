package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func GitHub() Provider {
	return Provider{
		Name:            "github",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialInstallation, domain.CredentialBearer},
		Scopes: ScopeRules{
			Ceiling:  []string{"admin:org_hook", "admin:repo_hook", "gist", "public_repo", "read:org", "read:user", "repo", "workflow"},
			Required: []string{"read:user"},
			Implications: domain.Implications{
				"admin:repo_hook": {"repo"},
				"public_repo":     {"repo"},
			},
		},
		OAuth2: &OAuth2{
			AuthorizationURL: "https://github.com/login/oauth/authorize",
			TokenURL:         "https://github.com/login/oauth/access_token",
			APIURL:           "https://api.github.com",
			PermanentErrors:  []string{"bad_refresh_token", "incorrect_client_credentials", "invalid_grant"},
			Identity:         githubIdentity,
			Revoke:           githubRevoke,
		},
		// Installations authorize the installing user through OAuth2 above;
		// GitHub confirms which installations that user may act as.
		Installation: &Installation{InstallURL: "https://github.com/apps/{slug}/installations/new", APIURL: "https://api.github.com"},
		Keys: map[domain.CredentialType]KeyAuth{
			// A personal access token, sent as OAuth2 grants are.
			domain.CredentialBearer: {APIURL: "https://api.github.com", Identity: githubUser},
		},
	}
}

func githubIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	return githubUser(ctx, client, apiURL, bearerCredential(grant.AccessToken))
}

// githubUser is githubIdentity generalized to any way of applying a
// credential, so a bearer personal access token (KeyAuth) can share it.
func githubUser(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	header := http.Header{"Accept": {"application/vnd.github+json"}, "X-GitHub-Api-Version": {"2022-11-28"}}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/user", apply, header, nil, &user); err != nil {
		return domain.ProviderAccount{}, err
	}
	if user.ID < 1 || user.Login == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: GitHub account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: strconv.FormatInt(user.ID, 10), Name: user.Login}, nil
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
