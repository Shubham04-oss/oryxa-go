package application

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Oryxa's own sign-in is the second broker beside Composio. A connection made
// through it has broker oryxa: signin keeps its credential sealed in apps'
// own tables, and hooks receives what its app pushes. Nothing here changes
// what a Composio connection does, and every Composio path refuses an Oryxa
// connection rather than acting through it with Composio's key.
//
// A toolkit takes this route only once APPS_ORYXA_TOOLKITS names it. With
// that list empty the broker is off and every Oryxa request is refused, but a
// connection already made through it can still be removed, checked and
// revoked for as long as signin is built.

// Signin is Oryxa's own sign-in and token system: *signin.Service.
type Signin interface {
	StartOAuth(ctx context.Context, request signin.StartRequest) (signin.Started, error)
	CompleteOAuth(ctx context.Context, callback signin.Callback) (signin.Completion, error)
	SaveCredential(ctx context.Context, request signin.CredentialRequest) (signin.Saved, error)
	Revoke(ctx context.Context, connectionID string) (signin.Revocation, error)
	CheckDue(ctx context.Context, limit int) ([]signin.Check, error)
	RetryRevocations(ctx context.Context, limit int) (int, error)
	PutClient(ctx context.Context, input signin.ClientInput) (signin.ClientView, bool, error)
	GetClient(ctx context.Context, organizationID, client string) (signin.ClientView, error)
	RemoveClient(ctx context.Context, organizationID, client string) (bool, error)
	// Session is what an integration calls its app with, as one active
	// connection: the credential placed in the innermost transport, the
	// connection's own fields, and a Token for the SDKs with no client hook.
	// A read tool's session refuses every method but GET, HEAD and that
	// tool's own read_posts, before anything is sent.
	Session(ctx context.Context, connectionID string, options ...signin.SessionOption) (integration.Session, error)
}

// Hooks is the webhook ingress's endpoints: *hooks.Service.
type Hooks interface {
	Create(ctx context.Context, input hooks.CreateInput) (hooks.Created, error)
	Rotate(ctx context.Context, id string, secret secrets.Secret) (hooks.Created, error)
	OrganizationEndpoint(ctx context.Context, organizationID, toolkit string) (hooks.Endpoint, error)
	DeploymentEndpoint(ctx context.Context, toolkit string) (hooks.Endpoint, string, error)
	// EndpointsForConnection answers a connection's endpoints, oldest first,
	// each with the app's own id for the subscription delivering to it, so a
	// subscription can be removed at the app and its endpoint deleted.
	EndpointsForConnection(ctx context.Context, connectionID string) ([]hooks.Endpoint, error)
	// SetRemote records the app's id for the subscription delivering to an
	// endpoint, once Subscribe answered it.
	SetRemote(ctx context.Context, id, remoteID string, expiresAt *time.Time) error
	Delete(ctx context.Context, id string) error
	DeleteForConnection(ctx context.Context, connectionID string) ([]hooks.Endpoint, error)
	RevealVerificationToken(ctx context.Context, id string) (secrets.Secret, error)
	SweepExpired(ctx context.Context) (int64, error)
	URL(endpointID string) string
}

// PushRouting finds the live subscriptions of an integration's triggers that a
// verified webhook delivery is for. *postgres.Store is one.
type PushRouting interface {
	// OwnSubscriptionsForEndpoint is every live subscription to trigger on one
	// connection, with the organization the connection belongs to. It is a
	// connection endpoint's whole reach, since the endpoint belongs to that
	// connection.
	OwnSubscriptionsForEndpoint(ctx context.Context, connectionID, organizationID, trigger string) ([]hooks.Subscription, error)
	// OwnSubscriptionsForAccount is every live subscription to trigger on a
	// toolkit's connections whose own account ref is accountRef, narrowed to
	// one organization when organizationID is not "".
	OwnSubscriptionsForAccount(ctx context.Context, toolkit, organizationID, accountRef, trigger string) ([]hooks.Subscription, error)
}

// Integrations are Oryxa's own integrations, by toolkit:
// *integration.Registry.
type Integrations interface {
	Lookup(toolkit string) (integration.Integration, bool)
	Toolkits() []string
}

