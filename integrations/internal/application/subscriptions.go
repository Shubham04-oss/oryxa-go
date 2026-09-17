package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/connectionsclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

const (
	// renewBefore is how long before a registration expires it is renewed, when
	// it lasts long enough; a shorter one is renewed halfway.
	renewBefore = 24 * time.Hour
	// registrationWindow is longer than creating a subscription can take. A
	// subscription stopped while pending within it may still be registering,
	// and the request creating it releases the hold.
	registrationWindow = 5 * time.Minute
)

type CreateSubscriptionInput struct {
	ConnectionID string
	Target       map[string]string
	Events       []string
}

// ListSubscriptions answers a connection's subscriptions to someone who can
// see the connection, or without one, the subscriptions the caller made; an
// org admin sees the organization's.
func (s *Service) ListSubscriptions(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string) ([]domain.Subscription, error) {
	membership, err := s.member(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	filter := domain.SubscriptionFilter{ConnectionID: strings.TrimSpace(connectionID)}
	switch {
	case filter.ConnectionID != "":
		if _, err := s.connections.Connection(ctx, organizationID, filter.ConnectionID, credentials.AccessToken()); err != nil {
			return nil, concealed(err)
		}
	case !membership.Role.IsOrgAdmin():
		filter.CreatedBy = membership.UserID
	}
	return s.store.Subscriptions(ctx, organizationID, filter)
}

// GetSubscription answers a subscription to its creator, an org admin, or
// anyone who can see its connection.
func (s *Service) GetSubscription(ctx context.Context, credentials authclient.Credentials, organizationID, subscriptionID string) (domain.Subscription, error) {
	membership, err := s.member(ctx, credentials, organizationID)
	if err != nil {
		return domain.Subscription{}, err
	}
	subscription, err := s.store.Subscription(ctx, organizationID, subscriptionID)
	if err != nil {
		return domain.Subscription{}, err
	}
	if membership.Role.IsOrgAdmin() || subscription.CreatedBy == membership.UserID {
		return subscription, nil
	}
	if _, err := s.connections.Connection(ctx, organizationID, subscription.ConnectionID, credentials.AccessToken()); err != nil {
		return domain.Subscription{}, concealed(err)
	}
	return subscription, nil
}

// CreateSubscription subscribes one of a member's connections to its
// provider's events. Connections decides whether the member may use the
// connection when it issues the credential the registration needs.
func (s *Service) CreateSubscription(ctx context.Context, credentials authclient.Credentials, organizationID string, input CreateSubscriptionInput) (domain.Subscription, error) {
	membership, err := s.member(ctx, credentials, organizationID)
	if err != nil {
		return domain.Subscription{}, err
	}
	accessToken := credentials.AccessToken()
	if accessToken == "" {
		return domain.Subscription{}, fmt.Errorf("a member's access token is required: %w", domain.ErrUnauthorized)
	}
	connectionID := strings.TrimSpace(input.ConnectionID)
	if connectionID == "" {
		return domain.Subscription{}, fmt.Errorf("connection_id is required: %w", domain.ErrInvalid)
	}
	connection, err := s.connections.Connection(ctx, organizationID, connectionID, accessToken)
	if err != nil {
		return domain.Subscription{}, concealed(err)
	}
	switch connection.Status {
	case "active":
	case "needs_reconnect":
		return domain.Subscription{}, domain.ErrNeedsReconnect
	default:
		return domain.Subscription{}, fmt.Errorf("the connection is %s: %w", connection.Status, domain.ErrConflict)
	}
	provider, err := s.provider(connection.Provider)
	if err != nil {
		return domain.Subscription{}, err
	}
	target, events, err := domain.CheckTarget(input.Target, input.Events)
	if err != nil {
		return domain.Subscription{}, err
	}
	if target, events, err = provider.Target(target, events); err != nil {
		return domain.Subscription{}, err
	}
	credential, err := s.connections.Token(ctx, connectionsclient.TokenRequest{
		OrganizationID: organizationID, ConnectionID: connectionID, Purpose: domain.PurposeTrigger, AccessToken: accessToken,
	})
	if err != nil {
		return domain.Subscription{}, err
	}
	id, err := newID("sub")
	if err != nil {
		return domain.Subscription{}, err
	}
	route, err := newRoute()
	if err != nil {
		return domain.Subscription{}, err
	}
	now := s.now()
	subscription := domain.Subscription{
		ID: id, OrganizationID: organizationID, ConnectionID: connectionID, AppID: connection.AppID, Provider: provider.Name,
		Scope: connection.Scope, TeamID: connection.TeamID, MemberID: connection.MemberID, ProviderAccount: connection.ProviderAccount,
		Target: target, Events: events, CreatedBy: membership.UserID, CreatedAt: now, UpdatedAt: now, Route: route,
	}
	if provider.Delivery == providers.DeliveryApp {
		return s.createAppSubscription(ctx, provider, subscription)
	}
	return s.createWebhookSubscription(ctx, provider, subscription, credential)
}

// createAppSubscription saves a subscription to a provider that delivers
// through its app: there is nothing to register, but the app's signing
// secret must be set.
func (s *Service) createAppSubscription(ctx context.Context, provider providers.Provider, subscription domain.Subscription) (domain.Subscription, error) {
	if _, err := s.store.AppWebhook(ctx, subscription.OrganizationID, subscription.AppID); errors.Is(err, domain.ErrNotFound) {
		return domain.Subscription{}, domain.ErrWebhookSecretRequired
	} else if err != nil {
		return domain.Subscription{}, err
	}
	accountKey, err := provider.AccountKey(subscription.ProviderAccount)
	if err != nil {
		return domain.Subscription{}, err
	}
	subscription.AccountKey = accountKey
	subscription.Status = domain.SubscriptionActive
	event, err := s.createdEvent(subscription)
	if err != nil {
		return domain.Subscription{}, err
	}
	if err := s.store.CreateSubscription(ctx, subscription, &event); err != nil {
		return domain.Subscription{}, err
	}
	return subscription, nil
}

// createWebhookSubscription registers a webhook at the provider for a
// subscription. The subscription is saved pending first, so the provider's
// handshake during registration finds its route, and connections holds the
// connection's credentials until what is registered is removed.
func (s *Service) createWebhookSubscription(ctx context.Context, provider providers.Provider, subscription domain.Subscription, credential domain.Credential) (domain.Subscription, error) {
	secret, err := newSecret()
	if err != nil {
		return domain.Subscription{}, err
	}
	sealed, err := s.sealer.Seal(ctx, subscriptionBinding(subscription), []byte(secret))
	if err != nil {
		return domain.Subscription{}, err
	}
	subscription.EncryptedSecret = &sealed
	subscription.Status = domain.SubscriptionPending
	organizationID, connectionID := subscription.OrganizationID, subscription.ConnectionID
	if err := s.connections.AddHold(ctx, organizationID, connectionID); err != nil {
		return domain.Subscription{}, err
	}
	if err := s.store.CreateSubscription(ctx, subscription, nil); err != nil {
		s.releaseHoldIfUnused(context.WithoutCancel(ctx), organizationID, connectionID)
		return domain.Subscription{}, err
	}
	request := providers.RegisterRequest{
		Credential: credential, Target: subscription.Target, Events: subscription.Events,
		CallbackURL: s.WebhookURL(provider.Name, subscription.Route), Secret: secret, Now: s.now(),
	}
	registration, err := provider.Register(ctx, s.http, request)
	if err != nil {
		s.abandon(context.WithoutCancel(ctx), subscription)
		return domain.Subscription{}, err
	}
	now := s.now()
	expiresAt, renewAt := renewalTimes(registration.ExpiresAt, now)
	// A provider that issues its own signing secret, as ClickUp and Asana do,
	// replaces the one generated above: every later delivery is signed with
	// the provider's, so Receive must be given that one.
	var issued *domain.SealedData
	if registration.Secret != "" {
		checked, err := domain.CheckWebhookSecret(registration.Secret)
		if err != nil {
			s.abandon(context.WithoutCancel(ctx), subscription)
			return domain.Subscription{}, fmt.Errorf("provider %s issued a signing secret this service cannot store: %w", provider.Name, err)
		}
		sealed, err := s.sealer.Seal(ctx, subscriptionBinding(subscription), []byte(checked))
		if err != nil {
			s.abandon(context.WithoutCancel(ctx), subscription)
			return domain.Subscription{}, err
		}
		issued = &sealed
	}
	activated, err := s.store.ActivateSubscription(ctx, organizationID, subscription.ID, registration.ID, issued, expiresAt, renewAt, now, s.createdEvent)
	if err != nil && !errors.Is(err, domain.ErrConflict) {
		// The registration exists but was not saved: remove it while its
		// credential is at hand.
		if unregisterErr := provider.Unregister(context.WithoutCancel(ctx), s.http, providers.UnregisterRequest{
			Credential: credential, RegistrationID: registration.ID, Target: subscription.Target,
		}); unregisterErr != nil {
			s.logger.ErrorContext(ctx, "remove an unsaved registration", "subscription_id", subscription.ID, "provider", provider.Name, "error", unregisterErr)
		}
		s.abandon(context.WithoutCancel(ctx), subscription)
	}
	return activated, err
}

// abandon deletes a subscription that never registered, and its hold when no
// other subscription needs it.
func (s *Service) abandon(ctx context.Context, subscription domain.Subscription) {
	if err := s.store.DeletePendingSubscription(ctx, subscription.OrganizationID, subscription.ID); err != nil {
		s.logger.ErrorContext(ctx, "delete an unregistered subscription", "subscription_id", subscription.ID, "error", err)
	}
	s.releaseHoldIfUnused(ctx, subscription.OrganizationID, subscription.ConnectionID)
}

// StopSubscription stops a subscription for its creator or an org admin. One
// that already stopped or failed is left as it is.
func (s *Service) StopSubscription(ctx context.Context, credentials authclient.Credentials, organizationID, subscriptionID string) error {
	membership, err := s.member(ctx, credentials, organizationID)
	if err != nil {
		return err
	}
	subscription, err := s.store.Subscription(ctx, organizationID, subscriptionID)
	if err != nil {
		return err
	}
	if !membership.Role.IsOrgAdmin() && subscription.CreatedBy != membership.UserID {
		if _, err := s.connections.Connection(ctx, organizationID, subscription.ConnectionID, credentials.AccessToken()); err != nil {
			return concealed(err)
		}
		return domain.ErrForbidden
	}
	_, err = s.stop(ctx, domain.SubscriptionSelector{OrganizationID: organizationID, SubscriptionID: subscriptionID},
		integrationsv1.SubscriptionStoppedReasonStopped, membership.UserID)
	return err
}

// stop stops the subscriptions a selector picks. A stopped subscription with
// nothing left to remove at the provider no longer holds its connection. One
// with a registration to remove keeps the hold until cleanup, and one that may
// still be registering keeps it until its creation finishes: a registration it
// makes is then queued for removal, which needs the connection's credentials.
func (s *Service) stop(ctx context.Context, selector domain.SubscriptionSelector, reason, stoppedBy string) ([]domain.Subscription, error) {
	now := s.now()
	stopped, err := s.store.StopSubscriptions(ctx, selector, reason, stoppedBy, now, func(subscription domain.Subscription) (domain.Event, error) {
		return s.event(integrationsv1.SubscriptionStoppedDetailType, integrationsv1.SubscriptionStopped{
			SubscriptionID: subscription.ID, OrganizationID: subscription.OrganizationID, ConnectionID: subscription.ConnectionID,
			AppID: subscription.AppID, Provider: subscription.Provider, Scope: subscription.Scope, TeamID: subscription.TeamID,
			MemberID: subscription.MemberID, CreatedBy: subscription.CreatedBy, StoppedBy: stoppedBy, Reason: reason, StoppedAt: now,
		}, now)
	})
	if err != nil {
		return nil, err
	}
	released := map[string]bool{}
	for _, subscription := range stopped {
		provider, ok := s.providers[subscription.Provider]
		registering := subscription.RegistrationID == "" && now.Sub(subscription.CreatedAt) < registrationWindow
		if subscription.CleanupAt != nil || registering || !ok || provider.Delivery != providers.DeliveryWebhook || released[subscription.ConnectionID] {
			continue
		}
		released[subscription.ConnectionID] = true
		s.releaseHoldIfUnused(ctx, subscription.OrganizationID, subscription.ConnectionID)
	}
	return stopped, nil
}

// releaseHoldIfUnused releases integrations' hold on a connection once none of
// its subscriptions is live or has a registration to remove.
func (s *Service) releaseHoldIfUnused(ctx context.Context, organizationID, connectionID string) {
	held, err := s.store.HoldsConnection(ctx, organizationID, connectionID)
	if err != nil {
		s.logger.ErrorContext(ctx, "check a connection's hold", "connection_id", connectionID, "error", err)
		return
	}
	if held {
		return
	}
	if err := s.connections.ReleaseHold(ctx, organizationID, connectionID); err != nil && !errors.Is(err, domain.ErrNotFound) {
		s.logger.WarnContext(ctx, "release a connection's hold", "connection_id", connectionID, "error", err)
	}
}

func (s *Service) createdEvent(subscription domain.Subscription) (domain.Event, error) {
	events := subscription.Events
	if events == nil {
		events = []string{}
	}
	return s.event(integrationsv1.SubscriptionCreatedDetailType, integrationsv1.SubscriptionCreated{
		SubscriptionID: subscription.ID, OrganizationID: subscription.OrganizationID, ConnectionID: subscription.ConnectionID,
		AppID: subscription.AppID, Provider: subscription.Provider, Scope: subscription.Scope, TeamID: subscription.TeamID,
		MemberID: subscription.MemberID, ProviderAccount: integrationsv1.ProviderAccount(subscription.ProviderAccount),
		Target: subscription.Target, Events: events, CreatedBy: subscription.CreatedBy, CreatedAt: subscription.CreatedAt,
		ExpiresAt: subscription.ExpiresAt,
	}, subscription.UpdatedAt)
}

// renewalTimes is when a registration expires and when to renew it: a day
// before, or halfway for one that lasts less than two days.
func renewalTimes(expiresAt, now time.Time) (*time.Time, *time.Time) {
	if expiresAt.IsZero() {
		return nil, nil
	}
	expires := expiresAt.UTC()
	renew := expires.Add(-min(renewBefore, expires.Sub(now)/2))
	return &expires, &renew
}

func subscriptionBinding(subscription domain.Subscription) secrets.Binding {
	return secrets.Binding{OrganizationID: subscription.OrganizationID, ResourceType: secrets.ResourceSubscription, ResourceID: subscription.ID}
}

// concealed hides a connection the caller may not see, as connections does.
func concealed(err error) error {
	if errors.Is(err, domain.ErrForbidden) {
		return domain.ErrNotFound
	}
	return err
}
