package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// salesforceDomainPattern is a host Salesforce users log in at:
// login.salesforce.com for production orgs, test.salesforce.com for sandboxes,
// or an org's My Domain, such as acme.my.salesforce.com, or
// acme--uat.sandbox.my.salesforce.com with a word for the kind of org.
var salesforceDomainPattern = regexp.MustCompile(`^(?:(?:login|test)\.salesforce\.com|[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z]{1,20})?\.my\.salesforce\.com)$`)

// Salesforce apps log in at their domain, login.salesforce.com unless the app
// names a sandbox or My Domain host. Callers call the org's API at the
// instance_url kept with the grant. Access tokens last as long as the org's
// session timeout, which Salesforce does not report, so a token is refreshed
// once the shortest timeout could have ended it.
func Salesforce() Provider {
	return Provider{
		Name:            "salesforce",
		CredentialTypes: []domain.CredentialType{domain.CredentialOAuth2},
		Scopes:          salesforceScopes(),
		OAuth2: &OAuth2{
			AuthorizationURL:    "https://{domain}/services/oauth2/authorize",
			TokenURL:            "https://{domain}/services/oauth2/token",
			APIURL:              "https://{domain}",
			RevokeURL:           "https://{domain}/services/oauth2/revoke",
			PKCE:                true,
			RequireRefreshToken: true,
			// Session timeouts run from 15 minutes to 24 hours.
			Lifetime: 15 * time.Minute,
			// The user or org being inactive, or the app no longer approved for
			// the user, also needs someone to act before the grant works again.
			PermanentErrors: []string{"inactive_org", "inactive_user", "invalid_app_access", "invalid_client", "invalid_client_id", "invalid_grant"},
			Metadata:        []string{"instance_url"},
			Identity:        salesforceIdentity,
			Revoke:          salesforceRevoke,
		},
		Domain: &DomainRule{Pattern: salesforceDomainPattern, Example: "acme.my.salesforce.com", Default: "login.salesforce.com"},
	}
}

// salesforceScopes are Salesforce's OAuth scopes. id, profile, email, address
// and phone are one scope under five names, as are refresh_token and
// offline_access; full covers every scope but a refresh token. The identity
// check needs id, and a grant needs a refresh token.
func salesforceScopes() ScopeRules {
	identity := []string{"address", "email", "id", "phone", "profile"}
	refresh := []string{"offline_access", "refresh_token"}
	others := []string{
		"api", "cdp_api", "cdp_ingest_api", "cdp_profile_api", "cdp_query_api", "chatbot_api", "chatter_api", "content",
		"custom_permissions", "eclair_api", "lightning", "openid", "pardot_api", "sfap_api", "visualforce", "wave_api", "web",
	}
	implications := domain.Implications{}
	for _, names := range [][]string{identity, refresh} {
		for _, scope := range names {
			implications[scope] = slices.DeleteFunc(slices.Clone(names), func(name string) bool { return name == scope })
		}
	}
	for _, scope := range slices.Concat(identity, others) {
		implications[scope] = append(implications[scope], "full")
	}
	return ScopeRules{
		Ceiling:      domain.NormalizeScopes(slices.Concat(identity, refresh, others, []string{"full"})),
		Required:     []string{"id", "refresh_token"},
		Implications: implications,
	}
}

// salesforceIdentity reads the user a grant belongs to from the UserInfo
// endpoint at the app's domain. The account is the org and the user in it.
func salesforceIdentity(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error) {
	var user struct {
		UserID            string `json:"user_id"`
		OrganizationID    string `json:"organization_id"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
		Name              string `json:"name"`
	}
	if err := callJSON(ctx, client, http.MethodGet, apiURL+"/services/oauth2/userinfo", bearerCredential(grant.AccessToken), nil, nil, &user); err != nil {
		return domain.ProviderAccount{}, err
	}
	if user.UserID == "" || user.OrganizationID == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: Salesforce account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: user.OrganizationID + ":" + user.UserID, Name: firstNonEmpty(user.PreferredUsername, user.Email, user.Name)}, nil
}

// salesforceRevoke revokes a grant at the app's domain. Revoking the refresh
// token also revokes the access tokens issued from it, so it goes first. A
// token Salesforce reports as invalid_token is already gone.
func salesforceRevoke(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error) {
	token := firstNonEmpty(request.Grant.RefreshToken, request.Grant.AccessToken)
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
	var failure struct {
		Error string `json:"error"`
	}
	if response.StatusCode == http.StatusBadRequest && json.Unmarshal(payload, &failure) == nil && failure.Error == "invalid_token" {
		return true, nil
	}
	return false, fmt.Errorf("%w: Salesforce returned %d", domain.ErrProviderUnavailable, response.StatusCode)
}
