package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
)

const (
	// renewLease bounds how long one caller may hold a connection's renewal.
	renewLease = 30 * time.Second
	// renewPoll is how often a waiting caller looks for another's renewal.
	renewPoll = 200 * time.Millisecond
)

var bearerPlacement = domain.TokenPlacement{In: "header", Name: "Authorization", Prefix: "Bearer "}

// Actor is who asks on the internal API: a service that proved its identity
// with a service token and, when it carries out a member's action, the
// credentials of that member, which it forwards.
type Actor struct {
	// Credentials are the forwarded credentials of the member the service acts
	// for. They are empty for work the service runs on its own, such as a
	// trigger delivery or cleanup.
	Credentials authclient.Credentials
	// Service is the verified calling service. The internal API refuses an
	// actor without one.
	Service string
	// Purpose is the purpose the service token names.
	Purpose domain.Purpose
}

// TokenRequest is POST /v1/internal/connections/{id}/token.
type TokenRequest struct {
	OrganizationID string
	Purpose        domain.Purpose
	// ActingMemberID is the member the work runs as. A service running work
	// on its own must name one; with a forwarded member it may be empty or
	// name that member.
	ActingMemberID string
	Scopes         []string
	// Token picks one token from a token set, such as Slack's "user". Empty
	// picks the primary token.
	Token string
}

// IssueToken hands a service a credential for a connection. A grant or minted
// token that expires within domain.RenewBefore is renewed first.
func (s *Service) IssueToken(ctx context.Context, actor Actor, connectionID string, request TokenRequest) (domain.IssuedToken, error) {
	if !request.Purpose.Valid() {
		return domain.IssuedToken{}, fmt.Errorf("purpose is not allowed: %w", domain.ErrInvalid)
	}
	connection, err := s.usableConnection(ctx, actor, request.OrganizationID, connectionID, request.Purpose, request.ActingMemberID)
	if err != nil {
		return domain.IssuedToken{}, err
	}
	app, err := s.store.AnyApp(ctx, connection.OrganizationID, connection.AppID)
	if err != nil {
		return domain.IssuedToken{}, err
	}
	holdOpen := false
	if connection.Status == domain.StatusDisconnected && request.Purpose == domain.PurposeCleanup {
		holds, err := s.store.CountHolds(ctx, connection.OrganizationID, connection.ID)
		if err != nil {
			return domain.IssuedToken{}, err
		}
		holdOpen = domain.HoldOpen(connection, holds, s.clock())
	}
	if err := domain.Issuable(connection, app, request.Purpose, holdOpen); err != nil {
		return domain.IssuedToken{}, err
	}
	provider, err := s.providerFor(app)
	if err != nil {
		return domain.IssuedToken{}, err
	}
	// Only oauth2 grants carry scopes connections can see. A key, an
	// installation, and a client-credentials token carry whatever the provider
	// gave them.
	scopesGranted := func(connection domain.Connection) error {
		if connection.CredentialType != domain.CredentialOAuth2 {
			return nil
		}
		if missing := domain.MissingScopes(request.Scopes, connection.Scopes, app.Scopes, provider.Scopes.Implications); len(missing) > 0 {
			return domain.ScopeRequiredError{Missing: missing}
		}
		return nil
	}
	if err := scopesGranted(connection); err != nil {
		return domain.IssuedToken{}, err
	}
	token, current, err := s.issue(ctx, connection, app, provider, strings.TrimSpace(request.Token))
	if err != nil {
		return domain.IssuedToken{}, err
	}
	// A renewal may have come back with fewer scopes than before.
	if err := scopesGranted(current); err != nil {
		return domain.IssuedToken{}, err
	}
	if request.Purpose == domain.PurposeAction || request.Purpose == domain.PurposeTrigger {
		if err := s.store.MarkUsed(ctx, connection.OrganizationID, connection.ID, s.clock()); err != nil {
			s.logger.WarnContext(ctx, "record connection use", "connection_id", connection.ID, "error", err)
		}
	}
	return token, nil
}

