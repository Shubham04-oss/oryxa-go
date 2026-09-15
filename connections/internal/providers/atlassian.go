package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// atlassianSitePattern is an Atlassian Cloud site, such as acme.atlassian.net.
var atlassianSitePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.atlassian\.net$`)

// Atlassian covers Jira and Confluence Cloud. An OAuth grant reaches every
// site the member lets the app use; callers list those sites, and the cloud IDs
// their API URLs need, at /oauth/token/accessible-resources. An API token works
// on the one site its app names as its domain.
func Atlassian() Provider {
	return Provider{
		Name:            "atlassian",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2, domain.CredentialBasic},
		Scopes: ScopeRules{
			// Atlassian recommends classic scopes where they exist. Jira
			// Software's boards, sprints and epics have only granular ones.
			Ceiling: []string{
				"manage:jira-configuration", "manage:jira-project", "manage:jira-webhook",
				"read:jira-user", "read:jira-work", "write:jira-work",
				"delete:comment:jira", "delete:issue:jira", "delete:project:jira",
				"read:comment:jira", "read:issue-meta:jira", "read:issue.property:jira", "read:issue:jira",
				"read:jql:jira", "read:project:jira", "read:user.property:jira", "read:user:jira",
				"validate:jql:jira", "write:comment:jira", "write:issue:jira", "write:project:jira",
				"delete:board-scope.admin:jira-software", "delete:sprint:jira-software",
				"read:board-scope.admin:jira-software", "read:board-scope:jira-software", "read:epic:jira-software",
				"read:issue:jira-software", "read:sprint:jira-software",
				"write:board-scope.admin:jira-software", "write:board-scope:jira-software", "write:epic:jira-software",
				"write:issue:jira-software", "write:sprint:jira-software",
				"manage:confluence-configuration", "read:confluence-content.all", "read:confluence-content.permission",
				"read:confluence-content.summary", "read:confluence-groups", "read:confluence-props",
				"read:confluence-space.summary", "read:confluence-user", "readonly:content.attachment:confluence",
				"search:confluence", "write:confluence-content", "write:confluence-file", "write:confluence-groups",
				"write:confluence-props", "write:confluence-space",
				"delete:attachment:confluence", "delete:comment:confluence", "delete:page:confluence", "delete:space:confluence",
				"read:attachment:confluence", "read:comment:confluence", "read:page:confluence", "read:space:confluence",
				"read:user.property:confluence", "read:user:confluence",
				"write:attachment:confluence", "write:comment:confluence", "write:page:confluence", "write:space:confluence",
				"write:user.property:confluence",
				"offline_access", "read:me",
			},
			// offline_access is what makes Atlassian issue a refresh token, and
			// the identity check reads the member at /me.
			Required: []string{"offline_access", "read:me"},
			Implications: domain.Implications{
				"read:confluence-content.summary": {"read:confluence-content.all"},
			},
		},
		OAuth2: &OAuth2{
			AuthorizationURL:  "https://auth.atlassian.com/authorize",
			TokenURL:          "https://auth.atlassian.com/oauth/token",
			APIURL:            "https://api.atlassian.com",
			Params:            map[string]string{"audience": "api.atlassian.com", "prompt": "consent"},
			JSONTokenRequests: true,
			// Refresh tokens rotate: each refresh returns a new one, and Refresh
			// keeps it.
			RequireRefreshToken: true,
			PermanentErrors:     []string{"access_denied", "invalid_client", "invalid_grant", "unauthorized_client"},
			GrantedScopes:       atlassianGrantedScopes,
			Identity:            atlassianIdentity,
		},
		Keys: map[domain.CredentialType]KeyAuth{
			// An API token, sent with its account's email as HTTP Basic
			// credentials, on the site the app names.
			domain.CredentialBasic: {APIURL: "https://{domain}", Identity: atlassianSiteUser},
		},
		Domain: &DomainRule{Pattern: atlassianSitePattern, Example: "acme.atlassian.net"},
	}
}

// atlassianGrantedScopes keeps a requested offline_access that the token
// response leaves out. Every grant must carry a refresh token, which shows
// offline access was granted.
func atlassianGrantedScopes(raw string, requested []string) []string {
	scopes := splitGrantedScopes(raw, requested)
	if slices.Contains(requested, "offline_access") && !slices.Contains(scopes, "offline_access") {
		scopes = append(scopes, "offline_access")
	}
	return domain.NormalizeScopes(scopes)
}

func atlassianIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	var user struct {
		AccountID string `json:"account_id"`
		Email     string `json:"email"`
		Name      string `json:"name"`
		Nickname  string `json:"nickname"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/me", bearerCredential(grant.AccessToken), nil, nil, &user); err != nil {
		return domain.ProviderAccount{}, err
	}
	if user.AccountID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Atlassian account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: user.AccountID, Name: firstNonEmpty(user.Email, user.Name, user.Nickname)}, nil
}

// atlassianSiteUser reads the account an API token belongs to on its site: from
// Jira, or from Confluence when the site has no Jira. The account ID is the
// same one an OAuth grant for the account reports.
func atlassianSiteUser(ctx context.Context, client *http.Client, apiURL string, apply func(*http.Request)) (domain.ProviderAccount, error) {
	var jira struct {
		AccountID    string `json:"accountId"`
		EmailAddress string `json:"emailAddress"`
		DisplayName  string `json:"displayName"`
	}
	err := callJSON(ctx, client, http.MethodGet, apiURL+"/rest/api/3/myself", apply, nil, nil, &jira)
	switch {
	case err == nil && jira.AccountID != "":
		return domain.ProviderAccount{ID: jira.AccountID, Name: firstNonEmpty(jira.EmailAddress, jira.DisplayName)}, nil
	case errors.Is(err, domain.ErrCredentialRejected):
		return domain.ProviderAccount{}, err
	}
	var confluence struct {
		AccountID   string `json:"accountId"`
		Email       string `json:"email"`
		PublicName  string `json:"publicName"`
		DisplayName string `json:"displayName"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/wiki/rest/api/user/current", apply, nil, nil, &confluence); err != nil {
		return domain.ProviderAccount{}, err
	}
	// Confluence answers a request it could not authenticate with an
	// anonymous user, which has no account ID.
	if confluence.AccountID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Atlassian did not recognize the API token", domain.ErrCredentialRejected)
	}
	return domain.ProviderAccount{ID: confluence.AccountID, Name: firstNonEmpty(confluence.Email, confluence.DisplayName, confluence.PublicName)}, nil
}
