package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/nativeclient"
	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// ConnectInput is what a member asks to connect.
type ConnectInput struct {
	Toolkit string
	// ReturnTo is where their browser goes once they finish; it must be under
	// one of the configured return origins.
	ReturnTo string
	// Alias is an optional provider-facing label for the account. Agents do
	// not route by it: a member has one current connection per app.
	Alias string
	// AutoSubscribe says whether connecting should subscribe to everything
	// this app can deliver. Unset means the deployment's own default, which
	// is to subscribe.
	AutoSubscribe *bool

	// Kind, Fields and Host are Oryxa's own sign-in's (ConnectOryxa).
	//
	// Kind picks the manifest's method by its kind; empty is its first.
	Kind string
	// Fields are what the member typed: a key's standard fields (token;
	// username and password; client_id and client_secret) and the method's
	// connection-level ones. They are never logged or answered.
	Fields map[string]string
	// Host is the host the request was sent to. The browser keeps the state
	// cookie for that host alone, so an OAuth sign-in starts only from the
	// redirect URI's host, and a Composio or Pipedream callback goes back to
	// it (callbackBase).
	Host string
	// BrowserNonce is a random value the HTTP layer keeps in an HttpOnly
	// cookie of the browser that started the connect, so that only that
	// browser can finish it (#476, #477). Composio's and Pipedream's
	// callbacks carry it as their state; the native sign-in keeps its hash.
	// Empty there means a new state, which only a browser signed in as the
	// member can finish.
	BrowserNonce string
}

// callbackPath is where Composio sends a member's browser back to.
const callbackPath = "/v1/apps/connect/callback"

// userSuppliedSchemes are auth schemes whose credential the connecting member
// types into Composio's hosted page themselves, so they need no app of
// Oryxa's.
var userSuppliedSchemes = []string{"API_KEY", "BEARER_TOKEN", "BASIC"}

// Connect starts connecting an app for the caller: it makes sure the project
// has an auth config for the app, asks Composio for a link, and records the
// pending connection. The member finishes in their browser, at RedirectURL.
func (s *Service) Connect(ctx context.Context, credentials authclient.Credentials, organizationID string, input ConnectInput) (domain.Connection, string, error) {
	// A native connect records nothing here: the connection's row arrives
	// with the integrations service's connection.created event once the
	// member finishes, so the answer is the redirect alone.
	if toolkit, err := domain.CheckToolkit(input.Toolkit); err == nil && s.nativeServes(toolkit) {
		redirect, err := s.connectNative(ctx, credentials, organizationID, input)
		return domain.Connection{}, redirect, err
	}
	start, err := s.startConnect(ctx, credentials, organizationID, input)
	if err != nil {
		return domain.Connection{}, "", err
	}
	config, err := s.authConfig(ctx, start.toolkit)
	if err != nil {
		return domain.Connection{}, "", err
	}

	state, stateHash, err := connectState(input)
	if err != nil {
		return domain.Connection{}, "", err
	}
	userID := domain.ComposioUserID(organizationID, start.memberID)
	link, err := s.composio.CreateConnectLink(ctx, composio.LinkRequest{
		AuthConfigID: config.AuthConfigID,
		UserID:       userID,
		CallbackURL:  s.callbackBase(input.Host) + callbackPath + "?state=" + url.QueryEscape(state),
		Alias:        strings.TrimSpace(input.Alias),
	})
	if err != nil {
		return domain.Connection{}, "", composioError("start the connection", err)
	}

	connection := start.connection(organizationID, s.now().UTC())
	connection.AuthConfigID, connection.ComposioUserID, connection.ComposioAccountID = config.AuthConfigID, userID, link.ConnectedAccountID
	connection, err = s.recordPending(ctx, connection, stateHash)
	if err != nil {
		// A concurrent connect may have won the one-current-connection slot
		// after our preflight. Do not leave the losing provider account alive
		// and untracked with standing access to the member's app.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if cleanupErr := s.composio.DeleteConnectedAccount(cleanup, link.ConnectedAccountID, true); cleanupErr != nil {
			s.logger.ErrorContext(ctx, "revoke unrecorded app connection",
				"organization_id", organizationID, "member_id", start.memberID,
				"toolkit", start.toolkit, "error", cleanupErr)
		}
		return domain.Connection{}, "", err
	}
	return connection, link.RedirectURL, nil
}

