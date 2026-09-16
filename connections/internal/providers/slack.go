package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

var slackBotScopes = []string{
	"channels:history", "channels:join", "channels:manage", "channels:read", "channels:write.invites",
	"chat:write", "chat:write.customize", "emoji:read", "files:read", "files:write",
	"groups:history", "groups:read", "groups:write", "groups:write.invites",
	"im:history", "im:read", "im:write", "links:read", "links:write",
	"mpim:history", "mpim:read", "mpim:write", "reactions:read", "reactions:write",
	"usergroups:read", "usergroups:write", "users.profile:read", "users:read", "users:read.email",
}

// slackUserScopes are requested on every Slack connect, so each connection
// also holds the consenting member's own user token.
var slackUserScopes = []string{
	"search:read", "users.profile:write", "reactions:read", "reactions:write",
	"im:history", "stars:read", "channels:write", "groups:write", "im:write",
	"mpim:write", "channels:write.invites", "groups:write.invites",
	"channels:history", "groups:history", "chat:write", "users:read",
	"usergroups:write",
}

func Slack() Provider {
	return Provider{
		Name:            "slack",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2},
		Scopes: ScopeRules{
			Ceiling:  slices.Clone(slackBotScopes),
			Required: []string{"channels:history", "groups:history"},
		},
		OAuth2: &OAuth2{
			AuthorizationURL: "https://slack.com/oauth/v2/authorize",
			TokenURL:         "https://slack.com/api/oauth.v2.access",
			APIURL:           "https://slack.com/api",
			ScopeSeparator:   ",",
			UserScopes:       slices.Clone(slackUserScopes),
			PermanentErrors:  []string{"bad_client_secret", "invalid_client_id", "invalid_refresh_token", "token_expired", "token_revoked"},
			DecodeToken:      decodeSlackToken,
			Identity:         slackIdentity,
			Revoke:           slackRevoke,
		},
	}
}

// decodeSlackToken reads oauth.v2.access. Slack answers errors with HTTP 200
// and ok=false, and puts the member's user token under authed_user next to
// the bot token.
func decodeSlackToken(body []byte) (TokenResponse, error) {
	var token struct {
		OK           bool        `json:"ok"`
		Error        string      `json:"error"`
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		TokenType    string      `json:"token_type"`
		Scope        string      `json:"scope"`
		ExpiresIn    json.Number `json:"expires_in"`
		AuthedUser   struct {
			AccessToken string `json:"access_token"`
			Scope       string `json:"scope"`
		} `json:"authed_user"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return TokenResponse{}, err
	}
	if !token.OK {
		return TokenResponse{Error: firstNonEmpty(token.Error, "oauth_request_rejected")}, nil
	}
	seconds, err := expiresIn(token.ExpiresIn)
	if err != nil {
		return TokenResponse{}, err
	}
	decoded := TokenResponse{
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: token.TokenType,
		Scope: token.Scope + "," + token.AuthedUser.Scope, ExpiresIn: seconds,
	}
	if token.AuthedUser.AccessToken != "" {
		decoded.Tokens = map[string]string{"user": token.AuthedUser.AccessToken}
	}
	return decoded, nil
}

// slackIdentity asks auth.test who the member's user token belongs to. The
// account is the workspace and user together, because Slack user IDs are
// only guaranteed unique within a workspace.
func slackIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	userToken := grant.Tokens["user"]
	if userToken == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Slack grant has no user token", domain.ErrCredentialRejected)
	}
	var result struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Team   string `json:"team"`
		User   string `json:"user"`
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
	}
	if err := callJSON(ctx, client, http.MethodPost, apiURL+"/auth.test", bearerCredential(userToken), nil, nil, &result); err != nil {
		return domain.ProviderAccount{}, err
	}
	if !result.OK {
		switch code := safeCode(result.Error); code {
		case "account_inactive", "invalid_auth", "not_authed", "token_expired", "token_revoked":
			return domain.ProviderAccount{}, fmt.Errorf("%w: %s", domain.ErrCredentialRejected, code)
		default:
			return domain.ProviderAccount{}, fmt.Errorf("%w: %s", domain.ErrProviderUnavailable, code)
		}
	}
	if result.TeamID == "" || result.UserID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Slack account is incomplete", domain.ErrProviderUnavailable)
	}
	name := firstNonEmpty(result.User, result.UserID)
	if result.Team != "" {
		name += " (" + result.Team + ")"
	}
	return domain.ProviderAccount{ID: result.TeamID + ":" + result.UserID, Name: name}, nil
}

// slackRevoke revokes only the connecting member's own user token. It never
// revokes grant.AccessToken, the bot token: Slack issues one bot token per
// workspace and app, shared by every member who connected that workspace, so
// revoking it on one member's disconnect would break the others. A grant
// without a user token has nothing this connection alone may revoke.
func slackRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	userToken := request.Grant.Tokens["user"]
	if userToken == "" {
		return false, nil
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(provider.APIURL, "/")+"/auth.revoke", nil)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request could not be built", domain.ErrProviderUnavailable)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+userToken)
	response, err := client.Do(httpRequest)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(payload) > maxResponseBytes {
		return false, fmt.Errorf("%w: revoke response could not be read", domain.ErrProviderUnavailable)
	}
	var result struct {
		OK      bool   `json:"ok"`
		Revoked bool   `json:"revoked"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(payload, &result) != nil {
		return false, fmt.Errorf("%w: revoke response is not valid", domain.ErrProviderUnavailable)
	}
	if result.OK {
		return result.Revoked, nil
	}
	switch safeCode(result.Error) {
	case "invalid_auth", "token_revoked", "account_inactive", "not_authed":
		return true, nil
	default:
		return false, fmt.Errorf("%w: Slack returned %s", domain.ErrProviderUnavailable, safeCode(result.Error))
	}
}