// usableConnection loads a connection the actor may have tokens issued on.
// Tokens go only to services, so a member holding their own credentials can
// never pull a team or organization connection's raw token. A service that
// forwards the member it acts for has that member's access checked live with
// auth and teams; one running work on its own vouches for the member it names.
func (s *Service) usableConnection(ctx context.Context, actor Actor, organizationID, connectionID string, purpose domain.Purpose, actingMemberID string) (domain.Connection, error) {
	if actor.Service == "" {
		return domain.Connection{}, domain.ErrForbidden
	}
	organizationID, actingMemberID = strings.TrimSpace(organizationID), strings.TrimSpace(actingMemberID)
	if organizationID == "" {
		return domain.Connection{}, fmt.Errorf("organization_id is required: %w", domain.ErrInvalid)
	}
	if strings.TrimSpace(connectionID) == "" {
		return domain.Connection{}, domain.ErrNotFound
	}
	// The service token names the one purpose it may be used for.
	if actor.Purpose != purpose {
		return domain.Connection{}, domain.ErrForbidden
	}
	if !actor.Credentials.Present() {
		connection, err := s.store.Connection(ctx, organizationID, connectionID)
		if err != nil {
			return domain.Connection{}, err
		}
		if err := domain.ServiceMayUse(connection, actingMemberID, purpose); err != nil {
			return domain.Connection{}, err
		}
		return connection, nil
	}
	// The heartbeat is connections' own; members act, trigger and clean up.
	if purpose == domain.PurposeHeartbeat {
		return domain.Connection{}, domain.ErrForbidden
	}
	membership, err := s.caller(ctx, actor.Credentials, organizationID)
	if err != nil {
		return domain.Connection{}, err
	}
	connection, caller, err := s.visibleConnection(ctx, actor.Credentials, membership, connectionID)
	if err != nil {
		return domain.Connection{}, err
	}
	if actingMemberID != "" && actingMemberID != membership.UserID {
		return domain.Connection{}, domain.ErrForbidden
	}
	if err := domain.MayUse(caller, connection); err != nil {
		return domain.Connection{}, err
	}
	return connection, nil
}

// issue returns the credential a connection hands out, and the connection as
// of that credential: after a renewal, its new version and granted scopes.
func (s *Service) issue(ctx context.Context, connection domain.Connection, app domain.App, provider providers.Provider, tokenName string) (domain.IssuedToken, domain.Connection, error) {
	if connection.CredentialType.KeyBased() {
		if tokenName != "" {
			return domain.IssuedToken{}, domain.Connection{}, fmt.Errorf("token %q is not in this connection's token set: %w", tokenName, domain.ErrInvalid)
		}
		sealed, err := s.connectionSecrets(ctx, connection)
		if err != nil {
			return domain.IssuedToken{}, domain.Connection{}, err
		}
		if sealed.Key == nil {
			return domain.IssuedToken{}, domain.Connection{}, errors.New("key-based connection holds no key")
		}
		value, placement, err := providers.KeyToken(provider, connection.CredentialType, *sealed.Key)
		if err != nil {
			return domain.IssuedToken{}, domain.Connection{}, err
		}
		token := issuedFor(connection)
		token.TokenType, token.AccessToken, token.Placement = string(connection.CredentialType), value, placement
		return token, connection, nil
	}

	grant, current, err := s.freshGrant(ctx, connection, app, provider)
	if err != nil {
		return domain.IssuedToken{}, domain.Connection{}, err
	}
	token := issuedFor(current)
	token.TokenType, token.AccessToken, token.Placement = "Bearer", grant.AccessToken, bearerPlacement
	if tokenName != "" {
		if token.AccessToken = grant.Tokens[tokenName]; token.AccessToken == "" {
			return domain.IssuedToken{}, domain.Connection{}, fmt.Errorf("token %q is not in this connection's token set: %w", tokenName, domain.ErrInvalid)
		}
	}
	if !grant.Expiry.IsZero() {
		expiry := grant.Expiry.UTC()
		token.ExpiresAt = &expiry
	}
	return token, current, nil
}

func issuedFor(connection domain.Connection) domain.IssuedToken {
	token := domain.IssuedToken{Scopes: connection.Scopes, ProviderAccount: connection.ProviderAccount}
	if token.Scopes == nil {
		token.Scopes = []string{}
	}
	return token
}