// OryxaStore is what Oryxa's own sign-in needs of apps' tables beyond Store.
// *postgres.Store is one.
type OryxaStore interface {
	// FailPendingOryxaConnectionsBefore fails every pending Oryxa connection
	// created before cutoff, writing the events built from them.
	FailPendingOryxaConnectionsBefore(ctx context.Context, cutoff, now time.Time, events func([]domain.Connection) ([]domain.Event, error)) (int64, error)
	// RelabelConnection gives an active connection the label its new
	// credential names, and writes event. It answers false, writing nothing,
	// when the connection is no longer active.
	RelabelConnection(ctx context.Context, id, label string, now time.Time, event domain.Event) (domain.Connection, bool, error)
	// RecordEvents writes events about a change kept elsewhere.
	RecordEvents(ctx context.Context, events ...domain.Event) error
	// LiveOAuthClient reads an organization's live OAuth client for a
	// registration, or Oryxa's default with organizationID "", without
	// opening anything sealed. signin.ErrNoClient when there is none.
	LiveOAuthClient(ctx context.Context, organizationID, client string) (signin.OAuthClient, error)
	// PushRouting is how the webhook ingress finds the subscription a
	// verified delivery is for. It is the same store, and is asked for
	// separately because it answers about integrations' triggers rather than
	// about a connection or a member.
	PushRouting
	// CreateOwnTrigger records a subscription made through an integration and
	// writes event. It is CreateTrigger without a Composio instance: asking
	// again for the same connection, event and config answers the one that
	// exists, with false, and writes nothing. It refuses with
	// domain.ErrNotConnected unless the connection is still active, as
	// CreateTrigger does.
	CreateOwnTrigger(ctx context.Context, subscription OwnSubscription, event domain.Event) (OwnSubscription, bool, error)
	// OwnSubscription reads one of an organization's subscriptions to an
	// integration's event, and answers false rather than ErrNotFound when the
	// trigger is Composio's, which is how a caller tells the two apart.
	OwnSubscription(ctx context.Context, organizationID, id string) (OwnSubscription, bool, error)
	// ClaimPolls claims up to limit active polled subscriptions of an
	// integration's triggers whose next poll is due at now, oldest first, by
	// moving their next poll to claimUntil, so no other replica takes them
	// meanwhile. It is the lease the refresh lease and the auto-subscribe
	// claim are both: FOR UPDATE SKIP LOCKED keeps two claiming statements
	// apart, and the moved time keeps them apart once one has committed.
	ClaimPolls(ctx context.Context, now, claimUntil time.Time, limit int) ([]OwnSubscription, error)
	// SavePoll records where a polled subscription's last poll got to and when
	// it is next due, which is what releases the claim.
	SavePoll(ctx context.Context, id, cursor string, next time.Time) error
}

// Oryxa is Oryxa's own sign-in, as apps is given it.
type Oryxa struct {
	Store        OryxaStore
	Signin       Signin
	Hooks        Hooks
	Integrations Integrations
	// Toolkits are the toolkits it is switched on for: APPS_ORYXA_TOOLKITS.
	// Empty means off: every Oryxa request is refused.
	Toolkits []string
	// RedirectURI is the one callback registered with every OAuth app:
	// APPS_SIGNIN_REDIRECT_URI. The state cookie comes back only to the host
	// that set it, so an OAuth sign-in starts only from a request to this
	// URI's host.
	RedirectURI string
	// DefaultClients are Oryxa's own OAuth apps, which members sign in
	// through unless their organization brings its own:
	// APPS_DEFAULT_OAUTH_CLIENTS. RunOryxa saves them, once it can.
	DefaultClients []DefaultClient
}

// DefaultClient is one of Oryxa's own OAuth apps, as configured.
type DefaultClient struct {
	// Client is the registration: github, slack, notion, google.
	Client       string
	ClientID     string
	ClientSecret secrets.Secret
	Fields       map[string]string
}

// oryxaBroker is Oryxa's own sign-in, once New has checked it.
type oryxaBroker struct {
	Oryxa
	enabled map[string]bool
	// tools is every tool the registered integrations declare, by slug, which
	// is what a run consults before Composio is asked anything.
	tools map[string]ownTool
	// redirectHost is RedirectURI's hostname, lower case: where the state
	// cookie lives. A cookie is scoped to a host, not a port.
	redirectHost string
}

