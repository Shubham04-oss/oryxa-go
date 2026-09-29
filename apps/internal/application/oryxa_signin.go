package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
)

// recordFor bounds recording how a sign-in ended. It runs detached from the
// request: once an app has issued a credential, or refused one, a member's
// browser going away must not leave the connection saying otherwise.
const recordFor = 10 * time.Second

// OryxaConnect is what starting a connection through Oryxa's own sign-in
// answered.
type OryxaConnect struct {
	Connection domain.Connection
	// RedirectURL is where the member's browser goes to sign in, for OAuth.
	// It is empty for a key, a password or a client's credentials, which are
	// proved and kept at once, so the connection is already active.
	RedirectURL string
	// State is what the callback's state cookie holds, for OAuth: set
	// HttpOnly, Secure and SameSite=Lax on the callback's path, until
	// StateExpiresAt. Only its hash is stored.
	State          string
	StateExpiresAt time.Time
}

// ConnectOryxa starts connecting an app through Oryxa's own sign-in for the
// caller. For OAuth it records the pending connection and answers the app's
// authorization URL and the state the callback's cookie must hold. For a key,
// a password or a client's credentials it proves them with the app's test
// call and keeps them, and the connection is active at once; if the app
// refuses them, the connection is failed and the error names the field,
// never its value.
func (s *Service) ConnectOryxa(ctx context.Context, credentials authclient.Credentials, organizationID string, input ConnectInput) (OryxaConnect, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return OryxaConnect{}, err
	}
	if caller.Agent {
		return OryxaConnect{}, fmt.Errorf("an agent cannot connect an app: %w", domain.ErrForbidden)
	}
	toolkit, err := domain.CheckToolkit(input.Toolkit)
	if err != nil {
		return OryxaConnect{}, err
	}
	if err := s.allowToolkit(ctx, organizationID, toolkit); err != nil {
		return OryxaConnect{}, err
	}
	if err := s.allowEmployeeConnect(ctx, organizationID, caller.Role.IsOrgAdmin()); err != nil {
		return OryxaConnect{}, err
	}
	autoSubscribe, err := s.defaultAutoSubscribe(ctx, organizationID)
	if err != nil {
		return OryxaConnect{}, err
	}
	if err := s.allowConnect(organizationID, caller.UserID); err != nil {
		return OryxaConnect{}, err
	}
	manifest, err := s.enabled(toolkit)
	if err != nil {
		return OryxaConnect{}, err
	}
	method, err := methodOf(manifest, input.Kind)
	if err != nil {
		return OryxaConnect{}, err
	}
	returnTo := strings.TrimSpace(input.ReturnTo)
	if returnTo != "" && !s.returnAllowed(returnTo) {
		return OryxaConnect{}, fmt.Errorf("return_to is not an allowed page: %w", domain.ErrInvalid)
	}
	oauth := method.Kind == integration.KindOAuth2Code
	if oauth && !s.sameCookieHost(input.Host) {
		// The state cookie set on this answer would never come back to the
		// callback, so the member would sign in at the app for nothing.
		s.logger.ErrorContext(ctx, "an OAuth sign-in was started on a host its callback cannot read the state cookie from",
			"toolkit", toolkit, "request_host", input.Host, "redirect_host", s.oryxa.redirectHost)
		return OryxaConnect{}, fmt.Errorf("signing in to %s starts from %s, where its callback is: %w",
			toolkit, s.oryxa.redirectHost, domain.ErrNotConnectable)
	}
	if err := s.oneCurrentConnection(ctx, organizationID, caller.UserID, toolkit); err != nil {
		return OryxaConnect{}, err
	}

	connection, err := s.createOryxaConnection(ctx, organizationID, caller.UserID, toolkit, returnTo, input.AutoSubscribe, autoSubscribe)
	if err != nil {
		return OryxaConnect{}, err
	}
	if oauth {
		started, err := s.oryxa.Signin.StartOAuth(ctx, signin.StartRequest{
			OrganizationID: organizationID, MemberID: caller.UserID, ConnectionID: connection.ID,
			Toolkit: toolkit, Kind: method.Kind, Fields: input.Fields, ReturnTo: returnTo,
		})
		if err != nil {
			_, err := s.failOryxa(ctx, connection, err)
			return OryxaConnect{}, err
		}
		s.logger.InfoContext(ctx, "app sign-in started",
			"connection_id", connection.ID, "organization_id", organizationID, "toolkit", toolkit, "broker", domain.BrokerOryxa)
		return OryxaConnect{
			Connection: connection, RedirectURL: started.AuthorizationURL,
			State: started.State, StateExpiresAt: started.ExpiresAt,
		}, nil
	}

	saved, err := s.oryxa.Signin.SaveCredential(ctx, signin.CredentialRequest{
		OrganizationID: organizationID, ConnectionID: connection.ID, Toolkit: toolkit,
		Kind: method.Kind, Fields: input.Fields,
	})
	if err != nil {
		_, err := s.failOryxa(ctx, connection, err)
		return OryxaConnect{}, err
	}
	activated, err := s.activateOryxa(ctx, connection, saved.Account.Label)
	if err != nil {
		return OryxaConnect{}, err
	}
	return OryxaConnect{Connection: activated}, nil
}