// freshGrant returns a connection's grant or minted token, renewed when it
// expires within domain.RenewBefore, with the connection as of that grant.
// One renewal runs at a time per connection, under a lease and the
// connection's version; other callers wait for it, up to domain.RenewWait,
// instead of renewing again.
func (s *Service) freshGrant(ctx context.Context, connection domain.Connection, app domain.App, provider providers.Provider) (providers.Grant, domain.Connection, error) {
	waitCtx, cancel := context.WithTimeout(ctx, domain.RenewWait)
	defer cancel()
	startedActive := connection.Status == domain.StatusActive
	for {
		sealed, err := s.connectionSecrets(ctx, connection)
		if err != nil {
			return providers.Grant{}, domain.Connection{}, err
		}
		now := s.clock()
		if sealed.Grant != nil && !renewDue(*sealed.Grant, now) {
			return *sealed.Grant, connection, nil
		}
		var done bool
		var grant providers.Grant
		var renewed domain.Connection
		if connection.CredentialType == domain.CredentialOAuth2 && (sealed.Grant == nil || sealed.Grant.RefreshToken == "") {
			// Nothing can renew this grant: use it while it lasts.
			if sealed.Grant != nil && usable(*sealed.Grant, now) {
				return *sealed.Grant, connection, nil
			}
			done, err = s.refused(ctx, connection, domain.ReconnectRefreshRejected)
		} else {
			grant, renewed, done, err = s.renew(ctx, connection, app, provider, sealed)
		}
		if done {
			return grant, renewed, err
		}
		// Another caller holds the lease, or the connection changed: wait for
		// it, then read the connection again.
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return providers.Grant{}, domain.Connection{}, ctx.Err()
			}
			return providers.Grant{}, domain.Connection{}, fmt.Errorf("%w: another renewal did not finish in time", domain.ErrProviderUnavailable)
		case <-time.After(renewPoll):
		}
		if connection, err = s.store.Connection(ctx, connection.OrganizationID, connection.ID); err != nil {
			return providers.Grant{}, domain.Connection{}, err
		}
		if connection.CredentialsDeletedAt != nil {
			return providers.Grant{}, domain.Connection{}, domain.ErrNotFound
		}
		// The renewal we waited for was refused for good.
		if startedActive && connection.Status == domain.StatusNeedsReconnect {
			return providers.Grant{}, domain.Connection{}, domain.ErrNeedsReconnect
		}
	}
}

// renew refreshes or mints under the connection's lease, and saves the result
// only if the connection's version has not moved since the lease was taken.
// done is false when another caller holds the lease, or the connection
// changed underneath, so the caller should wait and read the connection
// again.
func (s *Service) renew(ctx context.Context, connection domain.Connection, app domain.App, provider providers.Provider, sealed connectionSecrets) (providers.Grant, domain.Connection, bool, error) {
	leaseToken, err := s.randomToken(18)
	if err != nil {
		return providers.Grant{}, domain.Connection{}, true, err
	}
	now := s.clock()
	acquired, err := s.store.AcquireLease(ctx, connection.OrganizationID, connection.ID, connection.Version, leaseToken, now, now.Add(renewLease))
	if err != nil {
		return providers.Grant{}, domain.Connection{}, true, err
	}
	if !acquired {
		return providers.Grant{}, domain.Connection{}, false, nil
	}
	release := func() {
		if err := s.store.ReleaseLease(context.WithoutCancel(ctx), connection.OrganizationID, connection.ID, leaseToken); err != nil {
			s.logger.WarnContext(ctx, "release renewal lease", "connection_id", connection.ID, "error", err)
		}
	}

	opened, err := s.appSecrets(ctx, app)
	if err != nil {
		release()
		return providers.Grant{}, domain.Connection{}, true, err
	}
	renewed, err := s.renewWithProvider(ctx, connection, app, provider, opened, sealed)
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrCredentialRejected):
		release()
		done, err := s.refused(ctx, connection, renewalRejection(connection.CredentialType, err))
		return providers.Grant{}, domain.Connection{}, done, err
	default:
		release()
		// A token that has not actually expired still works.
		if sealed.Grant != nil && usable(*sealed.Grant, s.clock()) {
			return *sealed.Grant, connection, true, nil
		}
		return providers.Grant{}, domain.Connection{}, true, err
	}

	next := sealed
	next.Grant = &renewed
	encrypted, err := s.sealConnectionSecrets(ctx, connection, next)
	if err != nil {
		release()
		return providers.Grant{}, domain.Connection{}, true, err
	}
	var scopes []string
	if connection.CredentialType == domain.CredentialOAuth2 {
		scopes = domain.NormalizeScopes(renewed.Scopes)
	}
	version, err := s.store.SaveRenewedCredentials(ctx, connection.OrganizationID, connection.ID, leaseToken, connection.Version, encrypted, scopes, s.clock())
	if err != nil {
		release()
		if errors.Is(err, domain.ErrConflict) {
			// The connection changed while the provider was called, such as a
			// reconnect: its credentials win over this renewal.
			return providers.Grant{}, domain.Connection{}, false, nil
		}
		return providers.Grant{}, domain.Connection{}, true, err
	}
	current := connection
	current.Version, current.EncryptedCredentials = version, encrypted
	if scopes != nil {
		current.Scopes = scopes
	}
	return renewed, current, true, nil
}