// connectStart is what a connect through Composio or Pipedream has settled
// before it asks its broker for anything.
type connectStart struct {
	memberID, toolkit, returnTo string
	autoSubscribe               bool
}

// startConnect checks everything a connect through Composio or Pipedream must
// pass before its broker is called: who is asking, whether the app and the
// return page are allowed, and that the member has no current connection to
// the app already.
func (s *Service) startConnect(ctx context.Context, credentials authclient.Credentials, organizationID string, input ConnectInput) (connectStart, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return connectStart{}, err
	}
	if caller.Agent {
		return connectStart{}, fmt.Errorf("an agent cannot connect an app: %w", domain.ErrForbidden)
	}
	toolkit, err := domain.CheckToolkit(input.Toolkit)
	if err != nil {
		return connectStart{}, err
	}
	if err := s.allowToolkit(ctx, organizationID, toolkit); err != nil {
		return connectStart{}, err
	}
	if err := s.allowEmployeeConnect(ctx, organizationID, caller.Role.IsOrgAdmin()); err != nil {
		return connectStart{}, err
	}
	autoSubscribe, err := s.defaultAutoSubscribe(ctx, organizationID)
	if err != nil {
		return connectStart{}, err
	}
	if err := s.allowConnect(organizationID, caller.UserID); err != nil {
		return connectStart{}, err
	}
	if err := s.oneCurrentConnection(ctx, organizationID, caller.UserID, toolkit); err != nil {
		return connectStart{}, err
	}
	returnTo := strings.TrimSpace(input.ReturnTo)
	if returnTo != "" && !s.returnAllowed(returnTo) {
		return connectStart{}, fmt.Errorf("return_to is not an allowed page: %w", domain.ErrInvalid)
	}
	if input.AutoSubscribe != nil {
		// Recorded either way; whether it is ever acted on is a separate
		// question, decided at subscribe time by s.autoSubscribe. The
		// deployment switch (APPS_AUTO_SUBSCRIBE=false) is a kill switch, not
		// a default it merely falls back to: off means nothing is
		// auto-subscribed, whatever this connection asked for.
		autoSubscribe = *input.AutoSubscribe
	}
	return connectStart{memberID: caller.UserID, toolkit: toolkit, returnTo: returnTo, autoSubscribe: autoSubscribe}, nil
}

// connection is the pending connection a started connect records, before
// anything its broker answered is added to it.
func (start connectStart) connection(organizationID string, now time.Time) domain.Connection {
	return domain.Connection{
		OrganizationID: organizationID, MemberID: start.memberID, Toolkit: start.toolkit,
		Status: domain.ConnectionPending, ReturnTo: start.returnTo, AutoSubscribe: start.autoSubscribe,
		CreatedAt: now, UpdatedAt: now,
	}
}

// recordPending gives a pending connection its id and records it with the
// hash of its callback's state, announcing that it began.
func (s *Service) recordPending(ctx context.Context, connection domain.Connection, stateHash string) (domain.Connection, error) {
	id, err := newID("apc")
	if err != nil {
		return domain.Connection{}, err
	}
	connection.ID = id
	// Announced as it starts, so what follows — connected or failed — is
	// always the end of something the bus saw begin.
	event, err := s.event(appsv1.ConnectionInitiatedDetailType, appsv1.ConnectionInitiated{
		ConnectionID: id, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		Toolkit: connection.Toolkit, Broker: eventBroker(connection), InitiatedAt: connection.CreatedAt,
	})
	if err != nil {
		return domain.Connection{}, err
	}
	if err := s.store.CreateConnection(ctx, connection, stateHash, event); err != nil {
		return domain.Connection{}, err
	}
	s.connectionStarted(ctx, connection)
	s.logger.InfoContext(ctx, "app connection started", "connection_id", id,
		"organization_id", connection.OrganizationID, "toolkit", connection.Toolkit)
	return connection, nil
}

