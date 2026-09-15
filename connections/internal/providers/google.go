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

const googleScopePrefix = "https://www.googleapis.com/auth/"

func Google() Provider {
	scope := func(name string) string { return googleScopePrefix + name }
	return Provider{
		Name:            "google",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2},
		Scopes: ScopeRules{
			Ceiling: []string{
				"email", "openid", "profile",
				scope("calendar.events"), scope("calendar.readonly"),
				scope("documents"), scope("documents.readonly"),
				scope("drive"), scope("drive.metadata.readonly"), scope("drive.readonly"),
				scope("gmail.compose"), scope("gmail.metadata"), scope("gmail.modify"), scope("gmail.readonly"), scope("gmail.send"),
				scope("spreadsheets"), scope("spreadsheets.readonly"),
			},
			Required: []string{"email", "openid", "profile"},
			Implications: domain.Implications{
				scope("calendar.readonly"):       {scope("calendar.events")},
				scope("documents.readonly"):      {scope("documents")},
				scope("drive.metadata.readonly"): {scope("drive"), scope("drive.readonly")},
				scope("drive.readonly"):          {scope("drive")},
				scope("gmail.compose"):           {scope("gmail.modify")},
				scope("gmail.metadata"):          {scope("gmail.modify"), scope("gmail.readonly")},
				scope("gmail.readonly"):          {scope("gmail.modify")},
				scope("gmail.send"):              {scope("gmail.compose"), scope("gmail.modify")},
				scope("spreadsheets.readonly"):   {scope("spreadsheets")},
			},
		},
		OAuth2: &OAuth2{
			AuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:         "https://oauth2.googleapis.com/token",
			APIURL:           "https://openidconnect.googleapis.com/v1",
			// Offline access with forced consent is what makes Google issue a
			// refresh token on every connect, including a reconnect.
			Params:              map[string]string{"access_type": "offline", "include_granted_scopes": "true", "prompt": "consent"},
			PKCE:                true,
			RequireRefreshToken: true,
			PermanentErrors:     []string{"invalid_client", "invalid_grant", "unauthorized_client"},
			GrantedScopes:       googleGrantedScopes,
			Identity:            googleIdentity,
			// Google's revoke endpoint is a different host than APIURL, so it
			// needs its own RevokeURL rather than being derived from APIURL.
			RevokeURL: "https://oauth2.googleapis.com/revoke",
			Revoke:    googleRevoke,
		},
	}
}

// googleGrantedScopes maps the userinfo aliases Google reports back to the
// OpenID scope names that were requested.
func googleGrantedScopes(raw string, requested []string) []string {
	scopes := splitGrantedScopes(raw, requested)
	for index, scope := range scopes {
		switch scope {
		case googleScopePrefix + "userinfo.email":
			scopes[index] = "email"
		case googleScopePrefix + "userinfo.profile":
			scopes[index] = "profile"
		}
	}
	return domain.NormalizeScopes(scopes)
}

func googleIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	var user struct {
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/userinfo", bearerCredential(grant.AccessToken), nil, nil, &user); err != nil {
		return domain.ProviderAccount{}, err
	}
	if user.Subject == "" || user.Email == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Google account is incomplete", domain.ErrProviderUnavailable)
	}
	if !user.EmailVerified {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Google account email is not verified", domain.ErrCredentialRejected)
	}
	return domain.ProviderAccount{ID: user.Subject, Name: user.Email}, nil
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