func newOryxaBroker(oryxa *Oryxa) (*oryxaBroker, error) {
	if oryxa == nil {
		return nil, nil
	}
	if oryxa.Store == nil || oryxa.Signin == nil || oryxa.Hooks == nil || oryxa.Integrations == nil {
		return nil, errors.New("oryxa's sign-in needs a store, signin, hooks and the integrations")
	}
	broker := &oryxaBroker{Oryxa: *oryxa, enabled: map[string]bool{}}
	for _, toolkit := range oryxa.Toolkits {
		checked, err := domain.CheckToolkit(toolkit)
		if err != nil {
			return nil, fmt.Errorf("oryxa toolkit %q: %w", toolkit, err)
		}
		if _, registered := oryxa.Integrations.Lookup(checked); !registered {
			return nil, fmt.Errorf("oryxa toolkit %s: no integration in this build registers it", checked)
		}
		broker.enabled[checked] = true
	}
	if oryxa.RedirectURI != "" {
		parsed, err := url.Parse(oryxa.RedirectURI)
		if err != nil || parsed.Host == "" {
			return nil, errors.New("oryxa's redirect URI must be an absolute URL")
		}
		broker.redirectHost = strings.ToLower(parsed.Hostname())
	}
	if len(broker.enabled) > 0 && broker.redirectHost == "" {
		return nil, errors.New("oryxa's sign-in needs its redirect URI once a toolkit is switched on")
	}
	for _, client := range oryxa.DefaultClients {
		if !registeredClient(oryxa.Integrations, client.Client) {
			return nil, fmt.Errorf("oryxa's default OAuth client %s: no integration in this build signs in through it", client.Client)
		}
		if strings.TrimSpace(client.ClientID) == "" || strings.TrimSpace(client.ClientSecret.Reveal()) == "" {
			return nil, fmt.Errorf("oryxa's default OAuth client %s needs its client id and secret", client.Client)
		}
	}
	// The registry is fixed for the life of the process, so the tool index is
	// built once: a run reads it before it asks Composio for a tool.
	broker.tools = indexTools(oryxa.Integrations)
	return broker, nil
}

// registeredClient says whether any registered integration signs in through
// a client registration.
func registeredClient(integrations Integrations, client string) bool {
	for _, toolkit := range integrations.Toolkits() {
		registered, ok := integrations.Lookup(toolkit)
		if !ok {
			continue
		}
		for _, method := range registered.Manifest().Auth.Methods {
			if method.OAuth2 != nil && method.OAuth2.Client == client {
				return true
			}
		}
	}
	return false
}

// Errors every Oryxa path answers in the same words.
var (
	errOryxaOff = fmt.Errorf("connecting through Oryxa's own sign-in is not switched on here: %w", domain.ErrNotConnectable)
)

func errNotOnOryxa(toolkit string) error {
	return fmt.Errorf("%s is not switched on for Oryxa's own sign-in: %w", toolkit, domain.ErrNotConnectable)
}

// errOryxaNotYet is what a path that has no Oryxa route of its own answers
// for an Oryxa connection: nothing outside this package can act through one
// yet, and the tool and reader paths answer from the registry instead.
func errOryxaNotYet(toolkit string) error {
	return fmt.Errorf("%s is connected through Oryxa's own sign-in, whose tools, reads, targets and triggers are not available yet: %w",
		toolkit, domain.ErrNotConnectable)
}

// eventBroker is what a connection event says of its broker: BrokerOryxa
// for an Oryxa connection, and nothing for Composio's, whose events stay
// byte for byte what they were.
func eventBroker(connection domain.Connection) string {
	if connection.Oryxa() {
		return appsv1.BrokerOryxa
	}
	return ""
}

// on says whether Oryxa's own sign-in is switched on at all.
func (s *Service) on() bool { return s.oryxa != nil && len(s.oryxa.enabled) > 0 }