// allowConnect spends one of a member's connects, answering
// domain.ErrRateLimited when none is left.
func (s *Service) allowConnect(organizationID, memberID string) error {
	if !s.connects.Allow(organizationID + "/" + memberID) {
		return fmt.Errorf("connecting and replacing keys is limited to %d a minute: %w", ConnectBudget, domain.ErrRateLimited)
	}
	return nil
}

// oneCurrentConnection refuses a member a second pending or active
// connection to one app, whichever broker either is made through.
func (s *Service) oneCurrentConnection(ctx context.Context, organizationID, memberID, toolkit string) error {
	current, err := s.store.Connections(ctx, organizationID, domain.ConnectionQuery{
		MemberID: memberID, Toolkit: toolkit,
		Statuses: []domain.ConnectionStatus{domain.ConnectionPending, domain.ConnectionActive},
	})
	if err != nil {
		return err
	}
	if len(current) > 0 {
		state := "already connected"
		if current[0].Status == domain.ConnectionPending {
			state = "already being connected"
		}
		return fmt.Errorf("%s is %s for this member: %w", toolkit, state, domain.ErrConflict)
	}
	return nil
}

// createOryxaConnection records a pending Oryxa connection, announcing that
// it began. It carries no state hash: an Oryxa sign-in's state lives in its
// flow, which signin keeps.
func (s *Service) createOryxaConnection(ctx context.Context, organizationID, memberID, toolkit, returnTo string, autoSubscribe *bool, defaultAutoSubscribe bool) (domain.Connection, error) {
	id, err := newID("apc")
	if err != nil {
		return domain.Connection{}, err
	}
	now := s.now().UTC()
	connection := domain.Connection{
		ID: id, OrganizationID: organizationID, MemberID: memberID, Toolkit: toolkit, Broker: domain.BrokerOryxa,
		Status: domain.ConnectionPending, ReturnTo: returnTo, AutoSubscribe: defaultAutoSubscribe,
		CreatedAt: now, UpdatedAt: now,
	}
	if autoSubscribe != nil {
		// Recorded as the member asked, as for Composio. Nothing subscribes
		// an Oryxa connection until its integration has triggers.
		connection.AutoSubscribe = *autoSubscribe
	}
	event, err := s.event(appsv1.ConnectionInitiatedDetailType, appsv1.ConnectionInitiated{
		ConnectionID: id, OrganizationID: organizationID, MemberID: memberID, Toolkit: toolkit,
		Broker: appsv1.BrokerOryxa, InitiatedAt: now,
	})
	if err != nil {
		return domain.Connection{}, err
	}
	if err := s.store.CreateConnection(ctx, connection, "", event); err != nil {
		return domain.Connection{}, err
	}
	return connection, nil
}

// activateOryxa makes a pending Oryxa connection active under the label its
// credential's test call named, and announces it. A connection that stopped
// waiting meanwhile (removed, or swept) is answered as it now is; the
// credential signin kept for it is revoked once it has ended.
func (s *Service) activateOryxa(ctx context.Context, connection domain.Connection, label string) (domain.Connection, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordFor)
	defer cancel()
	now := s.now().UTC()
	event, err := s.event(appsv1.ConnectionConnectedDetailType, appsv1.ConnectionConnected{
		ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		Toolkit: connection.Toolkit, Broker: appsv1.BrokerOryxa, ConnectedAt: now,
	})
	if err != nil {
		return domain.Connection{}, err
	}
	activated, changed, err := s.store.ActivateConnection(ctx, connection.ID, label, now, event)
	if err != nil {
		return domain.Connection{}, err
	}
	if !changed {
		s.logger.WarnContext(ctx, "an app signed in for a connection that no longer waited for it", "connection_id", connection.ID)
		return s.store.Connection(ctx, connection.OrganizationID, connection.ID)
	}
	s.logger.InfoContext(ctx, "app connected",
		"connection_id", connection.ID, "organization_id", connection.OrganizationID,
		"toolkit", connection.Toolkit, "broker", domain.BrokerOryxa)
	return activated, nil
}