// authConfig is the auth config the project connects toolkit with, found or
// created the first time anyone connects it.
func (s *Service) authConfig(ctx context.Context, toolkit string) (AuthConfig, error) {
	recorded, err := s.store.AuthConfig(ctx, toolkit)
	if err == nil {
		return recorded, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return AuthConfig{}, err
	}

	// A config the project already has, made in Composio's dashboard or by an
	// earlier deployment, is reused rather than duplicated.
	existing, err := s.composio.AuthConfigs(ctx, composio.AuthConfigQuery{Toolkit: toolkit, Limit: 50})
	if err != nil {
		return AuthConfig{}, composioError("read the app's auth configs", err)
	}
	var chosen *composio.AuthConfig
	for index, candidate := range existing.Items {
		if !candidate.Enabled || candidate.Toolkit != toolkit {
			continue
		}
		if chosen == nil || (candidate.IsComposioManaged && !chosen.IsComposioManaged) {
			chosen = &existing.Items[index]
		}
	}
	if chosen == nil {
		created, err := s.createAuthConfig(ctx, toolkit)
		if err != nil {
			return AuthConfig{}, err
		}
		chosen = &created
	}
	return s.store.SaveAuthConfig(ctx, AuthConfig{
		Toolkit: toolkit, AuthConfigID: chosen.ID, AuthScheme: chosen.AuthScheme,
		ComposioManaged: chosen.IsComposioManaged, CreatedAt: s.now().UTC(),
	})
}

// createAuthConfig makes the first auth config for an app: Composio's own app
// when Composio has one, otherwise a scheme whose credential the member types
// in. An app that needs an OAuth app of Oryxa's cannot be connected yet.
func (s *Service) createAuthConfig(ctx context.Context, toolkit string) (composio.AuthConfig, error) {
	described, err := s.composio.Toolkit(ctx, toolkit)
	if err != nil {
		if errors.Is(err, composio.ErrNotFound) {
			return composio.AuthConfig{}, fmt.Errorf("no app is called %s: %w", toolkit, domain.ErrNotFound)
		}
		return composio.AuthConfig{}, composioError("read the app", err)
	}
	name := "oryxa-" + toolkit
	if len(described.ComposioManagedAuthSchemes) > 0 {
		created, err := s.composio.CreateManagedAuthConfig(ctx, toolkit, name)
		return created, composioError("set up the app", err)
	}
	for _, scheme := range described.AuthSchemes {
		if slices.Contains(userSuppliedSchemes, scheme) {
			created, err := s.composio.CreateUserSuppliedAuthConfig(ctx, toolkit, scheme, name)
			return created, composioError("set up the app", err)
		}
	}
	if described.NoAuth {
		return composio.AuthConfig{}, fmt.Errorf("%s needs no connection: %w", toolkit, domain.ErrNotConnectable)
	}
	return composio.AuthConfig{}, fmt.Errorf("%s needs an OAuth app of Oryxa's own, which is not set up: %w", toolkit, domain.ErrNotConnectable)
}

// Completion is where a member's browser goes once a connection finished,
// or did not.
type Completion struct {
	Connection domain.Connection
	ReturnTo   string
}

// connectPolls is how many times, a second apart, a callback reads the
// account back before leaving a connection pending: Composio may redirect
// a moment before it records the account active.
const connectPolls = 5

// ConnectCallback is what a member's browser brought back from Composio or
// Pipedream.
type ConnectCallback struct {
	State string
	// Status and AccountID are what the broker reported: hints only.
	Status, AccountID string
	// CookieState is the state cookie the browser sent, and AccessCookie
	// its Oryxa session, when it sent them.
	CookieState, AccessCookie string
}

