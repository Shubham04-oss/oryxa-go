package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// notionVersion is the Notion API version connections calls: the version the
// Notion client in internal/providerapi is generated from.
const notionVersion = "2025-09-03"

// Notion has no OAuth scopes: an integration's capabilities are set in Notion,
// and the member picks the pages it may reach while consenting. A connection is
// the integration's bot in the member's workspace.
func Notion() Provider {
	return Provider{
		Name:            "notion",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialBearer},
		OAuth2: &OAuth2{
			AuthorizationURL:  "https://api.notion.com/v1/oauth/authorize",
			TokenURL:          "https://api.notion.com/v1/oauth/token",
			APIURL:            "https://api.notion.com/v1",
			RevokeURL:         "https://api.notion.com/v1/oauth/revoke",
			Params:            map[string]string{"owner": "user"},
			TokenHeaders:      map[string]string{"Notion-Version": notionVersion},
			JSONTokenRequests: true,
			ClientSecretBasic: true,
			PermanentErrors:   []string{"invalid_client", "invalid_grant", "unauthorized_client"},
			DecodeToken:       decodeNotionToken,
			Metadata:          []string{"bot_id", "duplicated_template_id", "workspace_id", "workspace_name"},
			Identity:          notionIdentity,
			Revoke:            notionRevoke,
		},
		Keys: map[domain.CredentialType]KeyAuth{
			// An internal integration's token, sent as OAuth2 grants are.
			domain.CredentialBearer: {APIURL: "https://api.notion.com/v1", Identity: notionBot},
		},
	}
}

// decodeNotionToken reads Notion's token response, which reports an error's
// code in code rather than error.
func decodeNotionToken(body []byte) (TokenResponse, error) {
	token, err := decodeStandardToken(body)
	if err != nil || token.Error != "" {
		return token, err
	}
	var failure struct {
		Object string `json:"object"`
		Code   string `json:"code"`
	}
	if json.Unmarshal(body, &failure) == nil && failure.Object == "error" {
		token.Error = firstNonEmpty(failure.Code, "oauth_request_rejected")
	}
	return token, nil
}

// notionIdentity reads the bot a grant belongs to. A bot a member owns has no
// workspace name of its own, so the token response's workspace_name names it.
func notionIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	account, err := notionBot(ctx, client, apiURL, bearerCredential(grant.AccessToken))
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	if name := grant.Metadata["workspace_name"]; name != "" {
		account.Name = name
	}
	return account, nil
}

// notionBot reads the bot behind a token at /users/me. Its ID names the
// connection: one integration's bot in one workspace.
func notionBot(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error) {
	var user struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Name string `json:"name"`
		Bot  struct {
			WorkspaceName string `json:"workspace_name"`
		} `json:"bot"`
	}
	header := http.Header{"Notion-Version": {notionVersion}}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/users/me", apply, header, nil, &user); err != nil {
		return domain.ProviderAccount{}, err
	}
	if user.ID == "" || user.Type != "bot" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Notion account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: user.ID, Name: firstNonEmpty(user.Bot.WorkspaceName, user.Name)}, nil
}

// notionRevoke revokes a grant's access token. Notion documents no answer for a
// token that is already gone; one it reports as invalid_grant is not a failure.
func notionRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	if request.Grant.AccessToken == "" {
		return false, nil
	}
	body, err := json.Marshal(map[string]string{"token": request.Grant.AccessToken})
	if err != nil {
		return false, fmt.Errorf("%w: revoke request could not be built", domain.ErrProviderUnavailable)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.RevokeURL, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("%w: revoke request could not be built", domain.ErrProviderUnavailable)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Notion-Version", notionVersion)
	setClientBasicAuth(httpRequest, request.ClientID, request.ClientSecret)
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
		Code string `json:"code"`
	}
	if response.StatusCode == http.StatusBadRequest && json.Unmarshal(payload, &failure) == nil && failure.Code == "invalid_grant" {
		return true, nil
	}
	return false, fmt.Errorf("%w: Notion returned %d", domain.ErrProviderUnavailable, response.StatusCode)
}