// failOryxa fails a pending Oryxa connection whose sign-in ended without a
// credential, announcing why, and answers the connection as it now is and
// the error the caller is told. Nothing either carries quotes what a member
// typed.
func (s *Service) failOryxa(ctx context.Context, connection domain.Connection, cause error) (domain.Connection, error) {
	failed := failureOf(connection.Toolkit, cause)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordFor)
	defer cancel()
	now := s.now().UTC()
	event, err := s.event(appsv1.ConnectionFailedDetailType, appsv1.ConnectionFailed{
		ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		Toolkit: connection.Toolkit, Broker: appsv1.BrokerOryxa, Reason: failed.reason, FailedAt: now,
	})
	if err != nil {
		return connection, err
	}
	changed, err := s.store.FailConnection(ctx, connection.ID, failed.prose, now, event)
	if err != nil {
		// The sweep fails it once its sign-in could no longer finish.
		s.logger.ErrorContext(ctx, "record a failed app sign-in", "connection_id", connection.ID, "error", err)
		return connection, failed.err
	}
	s.logger.InfoContext(ctx, "app sign-in failed", "connection_id", connection.ID,
		"toolkit", connection.Toolkit, "reason", failed.reason)
	if changed {
		connection.Status, connection.StatusReason, connection.UpdatedAt = domain.ConnectionFailed, failed.prose, now
		return connection, failed.err
	}
	if current, err := s.store.Connection(ctx, connection.OrganizationID, connection.ID); err == nil {
		connection = current
	}
	return connection, failed.err
}

// SigninCallback is what a member's browser brought back to Oryxa's OAuth
// callback.
type SigninCallback struct {
	// State is the callback's state parameter, and CookieState the state
	// cookie the browser sent with it. They must be equal.
	State       string
	CookieState string
	Code        string
	// Error is the app's error parameter (access_denied), when it sent one.
	Error string
	// AccessCookie is the member's access cookie, when the browser sent one.
	// If it did, it must be the flow's own member's in the flow's own
	// organization.
	AccessCookie string
}

// CompleteSignin finishes the OAuth sign-in a callback names: signin checks
// the state against its cookie, spends the flow, exchanges the code and runs
// the test call, and the connection becomes active or failed, announced
// either way. It answers where the browser goes, as the Composio callback
// does. An error means no sign-in could be named at all: the state is
// missing, not the cookie's, spent or expired.
func (s *Service) CompleteSignin(ctx context.Context, callback SigninCallback) (Completion, error) {
	if s.oryxa == nil {
		return Completion{}, errOryxaOff
	}
	completion, err := s.oryxa.Signin.CompleteOAuth(ctx, signin.Callback{
		State: callback.State, CookieState: callback.CookieState, Code: callback.Code, Error: callback.Error,
		Verify: s.verifyCallback(callback.AccessCookie),
	})
	if completion.ConnectionID == "" {
		if err == nil {
			err = errors.New("a sign-in finished without naming its connection")
		}
		return Completion{}, err
	}
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordFor)
	defer cancel()
	connection, readErr := s.store.Connection(record, completion.OrganizationID, completion.ConnectionID)
	if readErr != nil {
		return Completion{}, readErr
	}
	switch {
	case err == nil:
		if connection, err = s.activateOryxa(record, connection, completion.Account.Label); err != nil {
			return Completion{}, err
		}
	case errors.Is(err, signin.ErrNotPending):
		// Removed or failed while the member was at the app: what the app
		// issued was revoked, and the connection says what it is.
		s.logger.InfoContext(record, "an app sign-in came back for a connection that no longer waited for it",
			"connection_id", connection.ID, "status", string(connection.Status))
	default:
		connection, _ = s.failOryxa(record, connection, err)
	}
	return Completion{Connection: connection, ReturnTo: completion.ReturnTo}, nil
}

// verifyCallback is the check an access cookie on a callback must pass: when
// the browser is signed in to Oryxa at all, it must be as the member who
// started the sign-in, in the organization they started it in. A cookie auth
// does not take for a session is no one signed in, and is not told apart.
func (s *Service) verifyCallback(accessCookie string) func(ctx context.Context, organizationID, memberID string) error {
	if accessCookie == "" {
		return nil
	}
	return func(ctx context.Context, organizationID, memberID string) error {
		membership, err := s.directory.Membership(ctx, organizationID, authclient.Credentials{AccessCookie: accessCookie})
		switch {
		case errors.Is(err, domain.ErrUnauthorized):
			return nil
		case errors.Is(err, domain.ErrUnavailable), errors.Is(err, domain.ErrRateLimited):
			return &signin.Failure{Reason: signin.ReasonUnavailable, Rule: "auth did not answer"}
		case err != nil:
			return fmt.Errorf("the browser is signed in outside the organization the sign-in began in: %w", err)
		case membership.Agent || membership.UserID != memberID:
			return errors.New("the browser is signed in as another member than the one who began the sign-in")
		}
		return nil
	}
}

