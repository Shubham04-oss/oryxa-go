// Package providers describes the providers connections can reach: which
// credential types each supports, its scope rules, and its OAuth 2.0 flow.
// Calling provider APIs for actions and triggers is left to the services that
// ask connections for tokens.
package providers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

const (
	maxResponseBytes = 1 << 20
	requestTimeout   = 10 * time.Second
)

// Grant is what a provider issued. It is encrypted before it is stored and
// leaves the service only as a token issued to a caller.
type Grant struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
	Scopes       []string  `json:"scopes"`
	// Tokens holds extra named tokens from the same grant, such as Slack's
	// user token next to its bot token.
	Tokens map[string]string `json:"tokens,omitempty"`
	// Metadata holds the token response fields the provider's
	// OAuth2.Metadata names.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ScopeRules are domain.ScopeRules, which apps are validated against.
type ScopeRules = domain.ScopeRules

type Provider struct {
	Name            string
	CredentialTypes []domain.CredentialType
	// Scopes are the rules for oauth2 apps. Other credential types take the
	// scopes ScopesFor returns.
	Scopes ScopeRules
	OAuth2 *OAuth2
	// ClientCredentials mints app-only tokens, for
	// oauth2_client_credentials apps.
	ClientCredentials *ClientCredentials
	// Installation is a GitHub App's installation flow, for installation
	// apps. The user authorization during installation uses OAuth2.
	Installation *Installation
	// Keys says how each key-based credential type is sent and checked.
	Keys map[domain.CredentialType]KeyAuth
	// Domain is the rule for the domain an app supplies when the URLs its
	// credential type uses name {domain}, such as an Atlassian site.
	Domain *DomainRule
}

func (provider Provider) Supports(credentialType domain.CredentialType) bool {
	if !slices.Contains(provider.CredentialTypes, credentialType) {
		return false
	}
	switch credentialType {
	case domain.CredentialOAuth2:
		return provider.OAuth2 != nil
	case domain.CredentialOAuth2ClientCredentials:
		return provider.ClientCredentials != nil
	case domain.CredentialInstallation:
		return provider.Installation != nil && provider.OAuth2 != nil
	case domain.CredentialAPIKey, domain.CredentialBearer, domain.CredentialBasic:
		_, ok := provider.Keys[credentialType]
		return ok
	default:
		return false
	}
}

// ScopesFor returns the scope rules for apps of a credential type. Only
// oauth2 and client-credentials apps carry scopes: an installation's
// permissions are set on the GitHub App, and a key has whatever its owner
// gave it.
func (provider Provider) ScopesFor(credentialType domain.CredentialType) ScopeRules {
	switch credentialType {
	case domain.CredentialOAuth2:
		return provider.Scopes
	case domain.CredentialOAuth2ClientCredentials:
		if provider.ClientCredentials != nil {
			return ScopeRules{Ceiling: provider.ClientCredentials.Scopes, Required: provider.ClientCredentials.Scopes}
		}
	}
	return ScopeRules{}
}

type Registry map[string]Provider

func (registry Registry) Lookup(name string) (Provider, bool) {
	provider, ok := registry[strings.ToLower(strings.TrimSpace(name))]
	return provider, ok
}

// OAuth2 is a provider's authorization-code flow. Its fields cover how
// providers differ; everything else follows RFC 6749.
type OAuth2 struct {
	AuthorizationURL string
	TokenURL         string
	// APIURL is the base URL the identity check calls.
	APIURL string
	// ScopeSeparator joins scopes in requests. Empty means a space.
	ScopeSeparator string
	// Params are fixed authorization parameters, such as Google's
	// access_type=offline.
	Params map[string]string
	// JSONTokenRequests sends token requests as a JSON object rather than a
	// form, for providers that document only JSON, such as Atlassian.
	JSONTokenRequests bool
	// ClientSecretBasic sends the client ID and secret as HTTP Basic
	// credentials rather than in the token request, for providers that
	// require it, such as Notion.
	ClientSecretBasic bool
	// Metadata names token response fields that are not secret and that
	// callers need with the token, such as Salesforce's instance_url. They are
	// kept with the grant and issued alongside its token.
	Metadata []string
	// UserScopes are requested on Slack's separate user_scope parameter.
	UserScopes []string
	PKCE       bool
	// SendScopeOnRefresh sends the connection's scopes with a refresh, which
	// keeps Microsoft from widening the grant.
	SendScopeOnRefresh bool
	// RequireRefreshToken rejects an exchange that issues no refresh token.
	RequireRefreshToken bool
	// PermanentErrors are token endpoint error codes meaning the grant or
	// client is gone for good, so the connection needs reconnecting.
	PermanentErrors []string
	// RequestScopes adjusts scopes before they are sent. Nil sends them as
	// given.
	RequestScopes func([]string) []string
	// GrantedScopes reads the token response's scope field. Nil splits on
	// spaces and commas, and falls back to the requested scopes when the
	// provider leaves the field out, as RFC 6749 allows.
	GrantedScopes func(raw string, requested []string) []string
	// DecodeToken reads a token response that does not follow RFC 6749, such
	// as Slack's. Nil reads the standard JSON.
	DecodeToken func([]byte) (TokenResponse, error)
	// Identity returns the account a grant belongs to.
	Identity func(ctx context.Context, client *http.Client, apiURL string, grant Grant) (domain.ProviderAccount, error)
	// RevokeURL is where Revoke ends a grant, when it differs from APIURL
	// (Google's does). Empty means Revoke derives the endpoint from APIURL.
	RevokeURL string
	// Revoke ends a grant at the provider. Nil means the provider has no way
	// to revoke one, so Client.Revoke reports (false, nil).
	Revoke func(ctx context.Context, client *http.Client, provider OAuth2, request RevokeRequest) (bool, error)
}

