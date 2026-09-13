// Package microsoft implements the OAuth 2.0 protocol boundary for Microsoft
// 365 connections. It deliberately returns only the grant and the small,
// durable identity projection needed by the integrations domain.
package microsoft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
)

const (
	// organizations intentionally excludes consumer Microsoft accounts. A
	// Microsoft 365 connection needs an Entra tenant so it can be identified and
	// isolated in downstream workflows.
	defaultAuthorizationURL = "https://login.microsoftonline.com/organizations/oauth2/v2.0/authorize"
	defaultTokenURL         = "https://login.microsoftonline.com/organizations/oauth2/v2.0/token"
	defaultGraphURL         = "https://graph.microsoft.com/v1.0"
	maxResponseBytes        = 1 << 20
	defaultClientTimeout    = 10 * time.Second
)

var defaultHTTPClient = &http.Client{Timeout: defaultClientTimeout}

// OAuth is safe to construct with no fields. The URL and clock fields exist to
// support deterministic tests and tenant-specific Microsoft identity hosts.
type OAuth struct {
	Client   *http.Client
	AuthURL  string
	TokenURL string
	GraphURL string
	Now      func() time.Time
}

func (o OAuth) AuthorizationURL(request oauthflow.AuthorizationRequest) (string, error) {
	endpoint := o.AuthURL
	if endpoint == "" {
		endpoint = defaultAuthorizationURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", errors.New("parse Microsoft authorization URL")
	}
	query := parsed.Query()
	query.Set("client_id", request.ClientID)
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("response_type", "code")
	query.Set("scope", strings.Join(authorizationScopes(request.Scopes), " "))
	query.Set("state", request.State)
	if request.PKCEChallenge != "" {
		query.Set("code_challenge", request.PKCEChallenge)
		query.Set("code_challenge_method", "S256")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (o OAuth) Exchange(ctx context.Context, request oauthflow.ExchangeRequest) (oauthflow.Grant, error) {
	requestedScopes := authorizationScopes(request.Scopes)
	values := url.Values{
		"client_id":     {request.ClientID},
		"client_secret": {request.ClientSecret},
		"code":          {request.Code},
		"redirect_uri":  {request.RedirectURI},
		"grant_type":    {"authorization_code"},
	}
	if request.PKCEVerifier != "" {
		values.Set("code_verifier", request.PKCEVerifier)
	}
	grant, err := o.token(ctx, values, requestedScopes, false)
	if err != nil {
		return oauthflow.Grant{}, err
	}
	// offline_access is always included in authorization requests so durable
	// connections do not silently become unusable once their access token
	// expires. Microsoft issues the refresh token only when that request was
	// accepted.
	if containsScope(requestedScopes, "offline_access") && grant.RefreshToken == "" {
		return oauthflow.Grant{}, errors.New("Microsoft OAuth exchange did not issue a refresh token")
	}
	return grant, nil
}

// Refresh implements oauthflow.Refresher. Microsoft rotates refresh tokens, so
// callers must persist a returned refresh token when one is supplied.
func (o OAuth) Refresh(ctx context.Context, request oauthflow.RefreshRequest) (oauthflow.Grant, error) {
	values := url.Values{
		"client_id":     {request.ClientID},
		"client_secret": {request.ClientSecret},
		"refresh_token": {request.RefreshToken},
		"grant_type":    {"refresh_token"},
	}
	// Sending the reviewed connection scopes prevents a refresh from silently
	// broadening its authority. offline_access is protocol-only and is not
	// persisted as a business capability by oauthflow.
	if scopes := requestScopes(request.Scopes); len(scopes) > 0 {
		values.Set("scope", strings.Join(scopes, " "))
	}
	return o.token(ctx, values, request.Scopes, true)
}

func (o OAuth) token(ctx context.Context, values url.Values, requestedScopes []string, refreshing bool) (oauthflow.Grant, error) {
	endpoint := o.TokenURL
	if endpoint == "" {
		endpoint = defaultTokenURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return oauthflow.Grant{}, errors.New("create Microsoft token request")
	}
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	request.Header.Set("accept", "application/json")
	response, err := o.client().Do(request)
	if err != nil {
		return oauthflow.Grant{}, errors.New("request Microsoft token")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return oauthflow.Grant{}, errors.New("Microsoft token response could not be read")
	}
	var token tokenResponse
	if json.Unmarshal(body, &token) != nil {
		return oauthflow.Grant{}, errors.New("Microsoft token endpoint returned invalid JSON")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || token.Error != "" || token.AccessToken == "" {
		code := safeProviderCode(token.Error)
		if refreshing {
			return oauthflow.Grant{}, oauthflow.RefreshError{Code: code, Permanent: permanentRefreshCode(code)}
		}
		return oauthflow.Grant{}, fmt.Errorf("Microsoft rejected OAuth exchange: %s", code)
	}
	grant := oauthflow.Grant{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		Scopes:       normalizedScopes(token.Scope, requestedScopes),
	}
	if token.ExpiresIn > 0 {
		grant.Expiry = o.now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	return grant, nil
}

// ResolveIdentity reads identity metadata from Graph rather than trusting a
// token claim. The tenant object gives us a stable tenant identifier and a
// human-readable label without retaining the original token response.
func (o OAuth) ResolveIdentity(ctx context.Context, grant oauthflow.Grant) (domain.ProviderIdentity, error) {
	base := strings.TrimRight(o.GraphURL, "/")
	if base == "" {
		base = defaultGraphURL
	}
	user, err := o.user(ctx, base, grant.AccessToken)
	if err != nil {
		return domain.ProviderIdentity{}, err
	}
	tenant, err := o.tenant(ctx, base, grant.AccessToken)
	if err != nil {
		return domain.ProviderIdentity{}, err
	}

	accountID := user.UserPrincipalName
	if accountID == "" {
		accountID = user.Mail
	}
	if accountID == "" {
		accountID = user.ID
	}
	accountName := user.DisplayName
	if accountName == "" {
		accountName = accountID
	}
	tenantName := tenant.DisplayName
	if tenantName == "" {
		tenantName = tenant.ID
	}
	if !safeIdentity(user.ID) || !safeIdentity(accountID) || !safeIdentity(accountName) || !safeIdentity(tenant.ID) || !safeIdentity(tenantName) {
		return domain.ProviderIdentity{}, errors.New("Microsoft identity response is incomplete")
	}
	return domain.ProviderIdentity{
		SubjectID:   user.ID,
		AccountID:   accountID,
		AccountName: accountName,
		TenantID:    tenant.ID,
		TenantName:  tenantName,
	}, nil
}

func (o OAuth) user(ctx context.Context, base, accessToken string) (graphUser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/me?$select=id,displayName,userPrincipalName,mail", nil)
	if err != nil {
		return graphUser{}, errors.New("create Microsoft identity request")
	}
	request.Header.Set("authorization", "Bearer "+accessToken)
	request.Header.Set("accept", "application/json")
	response, err := o.client().Do(request)
	if err != nil {
		return graphUser{}, errors.New("request Microsoft identity")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return graphUser{}, errors.New("Microsoft identity request failed")
	}
	var user graphUser
	if json.Unmarshal(body, &user) != nil || !safeIdentity(user.ID) {
		return graphUser{}, errors.New("Microsoft identity response is incomplete")
	}
	return user, nil
}

func (o OAuth) tenant(ctx context.Context, base, accessToken string) (graphOrganization, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/organization?$select=id,displayName", nil)
	if err != nil {
		return graphOrganization{}, errors.New("create Microsoft tenant request")
	}
	request.Header.Set("authorization", "Bearer "+accessToken)
	request.Header.Set("accept", "application/json")
	response, err := o.client().Do(request)
	if err != nil {
		return graphOrganization{}, errors.New("request Microsoft tenant")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return graphOrganization{}, errors.New("Microsoft tenant request failed")
	}
	var organizations struct {
		Value []graphOrganization `json:"value"`
	}
	if json.Unmarshal(body, &organizations) != nil || len(organizations.Value) != 1 || !safeIdentity(organizations.Value[0].ID) {
		return graphOrganization{}, errors.New("Microsoft tenant response is incomplete")
	}
	return organizations.Value[0], nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
}

type graphUser struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	UserPrincipalName string `json:"userPrincipalName"`
	Mail              string `json:"mail"`
}

type graphOrganization struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

func (o OAuth) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return defaultHTTPClient
}

func (o OAuth) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func authorizationScopes(scopes []string) []string {
	result := requestScopes(scopes)
	for _, scope := range result {
		if scope == "offline_access" {
			return result
		}
	}
	return append(result, "offline_access")
}

func requestScopes(scopes []string) []string {
	set := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			set[scope] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for scope := range set {
		result = append(result, scope)
	}
	slices.Sort(result)
	return result
}

func normalizedScopes(value string, requested []string) []string {
	scopes := requestScopes(strings.Fields(value))
	// RFC 6749 permits omitting scope when it is unchanged from the request.
	// oauthflow will validate the reviewed scopes against this projection.
	if len(scopes) == 0 {
		return requestScopes(requested)
	}
	// The Microsoft token response describes the access token's resource
	// permissions and commonly omits OIDC/protocol scopes. They are still part
	// of the approved authorization request and oauthflow must retain them for
	// its state-to-grant validation. Resource permissions remain response-led.
	for _, scope := range requestScopes(requested) {
		if isProtocolScope(scope) {
			scopes = append(scopes, scope)
		}
	}
	return requestScopes(scopes)
}

func isProtocolScope(scope string) bool {
	switch scope {
	case "openid", "profile", "email", "offline_access":
		return true
	default:
		return false
	}
}

func containsScope(scopes []string, target string) bool {
	for _, scope := range scopes {
		if scope == target {
			return true
		}
	}
	return false
}

func permanentRefreshCode(code string) bool {
	switch code {
	case "invalid_grant", "invalid_client", "invalid_scope", "interaction_required", "unauthorized_client":
		return true
	default:
		return false
	}
}

func safeProviderCode(value string) string {
	if value == "" || len(value) > 80 {
		return "oauth_exchange_failed"
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '_' && character != '-' {
			return "oauth_exchange_failed"
		}
	}
	return value
}

func safeIdentity(value string) bool {
	return value != "" && len(value) <= 1024 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\t")
}
