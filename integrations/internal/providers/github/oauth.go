package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
)

const (
	defaultAuthorizationURL = "https://github.com/login/oauth/authorize"
	defaultTokenURL         = "https://github.com/login/oauth/access_token"
	defaultAPIURL           = "https://api.github.com"
	maxResponseBytes        = 1 << 20
)

type OAuth struct {
	Client   *http.Client
	AuthURL  string
	TokenURL string
	APIURL   string
	Now      func() time.Time
}

func (o OAuth) AuthorizationURL(request oauthflow.AuthorizationRequest) (string, error) {
	endpoint := o.AuthURL
	if endpoint == "" {
		endpoint = defaultAuthorizationURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse GitHub authorization URL: %w", err)
	}
	query := parsed.Query()
	query.Set("client_id", request.ClientID)
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("scope", strings.Join(request.Scopes, " "))
	query.Set("state", request.State)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (o OAuth) Exchange(ctx context.Context, request oauthflow.ExchangeRequest) (oauthflow.Grant, error) {
	return o.token(ctx, url.Values{
		"client_id":     {request.ClientID},
		"client_secret": {request.ClientSecret},
		"code":          {request.Code},
		"redirect_uri":  {request.RedirectURI},
	}, false)
}

func (o OAuth) Refresh(ctx context.Context, request oauthflow.RefreshRequest) (oauthflow.Grant, error) {
	return o.token(ctx, url.Values{
		"client_id":     {request.ClientID},
		"client_secret": {request.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {request.RefreshToken},
	}, true)
}

func (o OAuth) token(ctx context.Context, values url.Values, refreshing bool) (oauthflow.Grant, error) {
	endpoint := o.TokenURL
	if endpoint == "" {
		endpoint = defaultTokenURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return oauthflow.Grant{}, fmt.Errorf("create GitHub token request: %w", err)
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	response, err := o.client().Do(request)
	if err != nil {
		return oauthflow.Grant{}, fmt.Errorf("request GitHub token: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return oauthflow.Grant{}, errors.New("GitHub token response could not be read")
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
		return oauthflow.Grant{}, errors.New("GitHub token endpoint returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || token.Error != "" || token.AccessToken == "" {
		code := safeProviderCode(token.Error)
		if refreshing {
			return oauthflow.Grant{}, oauthflow.RefreshError{Code: code, Permanent: code == "bad_refresh_token" || code == "invalid_grant"}
		}
		return oauthflow.Grant{}, fmt.Errorf("GitHub rejected OAuth exchange: %s", code)
	}
	grant := oauthflow.Grant{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		TokenType: token.TokenType, Scopes: splitScopes(token.Scope)}
	if token.ExpiresIn > 0 {
		grant.Expiry = o.now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	return grant, nil
}

func (o OAuth) ResolveIdentity(ctx context.Context, grant oauthflow.Grant) (domain.ProviderIdentity, error) {
	base := strings.TrimRight(o.APIURL, "/")
	if base == "" {
		base = defaultAPIURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return domain.ProviderIdentity{}, fmt.Errorf("create GitHub identity request: %w", err)
	}
	request.Header.Set("accept", "application/vnd.github+json")
	request.Header.Set("authorization", "Bearer "+grant.AccessToken)
	request.Header.Set("x-github-api-version", "2022-11-28")
	response, err := o.client().Do(request)
	if err != nil {
		return domain.ProviderIdentity{}, fmt.Errorf("request GitHub identity: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes || response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.ProviderIdentity{}, errors.New("GitHub identity request failed")
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if json.Unmarshal(body, &user) != nil || user.ID < 1 || user.Login == "" {
		return domain.ProviderIdentity{}, errors.New("GitHub identity response is incomplete")
	}
	name := user.Name
	if name == "" {
		name = user.Login
	}
	return domain.ProviderIdentity{SubjectID: strconv.FormatInt(user.ID, 10),
		AccountID: user.Login, AccountName: name}, nil
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
	for _, scope := range strings.FieldsFunc(value, func(character rune) bool { return character == ',' || character == ' ' }) {
		if scope = strings.TrimSpace(scope); scope != "" {
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
