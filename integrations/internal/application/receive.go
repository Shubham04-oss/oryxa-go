package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

const (
	maxResourceID   = 256
	maxResourceName = 256
	maxResourceURL  = 2048
	maxDeliveryKey  = 200
)

// Receive handles a delivery to a webhook URL: it finds what the route names,
// has the provider verify and read the delivery, publishes each event once to
// every subscription it reaches, and answers what the provider expects. A
// route that names nothing is domain.ErrNotFound, and a delivery that cannot
// be verified is domain.ErrUnauthorized.
func (s *Service) Receive(ctx context.Context, providerName, route string, delivery providers.Delivered) (providers.Reply, error) {
	provider, ok := s.providers[providerName]
	if !ok || route == "" {
		return providers.Reply{}, domain.ErrNotFound
	}
	if provider.Delivery == providers.DeliveryApp {
		return s.receiveApp(ctx, provider, route, delivery)
	}
	return s.receiveWebhook(ctx, provider, route, delivery)
}

func (s *Service) receiveApp(ctx context.Context, provider providers.Provider, route string, delivery providers.Delivered) (providers.Reply, error) {
	webhook, err := s.store.AppWebhookByRoute(ctx, route)
	if err != nil {
		return providers.Reply{}, err
	}
	if webhook.Provider != provider.Name {
		return providers.Reply{}, domain.ErrNotFound
	}
	secret, err := s.sealer.Open(ctx, appWebhookBinding(webhook), webhook.EncryptedSecret)
	if err != nil {
		return providers.Reply{}, err
	}
	defer secrets.Wipe(secret)
	now := s.now()
	reception, err := provider.Receive(delivery, secret, now)
	if err != nil {
		return providers.Reply{}, receiveError(err)
	}
	for _, accountKey := range reception.AccountKeys {
		subscriptions, err := s.store.AccountSubscriptions(ctx, webhook.AppID, accountKey)
		if err != nil {
			return providers.Reply{}, err
		}
		for _, subscription := range subscriptions {
			for _, event := range reception.Events {
				if !subscription.Accepts(firstNonEmpty(event.Type, event.Name)) {
					continue
				}
				if err := s.publish(ctx, subscription, event, now); err != nil {
					return providers.Reply{}, err
				}
			}
		}
	}
	return reply(reception), nil
}

func (s *Service) receiveWebhook(ctx context.Context, provider providers.Provider, route string, delivery providers.Delivered) (providers.Reply, error) {
	subscription, err := s.store.SubscriptionByRoute(ctx, route)
	if err != nil {
		return providers.Reply{}, err
	}
	if subscription.Provider != provider.Name {
		return providers.Reply{}, domain.ErrNotFound
	}
	if !subscription.Status.Receives() || subscription.EncryptedSecret == nil {
		// What a stopped subscription registered may deliver until it is
		// removed; its deliveries are acknowledged and dropped.
		return providers.Reply{Status: http.StatusOK}, nil
	}
	secret, err := s.sealer.Open(ctx, subscriptionBinding(subscription), *subscription.EncryptedSecret)
	if err != nil {
		return providers.Reply{}, err
	}
	defer secrets.Wipe(secret)
	now := s.now()
	reception, err := provider.Receive(delivery, secret, now)
	if err != nil {
		return providers.Reply{}, receiveError(err)
	}
	switch reception.Lifecycle {
	case providers.LifecycleRemoved:
		if _, err := s.store.FailSubscription(ctx, subscription.OrganizationID, subscription.ID, "",
			integrationsv1.SubscriptionFailedReasonProviderRemoved, false, now, s.failedEvent(integrationsv1.SubscriptionFailedReasonProviderRemoved, now)); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return providers.Reply{}, err
		}
		s.releaseHoldIfUnused(ctx, subscription.OrganizationID, subscription.ConnectionID)
	case providers.LifecycleRenew:
		if err := s.store.ScheduleRenewal(ctx, subscription.ID, now); err != nil {
			return providers.Reply{}, err
		}
	}
	for _, event := range reception.Events {
		if event.RegistrationID != "" && subscription.RegistrationID != "" && event.RegistrationID != subscription.RegistrationID {
			continue
		}
		if err := s.publish(ctx, subscription, event, now); err != nil {
			return providers.Reply{}, err
		}
	}
	return reply(reception), nil
}

// publish writes an event's trigger for a subscription, unless the
// subscription already published the delivery.
func (s *Service) publish(ctx context.Context, subscription domain.Subscription, event providers.Event, now time.Time) error {
	occurredAt := event.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = now
	}
	payload, omitted := json.RawMessage(event.Payload), false
	if len(payload) > integrationsv1.MaxPayloadBytes || (len(payload) > 0 && !json.Valid(payload)) {
		payload, omitted = nil, true
	}
	record, err := s.event(integrationsv1.TriggerReceivedDetailType, integrationsv1.TriggerReceived{
		SubscriptionID: subscription.ID, OrganizationID: subscription.OrganizationID, ConnectionID: subscription.ConnectionID,
		AppID: subscription.AppID, Provider: subscription.Provider, Event: subscription.Provider + "." + event.Name,
		Scope: subscription.Scope, TeamID: subscription.TeamID, MemberID: subscription.MemberID,
		ProviderAccount: integrationsv1.ProviderAccount(subscription.ProviderAccount), CreatedBy: subscription.CreatedBy,
		Resource: integrationsv1.Resource{
			Type: clip(event.Resource.Type, maxResourceName), ID: clip(event.Resource.ID, maxResourceID),
			Name: clip(event.Resource.Name, maxResourceName), URL: clip(event.Resource.URL, maxResourceURL),
		},
		Actor: event.Actor, DeliveryID: clip(event.DeliveryID, maxResourceID), OccurredAt: occurredAt.UTC(), ReceivedAt: now,
		Payload: payload, PayloadOmitted: omitted,
	}, occurredAt.UTC())
	if err != nil {
		return err
	}
	key := event.Key
	if key == "" || len(key) > maxDeliveryKey {
		key = providers.Digest([]byte(event.Key), []byte(event.Name), event.Payload)
	}
	_, err = s.store.RecordDelivery(ctx, subscription.ID, key, now, record)
	return err
}

func (s *Service) failedEvent(reason string, at time.Time) func(domain.Subscription) (domain.Event, error) {
	return func(subscription domain.Subscription) (domain.Event, error) {
		return s.event(integrationsv1.SubscriptionFailedDetailType, integrationsv1.SubscriptionFailed{
			SubscriptionID: subscription.ID, OrganizationID: subscription.OrganizationID, ConnectionID: subscription.ConnectionID,
			AppID: subscription.AppID, Provider: subscription.Provider, Scope: subscription.Scope, TeamID: subscription.TeamID,
			MemberID: subscription.MemberID, CreatedBy: subscription.CreatedBy, Reason: reason, FailedAt: at,
		}, at)
	}
}

// reply is the answer a reception asks for, or 200 OK.
func reply(reception providers.Reception) providers.Reply {
	if reception.Reply != nil {
		return *reception.Reply
	}
	return providers.Reply{Status: http.StatusOK}
}

// receiveError turns a provider's refusal of a delivery into the domain's
// errors.
func receiveError(err error) error {
	if errors.Is(err, providers.ErrUnverified) {
		return fmt.Errorf("%w: %w", domain.ErrUnauthorized, err)
	}
	return err
}

// clip shortens text to at most size bytes without splitting a character.
func clip(text string, size int) string {
	if len(text) <= size {
		return text
	}
	cut := size
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