// CompleteConnection finishes the connection a callback's state names, once
// the browser proves it is the one that started it (checkBrowser). The
// callback's own query is only a hint: the account is read back from
// Composio, and must belong to the member and app the connection was
// started for.
func (s *Service) CompleteConnection(ctx context.Context, callback ConnectCallback) (Completion, error) {
	state, reportedStatus, reportedAccountID := callback.State, callback.Status, callback.AccountID
	if state == "" {
		return Completion{}, fmt.Errorf("the callback carries no state: %w", domain.ErrInvalid)
	}
	connection, err := s.store.PendingConnection(ctx, hashState(state))
	if err != nil {
		return Completion{}, err
	}
	if !connection.Composio() {
		// Every other broker's connect comes back through its own callback;
		// Composio's never finishes one.
		return Completion{}, domain.ErrNotFound
	}
	if err := s.checkBrowser(ctx, connection, callback); err != nil {
		return Completion{}, err
	}
	if reportedAccountID != "" && reportedAccountID != connection.ComposioAccountID {
		s.logger.WarnContext(ctx, "callback named another account", "connection_id", connection.ID)
		return Completion{}, s.refuse(ctx, connection, "the callback named another account", appsv1.FailedWrongAccount)
	}

	for attempt := range connectPolls {
		account, err := s.composio.ConnectedAccount(ctx, connection.ComposioAccountID)
		if err != nil {
			return Completion{}, composioError("read the connected account", err)
		}
		if account.UserID != connection.ComposioUserID || account.Toolkit != connection.Toolkit ||
			(account.AuthConfigID != "" && account.AuthConfigID != connection.AuthConfigID) {
			s.logger.WarnContext(ctx, "connected account belongs to someone else", "connection_id", connection.ID)
			return Completion{}, s.refuse(ctx, connection, "the account belongs to another member or app", appsv1.FailedWrongAccount)
		}
		switch account.Status {
		case composio.StatusActive:
			return s.activate(ctx, connection, account)
		case composio.StatusFailed, composio.StatusExpired, composio.StatusRevoked, composio.StatusInactive:
			if err := s.fail(ctx, connection, strings.ToLower(account.Status), appsv1.FailedDeniedAtProvider); err != nil {
				return Completion{}, err
			}
			connection.Status = domain.ConnectionFailed
			return Completion{Connection: connection, ReturnTo: connection.ReturnTo}, nil
		}
		if reportedStatus == "failed" {
			if err := s.fail(ctx, connection, "the member did not finish connecting", appsv1.FailedDeniedAtProvider); err != nil {
				return Completion{}, err
			}
			connection.Status = domain.ConnectionFailed
			return Completion{Connection: connection, ReturnTo: connection.ReturnTo}, nil
		}
		if attempt < connectPolls-1 {
			if err := s.wait(ctx, time.Second); err != nil {
				return Completion{}, err
			}
		}
	}
	// Still initiating: the member can come back to it, and the sweep fails
	// it once its link has expired.
	return Completion{Connection: connection, ReturnTo: connection.ReturnTo}, nil
}

func (s *Service) activate(ctx context.Context, connection domain.Connection, account composio.ConnectedAccount) (Completion, error) {
	now := s.now().UTC()
	label := account.Alias
	if label == "" {
		label = account.WordID
	}
	event, err := s.event(appsv1.ConnectionConnectedDetailType, appsv1.ConnectionConnected{
		ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		Toolkit: connection.Toolkit, ConnectedAt: now,
	})
	if err != nil {
		return Completion{}, err
	}
	activated, changed, err := s.store.ActivateConnection(ctx, connection.ID, label, now, event)
	if err != nil {
		return Completion{}, err
	}
	if changed {
		s.logger.InfoContext(ctx, "app connected",
			"connection_id", connection.ID, "organization_id", connection.OrganizationID, "toolkit", connection.Toolkit)
		if s.autoSubscribe && activated.AutoSubscribe {
			// The member is about to land back in the app and look for
			// what it will now tell them.
			s.wakeSubscriber()
		}
	}
	return Completion{Connection: activated, ReturnTo: connection.ReturnTo}, nil
}

