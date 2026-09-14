package slack

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

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
)

const (
	defaultAuthorizationURL = "https://slack.com/oauth/v2/authorize"
	defaultTokenURL         = "https://slack.com/api/oauth.v2.access"
	maxTokenResponseBytes   = 1 << 20
)

type OAuth struct {
	Client   *http.Client
	AuthURL  string
	TokenURL string
}

func (o OAuth) AuthorizationURL(request oauthflow.AuthorizationRequest) (string, error) {
	endpoint := o.AuthURL
	if endpoint == "" {
		endpoint = defaultAuthorizationURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse Slack authorization URL: %w", err)
	}
	query := parsed.Query()
	query.Set("client_id", request.ClientID)
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("scope", strings.Join(request.Scopes, ","))
	query.Set("state", request.State)
	if len(request.UserScopes) > 0 {
		query.Set("user_scope", strings.Join(request.UserScopes, ","))
	}
	if request.PKCEChallenge != "" {
		query.Set("code_challenge", request.PKCEChallenge)
		query.Set("code_challenge_method", "S256")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (o OAuth) Exchange(ctx context.Context, request oauthflow.ExchangeRequest) (oauthflow.Grant, error) {
	endpoint := o.TokenURL
	if endpoint == "" {
		endpoint = defaultTokenURL
	}
	values := url.Values{
		"client_id":     {request.ClientID},
		"client_secret": {request.ClientSecret},
		"code":          {request.Code},
		"redirect_uri":  {request.RedirectURI},
	}
	if request.PKCEVerifier != "" {
		values.Set("code_verifier", request.PKCEVerifier)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return oauthflow.Grant{}, fmt.Errorf("create Slack token request: %w", err)
	}
	httpRequest.Header.Set("content-type", "application/x-www-form-urlencoded")
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return oauthflow.Grant{}, fmt.Errorf("request Slack token: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTokenResponseBytes+1))
	if err != nil {
		return oauthflow.Grant{}, fmt.Errorf("read Slack token response: %w", err)
	}
	if len(body) > maxTokenResponseBytes {
		return oauthflow.Grant{}, errors.New("Slack token response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return oauthflow.Grant{}, fmt.Errorf("Slack token endpoint returned HTTP %d", response.StatusCode)
	}
	var token slackTokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return oauthflow.Grant{}, errors.New("Slack token endpoint returned invalid JSON")
	}
	if !token.OK || token.AccessToken == "" {
		providerError := token.Error
		if providerError == "" {
			providerError = "oauth exchange failed"
		}
		return oauthflow.Grant{}, fmt.Errorf("Slack rejected OAuth exchange: %s", providerError)
	}
	if token.AuthedUser.AccessToken == "" {
		return oauthflow.Grant{}, errors.New("Slack token response has no user access token")
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return oauthflow.Grant{}, errors.New("Slack token endpoint returned invalid data")
	}
	return oauthflow.Grant{
		AccessToken: token.AccessToken,
		TokenType:   token.TokenType,
		Scopes:      combinedScopes(token.Scope, token.AuthedUser.Scope),
		Data:        data,
	}, nil
}

func (OAuth) ResolveIdentity(_ context.Context, grant oauthflow.Grant) (domain.ProviderIdentity, error) {
	team, ok := grant.Data["team"].(map[string]any)
	if !ok {
		return domain.ProviderIdentity{}, errors.New("Slack token response has no team identity")
	}
	authedUser, ok := grant.Data["authed_user"].(map[string]any)
	if !ok {
		return domain.ProviderIdentity{}, errors.New("Slack token response has no authorizing user")
	}
	identity := domain.ProviderIdentity{
		SubjectID:   stringValue(authedUser["id"]),
		AccountID:   stringValue(team["id"]),
		AccountName: stringValue(team["name"]),
		TenantID:    stringValue(team["id"]),
		TenantName:  stringValue(team["name"]),
	}
	if identity.SubjectID == "" || identity.TenantID == "" {
		return domain.ProviderIdentity{}, errors.New("Slack token response identity is incomplete")
	}
	return identity, nil
}

type slackTokenResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	AuthedUser  struct {
		ID          string `json:"id"`
		Scope       string `json:"scope"`
		AccessToken string `json:"access_token"`
	} `json:"authed_user"`
}

func combinedScopes(values ...string) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		for _, scope := range strings.Split(value, ",") {
			scope = strings.TrimSpace(scope)
			if scope != "" {
				set[scope] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(set))
	for scope := range set {
		result = append(result, scope)
	}
	slices.Sort(result)
	return result
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
