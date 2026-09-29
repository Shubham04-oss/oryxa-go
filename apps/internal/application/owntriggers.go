package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
)

// A trigger subscribed through one of Oryxa's own integrations is the same
// thing to a member — an event their app delivers, on their connection — but
// nothing about it is Composio's. There is no instance at Composio behind it:
// a pushed one is a webhook endpoint the core made and registered with the
// app, and a polled one is a cursor a worker carries forward. So the two are
// kept apart all the way down, and the member sees one list.

// OwnSubscription is a trigger of an integration's, as the store keeps it.
//
// It is a separate type from domain.Trigger because that one is Composio's
// shape and is not this lane's to change: the fields here are the ones a
// Composio subscription has no meaning for, and none of them is ever sent to
// a caller.
type OwnSubscription struct {
	ID             string
	OrganizationID string
	MemberID       string
	ConnectionID   string
	Toolkit        string
	Slug           string
	Config         json.RawMessage
	Status         domain.TriggerStatus
	StatusReason   string
	// Broker is domain.BrokerOryxa for every subscription of this shape, and
	// is kept beside the row so an operator reading the table can tell which
	// worker is responsible for it.
	Broker string
	// Kind is integration.TriggerPush or integration.TriggerPoll, as the
	// manifest declared the event.
	Kind string
	// Cursor is where the last poll of a polled subscription got to. It is
	// the app's own cursor and nothing else.
	Cursor string
	// NextPollAt is when a polled subscription is next asked. It is nil on a
	// pushed one, which the app delivers to rather than apps asking.
	NextPollAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// AsTrigger is the subscription as the member's own list shows it, which is
// domain.Trigger: a member chooses between events, not between brokers, and
// the list they read is the list they read today.
func (subscription OwnSubscription) AsTrigger() domain.Trigger {
	return domain.Trigger{
		ID: subscription.ID, OrganizationID: subscription.OrganizationID, MemberID: subscription.MemberID,
		ConnectionID: subscription.ConnectionID, Toolkit: subscription.Toolkit, Slug: subscription.Slug,
		Config: subscription.Config, Status: subscription.Status, StatusReason: subscription.StatusReason,
		Kind: subscription.Kind, CreatedAt: subscription.CreatedAt, UpdatedAt: subscription.UpdatedAt,
	}
}

// ownSpec is the manifest's declaration of one event, or why the toolkit does
// not have it. An event belonging to another app is not found rather than
// refused: this connection genuinely has no such event.
func (s *Service) ownSpec(toolkit, slug string) (integration.TriggerSpec, error) {
	if s.oryxa == nil {
		return integration.TriggerSpec{}, errNotOnOryxa(toolkit)
	}
	registered, ok := s.oryxa.Integrations.Lookup(toolkit)
	if !ok {
		return integration.TriggerSpec{}, errNotOnOryxa(toolkit)
	}
	for _, spec := range registered.Manifest().Triggers {
		if spec.Slug == slug {
			return spec, nil
		}
	}
	return integration.TriggerSpec{}, fmt.Errorf("no tool is called %s: %w", slug, domain.ErrNotFound)
}

// ownSubscription is one of an organization's subscriptions to an
// integration's event, or false when the trigger is Composio's. That is how
// a caller tells the two apart without either of them having to carry the
// other's fields.
func (s *Service) ownSubscription(ctx context.Context, organizationID, id string) (bool, error) {
	if s.oryxa == nil {
		// No Oryxa subscription can exist where there is no Oryxa sign-in.
		return false, nil
	}
	_, own, err := s.oryxa.Store.OwnSubscription(ctx, organizationID, id)
	if err != nil {
		return false, err
	}
	return own, nil
}

// subscribeOwn subscribes one of the caller's own connections to an event its
// integration declares, which is the same work whether a member asked for it
// or connecting the app did. It acts as the connection's own member: the
// caller has already decided that is allowed.
//
// A pushed event is registered with the app through hooks, which owns the
// endpoint and its secret: the core generates the secret, passes it to
// Subscribe, and verifies every delivery with it before the integration sees
// one. A polled event is recorded with a cursor, and the worker asks from
// there.
func (s *Service) subscribeOwn(ctx context.Context, connection domain.Connection, spec integration.TriggerSpec, config json.RawMessage) (domain.Trigger, error) {
	if problem := integration.CheckBytes(spec.ConfigSchema, config); problem != nil {
		return domain.Trigger{}, fmt.Errorf("%s does not take this config (%s): %w",
			spec.Slug, integration.CheckDescription(problem), domain.ErrInvalid)
	}
	kind := domain.TriggerKindWebhook
	if spec.Kind == integration.TriggerPoll {
		kind = domain.TriggerKindPoll
	}
	id, err := newID("apt")
	if err != nil {
		return domain.Trigger{}, err
	}
	now := s.now().UTC()
	// The event carries how it delivers, since a subscription that polls or
	// still needs its webhook registered explains a silence nothing else
	// would. It carries no broker: a member's own integration is reached
	// through their connection, and the event is about the subscription.
	event, err := s.event(appsv1.TriggerSubscribedDetailType, appsv1.TriggerSubscribed{
		TriggerID: id, ConnectionID: connection.ID, OrganizationID: connection.OrganizationID,
		MemberID: connection.MemberID, Toolkit: connection.Toolkit, Trigger: spec.Slug,
		Kind: kind, SubscribedAt: now,
	})
	if err != nil {
		return domain.Trigger{}, err
	}
	subscription := OwnSubscription{
		ID: id, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		ConnectionID: connection.ID, Toolkit: connection.Toolkit, Slug: spec.Slug, Config: config,
		Status: domain.TriggerActive, Broker: domain.BrokerOryxa, Kind: kind, CreatedAt: now, UpdatedAt: now,
	}
	if kind == domain.TriggerKindPoll {
		// Due on the next pass, which the worker looks for every oryxaEvery.
		due := now.Add(pollEvery)
		subscription.NextPollAt = &due
	}

	// A pushed event is registered at the app before the subscription is
	// recorded, so a trigger row never names an event the app does not send.
	// If recording then fails, the app is unsubscribed again.
	created, err := s.openOwnEndpoint(ctx, connection, spec, config)
	if err != nil {
		return domain.Trigger{}, err
	}
	recorded, createdRow, err := s.oryxa.Store.CreateOwnTrigger(ctx, subscription, event)
	if err != nil {
		s.closeOwnEndpoint(ctx, connection, created, "the subscription was not recorded")
		return domain.Trigger{}, err
	}
	if !createdRow {
		// The same connection, event and config is already subscribed, so
		// this one is that one. The endpoint this call made is not a
		// subscription of its own and is closed again.
		s.closeOwnEndpoint(ctx, connection, created, "the event was already subscribed")
	}
	s.logger.InfoContext(ctx, "trigger subscribed", "trigger_id", recorded.ID,
		"organization_id", recorded.OrganizationID, "trigger", spec.Slug, "kind", kind,
		"broker", domain.BrokerOryxa, "created", createdRow)
	return recorded.AsTrigger(), nil
}

// openOwnEndpoint makes a webhook endpoint for a pushed event and registers
// it with the app, and answers what was made. A polled event has no endpoint
// and answers nothing.
func (s *Service) openOwnEndpoint(ctx context.Context, connection domain.Connection, spec integration.TriggerSpec, config json.RawMessage) (hooks.Endpoint, error) {
	if spec.Kind != integration.TriggerPush {
		return hooks.Endpoint{}, nil
	}
	manifest, err := s.enabled(connection.Toolkit)
	if err != nil {
		return hooks.Endpoint{}, err
	}
	if manifest.Webhook == nil {
		return hooks.Endpoint{}, fmt.Errorf("%s declares no webhook, so %s cannot be pushed: %w",
			connection.Toolkit, spec.Slug, domain.ErrInvalid)
	}
	scope := manifest.Webhook.Scope
	if scope != integration.HookScopeConnection {
		// A deployment or organization endpoint's secret is not handed to a
		// caller: the deployment's is configuration and an organization's is
		// the admin's, and hooks keeps both sealed. Subscribe has nowhere to
		// get the secret to register with the app, so this waits for hooks to
		// offer one rather than registering an endpoint nothing can verify.
		return hooks.Endpoint{}, fmt.Errorf(
			"%s delivers to the %s's own endpoint, whose secret apps keeps sealed; a subscription to %s needs hooks to hand it out: %w",
			connection.Toolkit, scope, spec.Slug, domain.ErrNotConnectable)
	}
	created, err := s.oryxa.Hooks.Create(ctx, hooks.CreateInput{
		Toolkit: connection.Toolkit, Scope: integration.HookScopeConnection,
		OrganizationID: connection.OrganizationID, ConnectionID: connection.ID,
	})
	if err != nil {
		return hooks.Endpoint{}, err
	}
	pusher, ok := s.oryxa.Integrations.Lookup(connection.Toolkit)
	if !ok {
		s.oryxa.Hooks.Delete(ctx, created.Endpoint.ID)
		return hooks.Endpoint{}, errNotOnOryxa(connection.Toolkit)
	}
	pusherHooks, ok := pusher.(integration.Pusher)
	if !ok {
		s.oryxa.Hooks.Delete(ctx, created.Endpoint.ID)
		return hooks.Endpoint{}, fmt.Errorf("%s declares pushed events but cannot register one: %w",
			connection.Toolkit, domain.ErrNotConnectable)
	}
	session, err := s.oryxa.Signin.Session(ctx, connection.ID)
	if err != nil {
		s.oryxa.Hooks.Delete(ctx, created.Endpoint.ID)
		return hooks.Endpoint{}, err
	}
	remoteID, err := pusherHooks.Subscribe(ctx, session, spec.Slug, config, created.Hook)
	if err != nil {
		// The app refused the subscription, so the endpoint is not left
		// behind: it would answer deliveries for an event nothing asked for.
		if deleteErr := s.oryxa.Hooks.Delete(ctx, created.Endpoint.ID); deleteErr != nil {
			s.logger.ErrorContext(ctx, "delete a webhook endpoint whose subscription was refused",
				"endpoint_id", created.Endpoint.ID, "error", deleteErr)
		}
		return hooks.Endpoint{}, ownTriggerError(connection.Toolkit, spec.Slug, err)
	}
	if err := s.oryxa.Hooks.SetRemote(ctx, created.Endpoint.ID, remoteID, nil); err != nil {
		s.logger.ErrorContext(ctx, "record a webhook endpoint's subscription",
			"endpoint_id", created.Endpoint.ID, "error", err)
	}
	return created.Endpoint, nil
}

// closeOwnEndpoint undoes what openOwnEndpoint did: it removes the
// subscription at the app, where the app says how, and deletes the endpoint.
// It is best effort — the endpoint lapses and the sweep deletes it — and
// never blocks the caller's own bookkeeping.
func (s *Service) closeOwnEndpoint(ctx context.Context, connection domain.Connection, endpoint hooks.Endpoint, reason string) {
	if endpoint.ID == "" {
		return
	}
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if endpoint.RemoteID != "" {
		registered, ok := s.oryxa.Integrations.Lookup(connection.Toolkit)
		if !ok {
			return
		}
		pusher, ok := registered.(integration.Pusher)
		if !ok {
			return
		}
		session, err := s.oryxa.Signin.Session(detached, connection.ID)
		if err == nil {
			if err := pusher.Unsubscribe(detached, session, endpoint.RemoteID); err != nil {
				s.logger.WarnContext(detached, "unsubscribe a webhook the app still delivers to",
					"endpoint_id", endpoint.ID, "toolkit", connection.Toolkit, "error", err)
			}
		}
	}
	if err := s.oryxa.Hooks.Delete(detached, endpoint.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
		s.logger.WarnContext(detached, "delete a webhook endpoint", "endpoint_id", endpoint.ID, "error", err)
		return
	}
	s.logger.InfoContext(detached, "webhook endpoint closed", "endpoint_id", endpoint.ID, "reason", reason)
}

// unsubscribeOwn removes a member's subscription to an integration's event:
// the app stops sending it, the endpoints that carried it are deleted, and
// the row is recorded removed. A polled event has nothing at the app to stop.
func (s *Service) unsubscribeOwn(ctx context.Context, trigger domain.Trigger) error {
	endpoints, err := s.oryxa.Hooks.EndpointsForConnection(ctx, trigger.ConnectionID)
	if err != nil {
		return err
	}
	s.unsubscribeEndpoints(ctx, trigger.ConnectionID, trigger.Toolkit, endpoints, "the subscription was removed")
	now := s.now().UTC()
	event, err := s.event(appsv1.TriggerUnsubscribedDetailType, appsv1.TriggerUnsubscribed{
		TriggerID: trigger.ID, ConnectionID: trigger.ConnectionID, OrganizationID: trigger.OrganizationID,
		MemberID: trigger.MemberID, Toolkit: trigger.Toolkit, Trigger: trigger.Slug, UnsubscribedAt: now,
	})
	if err != nil {
		return err
	}
	_, err = s.store.RemoveTrigger(ctx, trigger.ID, now, event)
	return err
}

// unsubscribeEndpoints stops the app sending to a connection's endpoints and
// deletes them, which is what removing a subscription or a connection has to
// do for the app to stop.
//
// Neither step is allowed to block the other: an app that will not unsubscribe
// keeps delivering to an endpoint that is gone, and a delivery to an endpoint
// that is gone is answered as an unknown one, so the app stops on its own. An
// endpoint whose connection has no credential left cannot be unsubscribed at
// all, and is deleted all the same.
func (s *Service) unsubscribeEndpoints(ctx context.Context, connectionID, toolkit string, endpoints []hooks.Endpoint, reason string) {
	if len(endpoints) == 0 {
		return
	}
	registered, _ := s.oryxa.Integrations.Lookup(toolkit)
	pusher, isPusher := registered.(integration.Pusher)
	for _, endpoint := range endpoints {
		if endpoint.Toolkit != toolkit || endpoint.RemoteID == "" {
			continue
		}
		if !isPusher {
			s.logger.WarnContext(ctx, "an endpoint's app cannot be unsubscribed from; it will stop on its own",
				"endpoint_id", endpoint.ID, "toolkit", toolkit)
			continue
		}
		session, err := s.oryxa.Signin.Session(ctx, connectionID)
		if err != nil {
			s.logger.WarnContext(ctx, "unsubscribe a webhook whose connection has no credential",
				"connection_id", connectionID, "endpoint_id", endpoint.ID, "error", err)
			break
		}
		if err := pusher.Unsubscribe(ctx, session, endpoint.RemoteID); err != nil {
			s.logger.WarnContext(ctx, "unsubscribe a webhook the app still delivers to",
				"endpoint_id", endpoint.ID, "toolkit", toolkit, "error", err)
		}
	}
	for _, endpoint := range endpoints {
		if err := s.oryxa.Hooks.Delete(ctx, endpoint.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.logger.WarnContext(ctx, "delete a webhook endpoint", "endpoint_id", endpoint.ID, "error", err)
			continue
		}
		s.logger.InfoContext(ctx, "webhook endpoint deleted", "endpoint_id", endpoint.ID, "toolkit", toolkit, "reason", reason)
	}
}

// ownTriggerError is what an integration's own failure to register or drop a
// subscription becomes in apps' terms, so a member reads one set of words.
func ownTriggerError(toolkit, slug string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var refused *integration.AppError
	switch {
	case errors.As(err, &refused):
		return fmt.Errorf("%s refused to subscribe to %s: %s: %w",
			toolkit, slug, truncate(refused.Error(), maxComposioMessage), domain.ErrInvalid)
	case errors.Is(err, integration.ErrNeedsReconnect):
		return fmt.Errorf("subscribe to %s: the app no longer accepts this connection: %w", slug, domain.ErrNeedsReconnect)
	case isRateLimited(err):
		return fmt.Errorf("subscribe to %s: the app is rate limiting: %w", slug, domain.ErrRateLimited)
	case errors.Is(err, signin.ErrNotActive):
		return fmt.Errorf("subscribe to %s: the connection is not active: %w", slug, domain.ErrNotConnected)
	default:
		return fmt.Errorf("subscribe to %s: the app did not answer: %w", slug, domain.ErrUnavailable)
	}
}

// OwnKey is the one live subscription per connection, event and config, which
// is what Composio keys its instances on too. The config is compacted and then
// hashed rather than carried, so the key is short and a config that only
// differs in its whitespace is the one subscription it means.
//
// The store builds the same key from the same three parts, and both sides
// agree on what "the same subscription" means.
func OwnKey(connectionID, slug string, config json.RawMessage) string {
	canonical := &bytes.Buffer{}
	if err := json.Compact(canonical, config); err != nil {
		// A config that is not JSON is not one this ever makes, and the store
		// would refuse the row; hashing it as it came keeps the key stable.
		canonical.Reset()
		canonical.Write(config)
	}
	sum := sha256.Sum256(canonical.Bytes())
	return connectionID + "|" + slug + "|" + hex.EncodeToString(sum[:16])
}

// HookSubscriptions answers which of apps' trigger subscriptions a verified
// delivery is for, so that a pushed event reaches the member who subscribed
// it and nobody else.
//
// A connection endpoint's deliveries reach that connection's own
// subscriptions and nothing else. An endpoint shared by many accounts —
// the deployment's own, or an organization's — reaches a subscription only
// when the event names the account that subscription's connection is itself,
// so an event about a private channel or a private page reaches the member
// it was delivered for and no one who could also see it.
type HookSubscriptions struct {
	// Store finds the subscriptions. The zero value, which is what apps'
	// mount hands the ingress, reads the store New installed.
	Store PushRouting
}

var _ hooks.Subscriptions = HookSubscriptions{}

// installed is the store a zero HookSubscriptions reads. apps' mount builds
// the ingress with a zero HookSubscriptions before this package's New runs,
// and that file is not this lane's to change, so the zero value has to reach
// the store New was given. apps builds one Service per process, so there is
// nothing to choose between: whichever was installed first answers, and there
// is only ever one.
var installed atomic.Pointer[PushRouting]

// installPushRouting is New's, and is the only thing that sets it.
func installPushRouting(store PushRouting) { installed.Store(&store) }

func (subscriptions HookSubscriptions) routing() PushRouting {
	if subscriptions.Store != nil {
		return subscriptions.Store
	}
	if store := installed.Load(); store != nil {
		return *store
	}
	return nil
}

// ForEndpoint is every live subscription to trigger on the endpoint's own
// connection. A replaced endpoint is asked about too, while a renewal
// overlaps, and hooks checks each answer against the delivery's reach.
func (subscriptions HookSubscriptions) ForEndpoint(ctx context.Context, endpoint hooks.Endpoint, trigger string) ([]hooks.Subscription, error) {
	store := subscriptions.routing()
	if store == nil {
		return nil, nil
	}
	return store.OwnSubscriptionsForEndpoint(ctx, endpoint.ConnectionID, endpoint.OrganizationID, trigger)
}

// ForAccount is every live subscription to trigger whose connection's own
// account ref is the one the event names, in one organization when the
// endpoint has one. Routing by workspace would reach every member who can
// see the same thing, which for a private channel is the wrong half of the
// workspace.
func (subscriptions HookSubscriptions) ForAccount(ctx context.Context, toolkit, organizationID, accountRef, trigger string) ([]hooks.Subscription, error) {
	store := subscriptions.routing()
	if store == nil {
		return nil, nil
	}
	if strings.TrimSpace(accountRef) == "" {
		// An endpoint shared by many accounts cannot be routed without the
		// account, and guessing one would be reaching other members' events.
		return nil, nil
	}
	found, err := store.OwnSubscriptionsForAccount(ctx, toolkit, organizationID, accountRef, trigger)
	if err != nil {
		return nil, err
	}
	// The account the event named is what these were found by, and hooks
	// checks each answer against the delivery's reach: a subscription to
	// another account must never be offered for this one.
	for i := range found {
		found[i].AccountRef = accountRef
	}
	return found, nil
}
