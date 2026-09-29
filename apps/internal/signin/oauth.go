package signin

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// StartRequest is a member starting an OAuth sign-in for a pending
// connection.
type StartRequest struct {
	OrganizationID string
	MemberID       string
	ConnectionID   string
	Toolkit        string
	// Kind picks the manifest's method by its kind; "" is the manifest's
	// first. It must sign in with oauth2_code.
	Kind string
	// Fields are what the member typed for the method's connection-level
	// fields, such as a subdomain. Keys and passwords go through
	// SaveCredential instead.
	Fields map[string]string
	// ReturnTo is the page the member's browser goes back to. Empty, or one
	// Config.ReturnAllowed allows.
	ReturnTo string
}

// Started is where to send the member's browser, and what to remember.
type Started struct {
	AuthorizationURL string
	// State is set as the callback's cookie: HttpOnly, SameSite=Lax, scoped
	// to /v1/apps/oauth/callback, and expiring with the flow. Only its hash
	// is stored.
	State     string
	ExpiresAt time.Time
}

// StartOAuth records an OAuth sign-in for a pending connection and answers
// the app's authorization URL. The state is 32 random bytes, of which only
// the SHA-256 is kept; PKCE is S256 unless the manifest turns it off; the
// flow lasts FlowLifetime. The OAuth client is the organization's own, or
// Oryxa's default unless the organization refused it.
func (s *Service) StartOAuth(ctx context.Context, request StartRequest) (Started, error) {
	if request.OrganizationID == "" || request.MemberID == "" || request.ConnectionID == "" {
		return Started{}, fmt.Errorf("signin: an organization, a member and a connection are required: %w", domain.ErrInvalid)
	}
	_, method, err := s.method(request.Toolkit, request.Kind)
	if err != nil {
		return Started{}, err
	}
	if method.Kind != integration.KindOAuth2Code {
		return Started{}, fmt.Errorf("%w: it takes a %s credential, through SaveCredential", ErrUnsupportedKind, method.Kind)
	}
	returnTo := strings.TrimSpace(request.ReturnTo)
	if !s.returnAllowed(returnTo) {
		return Started{}, fmt.Errorf("signin: return_to is not an allowed page: %w", domain.ErrInvalid)
	}
	settings, secretFields, err := memberFields(method, request.Fields)
	if err != nil {
		return Started{}, err
	}
	client, err := s.resolveClient(ctx, request.OrganizationID, method.OAuth2.Client)
	if err != nil {
		return Started{}, err
	}
	if err := requireClientFields(method, &client); err != nil {
		return Started{}, err
	}
	authURL, err := fillURL(method.OAuth2.AuthURL, merged(clientValues(method, &client), settings))
	if err != nil {
		return Started{}, err
	}
	// Secret fields are sealed for the credential they will join, so the
	// callback moves them across without opening them.
	sealed, err := s.sealEach(ctx, func(name string) secrets.Binding {
		return credentialBinding(request.ConnectionID, name)
	}, secretFields)
	if err != nil {
		return Started{}, err
	}
	state, stateHash, err := newState()
	if err != nil {
		return Started{}, err
	}
	verifier := ""
	if method.OAuth2.PKCE {
		verifier = oauth2.GenerateVerifier()
	}
	now := s.now().UTC()
	flow := Flow{
		StateHash: stateHash, OrganizationID: request.OrganizationID, MemberID: request.MemberID,
		ConnectionID: request.ConnectionID, Toolkit: request.Toolkit, OAuthClientID: client.ID,
		CodeVerifier: verifier, RedirectURI: s.config.RedirectURI, ReturnTo: returnTo,
		Settings: settings, Sealed: sealed, ExpiresAt: now.Add(FlowLifetime),
	}
	if err := s.store.InsertSigninFlow(ctx, flow); err != nil {
		return Started{}, err
	}
	config := oauth2.Config{
		ClientID:    client.ClientID,
		Endpoint:    oauth2.Endpoint{AuthURL: authURL.String()},
		RedirectURL: s.config.RedirectURI,
	}
	return Started{
		AuthorizationURL: config.AuthCodeURL(state, authOptions(method.OAuth2, verifier)...),
		State:            state,
		ExpiresAt:        flow.ExpiresAt,
	}, nil
}

// Callback is what the member's browser brought back.
type Callback struct {
	// State is the callback's state parameter, and CookieState the cookie
	// StartOAuth's State was set as. They must be equal.
	State       string
	CookieState string
	// Code is the authorization code, and Error the app's error parameter
	// (access_denied) when it sent one instead.
	Code  string
	Error string
	// Verify, when set, runs once the flow is found and before the code is
	// exchanged, with the organization and member who started it. It is
	// where an access cookie on the callback is checked against them. An
	// error ends the sign-in: a *Failure keeps its reason, and anything else
	// is wrong_account.
	Verify func(ctx context.Context, organizationID, memberID string) error
}

// Account is the account a credential belongs to, as its test call named
// it: Label for people, Ref for routing pushed events.
type Account struct {
	Label string
	Ref   string
}