func (provider OAuth2) separator() string {
	if provider.ScopeSeparator == "" {
		return " "
	}
	return provider.ScopeSeparator
}

func (provider OAuth2) requestScopes(scopes []string) []string {
	if provider.RequestScopes != nil {
		return provider.RequestScopes(scopes)
	}
	return domain.NormalizeScopes(scopes)
}

// TokenResponse is a decoded token endpoint answer.
type TokenResponse struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	ExpiresIn    int64
	Error        string
	Tokens       map[string]string
	Metadata     map[string]string
}

type AuthorizationRequest struct {
	ClientID      string
	RedirectURI   string
	Scopes        []string
	State         string
	PKCEChallenge string
}

type ExchangeRequest struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Code         string
	PKCEVerifier string
	Scopes       []string
}

type RefreshRequest struct {
	ClientID     string
	ClientSecret string
	RefreshToken string
	Scopes       []string
	// Metadata is the grant's metadata before this refresh, kept for any
	// field the refresh response leaves out.
	Metadata map[string]string
}

// Client runs OAuth requests for any provider. Errors wrap
// domain.ErrCredentialRejected when the provider refused, or
// domain.ErrProviderUnavailable when it could not answer. They never carry
// tokens, codes, secrets, or provider response bodies.
type Client struct {
	HTTP *http.Client
	Now  func() time.Time
}

// NewHTTPClient is declared in dialer.go, alongside the address check it
// applies to every provider call.
var defaultHTTPClient = NewHTTPClient()

func (client Client) httpClient() *http.Client {
	if client.HTTP != nil {
		return client.HTTP
	}
	return defaultHTTPClient
}

func (client Client) now() time.Time {
	if client.Now != nil {
		return client.Now().UTC()
	}
	return time.Now().UTC()
}

