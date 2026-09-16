package providers

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

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