// Completion is how a callback ended for the connection its flow names.
type Completion struct {
	ConnectionID   string
	OrganizationID string
	MemberID       string
	Toolkit        string
	Kind           string
	// ReturnTo is the page to send the browser back to, checked again now:
	// "" when none was given or it is no longer allowed.
	ReturnTo string
	Account  Account
	Scopes   []string
}

// CompleteOAuth finishes the sign-in a callback's state names. It checks the
// state against its cookie, spends the flow, exchanges the code with the
// manifest's client_auth and PKCE, reads discovered fields out of the token
// answer, runs the test call, and keeps the sealed credential at version 1.
//
// Once the flow is found, the Completion names its connection and return
// page even when the error is not nil: every error from then on ends that
// connection's sign-in, and a *Failure says why. Tokens the app issued that
// are not kept are revoked, best effort.
func (s *Service) CompleteOAuth(ctx context.Context, callback Callback) (Completion, error) {
	if callback.State == "" || callback.CookieState == "" ||
		subtle.ConstantTimeCompare([]byte(callback.State), []byte(callback.CookieState)) != 1 {
		return Completion{}, ErrBadState
	}
	flow, err := s.store.ConsumeSigninFlow(ctx, hashState(callback.State), s.now().UTC())
	if err != nil {
		return Completion{}, err
	}
	// The flow is spent, so this is the sign-in's one chance: it finishes
	// even if the member's browser goes away, rather than leaving tokens
	// issued at the app and nothing kept.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completeTimeout)
	defer cancel()
	completion := Completion{
		ConnectionID: flow.ConnectionID, OrganizationID: flow.OrganizationID, MemberID: flow.MemberID,
		Toolkit: flow.Toolkit, Kind: integration.KindOAuth2Code,
	}
	// Checked again: a page allowed ten minutes ago may not be now, and the
	// callback must never send a browser anywhere it has not just checked.
	if s.returnAllowed(flow.ReturnTo) {
		completion.ReturnTo = flow.ReturnTo
	}
	fail := func(failure *Failure) (Completion, error) {
		s.logger.WarnContext(ctx, "app sign-in failed", "connection_id", flow.ConnectionID,
			"toolkit", flow.Toolkit, "reason", failure.Reason, "error", failure)
		return completion, failure
	}

	if callback.Verify != nil {
		if err := callback.Verify(ctx, flow.OrganizationID, flow.MemberID); err != nil {
			var failure *Failure
			if errors.As(err, &failure) {
				return fail(failure)
			}
			return fail(&Failure{Reason: ReasonWrongAccount, cause: err})
		}
	}
	if callback.Error != "" || callback.Code == "" {
		return fail(&Failure{Reason: ReasonDenied, Rule: "the app sent back no code"})
	}

	manifest, method, err := s.method(flow.Toolkit, integration.KindOAuth2Code)
	if err != nil {
		return fail(&Failure{Reason: ReasonUnavailable, cause: err})
	}
	client, err := s.store.OAuthClient(ctx, flow.OAuthClientID)
	if err != nil {
		return fail(&Failure{Reason: ReasonUnavailable, cause: err})
	}
	clientSecret, err := s.clientSecret(ctx, client)
	if err != nil {
		return fail(&Failure{Reason: ReasonUnavailable, cause: err})
	}
	values := merged(clientValues(method, &client), flow.Settings)
	tokenURL, err := fillURL(method.OAuth2.TokenURL, values)
	if err != nil {
		return fail(&Failure{Reason: ReasonUnavailable, cause: err})
	}
	// Secret values for the token request: the member's, sealed with the
	// flow, and the client's.
	secretValues, err := s.clientFieldSecrets(ctx, method, &client)
	if err != nil {
		return fail(&Failure{Reason: ReasonUnavailable, cause: err})
	}
	for name, sealed := range flow.Sealed {
		value, err := s.open(ctx, credentialBinding(flow.ConnectionID, name), sealed)
		if err != nil {
			return fail(&Failure{Reason: ReasonUnavailable, cause: err})
		}
		secretValues[name] = value
	}

	config := &oauth2.Config{
		ClientID: client.ClientID, ClientSecret: clientSecret.Reveal(),
		Endpoint:    oauth2.Endpoint{TokenURL: tokenURL.String(), AuthStyle: authStyle(method.OAuth2.ClientAuth)},
		RedirectURL: flow.RedirectURI,
	}
	token, err := s.exchangeCode(ctx, config, callback.Code, flow.CodeVerifier, tokenParams(method, values, secretValues))
	switch {
	case errors.Is(err, errTokenUnavailable):
		return fail(&Failure{Reason: ReasonUnavailable, cause: err})
	case errors.Is(err, errClientRefused):
		// The code was fine as far as anyone can tell; the app refused the
		// client. An operator's problem, reported as the member sees it.
		s.logger.ErrorContext(ctx, "an app refused an OAuth client's token request",
			"toolkit", flow.Toolkit, "oauth_client", client.ID, "error", err)
		return fail(&Failure{Reason: ReasonDenied, cause: err})
	case err != nil:
		return fail(&Failure{Reason: ReasonDenied, cause: err})
	}
	issued := s.grantFrom(token)
	unkept := revocation{
		manifest: manifest, method: method, values: values, connectionID: flow.ConnectionID,
		clientID: client.ClientID, clientSecret: clientSecret, access: issued.access, refresh: issued.refresh,
	}

	discovered, discoveredSecrets, err := discover(method, token)
	if err != nil {
		s.discard(ctx, unkept)
		var failure *Failure
		if !errors.As(err, &failure) {
			failure = &Failure{Reason: ReasonInvalidCredentials, cause: err}
		}
		return fail(failure)
	}
	settings := merged(flow.Settings, discovered)
	maps.Copy(secretValues, discoveredSecrets)
	unkept.values = merged(values, discovered)

	prepared, err := s.prepare(manifest, method, &client, settings)
	if err != nil {
		s.discard(ctx, unkept)
		return fail(&Failure{Reason: ReasonInvalidCredentials, cause: err})
	}
	held := material{token: issued.access, expires: issued.expiresAt, fields: placedValues(method, secretValues)}
	account, err := s.runTest(ctx, prepared, held)
	if err != nil {
		s.discard(ctx, unkept)
		return fail(testFailure(err))
	}
	unkept.label = account.Label

	settings[integration.SettingAccountLabel] = account.Label
	if account.Ref != "" {
		settings[integration.SettingAccountRef] = account.Ref
	}
	tokens := map[string]secrets.Secret{sealedAccessToken: issued.access}
	if issued.refresh != "" {
		tokens[sealedRefreshToken] = issued.refresh
	}
	for name, value := range discoveredSecrets {
		tokens[name] = value
	}
	sealedTokens, err := s.sealEach(ctx, func(name string) secrets.Binding {
		return credentialBinding(flow.ConnectionID, name)
	}, tokens)
	if err != nil {
		s.discard(ctx, unkept)
		return fail(&Failure{Reason: ReasonUnavailable, cause: err})
	}
	sealed := mergedSealed(flow.Sealed, sealedTokens)
	scopes := issued.scopes
	if scopes == nil {
		scopes = append([]string{}, method.OAuth2.Scopes...)
	}
	now := s.now().UTC()
	credential := Credential{
		ConnectionID: flow.ConnectionID, OrganizationID: flow.OrganizationID, MemberID: flow.MemberID,
		Toolkit: flow.Toolkit, Kind: integration.KindOAuth2Code, OAuthClientID: client.ID,
		Sealed: sealed, Settings: settings, Scopes: scopes, ExpiresAt: issued.expiresAt, Version: 1,
		NextCheckAt: nextCheck(now), UpdatedAt: now,
	}
	if err := s.store.InsertCredential(ctx, credential); err != nil {
		s.discard(ctx, unkept)
		if errors.Is(err, domain.ErrConflict) {
			return completion, ErrNotPending
		}
		return fail(&Failure{Reason: ReasonUnavailable, cause: err})
	}
	s.cache.put(flow.ConnectionID, 1, held, cacheUntil(held, now), now)
	s.logger.InfoContext(ctx, "app signed in", "connection_id", flow.ConnectionID, "toolkit", flow.Toolkit)
	completion.Account = account
	completion.Scopes = scopes
	return completion, nil
}

