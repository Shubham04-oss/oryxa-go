package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// zendeskSubdomainPattern is a Zendesk account's subdomain, such as acme in
// acme.zendesk.com: 3 to 63 letters, digits and hyphens. A host-mapped domain
// does not serve OAuth or the API.
var zendeskSubdomainPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

// Zendesk apps name their account's subdomain as their domain. An OAuth client
// Zendesk created after 2026-04-30 issues access tokens that expire, with a
// refresh token that works once; an older client may still issue tokens that
// never expire, until Zendesk requires expiry for every client on 2027-04-01.
// API tokens are left out: Zendesk deactivates every one on 2027-04-30.
func Zendesk() Provider {
	return Provider{
		Name:            "zendesk",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2},
		Scopes:          zendeskScopes(),
		OAuth2: &OAuth2{
			AuthorizationURL:  "https://{domain}.zendesk.com/oauth/authorizations/new",
			TokenURL:          "https://{domain}.zendesk.com/oauth/tokens",
			APIURL:            "https://{domain}.zendesk.com/api/v2",
			PKCE:              true,
			JSONTokenRequests: true,
			PermanentErrors:   []string{"invalid_client", "invalid_grant"},
			Identity:          zendeskIdentity,
			Revoke:            zendeskRevoke,
		},
		Domain: &DomainRule{Pattern: zendeskSubdomainPattern, Example: "acme"},
	}
}

// zendeskScopes are Zendesk's OAuth scopes. read covers reading any resource and
// write writing any; each resource scope is otherwise its own. The identity
// check needs users:read.
func zendeskScopes() ScopeRules {
	ceiling := []string{
		"account_settings:read", "account_settings:write", "ai_agents:chat", "any_channel:write", "apps:read", "apps:write",
		"auditlogs:read", "automations:read", "automations:write", "brands:read", "brands:write", "custom_objects:read",
		"custom_objects:write", "deletion_schedules:read", "deletion_schedules:write", "dynamic_content:read",
		"dynamic_content:write", "groups:read", "groups:write", "hc:read", "hc:write", "impersonate", "macros:read",
		"macros:write", "organizations:read", "organizations:write", "read", "requests:read", "requests:write",
		"satisfaction_ratings:read", "satisfaction_ratings:write", "security:read", "sla_policies:read", "sla_policies:write",
		"targets:read", "targets:write", "themes:read", "themes:write", "ticket_attachments:read", "ticket_attachments:write",
		"ticket_views:read", "ticket_views:write", "tickets:read", "tickets:write", "triggers:read", "triggers:write",
		"users:read", "users:write", "web_widget:write", "webhooks:read", "webhooks:write", "write", "zis:read", "zis:write",
	}
	implications := domain.Implications{}
	for _, scope := range ceiling {
		switch {
		case strings.HasSuffix(scope, ":read"):
			implications[scope] = []string{"read"}
		case strings.HasSuffix(scope, ":write"):
			implications[scope] = []string{"write"}
		}
	}
	return ScopeRules{Ceiling: ceiling, Required: []string{"users:read"}, Implications: implications}
}

// zendeskIdentity reads the user a grant belongs to. /users/me answers a
// request it could not authenticate with an anonymous user, which has no ID.
func zendeskIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	var result struct {
		User struct {
			ID    *int64 `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/users/me", bearerCredential(grant.AccessToken), nil, nil, &result); err != nil {
		return domain.ProviderAccount{}, err
	}
	if result.User.ID == nil || *result.User.ID <= 0 {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Zendesk did not recognize the credential", domain.ErrCredentialRejected)
	}
	return domain.ProviderAccount{ID: strconv.FormatInt(*result.User.ID, 10), Name: firstNonEmpty(result.User.Email, result.User.Name)}, nil
}

// zendeskRevoke deletes a grant's token, which Zendesk keeps with its refresh
// token, by calling the delete endpoint with the access token itself. An access
// token Zendesk refuses may only have expired, so a grant with a refresh token
// is refreshed and the new token deleted; one without is already gone, as is a
// grant whose refresh Zendesk refuses.
func zendeskRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	grant := request.Grant
	if grant.AccessToken != "" {
		deleted, err := zendeskDeleteToken(ctx, client, provider.APIURL, grant.AccessToken)
		switch {
		case err != nil:
			return false, err
		case deleted, grant.RefreshToken == "":
			return true, nil
		}
	}
	if grant.RefreshToken == "" {
		return false, nil
	}
	refreshed, err := Client{HTTP: client}.Refresh(ctx, provider, RefreshRequest{
		ClientID: request.ClientID, ClientSecret: request.ClientSecret, RefreshToken: grant.RefreshToken,
	})
	switch {
	case errors.Is(err, domain.ErrCredentialRejected):
		return true, nil
	case err != nil:
		return false, err
	}
	// A token Zendesk refuses as soon as it issued it is gone too.
	if _, err := zendeskDeleteToken(ctx, client, provider.APIURL, refreshed.AccessToken); err != nil {
		return false, err
	}
	return true, nil
}

// zendeskDeleteToken deletes the token an access token belongs to. It reports
// false, with no error, when Zendesk refuses the access token.
func zendeskDeleteToken(ctx context.Context, client *http.Client, apiURL, accessToken string) (bool, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodDelete, apiURL+"/oauth/tokens/current", nil)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request could not be built", domain.ErrProviderUnavailable)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := client.Do(httpRequest)
	if err != nil {
		return false, fmt.Errorf("%w: revoke request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	switch response.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return true, nil
	case http.StatusUnauthorized:
		return false, nil
	default:
		return false, fmt.Errorf("%w: Zendesk returned %d", domain.ErrProviderUnavailable, response.StatusCode)
	}
}
