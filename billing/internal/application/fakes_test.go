package application

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Shubham04-oss/oryxa-go/billing/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/dodo"
	"github.com/Shubham04-oss/oryxa-go/billing/internal/domain"
)

// fakeStore keeps the Postgres store's rules: organizations are created on
// first touch, renames only move forward, webhook deliveries apply once, and
// a subscription's organization is found the same way.
type fakeStore struct {
	organizations map[string]*storedOrganization
	subscriptions map[string]domain.Subscription
	deliveries    map[string]bool
	reminded      map[string]time.Time
	events        []domain.Event
}

type storedOrganization struct {
	organization  domain.Organization
	nameUpdatedAt time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		organizations: map[string]*storedOrganization{},
		subscriptions: map[string]domain.Subscription{},
		deliveries:    map[string]bool{},
		reminded:      map[string]time.Time{},
	}
}

func (store *fakeStore) Organization(_ context.Context, organizationID string) (domain.Organization, *domain.Subscription, error) {
	stored, ok := store.organizations[organizationID]
	if !ok {
		return domain.Organization{}, nil, domain.ErrNotFound
	}
	return stored.organization, store.subscription(stored.organization.SubscriptionID), nil
}

func (store *fakeStore) UpdateOrganization(_ context.Context, organizationID string, update func(domain.OrganizationState) (domain.Change, error)) error {
	stored := store.lock(organizationID)
	change, err := update(domain.OrganizationState{
		Organization: stored.organization,
		Subscription: store.subscription(stored.organization.SubscriptionID),
	})
	if err != nil {
		return err
	}
	store.write(change)
	return nil
}

func (store *fakeStore) ApplyWebhook(_ context.Context, delivery domain.WebhookDelivery, incoming domain.Subscription, update func(domain.WebhookState) (domain.Change, error)) (domain.WebhookOutcome, error) {
	if store.deliveries[delivery.ID] {
		return domain.WebhookDuplicate, nil
	}
	store.deliveries[delivery.ID] = true

	organizationID := ""
	if existing, ok := store.subscriptions[incoming.ID]; ok {
		organizationID = existing.OrganizationID
	} else if incoming.OrganizationID != "" {
		organizationID = incoming.OrganizationID
	} else {
		for id, stored := range store.organizations {
			if stored.organization.CustomerID == incoming.CustomerID {
				organizationID = id
			}
		}
	}
	if organizationID == "" {
		return domain.WebhookUnmatched, nil
	}

	stored := store.lock(organizationID)
	state := domain.WebhookState{Organization: stored.organization, Previous: store.subscription(incoming.ID)}
	if stored.organization.SubscriptionID != incoming.ID {
		state.Current = store.subscription(stored.organization.SubscriptionID)
	}
	change, err := update(state)
	if err != nil {
		return "", err
	}
	store.write(change)
	return domain.WebhookApplied, nil
}

func (store *fakeStore) ClaimRenewals(_ context.Context, now time.Time, window time.Duration, intervals []string, limit int, remind func(domain.Renewal) (*domain.Event, error)) (int, error) {
	claimed := 0
	for _, id := range sortedKeys(store.subscriptions) {
		subscription := store.subscriptions[id]
		stored := store.organizations[subscription.OrganizationID]
		due := subscription.Status == domain.SubscriptionActive && !subscription.CancelAtNextBillingDate &&
			stored != nil && !stored.organization.Deleted && stored.organization.SubscriptionID == id &&
			slices.Contains(intervals, subscription.BillingInterval) &&
			subscription.NextBillingDate.After(now) && !subscription.NextBillingDate.After(now.Add(window)) &&
			!store.reminded[id].Equal(subscription.NextBillingDate)
		if !due || claimed == limit {
			continue
		}
		event, err := remind(domain.Renewal{Subscription: subscription, OrganizationName: stored.organization.Name})
		if err != nil {
			return claimed, err
		}
		if event != nil {
			store.events = append(store.events, *event)
		}
		store.reminded[id] = subscription.NextBillingDate
		claimed++
	}
	return claimed, nil
}

func (store *fakeStore) OrganizationIDs(context.Context) ([]string, error) {
	var ids []string
	for _, id := range sortedKeys(store.organizations) {
		if !store.organizations[id].organization.Deleted {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (store *fakeStore) lock(organizationID string) *storedOrganization {
	stored, ok := store.organizations[organizationID]
	if !ok {
		stored = &storedOrganization{organization: domain.Organization{ID: organizationID, Entitlements: domain.Entitlements{Status: "unpaid"}}}
		store.organizations[organizationID] = stored
	}
	return stored
}

func (store *fakeStore) write(change domain.Change) {
	if change.Subscription != nil {
		store.subscriptions[change.Subscription.ID] = *change.Subscription
	}
	if change.Organization != nil {
		stored := store.organizations[change.Organization.ID]
		name := stored.organization.Name
		if change.NameUpdatedAt.IsZero() || !change.NameUpdatedAt.Before(stored.nameUpdatedAt) {
			name = change.Organization.Name
			if !change.NameUpdatedAt.IsZero() {
				stored.nameUpdatedAt = change.NameUpdatedAt
			}
		}
		stored.organization = *change.Organization
		stored.organization.Name = name
	}
	store.events = append(store.events, change.Events...)
}

func (store *fakeStore) subscription(id string) *domain.Subscription {
	subscription, ok := store.subscriptions[id]
	if !ok {
		return nil
	}
	return &subscription
}

func (store *fakeStore) takeEvents() []domain.Event {
	events := store.events
	store.events = nil
	return events
}

type fakePayments struct {
	customers []dodo.CustomerRequest
	checkouts []dodo.CheckoutRequest
	portals   []string
	cancelled []string
	cancelErr error
}

func (payments *fakePayments) CreateCustomer(_ context.Context, request dodo.CustomerRequest) (string, error) {
	payments.customers = append(payments.customers, request)
	return fmt.Sprintf("cus_%d", len(payments.customers)), nil
}

func (payments *fakePayments) CreateCheckout(_ context.Context, request dodo.CheckoutRequest) (string, error) {
	payments.checkouts = append(payments.checkouts, request)
	return "https://checkout.test/" + request.ProductID, nil
}

func (payments *fakePayments) CreatePortalSession(_ context.Context, customerID, returnURL string) (string, error) {
	payments.portals = append(payments.portals, customerID+" "+returnURL)
	return "https://portal.test/" + customerID, nil
}

func (payments *fakePayments) CancelSubscription(_ context.Context, subscriptionID string) error {
	if payments.cancelErr != nil {
		return payments.cancelErr
	}
	payments.cancelled = append(payments.cancelled, subscriptionID)
	return nil
}

// fakeMemberships answers as auth would for callers presenting "Bearer <user id>".
type fakeMemberships struct {
	members map[string]domain.Membership
}

func (memberships fakeMemberships) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.Membership, error) {
	if !credentials.Present() {
		return domain.Membership{}, domain.ErrUnauthorized
	}
	membership, ok := memberships.members[organizationID+" "+credentials.Authorization]
	if !ok {
		return domain.Membership{}, domain.ErrNotFound
	}
	return membership, nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
