package signin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

var (
	// errGrantRefused is the app refusing the grant itself: the code, the
	// refresh token, or, for a client's own credentials, the client.
	errGrantRefused = errors.New("the app refused the grant")
	// errClientRefused is the app refusing the request for any other
	// reason, such as Oryxa's client secret being wrong: an operator's
	// problem, which never costs a member their connection.
	errClientRefused = errors.New("the app refused the token request")
	// errTokenUnavailable is a token endpoint that did not answer, or not in
	// a way that could be read.
	errTokenUnavailable = errors.New("the app's token endpoint did not answer")
)

// refusedGrantCodes are the error codes that refuse the grant itself: RFC
// 6749's invalid_grant, and the codes GitHub and Slack answer in its place.
var refusedGrantCodes = []string{
	"invalid_grant",
	"invalid_code",          // Slack: the code was used already, or expired
	"invalid_refresh_token", // Slack: the refresh token is not valid
	"bad_verification_code", // GitHub: the code is wrong, or expired
	"bad_refresh_token",     // GitHub: the refresh token is wrong, or expired
}

// refusedClientCodes refuse a client's own credentials, which for the
// oauth2_client_credentials kind are the member's credential.
var refusedClientCodes = []string{"invalid_client", "unauthorized_client"}

var errorCodePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// tokenError is how a call to a token endpoint went wrong. It carries the
// app's error code and the HTTP status, and never the answer's body.
type tokenError struct {
	kind   error
	code   string
	status int
	cause  error
}

func (e *tokenError) Error() string {
	message := e.kind.Error()
	if e.code != "" {
		message += " (" + e.code + ")"
	}
	if e.status != 0 {
		message += fmt.Sprintf(", status %d", e.status)
	}
	if e.cause != nil {
		message += ": " + e.cause.Error()
	}
	return message
}

func (e *tokenError) Unwrap() error { return e.kind }

// classifyTokenError turns what x/oauth2 answered into a tokenError. A
// RetrieveError's body is left behind: it is the app's, and not for logs.
func classifyTokenError(err error) error {
	if err == nil {
		return nil
	}
	var retrieve *oauth2.RetrieveError
	if !errors.As(err, &retrieve) {
		// A network failure, a timeout, or an answer that could not be read.
		return &tokenError{kind: errTokenUnavailable, cause: err}
	}
	status := 0
	if retrieve.Response != nil {
		status = retrieve.Response.StatusCode
	}
	code := retrieve.ErrorCode
	if !errorCodePattern.MatchString(code) {
		code = ""
	}
	switch {
	case slices.Contains(refusedGrantCodes, code):
		return &tokenError{kind: errGrantRefused, code: code, status: status}
	case status == 429 || status >= 500:
		return &tokenError{kind: errTokenUnavailable, code: code, status: status}
	default:
		return &tokenError{kind: errClientRefused, code: code, status: status}
	}
}

// clientRefusedGrant treats the app refusing a client's own credentials as
// refusing the grant: for oauth2_client_credentials, the client is the
// member's credential.
func clientRefusedGrant(err error) error {
	var refused *tokenError
	if errors.As(err, &refused) && refused.kind == errClientRefused &&
		(slices.Contains(refusedClientCodes, refused.code) || refused.status == 401) {
		return &tokenError{kind: errGrantRefused, code: refused.code, status: refused.status}
	}
	return err
}

func authStyle(clientAuth string) oauth2.AuthStyle {
	if clientAuth == integration.ClientAuthHeader {
		return oauth2.AuthStyleInHeader
	}
	return oauth2.AuthStyleInParams
}

// tokenContext makes x/oauth2 call token endpoints with s.endpoints: safe
// dialing, a trace, and no redirects.
func (s *Service) tokenContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, s.endpoints)
}

// authOptions are an authorization request's parameters beyond client_id,
// redirect_uri, response_type and state: the scopes, joined as the app
// wants them, PKCE, and the manifest's fixed parameters.
func authOptions(oauth *integration.OAuth2, verifier string) []oauth2.AuthCodeOption {
	var options []oauth2.AuthCodeOption
	if scope := joinScopes(oauth); scope != "" {
		options = append(options, oauth2.SetAuthURLParam("scope", scope))
	}
	if verifier != "" {
		options = append(options, oauth2.S256ChallengeOption(verifier))
	}
	for _, name := range slices.Sorted(maps.Keys(oauth.AuthParams)) {
		options = append(options, oauth2.SetAuthURLParam(name, oauth.AuthParams[name]))
	}
	return options
}

func joinScopes(oauth *integration.OAuth2) string {
	separator := oauth.ScopeSep
	if separator == "" {
		separator = " "
	}
	return strings.Join(oauth.Scopes, separator)
}