// testFailure is the Failure a test call that did not pass gets.
func testFailure(err error) *Failure {
	if errors.Is(err, errTestUnavailable) {
		return &Failure{Reason: ReasonUnavailable, cause: err}
	}
	return &Failure{Reason: ReasonInvalidCredentials, Rule: "was refused by the app", cause: err}
}

// placedValues picks, of secret values, the fields a method places on
// requests.
func placedValues(method integration.Method, secretValues map[string]secrets.Secret) map[string]secrets.Secret {
	placed := map[string]secrets.Secret{}
	for _, field := range method.Fields {
		if value, ok := secretValues[field.Name]; ok && field.Secret && placesOnRequests(field) {
			placed[field.Name] = value
		}
	}
	return placed
}

// placesOnRequests says whether a field goes on the app's requests: a header
// or a query parameter.
func placesOnRequests(field integration.Field) bool {
	for _, use := range field.Use {
		if use.In == integration.InHeader || use.In == integration.InQuery {
			return true
		}
	}
	return false
}

func mergedSealed(sources ...map[string]secrets.SealedData) map[string]secrets.SealedData {
	out := map[string]secrets.SealedData{}
	for _, source := range sources {
		maps.Copy(out, source)
	}
	return out
}

// nextCheck is when a credential kept or checked at now is next checked:
// a day later, give or take a tenth, so a batch kept together does not come
// due together.
func nextCheck(now time.Time) time.Time {
	return now.Add(checkInterval - checkInterval/10 + jitter(checkInterval/5))
}
