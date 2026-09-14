package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// Builtin returns the providers connections knows without a custom
// definition. Each call returns fresh values, so a caller may point one at a
// test server without affecting any other.
func Builtin() Registry {
	registry := Registry{}
	for _, provider := range []Provider{GitHub(), Google(), Linear(), Microsoft(), Slack()} {
		registry[provider.Name] = provider
	}
	return registry
}

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

func Microsoft() Provider {
	return Provider{
		Name:            "microsoft",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialOAuth2ClientCredentials},
		ClientCredentials: &ClientCredentials{
			TokenURL:        "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token",
			AdminConsentURL: "https://login.microsoftonline.com/organizations/v2.0/adminconsent",
			Scopes:          []string{"https://graph.microsoft.com/.default"},
			PermanentErrors: []string{"invalid_client", "invalid_request", "invalid_scope", "unauthorized_client"},
		},
		Scopes: ScopeRules{
			Ceiling: []string{
				"Calendars.Read", "Calendars.ReadWrite", "Channel.Create", "Channel.ReadBasic.All",
				"ChannelMessage.Read.All", "ChannelMessage.Send", "Chat.ReadWrite",
				"Files.Read", "Files.ReadWrite", "Files.ReadWrite.All",
				"Mail.Read", "Mail.ReadWrite", "Mail.Send",
				"OnlineMeetingRecording.Read.All", "OnlineMeetingTranscript.Read.All", "Presence.Read.All",
				"Sites.Manage.All", "Sites.Read.All", "Sites.ReadWrite.All",
				"Team.ReadBasic.All", "TeamMember.Read.All", "User.Read", "User.ReadBasic.All",
				"email", "offline_access", "openid", "profile",
			},
			Required: []string{"User.Read", "openid", "profile"},
			Implications: domain.Implications{
				"Calendars.Read":      {"Calendars.ReadWrite"},
				"Files.Read":          {"Files.ReadWrite", "Files.ReadWrite.All"},
				"Files.ReadWrite":     {"Files.ReadWrite.All"},
				"Mail.Read":           {"Mail.ReadWrite"},
				"Sites.Read.All":      {"Sites.Manage.All", "Sites.ReadWrite.All"},
				"Sites.ReadWrite.All": {"Sites.Manage.All"},
			},
		},
		OAuth2: &OAuth2{
			// The organizations endpoint excludes consumer Microsoft accounts.
			AuthorizationURL:    "https://login.microsoftonline.com/organizations/oauth2/v2.0/authorize",
			TokenURL:            "https://login.microsoftonline.com/organizations/oauth2/v2.0/token",
			APIURL:              "https://graph.microsoft.com/v1.0",
			PKCE:                true,
			SendScopeOnRefresh:  true,
			RequireRefreshToken: true,
			PermanentErrors:     []string{"interaction_required", "invalid_client", "invalid_grant", "invalid_scope", "unauthorized_client"},
			RequestScopes:       microsoftRequestScopes,
			GrantedScopes:       microsoftGrantedScopes,
			Identity:            microsoftIdentity,
		},
	}
}

// microsoftRequestScopes always asks for offline_access, without which
// Microsoft issues no refresh token and the connection dies within an hour.
func microsoftRequestScopes(scopes []string) []string {
	result := domain.NormalizeScopes(scopes)
	if !slices.Contains(result, "offline_access") {
		result = domain.NormalizeScopes(append(result, "offline_access"))
	}
	return result
}

// microsoftGrantedScopes keeps the requested OpenID and offline_access scopes,
// which Microsoft's token response describes only by the resource scopes.
func microsoftGrantedScopes(raw string, requested []string) []string {
	scopes := domain.NormalizeScopes(strings.Fields(raw))
	if len(scopes) == 0 {
		return domain.NormalizeScopes(requested)
	}
	for _, scope := range requested {
		switch scope {
		case "email", "offline_access", "openid", "profile":
			scopes = append(scopes, scope)
		}
	}
	return domain.NormalizeScopes(scopes)
}

func microsoftIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	var user struct {
		ID                string `json:"id"`
		DisplayName       string `json:"displayName"`
		UserPrincipalName string `json:"userPrincipalName"`
		Mail              string `json:"mail"`
	}
	endpoint := apiURL + "/me?$select=id,displayName,userPrincipalName,mail"
	if err := callJSON(ctx, client, http.MethodGet, endpoint, bearerCredential(grant.AccessToken), nil, nil, &user); err != nil {
		return domain.ProviderAccount{}, err
	}
	if user.ID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Microsoft account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: user.ID, Name: firstNonEmpty(user.UserPrincipalName, user.Mail, user.DisplayName)}, nil
}

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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