func (client Client) AuthorizationURL(provider OAuth2, request AuthorizationRequest) (string, error) {
	parsed, err := url.Parse(provider.AuthorizationURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("provider authorization URL is invalid")
	}
	if request.ClientID == "" || request.RedirectURI == "" || request.State == "" {
		return "", fmt.Errorf("client ID, redirect URI and state are required: %w", domain.ErrInvalid)
	}
	query := parsed.Query()
	for key, value := range provider.Params {
		query.Set(key, value)
	}
	query.Set("client_id", request.ClientID)
	query.Set("redirect_uri", request.RedirectURI)
	query.Set("response_type", "code")
	query.Set("state", request.State)
	if scopes := provider.requestScopes(request.Scopes); len(scopes) > 0 {
		query.Set("scope", strings.Join(scopes, provider.separator()))
	}
	if len(provider.UserScopes) > 0 {
		query.Set("user_scope", strings.Join(provider.UserScopes, provider.separator()))
	}
	if provider.PKCE {
		if request.PKCEChallenge == "" {
			return "", fmt.Errorf("PKCE challenge is required: %w", domain.ErrInvalid)
		}
		query.Set("code_challenge", request.PKCEChallenge)
		query.Set("code_challenge_method", "S256")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (client Client) Exchange(ctx context.Context, provider OAuth2, request ExchangeRequest) (Grant, error) {
	if request.Code == "" {
		return Grant{}, fmt.Errorf("authorization code is required: %w", domain.ErrInvalid)
	}
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {request.ClientID},
		"client_secret": {request.ClientSecret},
		"code":          {request.Code},
		"redirect_uri":  {request.RedirectURI},
	}
	if request.PKCEVerifier != "" {
		values.Set("code_verifier", request.PKCEVerifier)
	}
	token, err := client.token(ctx, provider, values)
	if err != nil {
		return Grant{}, err
	}
	if token.Error != "" || token.AccessToken == "" {
		return Grant{}, fmt.Errorf("%w: %s", domain.ErrCredentialRejected, safeCode(token.Error))
	}
	if provider.RequireRefreshToken && token.RefreshToken == "" {
		return Grant{}, fmt.Errorf("%w: no refresh token was issued", domain.ErrCredentialRejected)
	}
	return client.grant(provider, token, provider.requestScopes(request.Scopes)), nil
}

// Refresh renews a grant. A permanent refusal wraps
// domain.ErrCredentialRejected; anything else wraps
// domain.ErrProviderUnavailable, so the caller can retry. A provider that
// does not rotate refresh tokens keeps the old one.
func (client Client) Refresh(ctx context.Context, provider OAuth2, request RefreshRequest) (Grant, error) {
	if request.RefreshToken == "" {
		return Grant{}, fmt.Errorf("%w: refresh_token_missing", domain.ErrCredentialRejected)
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {request.ClientID},
		"client_secret": {request.ClientSecret},
		"refresh_token": {request.RefreshToken},
	}
	scopes := domain.NormalizeScopes(request.Scopes)
	if provider.SendScopeOnRefresh && len(scopes) > 0 {
		values.Set("scope", strings.Join(scopes, provider.separator()))
	}
	token, err := client.token(ctx, provider, values)
	if err != nil {
		return Grant{}, err
	}
	if token.Error != "" || token.AccessToken == "" {
		code := safeCode(token.Error)
		if slices.Contains(provider.PermanentErrors, code) {
			return Grant{}, fmt.Errorf("%w: %s", domain.ErrCredentialRejected, code)
		}
		return Grant{}, fmt.Errorf("%w: %s", domain.ErrProviderUnavailable, code)
	}
	grant := client.grant(provider, token, scopes)
	if grant.RefreshToken == "" {
		grant.RefreshToken = request.RefreshToken
	}
	for name, value := range request.Metadata {
		if _, sent := grant.Metadata[name]; !sent && slices.Contains(provider.Metadata, name) {
			if grant.Metadata == nil {
				grant.Metadata = map[string]string{}
			}
			grant.Metadata[name] = value
		}
	}
	return grant, nil
}

// Identity returns the provider account a grant belongs to.
func (client Client) Identity(ctx context.Context, provider OAuth2, grant Grant) (domain.ProviderAccount, error) {
	if provider.Identity == nil {
		return domain.ProviderAccount{}, errors.New("provider has no identity check")
	}
	account, err := provider.Identity(ctx, client.httpClient(), strings.TrimRight(provider.APIURL, "/"), grant)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	if !safeValue(account.ID) || (account.Name != "" && !safeValue(account.Name)) {
		return domain.ProviderAccount{}, fmt.Errorf("%w: provider account is incomplete", domain.ErrProviderUnavailable)
	}
	return account, nil
}

func (client Client) token(ctx context.Context, provider OAuth2, values url.Values) (TokenResponse, error) {
	clientID, clientSecret := values.Get("client_id"), values.Get("client_secret")
	if provider.ClientSecretBasic {
		values = url.Values(maps.Clone(values))
		values.Del("client_id")
		values.Del("client_secret")
	}
	requestBody, contentType, err := tokenRequestBody(provider, values)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("%w: token request could not be built", domain.ErrProviderUnavailable)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.TokenURL, requestBody)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("%w: token request could not be built", domain.ErrProviderUnavailable)
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")
	if provider.ClientSecretBasic {
		// RFC 6749 section 2.3.1 form-encodes the ID and secret before they
		// become Basic credentials.
		request.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}
	response, err := client.httpClient().Do(request)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("%w: token request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return TokenResponse{}, fmt.Errorf("%w: token response could not be read", domain.ErrProviderUnavailable)
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
		return TokenResponse{}, fmt.Errorf("%w: token endpoint returned %d", domain.ErrProviderUnavailable, response.StatusCode)
	}
	decode := provider.DecodeToken
	if decode == nil {
		decode = decodeStandardToken
	}
	token, err := decode(body)
	if err != nil {
		if response.StatusCode >= http.StatusBadRequest {
			return TokenResponse{Error: "oauth_request_rejected"}, nil
		}
		return TokenResponse{}, fmt.Errorf("%w: token response is not valid", domain.ErrProviderUnavailable)
	}
	if response.StatusCode >= http.StatusMultipleChoices && token.Error == "" {
		token.Error = "oauth_request_rejected"
	}
	if token.Error == "" && len(provider.Metadata) > 0 {
		token.Metadata = tokenMetadata(body, provider.Metadata)
	}
	return token, nil
}

