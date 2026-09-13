package google

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
	defaultAuthorizationURL = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultTokenURL         = "https://oauth2.googleapis.com/token"
	defaultUserInfoURL      = "https://openidconnect.googleapis.com/v1/userinfo"
	maxResponseBytes        = 1 << 20
)

type OAuth struct {
	Client      *http.Client
	AuthURL     string
	TokenURL    string
	UserInfoURL string
	Now         func() time.Time
}

func (o OAuth) AuthorizationURL(request oauthflow.AuthorizationRequest) (string, error) {
	endpoint := o.AuthURL
	if endpoint == "" {
		endpoint = defaultAuthorizationURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse Google authorization URL: %w", err)
	}
	query := parsed.Query()
	query.Set("client_id", request.ClientID)
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("response_type", "code")
	query.Set("scope", strings.Join(request.Scopes, " "))
	query.Set("state", request.State)
	query.Set("access_type", "offline")
	query.Set("include_granted_scopes", "true")
	query.Set("prompt", "consent")
	if request.PKCEChallenge != "" {
		query.Set("code_challenge", request.PKCEChallenge)
		query.Set("code_challenge_method", "S256")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (o OAuth) Exchange(ctx context.Context, request oauthflow.ExchangeRequest) (oauthflow.Grant, error) {
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
	return o.token(ctx, values, request.Scopes, false)
}

func (o OAuth) Refresh(ctx context.Context, request oauthflow.RefreshRequest) (oauthflow.Grant, error) {
	return o.token(ctx, url.Values{
		"client_id":     {request.ClientID},
		"client_secret": {request.ClientSecret},
		"refresh_token": {request.RefreshToken},
		"grant_type":    {"refresh_token"},
	}, request.Scopes, true)
}

func (o OAuth) token(ctx context.Context, values url.Values, requestedScopes []string, refreshing bool) (oauthflow.Grant, error) {
	endpoint := o.TokenURL
	if endpoint == "" {
		endpoint = defaultTokenURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return oauthflow.Grant{}, fmt.Errorf("create Google token request: %w", err)
	}
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	response, err := o.client().Do(request)
	if err != nil {
		return oauthflow.Grant{}, fmt.Errorf("request Google token: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return oauthflow.Grant{}, errors.New("Google token response could not be read")
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
	}
	if json.Unmarshal(body, &token) != nil {
		return oauthflow.Grant{}, errors.New("Google token endpoint returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || token.Error != "" || token.AccessToken == "" {
		code := safeProviderCode(token.Error)
		if refreshing {
			return oauthflow.Grant{}, oauthflow.RefreshError{Code: code, Permanent: code == "invalid_grant" || code == "invalid_client"}
		}
		return oauthflow.Grant{}, fmt.Errorf("Google rejected OAuth exchange: %s", code)
	}
	grant := oauthflow.Grant{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		TokenType: token.TokenType, Scopes: normalizedScopes(token.Scope, requestedScopes)}
	if token.ExpiresIn > 0 {
		grant.Expiry = o.now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	return grant, nil
}

func (o OAuth) ResolveIdentity(ctx context.Context, grant oauthflow.Grant) (domain.ProviderIdentity, error) {
	endpoint := o.UserInfoURL
	if endpoint == "" {
		endpoint = defaultUserInfoURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return domain.ProviderIdentity{}, fmt.Errorf("create Google identity request: %w", err)
	}
	request.Header.Set("authorization", "Bearer "+grant.AccessToken)
	response, err := o.client().Do(request)
	if err != nil {
		return domain.ProviderIdentity{}, fmt.Errorf("request Google identity: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes || response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.ProviderIdentity{}, errors.New("Google identity request failed")
	}
	var user struct {
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		HostedDomain  string `json:"hd"`
	}
	if json.Unmarshal(body, &user) != nil || user.Subject == "" || user.Email == "" || !user.EmailVerified {
		return domain.ProviderIdentity{}, errors.New("Google identity response is incomplete")
	}
	name := user.Name
	if name == "" {
		name = user.Email
	}
	return domain.ProviderIdentity{SubjectID: user.Subject, AccountID: user.Email,
		AccountName: name, TenantID: user.HostedDomain, TenantName: user.HostedDomain}, nil
}

func (o OAuth) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return http.DefaultClient
}

func (o OAuth) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func splitScopes(value string) []string {
	set := map[string]struct{}{}
	for _, scope := range strings.Fields(value) {
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
	scopes := splitScopes(value)
	// RFC 6749 allows the token endpoint to omit scope when it is identical to
	// what was requested. Preserve that reviewed request instead of creating a
	// connection that can never satisfy its own catalog policy.
	if len(scopes) == 0 {
		scopes = append([]string(nil), requested...)
	}
	set := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		switch scope {
		case "https://www.googleapis.com/auth/userinfo.email":
			scope = "email"
		case "https://www.googleapis.com/auth/userinfo.profile":
			scope = "profile"
		}
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