// ReplaceCredentialInput is a member giving an Oryxa connection a new key, a
// new password or new client credentials.
type ReplaceCredentialInput struct {
	// Kind is the credential's kind; empty means the kind it holds. A
	// connection holds one kind for life.
	Kind string
	// Fields are the kind's standard fields and the method's
	// connection-level ones, as when connecting.
	Fields map[string]string
}

// ReplaceCredential replaces the key, the password or the client credentials
// an active Oryxa connection holds, once the app accepts the new one in its
// test call; until it does, the old one is kept. It is the member's own
// connection alone: an admin can see another member's, never give it a key.
func (s *Service) ReplaceCredential(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string, input ReplaceCredentialInput) (domain.Connection, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Connection{}, err
	}
	if caller.Agent {
		return domain.Connection{}, fmt.Errorf("an agent cannot replace a connection's key: %w", domain.ErrForbidden)
	}
	if err := s.allowConnect(organizationID, caller.UserID); err != nil {
		return domain.Connection{}, err
	}
	if !s.on() {
		return domain.Connection{}, errOryxaOff
	}
	connection, err := s.store.Connection(ctx, organizationID, strings.TrimSpace(connectionID))
	if err != nil {
		return domain.Connection{}, err
	}
	if connection.MemberID != caller.UserID {
		return domain.Connection{}, domain.ErrNotFound
	}
	// Replacing a key establishes access to the provider just as connecting
	// does. An existing connection must not bypass a changed workspace policy.
	if err := s.allowToolkit(ctx, organizationID, connection.Toolkit); err != nil {
		return domain.Connection{}, err
	}
	if err := s.allowEmployeeConnect(ctx, organizationID, caller.Role.IsOrgAdmin()); err != nil {
		return domain.Connection{}, err
	}
	if !connection.Oryxa() {
		return domain.Connection{}, fmt.Errorf("the %s connection's credential is Composio's, so it is replaced by connecting again: %w",
			connection.Toolkit, domain.ErrInvalid)
	}
	if _, err := s.enabled(connection.Toolkit); err != nil {
		return domain.Connection{}, err
	}
	switch connection.Status {
	case domain.ConnectionActive:
	case domain.ConnectionExpired:
		return domain.Connection{}, fmt.Errorf("the %s connection expired, so it is connected again rather than given a new key: %w",
			connection.Toolkit, domain.ErrNeedsReconnect)
	default:
		return domain.Connection{}, fmt.Errorf("the %s connection is %s: %w", connection.Toolkit, connection.Status, domain.ErrNotConnected)
	}
	kind := strings.TrimSpace(input.Kind)
	switch {
	case connection.CredentialKind == "":
		return domain.Connection{}, fmt.Errorf("the %s connection holds no credential: %w", connection.Toolkit, domain.ErrNotConnected)
	case connection.CredentialKind == integration.KindOAuth2Code:
		return domain.Connection{}, fmt.Errorf("the %s connection signed in with OAuth, so it is connected again rather than given a key: %w",
			connection.Toolkit, domain.ErrInvalid)
	case kind != "" && kind != connection.CredentialKind:
		return domain.Connection{}, fmt.Errorf("the %s connection holds a %s credential, not a %s one: %w",
			connection.Toolkit, connection.CredentialKind, kind, domain.ErrInvalid)
	}

	saved, err := s.oryxa.Signin.SaveCredential(ctx, signin.CredentialRequest{
		OrganizationID: organizationID, ConnectionID: connection.ID, Toolkit: connection.Toolkit,
		Kind: connection.CredentialKind, Fields: input.Fields,
	})
	if err != nil {
		var failed *signin.Failure
		if errors.As(err, &failed) {
			// Nothing was kept: the connection goes on with the old key.
			return domain.Connection{}, failureOf(connection.Toolkit, err).err
		}
		return domain.Connection{}, err
	}

	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordFor)
	defer cancel()
	now := s.now().UTC()
	event, err := s.event(appsv1.CredentialReplacedDetailType, appsv1.CredentialReplaced{
		ConnectionID: connection.ID, OrganizationID: organizationID, MemberID: connection.MemberID,
		Toolkit: connection.Toolkit, Kind: saved.Kind, ReplacedAt: now,
	})
	if err != nil {
		return domain.Connection{}, err
	}
	relabelled, changed, err := s.oryxa.Store.RelabelConnection(record, connection.ID, saved.Account.Label, now, event)
	if err != nil {
		return domain.Connection{}, err
	}
	if !changed {
		// Removed while the key was being tried; its credential is revoked
		// with the rest of what it held.
		return domain.Connection{}, fmt.Errorf("the %s connection was removed meanwhile: %w", connection.Toolkit, domain.ErrNotConnected)
	}
	s.logger.InfoContext(ctx, "app credential replaced", "connection_id", connection.ID,
		"organization_id", organizationID, "toolkit", connection.Toolkit, "kind", saved.Kind)
	return relabelled, nil
}