// enabled answers the manifest of a toolkit Oryxa's own sign-in is switched
// on for, or why it cannot be used.
func (s *Service) enabled(toolkit string) (integration.Manifest, error) {
	if !s.on() {
		return integration.Manifest{}, errOryxaOff
	}
	if !s.oryxa.enabled[toolkit] {
		return integration.Manifest{}, errNotOnOryxa(toolkit)
	}
	registered, ok := s.oryxa.Integrations.Lookup(toolkit)
	if !ok {
		// New refused this; kept so a registry that shrank cannot panic.
		return integration.Manifest{}, errNotOnOryxa(toolkit)
	}
	return registered.Manifest(), nil
}

// enabledToolkits answers the toolkits Oryxa's own sign-in is on for, in
// order.
func (s *Service) enabledToolkits() []string {
	if s.oryxa == nil {
		return nil
	}
	toolkits := make([]string, 0, len(s.oryxa.enabled))
	for toolkit := range s.oryxa.enabled {
		toolkits = append(toolkits, toolkit)
	}
	slices.Sort(toolkits)
	return toolkits
}

// methodOf answers a manifest's method of kind, or its first when kind is "".
func methodOf(manifest integration.Manifest, kind string) (integration.Method, error) {
	kind = strings.TrimSpace(kind)
	if len(manifest.Auth.Methods) == 0 {
		return integration.Method{}, fmt.Errorf("%s declares no way to sign in: %w", manifest.Toolkit, domain.ErrNotConnectable)
	}
	if kind == "" {
		return manifest.Auth.Methods[0], nil
	}
	for _, method := range manifest.Auth.Methods {
		if method.Kind == kind {
			return method, nil
		}
	}
	return integration.Method{}, fmt.Errorf("%s does not sign in with %s: %w", manifest.Toolkit, kind, domain.ErrInvalid)
}

// sameCookieHost says whether a request sent to host shares the redirect
// URI's cookies: the same hostname, whatever the port, since a cookie is
// scoped to a host and not a port.
func (s *Service) sameCookieHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		// Nothing says where the request was sent; the callback will tell.
		return true
	}
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return strings.EqualFold(strings.Trim(host, "[]"), s.oryxa.redirectHost)
}

// failure is how an Oryxa sign-in that ended without a credential is
// recorded: the reason a member reads on the connection, the constant the bus
// carries, and the error the caller is answered.
type failure struct {
	prose  string
	reason string
	err    error
}

// failureOf reads why signin did not keep a credential. Nothing in it quotes
// what a member typed.
func failureOf(toolkit string, err error) failure {
	var failed *signin.Failure
	switch {
	case errors.As(err, &failed):
		switch failed.Reason {
		case signin.ReasonInvalidCredentials:
			invalid := &domain.InvalidCredentials{Field: failed.Field, Rule: failed.Rule}
			return failure{prose: invalid.Error(), reason: appsv1.FailedInvalidCredentials, err: invalid}
		case signin.ReasonDenied:
			return failure{prose: "the app did not grant access", reason: appsv1.FailedDeniedAtProvider,
				err: fmt.Errorf("%s did not grant access: %w", toolkit, domain.ErrForbidden)}
		case signin.ReasonWrongAccount:
			return failure{prose: "the sign-in came back to another member", reason: appsv1.FailedWrongAccount,
				err: fmt.Errorf("the sign-in came back to another member: %w", domain.ErrForbidden)}
		}
		return failure{prose: "the app did not answer", reason: appsv1.FailedUnavailable,
			err: fmt.Errorf("%s did not answer: %w", toolkit, domain.ErrUnavailable)}
	case errors.Is(err, domain.ErrNotConnectable):
		return failure{prose: "there is no OAuth app to sign in through", reason: appsv1.FailedNotConnectable, err: err}
	case errors.Is(err, domain.ErrInvalid):
		return failure{prose: "the sign-in could not be started", reason: appsv1.FailedInvalidCredentials, err: err}
	}
	return failure{prose: "the sign-in could not be finished", reason: appsv1.FailedUnavailable, err: err}
}

// Expired connections' reasons, in a member's words.
func expiredProse(reason string) string {
	switch reason {
	case appsv1.ExpiredRefreshRejected:
		return "the app no longer accepts the sign-in"
	case appsv1.ExpiredCredentialRejected:
		return "the app no longer accepts the credential"
	}
	return "the app no longer accepts the connection"
}