// exchangeCode trades an authorization code for tokens.
func (s *Service) exchangeCode(ctx context.Context, config *oauth2.Config, code, verifier string, params url.Values) (*oauth2.Token, error) {
	options := make([]oauth2.AuthCodeOption, 0, len(params)+1)
	if verifier != "" {
		options = append(options, oauth2.VerifierOption(verifier))
	}
	for _, name := range slices.Sorted(maps.Keys(params)) {
		options = append(options, oauth2.SetAuthURLParam(name, params.Get(name)))
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	token, err := config.Exchange(s.tokenContext(ctx), code, options...)
	return token, classifyTokenError(err)
}

// refreshGrant trades a refresh token for new tokens: RFC 6749's refresh
// request, nothing more. When the app answers no refresh token, x/oauth2
// hands back the one sent.
func (s *Service) refreshGrant(ctx context.Context, config *oauth2.Config, refresh secrets.Secret) (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	token, err := config.TokenSource(s.tokenContext(ctx), &oauth2.Token{RefreshToken: refresh.Reveal()}).Token()
	return token, classifyTokenError(err)
}

// mintToken asks for a client credentials token, with the scopes joined as
// the app wants them and params beside them.
func (s *Service) mintToken(ctx context.Context, oauth *integration.OAuth2, tokenURL, clientID string, clientSecret secrets.Secret, params url.Values) (*oauth2.Token, error) {
	params = cloneValues(params)
	if scope := joinScopes(oauth); scope != "" {
		params.Set("scope", scope)
	}
	config := clientcredentials.Config{
		ClientID: clientID, ClientSecret: clientSecret.Reveal(), TokenURL: tokenURL,
		EndpointParams: params, AuthStyle: authStyle(oauth.ClientAuth),
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	token, err := config.Token(s.tokenContext(ctx))
	return token, clientRefusedGrant(classifyTokenError(err))
}

func cloneValues(values url.Values) url.Values {
	out := url.Values{}
	for name, value := range values {
		out[name] = slices.Clone(value)
	}
	return out
}

// tokenParams are the parameters a method's fields add to its token
// requests: the code exchange, and a client credentials request.
func tokenParams(method integration.Method, values map[string]string, secretValues map[string]secrets.Secret) url.Values {
	params := url.Values{}
	for _, field := range method.Fields {
		value := values[field.Name]
		if field.Secret {
			value = secretValues[field.Name].Reveal()
		}
		if value == "" {
			continue
		}
		for _, use := range field.Use {
			if use.In == integration.InTokenRequest {
				params.Set(use.Name, render(use.Template, value))
			}
		}
	}
	return params
}

// render fills a use's template, "{value}" by default, with value.
func render(template, value string) string {
	if template == "" {
		return value
	}
	return strings.ReplaceAll(template, "{value}", value)
}

// grant is what a token endpoint answered, ready to keep.
type grant struct {
	access  secrets.Secret
	refresh secrets.Secret
	// expiresAt is on signin's clock, or nil when the app named no expiry.
	expiresAt *time.Time
	// scopes are what the app said it granted, or nil when it did not say.
	scopes []string
	token  *oauth2.Token
}

func (s *Service) grantFrom(token *oauth2.Token) grant {
	g := grant{access: secrets.Secret(token.AccessToken), refresh: secrets.Secret(token.RefreshToken), token: token}
	if !token.Expiry.IsZero() {
		// x/oauth2 measures expires_in from the real clock; this keeps the
		// same span on signin's.
		at := s.now().UTC().Add(time.Until(token.Expiry)).Truncate(time.Second)
		g.expiresAt = &at
	}
	if text, ok := token.Extra("scope").(string); ok && strings.TrimSpace(text) != "" {
		g.scopes = splitScopes(text)
	}
	return g
}

// splitScopes reads a granted scope list, space-separated as RFC 6749 has
// it or comma-separated as Slack does.
func splitScopes(text string) []string {
	scopes := []string{}
	for _, scope := range strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == ',' }) {
		if !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// discover reads a method's discovered fields out of a token answer, and
// checks each against its rule. A required one that is missing fails.
func discover(method integration.Method, token *oauth2.Token) (map[string]string, map[string]secrets.Secret, error) {
	settings := map[string]string{}
	secretValues := map[string]secrets.Secret{}
	for _, field := range method.Fields {
		if field.Level != integration.LevelDiscovered {
			continue
		}
		value, found := scalar(tokenPointer(token, field.Discover))
		if !found {
			if field.Validate.Required {
				return nil, nil, invalidField(field.Name, "was not in the app's answer")
			}
			continue
		}
		if rule := checkValue(field.Validate, value); rule != "" {
			return nil, nil, invalidField(field.Name, rule)
		}
		if field.Secret {
			secretValues[field.Name] = secrets.Secret(value)
		} else {
			settings[field.Name] = value
		}
	}
	return settings, secretValues, nil
}

// tokenPointer resolves a JSON pointer in a token answer, through what
// x/oauth2 kept of it.
func tokenPointer(token *oauth2.Token, jsonPointer string) (any, bool) {
	if !strings.HasPrefix(jsonPointer, "/") {
		return nil, false
	}
	first, rest, nested := strings.Cut(jsonPointer[1:], "/")
	first = strings.ReplaceAll(strings.ReplaceAll(first, "~1", "/"), "~0", "~")
	value := token.Extra(first)
	if value == nil {
		return nil, false
	}
	if !nested {
		return value, true
	}
	return pointer(value, "/"+rest)
}
