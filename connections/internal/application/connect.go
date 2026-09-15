package application

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/secrets"
)

// Errors the callback sends the browser back with.
const (
	CallbackProviderDenied = "provider_denied"
	CallbackSessionExpired = "session_expired"
	// CallbackInstallationPending means a GitHub App installation waits for an
	// organization owner's approval.
	CallbackInstallationPending = "installation_pending"
)

// CallbackPath is where providers send the browser back to, followed by the
// provider's name.
const CallbackPath = "/v1/oauth/callback/"

// maxHandleLength bounds the state, code and session handle a request may
// carry. Real ones are far shorter.
const maxHandleLength = 4096

// revokeTimeout bounds a best-effort revocation.
const revokeTimeout = 10 * time.Second

type AuthorizeInput struct {
	AppID string
	// Scope defaults to member.
	Scope  domain.Scope
	TeamID string
	// Usage defaults to members; only organization connections take admins.
	Usage     domain.Usage
	ReturnURI string
}

// Authorization is where to send the member to consent.
type Authorization struct {
	URL       string    `json:"authorization_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CallbackParams are what a provider's redirect carries.
type CallbackParams struct {
	State string
	Code  string
	Error string
	// InstallationID and SetupAction come back from a GitHub App
	// installation.
	InstallationID string
	SetupAction    string
	// Tenant and AdminConsent come back from Microsoft admin consent.
	Tenant       string
	AdminConsent string
}

// Completion is a finished connect, or a reconnect of an existing connection.
type Completion struct {
	Connection  domain.Connection
	Reconnected bool
}

// obtained is what completing a session got from the provider.
type obtained struct {
	account domain.ProviderAccount
	secrets connectionSecrets
	scopes  []string
}

// Authorize starts connecting a provider account at a scope: OAuth consent,
// a GitHub App installation, or an admin's consent for client credentials.
func (s *Service) Authorize(ctx context.Context, credentials authclient.Credentials, organizationID string, input AuthorizeInput) (Authorization, error) {
	membership, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return Authorization{}, err
	}
	scope, err := domain.ParseScope(string(input.Scope))
	if err != nil {
		return Authorization{}, err
	}
	usage, err := domain.ParseUsage(string(input.Usage), scope)
	if err != nil {
		return Authorization{}, err
	}
	placement, err := domain.Place(scope, input.TeamID, membership.UserID)
	if err != nil {
		return Authorization{}, err
	}
	caller, err := s.callerAt(ctx, credentials, membership, placement.Scope, placement.TeamID)
	if err != nil {
		return Authorization{}, err
	}
	if err := domain.MayCreate(caller, placement); err != nil {
		return Authorization{}, err
	}
	app, provider, err := s.authorizingApp(ctx, organizationID, input.AppID)
	if err != nil {
		return Authorization{}, err
	}
	if err := app.CredentialType.Fits(placement.Scope); err != nil {
		return Authorization{}, err
	}
	return s.startSession(ctx, app, provider, domain.Session{
		OrganizationID: organizationID, MemberID: membership.UserID, AppID: app.ID, Intent: domain.IntentConnect,
		Scope: placement.Scope, TeamID: placement.TeamID, Usage: usage,
	}, input.ReturnURI)
}

// Reconnect starts replacing a connection's grant in place. Its app's current
// scopes are requested, so reconnecting is also how a connection gains scopes
// its app newly allows.
func (s *Service) Reconnect(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID, returnURI string) (Authorization, error) {
	membership, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return Authorization{}, err
	}
	connection, err := s.manageableConnection(ctx, credentials, membership, connectionID)
	if err != nil {
		return Authorization{}, err
	}
	app, provider, err := s.authorizingApp(ctx, organizationID, connection.AppID)
	if err != nil {
		return Authorization{}, err
	}
	return s.startSession(ctx, app, provider, domain.Session{
		OrganizationID: organizationID, MemberID: membership.UserID, AppID: app.ID, Intent: domain.IntentReconnect,
		ConnectionID: connection.ID, Scope: connection.Scope, TeamID: connection.TeamID, Usage: connection.Usage,
	}, returnURI)
}

// Callback records what the provider sent back and returns where to send the
// browser: the session's return URI with session_id, or with error set to
// provider_denied, installation_pending or session_expired. Only a state no
// session knows fails, with domain.ErrNotFound, because there is nowhere safe
// to send the browser.
func (s *Service) Callback(ctx context.Context, providerName string, params CallbackParams) (string, error) {
	if params.State == "" || len(params.State) > maxHandleLength {
		return "", domain.ErrNotFound
	}
	session, err := s.store.SessionByState(ctx, hashValue(params.State))
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(session.RedirectURI, CallbackPath+url.PathEscape(providerName)) {
		return "", domain.ErrNotFound
	}
	now := s.clock()
	if session.CallbackAt != nil || !now.Before(session.ExpiresAt) {
		return withQuery(session.ReturnURI, "error", CallbackSessionExpired)
	}
	app, err := s.store.AnyApp(ctx, session.OrganizationID, session.AppID)
	if err != nil {
		return "", err
	}
	result, refusal := callbackOutcome(app.CredentialType, params)
	if refusal != "" {
		if err := s.store.RecordCallback(ctx, session.StateHash, "", nil, refusal, now); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return "", err
		}
		return withQuery(session.ReturnURI, "error", refusal)
	}

	handle, err := s.randomToken(32)
	if err != nil {
		return "", err
	}
	sealed, err := s.seal(ctx, sessionBinding(session), result)
	if err != nil {
		return "", err
	}
	if err := s.store.RecordCallback(ctx, session.StateHash, hashValue(handle), sealed, "", now); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return withQuery(session.ReturnURI, "error", CallbackSessionExpired)
		}
		return "", err
	}
	return withQuery(session.ReturnURI, "session_id", handle)
}

// callbackOutcome reads a redirect for an app's credential type: what to keep
// on the session, or why there is nothing to keep. The provider's own error
// text is never kept or passed on.
func callbackOutcome(credentialType domain.CredentialType, params CallbackParams) (callbackResult, string) {
	for _, value := range []string{params.Code, params.InstallationID, params.Tenant} {
		if len(value) > maxHandleLength {
			return callbackResult{}, CallbackProviderDenied
		}
	}
	if params.Error != "" {
		return callbackResult{}, CallbackProviderDenied
	}
	switch credentialType {
	case domain.CredentialInstallation:
		if params.SetupAction == "request" {
			return callbackResult{}, CallbackInstallationPending
		}
		if params.Code == "" || params.InstallationID == "" {
			return callbackResult{}, CallbackProviderDenied
		}
		return callbackResult{Code: params.Code, InstallationID: params.InstallationID}, ""
	case domain.CredentialOAuth2ClientCredentials:
		if !strings.EqualFold(params.AdminConsent, "true") || params.Tenant == "" {
			return callbackResult{}, CallbackProviderDenied
		}
		return callbackResult{Tenant: params.Tenant}, ""
	default:
		if params.Code == "" {
			return callbackResult{}, CallbackProviderDenied
		}
		return callbackResult{Code: params.Code}, ""
	}
}

// Complete finishes a session the caller started: it trades what the
// provider sent back for credentials, checks the account, and saves the
// connection. The session is claimed before anything is exchanged, so a
// repeated request never exchanges a code twice, and any failure after that
// uses the session up.
func (s *Service) Complete(ctx context.Context, credentials authclient.Credentials, organizationID, sessionID string) (Completion, error) {
	membership, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return Completion{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > maxHandleLength {
		return Completion{}, fmt.Errorf("session_id is required: %w", domain.ErrInvalid)
	}
	session, err := s.store.SessionByHandle(ctx, hashValue(sessionID))
	if err != nil {
		return Completion{}, err
	}
	if session.OrganizationID != organizationID {
		return Completion{}, domain.ErrNotFound
	}
	now := s.clock()
	if err := session.Completable(membership.UserID, now); err != nil {
		return Completion{}, err
	}
	if err := s.store.ClaimSession(ctx, session.StateHash, now); err != nil {
		return Completion{}, err
	}
	completion, err := s.complete(ctx, credentials, membership, session)
	if err != nil {
		if burnErr := s.store.BurnSession(context.WithoutCancel(ctx), session.StateHash, s.clock()); burnErr != nil {
			s.logger.WarnContext(ctx, "use up authorization session", "error", burnErr)
		}
		return Completion{}, err
	}
	return completion, nil
}

func (s *Service) complete(ctx context.Context, credentials authclient.Credentials, membership domain.OrgMembership, session domain.Session) (Completion, error) {
	app, provider, err := s.authorizingApp(ctx, session.OrganizationID, session.AppID)
	if err != nil {
		return Completion{}, err
	}
	// Access is decided again: the member may have lost it since authorizing.
	var current domain.Connection
	switch session.Intent {
	case domain.IntentConnect:
		placement := session.Placement()
		caller, err := s.callerAt(ctx, credentials, membership, placement.Scope, placement.TeamID)
		if err != nil {
			return Completion{}, err
		}
		if err := domain.MayCreate(caller, placement); err != nil {
			return Completion{}, err
		}
	case domain.IntentReconnect:
		if current, err = s.manageableConnection(ctx, credentials, membership, session.ConnectionID); err != nil {
			return Completion{}, err
		}
		if current.AppID != app.ID {
			return Completion{}, domain.ErrNotFound
		}
	default:
		return Completion{}, fmt.Errorf("session intent is not allowed: %w", domain.ErrInvalid)
	}

	if session.EncryptedResult == nil {
		return Completion{}, domain.ErrSessionExpired
	}
	var result callbackResult
	if err := s.open(ctx, sessionBinding(session), session.EncryptedResult, &result); err != nil {
		return Completion{}, err
	}
	opened, err := s.appSecrets(ctx, app)
	if err != nil {
		return Completion{}, err
	}
	var got obtained
	switch app.CredentialType {
	case domain.CredentialOAuth2:
		got, err = s.exchangeCode(ctx, app, provider, opened, session, result)
	case domain.CredentialInstallation:
		got, err = s.exchangeInstallation(ctx, app, provider, opened, session, result)
	case domain.CredentialOAuth2ClientCredentials:
		got, err = s.mintConsented(ctx, app, provider, opened, result)
	default:
		err = fmt.Errorf("%s apps are not connected through authorization: %w", app.CredentialType, domain.ErrInvalid)
	}
	if err != nil {
		return Completion{}, err
	}

	if session.Intent == domain.IntentReconnect {
		connection, err := s.saveReconnect(ctx, session.StateHash, current, got, membership.UserID)
		if errors.Is(err, domain.ErrAccountChanged) {
			s.revokeUnused(ctx, app, provider, opened, got)
		}
		if err != nil {
			return Completion{}, err
		}
		return Completion{Connection: connection, Reconnected: true}, nil
	}
	connection, err := s.saveConnect(ctx, session, app, got, membership.UserID)
	if err != nil {
		return Completion{}, err
	}
	return Completion{Connection: connection}, nil
}

// exchangeCode trades an authorization code for a grant, checks that it
// carries every requested scope, and asks the provider whose account it is.
func (s *Service) exchangeCode(ctx context.Context, app domain.App, provider providers.Provider, opened appSecrets, session domain.Session, result callbackResult) (obtained, error) {
	request := providers.ExchangeRequest{
		ClientID: app.ClientID, ClientSecret: opened.ClientSecret, RedirectURI: session.RedirectURI,
		Code: result.Code, Scopes: session.RequestedScopes,
	}
	if session.EncryptedVerifier != nil {
		verifier, err := s.sealer.Open(ctx, sessionBinding(session), *session.EncryptedVerifier)
		if err != nil {
			return obtained{}, fmt.Errorf("open PKCE verifier: %w", err)
		}
		defer secrets.Wipe(verifier)
		request.PKCEVerifier = string(verifier)
	}
	grant, err := s.oauth.Exchange(ctx, *provider.OAuth2, request)
	if err != nil {
		return obtained{}, err
	}
	// A grant missing scopes is not revoked: it may belong to an account that
	// is already connected with the same client, and revoking would cut that
	// connection off.
	requested := session.RequestedScopes
	if missing := domain.MissingScopes(requested, grant.Scopes, requested, provider.Scopes.Implications); len(missing) > 0 {
		return obtained{}, domain.ScopeRequiredError{Missing: missing}
	}
	account, err := s.oauth.Identity(ctx, *provider.OAuth2, grant)
	if err != nil {
		return obtained{}, err
	}
	return obtained{account: account, secrets: connectionSecrets{Grant: &grant}, scopes: grant.Scopes}, nil
}

// exchangeInstallation accepts a GitHub App installation only when the user
// who authorized during installation can reach it: the installation_id in the
// redirect is not proof on its own, because anyone can forge it.
func (s *Service) exchangeInstallation(ctx context.Context, app domain.App, provider providers.Provider, opened appSecrets, session domain.Session, result callbackResult) (obtained, error) {
	user, err := s.oauth.Exchange(ctx, *provider.OAuth2, providers.ExchangeRequest{
		ClientID: app.ClientID, ClientSecret: opened.ClientSecret, RedirectURI: session.RedirectURI, Code: result.Code,
	})
	if err != nil {
		return obtained{}, err
	}
	installations, err := s.oauth.UserInstallations(ctx, provider, user)
	if err != nil {
		return obtained{}, err
	}
	if !slices.Contains(installations, result.InstallationID) {
		return obtained{}, fmt.Errorf("the authorizing GitHub account cannot reach the installation: %w", domain.ErrForbidden)
	}
	installer := githubApp(app, opened)
	account, err := s.oauth.Installation(ctx, provider, installer, result.InstallationID)
	if err != nil {
		return obtained{}, err
	}
	token, err := s.oauth.InstallationToken(ctx, provider, installer, result.InstallationID)
	if err != nil {
		return obtained{}, err
	}
	return obtained{account: account, secrets: connectionSecrets{Grant: &token, InstallationID: result.InstallationID}}, nil
}

// mintConsented proves an admin's consent by minting a token in the tenant the
// consent came back for.
func (s *Service) mintConsented(ctx context.Context, app domain.App, provider providers.Provider, opened appSecrets, result callbackResult) (obtained, error) {
	tenant, err := domain.CredentialInput{Tenant: result.Tenant}.Validate(domain.CredentialOAuth2ClientCredentials)
	if err != nil {
		return obtained{}, err
	}
	grant, err := s.oauth.Mint(ctx, provider, providers.MintRequest{ClientID: app.ClientID, ClientSecret: opened.ClientSecret, Tenant: tenant.Tenant})
	if err != nil {
		return obtained{}, err
	}
	return obtained{
		account: domain.ProviderAccount{ID: tenant.Tenant, Name: tenant.Tenant},
		secrets: connectionSecrets{Grant: &grant, Tenant: tenant.Tenant}, scopes: app.Scopes,
	}, nil
}

func (s *Service) saveConnect(ctx context.Context, session domain.Session, app domain.App, got obtained, actorID string) (domain.Connection, error) {
	connection, event, err := s.newConnection(ctx, app, session.Placement(), session.Usage, "", got, actorID)
	if err != nil {
		return domain.Connection{}, err
	}
	return s.store.CompleteConnect(ctx, session.StateHash, connection, event, s.clock().Add(s.heartbeatInterval))
}

// newConnection builds a new active connection with its sealed secrets and
// its connected event.
func (s *Service) newConnection(ctx context.Context, app domain.App, placement domain.Placement, usage domain.Usage, label string, got obtained, actorID string) (domain.Connection, domain.Event, error) {
	id, err := s.newID("con")
	if err != nil {
		return domain.Connection{}, domain.Event{}, err
	}
	now := s.clock()
	connection := domain.Connection{
		ID: id, OrganizationID: app.OrganizationID, AppID: app.ID, Provider: app.Provider, CredentialType: app.CredentialType,
		Scope: placement.Scope, TeamID: placement.TeamID, MemberID: placement.MemberID, Usage: usage, Label: label,
		ProviderAccount: got.account, Scopes: domain.NormalizeScopes(got.scopes), Status: domain.StatusActive,
		StatusChangedAt: now, CreatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	if connection.EncryptedCredentials, err = s.sealConnectionSecrets(ctx, connection, got.secrets); err != nil {
		return domain.Connection{}, domain.Event{}, err
	}
	event, err := s.event(connectionseventsv1.ConnectionConnectedDetailType, connectionseventsv1.ConnectionConnected{
		ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, AppID: connection.AppID,
		Provider: connection.Provider, CredentialType: string(connection.CredentialType), Scope: string(connection.Scope),
		TeamID: connection.TeamID, MemberID: connection.MemberID, ProviderAccount: eventAccount(connection),
		Scopes: connection.Scopes, ConnectedBy: actorID, ConnectedAt: now,
	}, now)
	if err != nil {
		return domain.Connection{}, domain.Event{}, err
	}
	return connection, event, nil
}

// saveReconnect replaces a connection's credentials in place through a
// session. The provider account must not change.
func (s *Service) saveReconnect(ctx context.Context, stateHash string, current domain.Connection, got obtained, actorID string) (domain.Connection, error) {
	restored, event, err := s.restoredConnection(ctx, current, got, actorID)
	if err != nil {
		return domain.Connection{}, err
	}
	return s.store.CompleteReconnect(ctx, stateHash, restored, current.Version, event)
}

// restoredConnection is current with new credentials, back to active, with its
// reconnected event.
func (s *Service) restoredConnection(ctx context.Context, current domain.Connection, got obtained, actorID string) (domain.Connection, domain.Event, error) {
	now := s.clock()
	restored, err := current.Reconnect(got.account, got.scopes, now)
	if err != nil {
		return domain.Connection{}, domain.Event{}, err
	}
	if restored.EncryptedCredentials, err = s.sealConnectionSecrets(ctx, restored, got.secrets); err != nil {
		return domain.Connection{}, domain.Event{}, err
	}
	event, err := s.event(connectionseventsv1.ConnectionReconnectedDetailType, connectionseventsv1.ConnectionReconnected{
		ConnectionID: restored.ID, OrganizationID: restored.OrganizationID, AppID: restored.AppID,
		Provider: restored.Provider, Scope: string(restored.Scope), TeamID: restored.TeamID, MemberID: restored.MemberID,
		ProviderAccount: eventAccount(restored), Scopes: restored.Scopes, ReconnectedBy: actorID, ReconnectedAt: now,
	}, now)
	if err != nil {
		return domain.Connection{}, domain.Event{}, err
	}
	return restored, event, nil
}

// revokeUnused revokes, best effort, an oauth2 grant nothing will keep. It
// leaves the grant alone when another connection holds credentials for the
// same app and account: providers revoke per account and client, so that
// connection would lose its grant too.
func (s *Service) revokeUnused(ctx context.Context, app domain.App, provider providers.Provider, opened appSecrets, got obtained) {
	if app.CredentialType != domain.CredentialOAuth2 || got.secrets.Grant == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
	defer cancel()
	shared, err := s.store.SharesProviderGrant(ctx, app.OrganizationID, app.ID, got.account.ID, "")
	if err != nil {
		s.logger.WarnContext(ctx, "check grant before revoking", "app_id", app.ID, "error", err)
		return
	}
	if shared {
		return
	}
	if _, err := s.oauth.Revoke(ctx, provider, providers.RevokeRequest{
		ClientID: app.ClientID, ClientSecret: opened.ClientSecret, Grant: *got.secrets.Grant,
	}); err != nil {
		s.logger.WarnContext(ctx, "revoke unused grant", "app_id", app.ID, "error", err)
	}
}

// authorizingApp returns an active app whose connections are made through a
// provider redirect, with its provider.
func (s *Service) authorizingApp(ctx context.Context, organizationID, appID string) (domain.App, providers.Provider, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return domain.App{}, providers.Provider{}, fmt.Errorf("app_id is required: %w", domain.ErrInvalid)
	}
	app, err := s.store.App(ctx, organizationID, appID)
	if err != nil {
		return domain.App{}, providers.Provider{}, err
	}
	provider, ok := s.providers.Lookup(app.Provider)
	switch {
	case !app.CredentialType.Authorizes() && app.CredentialType != domain.CredentialOAuth2ClientCredentials:
		return domain.App{}, providers.Provider{}, fmt.Errorf("%s apps are connected with POST …/connections: %w", app.CredentialType, domain.ErrInvalid)
	case !ok || !provider.Supports(app.CredentialType):
		return domain.App{}, providers.Provider{}, fmt.Errorf("provider is not available: %w", domain.ErrInvalid)
	case app.ClientID == "" || app.EncryptedSecret == nil:
		return domain.App{}, providers.Provider{}, fmt.Errorf("app has no client: %w", domain.ErrInvalid)
	case app.Status != domain.AppStatusActive:
		return domain.App{}, providers.Provider{}, domain.ErrAppDisabled
	}
	return app, provider.WithDomain(app.Settings.Domain), nil
}

// startSession stores a session and returns the provider URL it starts at.
// The raw state goes only to the provider; the session keeps its hash.
func (s *Service) startSession(ctx context.Context, app domain.App, provider providers.Provider, session domain.Session, rawReturnURI string) (Authorization, error) {
	returnURI, err := s.returnURI(rawReturnURI)
	if err != nil {
		return Authorization{}, err
	}
	now := s.clock()
	open, err := s.store.CountOpenSessions(ctx, session.OrganizationID, session.MemberID, now)
	if err != nil {
		return Authorization{}, err
	}
	if open >= domain.MaxOpenSessions {
		return Authorization{}, domain.ErrRateLimited
	}
	state, err := s.randomToken(32)
	if err != nil {
		return Authorization{}, err
	}
	session.StateHash = hashValue(state)
	session.ReturnURI = returnURI
	session.RedirectURI = s.publicBaseURL + CallbackPath + url.PathEscape(provider.Name)
	session.RequestedScopes = app.Scopes
	session.CreatedAt, session.ExpiresAt = now, now.Add(domain.SessionTTL)

	var authorizationURL string
	switch app.CredentialType {
	case domain.CredentialInstallation:
		authorizationURL, err = s.oauth.InstallationURL(provider, providers.InstallationRequest{AppSlug: app.Settings.GitHubAppSlug, State: state})
	case domain.CredentialOAuth2ClientCredentials:
		authorizationURL, err = s.oauth.AdminConsentURL(provider, providers.AdminConsentRequest{
			ClientID: app.ClientID, RedirectURI: session.RedirectURI, State: state,
		})
	default:
		request := providers.AuthorizationRequest{ClientID: app.ClientID, RedirectURI: session.RedirectURI, Scopes: app.Scopes, State: state}
		if provider.OAuth2.PKCE {
			verifier, challenge, err := providers.NewPKCE(s.random)
			if err != nil {
				return Authorization{}, err
			}
			plaintext := []byte(verifier)
			sealed, err := s.sealer.Seal(ctx, sessionBinding(session), plaintext)
			secrets.Wipe(plaintext)
			if err != nil {
				return Authorization{}, fmt.Errorf("seal PKCE verifier: %w", err)
			}
			session.EncryptedVerifier = &sealed
			request.PKCEChallenge = challenge
		}
		authorizationURL, err = s.oauth.AuthorizationURL(*provider.OAuth2, request)
	}
	if err != nil {
		return Authorization{}, err
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return Authorization{}, err
	}
	return Authorization{URL: authorizationURL, ExpiresAt: session.ExpiresAt}, nil
}

// manageableConnection loads a live connection the caller may reconnect or
// change.
func (s *Service) manageableConnection(ctx context.Context, credentials authclient.Credentials, membership domain.OrgMembership, connectionID string) (domain.Connection, error) {
	connection, caller, err := s.visibleConnection(ctx, credentials, membership, connectionID)
	if err != nil {
		return domain.Connection{}, err
	}
	if err := domain.MayManage(caller, connection); err != nil {
		return domain.Connection{}, err
	}
	if connection.Status == domain.StatusDisconnected {
		return domain.Connection{}, domain.ErrNotFound
	}
	return connection, nil
}