// fail marks a pending connection failed. Reason is what a member reads, in
// this service's own words or Composio's status; cause is the one of
// appsv1's Failed constants the bus carries, since an event's reason is
// grouped and counted and must not be prose that changes.
func (s *Service) fail(ctx context.Context, connection domain.Connection, reason, cause string) error {
	now := s.now().UTC()
	event, err := s.event(appsv1.ConnectionFailedDetailType, appsv1.ConnectionFailed{
		ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		Toolkit: connection.Toolkit, Reason: cause, FailedAt: now,
	})
	if err != nil {
		return err
	}
	if _, err := s.store.FailConnection(ctx, connection.ID, reason, now, event); err != nil {
		return err
	}
	return nil
}

// refuse fails a connection whose callback cannot be believed, and says so:
// its state is spent, so the same trick cannot be tried against it again.
func (s *Service) refuse(ctx context.Context, connection domain.Connection, reason, cause string) error {
	if err := s.fail(ctx, connection, reason, cause); err != nil {
		return err
	}
	return fmt.Errorf("%s: %w", reason, domain.ErrForbidden)
}

// Connections lists the caller's connections, or with Organization set, an
// org admin's view of everyone's.
func (s *Service) Connections(ctx context.Context, credentials authclient.Credentials, organizationID, toolkit string, organization bool) ([]domain.Connection, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	query := domain.ConnectionQuery{MemberID: caller.UserID}
	if organization && caller.ActsAsAdmin() {
		query.MemberID = ""
	}
	if toolkit != "" {
		if query.Toolkit, err = domain.CheckToolkit(toolkit); err != nil {
			return nil, err
		}
	}
	connections, err := s.store.Connections(ctx, organizationID, query)
	if err != nil {
		return nil, err
	}
	// The query already kept the caller to what the code lets them see; the
	// engine may only narrow that, asked once for the whole list.
	allowed, err := s.decideMany(ctx, caller, connections, access.ActionRead)
	if err != nil {
		return nil, err
	}
	visible := connections[:0]
	for i, connection := range connections {
		if allowed[i] {
			visible = append(visible, connection)
		}
	}
	return visible, nil
}

// Connection reads one connection, for the member it belongs to or an org
// admin. Anyone else learns nothing, not even that it exists.
func (s *Service) Connection(ctx context.Context, credentials authclient.Credentials, organizationID, id string) (domain.Connection, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Connection{}, err
	}
	connection, err := s.store.Connection(ctx, organizationID, id)
	if err != nil {
		return domain.Connection{}, err
	}
	allowed := connection.MemberID == caller.UserID || caller.ActsAsAdmin()
	if err := s.decide(ctx, caller, connection, access.ActionRead, allowed); err != nil {
		return domain.Connection{}, err
	}
	return connection, nil
}

// RemoveConnection disconnects an app: Composio deletes the account and
// revokes its credential at the app, then the connection and its triggers
// are recorded removed.
func (s *Service) RemoveConnection(ctx context.Context, credentials authclient.Credentials, organizationID, id string) error {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return err
	}
	if caller.Agent {
		return fmt.Errorf("an agent cannot remove a connection: %w", domain.ErrForbidden)
	}
	connection, err := s.store.Connection(ctx, organizationID, id)
	if err != nil {
		return err
	}
	if err := s.decide(ctx, caller, connection, access.ActionDelete, connection.MemberID == caller.UserID || caller.Role.IsOrgAdmin()); err != nil {
		return err
	}
	reason := appsv1.RemovedByMember
	if connection.MemberID != caller.UserID {
		if !caller.Role.IsOrgAdmin() {
			return domain.ErrNotFound
		}
		reason = appsv1.RemovedByAdmin
	}
	return s.remove(ctx, connection, reason)
}