func (s *Service) renewWithProvider(ctx context.Context, connection domain.Connection, app domain.App, provider providers.Provider, opened appSecrets, sealed connectionSecrets) (providers.Grant, error) {
	switch connection.CredentialType {
	case domain.CredentialOAuth2:
		renewed, err := s.oauth.Refresh(ctx, *provider.OAuth2, providers.RefreshRequest{
			ClientID: app.ClientID, ClientSecret: opened.ClientSecret, RefreshToken: sealed.Grant.RefreshToken, Scopes: connection.Scopes,
		})
		if err != nil {
			return providers.Grant{}, err
		}
		// A refresh that returns only the primary token keeps the rest of the
		// token set.
		if len(renewed.Tokens) == 0 {
			renewed.Tokens = sealed.Grant.Tokens
		}
		return renewed, nil
	case domain.CredentialOAuth2ClientCredentials:
		return s.oauth.Mint(ctx, provider, providers.MintRequest{ClientID: app.ClientID, ClientSecret: opened.ClientSecret, Tenant: sealed.Tenant})
	case domain.CredentialInstallation:
		return s.oauth.InstallationToken(ctx, provider, githubApp(app, opened), sealed.InstallationID)
	default:
		return providers.Grant{}, fmt.Errorf("%s credentials are not renewed: %w", connection.CredentialType, domain.ErrInvalid)
	}
}

// renewDue reports whether a grant must be renewed before it is issued.
func renewDue(grant providers.Grant, now time.Time) bool {
	return grant.AccessToken == "" || (!grant.Expiry.IsZero() && !now.Add(domain.RenewBefore).Before(grant.Expiry))
}

// usable reports whether a grant's access token has not expired yet.
func usable(grant providers.Grant, now time.Time) bool {
	return grant.AccessToken != "" && (grant.Expiry.IsZero() || now.Before(grant.Expiry))
}

func renewalRejection(credentialType domain.CredentialType, err error) domain.ReconnectReason {
	switch {
	case errors.Is(err, providers.ErrInstallationNotFound):
		return domain.ReconnectInstallationRemoved
	case credentialType == domain.CredentialOAuth2:
		return domain.ReconnectRefreshRejected
	default:
		return domain.ReconnectCredentialRejected
	}
}

// refused handles a provider refusing a connection's credentials for good
// while renewing. done is false when the connection was active but changed
// since it was read, such as a reconnect while the provider was called: the
// refusal was about credentials it no longer holds, so the caller should read
// it again rather than fail.
func (s *Service) refused(ctx context.Context, connection domain.Connection, reason domain.ReconnectReason) (bool, error) {
	if connection.Status != domain.StatusActive {
		return true, domain.ErrNeedsReconnect
	}
	marked, err := s.markNeedsReconnect(ctx, connection, reason)
	if err == nil && !marked {
		return false, nil
	}
	return true, domain.ErrNeedsReconnect
}

// markNeedsReconnect records a permanent credential failure seen on
// connection, and reports whether it moved the connection. A connection that
// is no longer active, or whose version changed since, stays as it is.
func (s *Service) markNeedsReconnect(ctx context.Context, connection domain.Connection, reason domain.ReconnectReason) (bool, error) {
	now := s.clock()
	marked, err := s.store.MarkNeedsReconnect(context.WithoutCancel(ctx), connection.OrganizationID, connection.ID, connection.Version, reason, now, s.needsReconnectEvent(now))
	if err != nil {
		s.logger.ErrorContext(ctx, "mark connection needs reconnect", "connection_id", connection.ID, "reason", reason, "error", err)
	}
	return marked, err
}

// providerFor returns an app's provider.
func (s *Service) providerFor(app domain.App) (providers.Provider, error) {
	provider, ok := s.providers.Lookup(app.Provider)
	if !ok || !provider.Supports(app.CredentialType) {
		return providers.Provider{}, fmt.Errorf("provider %q no longer supports %s apps", app.Provider, app.CredentialType)
	}
	return provider, nil
}