// tokenMetadata reads the named top-level fields of a token response. A field
// that is missing, is not a string, or is not a safe value is left out.
func tokenMetadata(body []byte, names []string) map[string]string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil
	}
	metadata := map[string]string{}
	for _, name := range names {
		var value string
		if raw, ok := fields[name]; ok && json.Unmarshal(raw, &value) == nil && safeValue(value) {
			metadata[name] = value
		}
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func (client Client) grant(provider OAuth2, token TokenResponse, requested []string) Grant {
	granted := provider.GrantedScopes
	if granted == nil {
		granted = splitGrantedScopes
	}
	grant := Grant{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		Scopes:       granted(token.Scope, requested),
		Tokens:       token.Tokens,
		Metadata:     token.Metadata,
	}
	if token.ExpiresIn > 0 {
		grant.Expiry = client.now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	return grant
}

// tokenRequestBody encodes a token request's fields as a form, or as a JSON
// object of strings for a provider with JSONTokenRequests.
func tokenRequestBody(provider OAuth2, values url.Values) (io.Reader, string, error) {
	if !provider.JSONTokenRequests {
		return strings.NewReader(values.Encode()), "application/x-www-form-urlencoded", nil
	}
	fields := make(map[string]string, len(values))
	for name := range values {
		fields[name] = values.Get(name)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, "", err
	}
	return bytes.NewReader(encoded), "application/json", nil
}

func decodeStandardToken(body []byte) (TokenResponse, error) {
	var token struct {
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		TokenType    string      `json:"token_type"`
		Scope        string      `json:"scope"`
		ExpiresIn    json.Number `json:"expires_in"`
		Error        string      `json:"error"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return TokenResponse{}, err
	}
	expiresIn, err := expiresIn(token.ExpiresIn)
	if err != nil {
		return TokenResponse{}, err
	}
	return TokenResponse{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, TokenType: token.TokenType,
		Scope: token.Scope, ExpiresIn: expiresIn, Error: token.Error}, nil
}

func expiresIn(value json.Number) (int64, error) {
	if value == "" {
		return 0, nil
	}
	seconds, err := strconv.ParseInt(value.String(), 10, 64)
	if err != nil || seconds < 0 {
		return 0, errors.New("expires_in is not a whole number of seconds")
	}
	return seconds, nil
}

func splitGrantedScopes(raw string, requested []string) []string {
	scopes := domain.NormalizeScopes(strings.FieldsFunc(raw, func(character rune) bool {
		return character == ' ' || character == ','
	}))
	if len(scopes) == 0 {
		return domain.NormalizeScopes(requested)
	}
	return scopes
}

// NewPKCE returns an S256 code verifier and its challenge.
func NewPKCE(random io.Reader) (verifier, challenge string, err error) {
	if random == nil {
		random = rand.Reader
	}
	value := make([]byte, 32)
	if _, err := io.ReadFull(random, value); err != nil {
		return "", "", fmt.Errorf("generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(value)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// callJSON sends an authenticated provider API request and decodes a bounded
// JSON response. apply puts the credential on the request — a bearer header,
// a differently named header, or a query parameter — which lets one function
// serve OAuth2 grants and key-based credentials alike. A 401 or 403 means the
// provider refused the credential; any other failure means it is
// unavailable. Response bodies never reach errors.
func callJSON(ctx context.Context, client *http.Client, method, endpoint string, apply func(*http.Request), header http.Header, body io.Reader, target any) error {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("%w: provider request could not be built", domain.ErrProviderUnavailable)
	}
	for key, values := range header {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	apply(request)
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: provider request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: provider refused the credential", domain.ErrCredentialRejected)
	case response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices:
		return fmt.Errorf("%w: provider returned %d", domain.ErrProviderUnavailable, response.StatusCode)
	case err != nil || len(payload) > maxResponseBytes:
		return fmt.Errorf("%w: provider response could not be read", domain.ErrProviderUnavailable)
	}
	if json.Unmarshal(payload, target) != nil {
		return fmt.Errorf("%w: provider response is not valid", domain.ErrProviderUnavailable)
	}
	return nil
}

// bearerCredential applies a token the way callJSON callers with an OAuth2
// access token have always sent one: an Authorization header with a Bearer
// prefix.
func bearerCredential(token string) func(*http.Request) {
	return func(request *http.Request) { request.Header.Set("Authorization", "Bearer "+token) }
}

func safeCode(value string) string {
	if value == "" || len(value) > 80 {
		return "oauth_request_rejected"
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '_' && character != '-' {
			return "oauth_request_rejected"
		}
	}
	return value
}

func safeValue(value string) bool {
	return value != "" && len(value) <= 512 && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}