// remove deletes a connection at Composio and records it removed. Deleting
// what Composio no longer has is not an error, so a retry after a failed
// record finishes the job. An Oryxa connection is revoked at its app instead.
func (s *Service) remove(ctx context.Context, connection domain.Connection, reason string) error {
	if connection.Status == domain.ConnectionRemoved {
		return nil
	}
	if connection.Oryxa() {
		return s.removeOryxa(ctx, connection, reason)
	}
	if connection.Pipedream() {
		return s.removePipedream(ctx, connection, reason)
	}
	if connection.Native() {
		// The credential is the integrations service's: it is revoked there
		// before the row is retired, as with every other broker, so a member
		// who disconnects (or leaves) leaves nothing live. One the service no
		// longer knows is already gone.
		if s.native == nil {
			return fmt.Errorf("remove the connection: the integrations service is not configured: %w", domain.ErrUnavailable)
		}
		if err := s.native.Remove(ctx, connection.OrganizationID, connection.NativeConnectionID); err != nil && !errors.Is(err, nativeclient.ErrNotFound) {
			s.logger.ErrorContext(ctx, "remove a native connection", "connection_id", connection.ID, "error", err)
			return fmt.Errorf("remove the connection: the integrations service did not answer: %w", domain.ErrUnavailable)
		}
		return s.recordRemoved(ctx, connection, reason)
	}
	if !connection.Composio() {
		// Deleting at Composio would leave the account alive wherever it
		// really is, while recording it removed.
		return errNotComposio(connection)
	}
	triggers, err := s.store.Triggers(ctx, connection.OrganizationID, TriggerQuery{ConnectionID: connection.ID})
	if err != nil {
		return err
	}
	for _, trigger := range triggers {
		if err := s.composio.DeleteTrigger(ctx, trigger.ComposioTriggerID); err != nil && !errors.Is(err, composio.ErrNotFound) {
			return composioError("remove the connection's trigger", err)
		}
	}
	if connection.ComposioAccountID != "" {
		if err := s.composio.DeleteConnectedAccount(ctx, connection.ComposioAccountID, true); err != nil && !errors.Is(err, composio.ErrNotFound) {
			return composioError("remove the connected account", err)
		}
	}
	return s.recordRemoved(ctx, connection, reason)
}

// recordRemoved records a connection removed, with its triggers, once its
// broker no longer holds its account.
func (s *Service) recordRemoved(ctx context.Context, connection domain.Connection, reason string) error {
	now := s.now().UTC()
	removed, err := s.store.RemoveConnection(ctx, connection.ID, reason, now, func(removed domain.Connection, _ []domain.Trigger) ([]domain.Event, error) {
		event, err := s.event(appsv1.ConnectionRemovedDetailType, appsv1.ConnectionRemoved{
			ConnectionID: removed.ID, OrganizationID: removed.OrganizationID, MemberID: removed.MemberID,
			Toolkit: removed.Toolkit, Broker: eventBroker(removed), Reason: reason, RemovedAt: now,
		})
		if err != nil {
			return nil, err
		}
		return []domain.Event{event}, nil
	})
	if err != nil {
		return err
	}
	if removed {
		s.logger.InfoContext(ctx, "app connection removed",
			"connection_id", connection.ID, "organization_id", connection.OrganizationID, "reason", reason)
	}
	return nil
}

// returnAllowed says whether target is one of the return origins or a page
// beneath one, refusing the tricks a redirect target can play.
func (s *Service) returnAllowed(target string) bool { return returnAllowed(s.returnOrigins, target) }

func returnAllowed(origins []*url.URL, target string) bool {
	parsed, err := url.Parse(target)
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	// A browser resolves dot segments, which could climb out of an allowed
	// path; the path is decoded, so encoded dots are caught too.
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	for _, origin := range origins {
		if !strings.EqualFold(parsed.Scheme, origin.Scheme) || !strings.EqualFold(parsed.Host, origin.Host) {
			continue
		}
		base := strings.TrimSuffix(origin.Path, "/")
		if base == "" || parsed.Path == base || strings.HasPrefix(parsed.Path, base+"/") {
			return true
		}
	}
	return false
}

// RemovedWrongBrowser is why a pending connection was removed when its
// callback came from a browser that did not start it, as a forwarded link's
// does.
const RemovedWrongBrowser = "callback_from_another_browser"

// checkBrowser refuses a callback from any browser but the one that started
// the connect (#476). It must bring back the state cookie, or be signed in as
// the connection's member; signed in as anyone else, it is refused even with
// the cookie. A refused connection is removed with whatever its broker made
// for it, so a forwarded link leaves the sender nothing.
func (s *Service) checkBrowser(ctx context.Context, connection domain.Connection, callback ConnectCallback) error {
	sameBrowser := callback.CookieState != "" &&
		subtle.ConstantTimeCompare([]byte(callback.CookieState), []byte(callback.State)) == 1
	sameMember := false
	if callback.AccessCookie != "" {
		membership, err := s.directory.Membership(ctx, connection.OrganizationID, authclient.Credentials{AccessCookie: callback.AccessCookie})
		switch {
		case errors.Is(err, domain.ErrUnauthorized):
			// A cookie auth does not take for a session is no one signed in.
		case errors.Is(err, domain.ErrUnavailable), errors.Is(err, domain.ErrRateLimited),
			errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return err
		case err != nil || membership.Agent || membership.UserID != connection.MemberID:
			return s.refuseBrowser(ctx, connection, "the browser is signed in as someone other than the member who began connecting")
		default:
			sameMember = true
		}
	}
	if !sameBrowser && !sameMember {
		return s.refuseBrowser(ctx, connection, "the callback came from another browser than the one that began connecting")
	}
	return nil
}

func (s *Service) refuseBrowser(ctx context.Context, connection domain.Connection, reason string) error {
	s.logger.WarnContext(ctx, "connect callback refused", "connection_id", connection.ID, "reason", reason)
	if connection.Pipedream() && s.pipedream != nil {
		// Pipedream makes the account only as the member finishes; find it
		// so it goes too.
		if account, found, err := s.newPipedreamAccount(ctx, connection); err == nil && found {
			connection.PipedreamAccountID = account.ID
		}
	}
	if err := s.remove(ctx, connection, RemovedWrongBrowser); err != nil {
		return err
	}
	return fmt.Errorf("%s: %w", reason, domain.ErrForbidden)
}

// connectState is a Composio or Pipedream connect's callback state and its
// hash: the browser's nonce when the HTTP layer set one, else a new one.
func connectState(input ConnectInput) (string, string, error) {
	if input.BrowserNonce != "" {
		return input.BrowserNonce, hashState(input.BrowserNonce), nil
	}
	return newState()
}

// callbackBase is the origin a Composio or Pipedream callback goes back to:
// the return origin the connect was sent to, so the state cookie set on its
// answer comes back too; else the first return origin, the web app's, where
// a member's session cookie lives (a connect started from the CLI); else the
// public base URL.
func (s *Service) callbackBase(host string) string {
	for _, origin := range s.returnOrigins {
		if strings.EqualFold(origin.Host, host) {
			return origin.Scheme + "://" + origin.Host
		}
	}
	if len(s.returnOrigins) > 0 {
		return s.returnOrigins[0].Scheme + "://" + s.returnOrigins[0].Host
	}
	return s.publicBaseURL
}

// newState is a callback state and the hash apps keeps of it. Only the member's
// browser and Composio ever see the state itself.
func newState() (string, string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", "", fmt.Errorf("generate state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(value)
	return state, hashState(state), nil
}

func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

// pendingLinkLifetime is how long a pending connection waits for its member
// before the sweep fails it: well past Composio's link expiry.
const pendingLinkLifetime = 24 * time.Hour
