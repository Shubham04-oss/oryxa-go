package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/connectionsclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

// fakeStore keeps subscriptions, app webhooks, deliveries and events in
// memory, following the rules the Postgres store enforces.
type fakeStore struct {
	mu            sync.Mutex
	subscriptions map[string]domain.Subscription
	leases        map[string]string
	webhooks      map[string]domain.AppWebhook
	deliveries    map[[2]string]time.Time
	actions       map[string]domain.Action
	actionKeys    map[[3]string]string
	events        []domain.Event
	activateErr   error
	// onActivate runs before a subscription is activated, as when something
	// else changes it while the provider registers.
	onActivate func()
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		subscriptions: map[string]domain.Subscription{},
		leases:        map[string]string{},
		webhooks:      map[string]domain.AppWebhook{},
		deliveries:    map[[2]string]time.Time{},
		actions:       map[string]domain.Action{},
		actionKeys:    map[[3]string]string{},
	}
}

func (store *fakeStore) CreateSubscription(_ context.Context, subscription domain.Subscription, event *domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.subscriptions {
		if existing.ID == subscription.ID || existing.Route == subscription.Route {
			return domain.ErrConflict
		}
	}
	store.subscriptions[subscription.ID] = subscription
	if event != nil {
		store.events = append(store.events, *event)
	}
	return nil
}

func (store *fakeStore) ActivateSubscription(_ context.Context, organizationID, subscriptionID, registrationID string, expiresAt, renewAt *time.Time, at time.Time, eventFor func(domain.Subscription) (domain.Event, error)) (domain.Subscription, error) {
	if store.onActivate != nil {
		store.onActivate()
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.activateErr != nil {
		return domain.Subscription{}, store.activateErr
	}
	subscription, ok := store.subscriptions[subscriptionID]
	if !ok || subscription.OrganizationID != organizationID {
		return domain.Subscription{}, domain.ErrNotFound
	}
	switch subscription.Status {
	case domain.SubscriptionPending:
	case domain.SubscriptionStopped, domain.SubscriptionFailed:
		subscription.RegistrationID, subscription.CleanupAt, subscription.UpdatedAt = registrationID, &at, at
		store.subscriptions[subscriptionID] = subscription
		return domain.Subscription{}, domain.ErrConflict
	default:
		return domain.Subscription{}, domain.ErrConflict
	}
	subscription.Status, subscription.RegistrationID = domain.SubscriptionActive, registrationID
	subscription.ExpiresAt, subscription.RenewAt, subscription.UpdatedAt = expiresAt, renewAt, at
	event, err := eventFor(subscription)
	if err != nil {
		return domain.Subscription{}, err
	}
	store.subscriptions[subscriptionID] = subscription
	store.events = append(store.events, event)
	return subscription, nil
}

func (store *fakeStore) DeletePendingSubscription(_ context.Context, organizationID, subscriptionID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if subscription, ok := store.subscriptions[subscriptionID]; ok && subscription.OrganizationID == organizationID && subscription.Status == domain.SubscriptionPending {
		delete(store.subscriptions, subscriptionID)
	}
	return nil
}

func (store *fakeStore) Subscription(_ context.Context, organizationID, subscriptionID string) (domain.Subscription, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	subscription, ok := store.subscriptions[subscriptionID]
	if !ok || subscription.OrganizationID != organizationID {
		return domain.Subscription{}, domain.ErrNotFound
	}
	return subscription, nil
}

func (store *fakeStore) SubscriptionByRoute(_ context.Context, route string) (domain.Subscription, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, subscription := range store.subscriptions {
		if subscription.Route == route {
			return subscription, nil
		}
	}
	return domain.Subscription{}, domain.ErrNotFound
}

func (store *fakeStore) Subscriptions(_ context.Context, organizationID string, filter domain.SubscriptionFilter) ([]domain.Subscription, error) {
	return store.matching(func(subscription domain.Subscription) bool {
		return subscription.OrganizationID == organizationID &&
			(filter.ConnectionID == "" || subscription.ConnectionID == filter.ConnectionID) &&
			(filter.CreatedBy == "" || subscription.CreatedBy == filter.CreatedBy) &&
			(!filter.Live || subscription.Status.Receives())
	}), nil
}

func (store *fakeStore) AccountSubscriptions(_ context.Context, appID, accountKey string) ([]domain.Subscription, error) {
	return store.matching(func(subscription domain.Subscription) bool {
		return subscription.AppID == appID && subscription.AccountKey == accountKey && subscription.Status == domain.SubscriptionActive
	}), nil
}

func (store *fakeStore) StopSubscriptions(_ context.Context, selector domain.SubscriptionSelector, reason, stoppedBy string, at time.Time, eventFor func(domain.Subscription) (domain.Event, error)) ([]domain.Subscription, error) {
	if selector.OrganizationID == "" {
		return nil, domain.ErrInvalid
	}
	picked := store.matching(func(subscription domain.Subscription) bool {
		return subscription.OrganizationID == selector.OrganizationID && subscription.Status.Receives() &&
			(selector.SubscriptionID == "" || subscription.ID == selector.SubscriptionID) &&
			(selector.ConnectionID == "" || subscription.ConnectionID == selector.ConnectionID) &&
			(selector.CreatedBy == "" || subscription.CreatedBy == selector.CreatedBy) &&
			(selector.TeamID == "" || subscription.TeamID == selector.TeamID)
	})
	store.mu.Lock()
	defer store.mu.Unlock()
	stopped := []domain.Subscription{}
	for _, subscription := range picked {
		subscription.Status, subscription.Reason, subscription.StoppedBy = domain.SubscriptionStopped, reason, stoppedBy
		subscription.StoppedAt, subscription.UpdatedAt, subscription.RenewAt, subscription.CleanupAt = &at, at, nil, nil
		if subscription.RegistrationID != "" {
			subscription.CleanupAt = &at
		}
		event, err := eventFor(subscription)
		if err != nil {
			return nil, err
		}
		store.subscriptions[subscription.ID] = subscription
		store.events = append(store.events, event)
		stopped = append(stopped, subscription)
	}
	return stopped, nil
}

func (store *fakeStore) FailSubscription(_ context.Context, organizationID, subscriptionID, leaseToken, reason string, cleanup bool, at time.Time, eventFor func(domain.Subscription) (domain.Event, error)) (domain.Subscription, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	subscription, ok := store.subscriptions[subscriptionID]
	if !ok || subscription.OrganizationID != organizationID || !subscription.Status.Receives() || (leaseToken != "" && store.leases[subscriptionID] != leaseToken) {
		return domain.Subscription{}, domain.ErrNotFound
	}
	subscription.Status, subscription.Reason, subscription.UpdatedAt, subscription.RenewAt, subscription.CleanupAt = domain.SubscriptionFailed, reason, at, nil, nil
	if cleanup && subscription.RegistrationID != "" {
		subscription.CleanupAt = &at
	}
	event, err := eventFor(subscription)
	if err != nil {
		return domain.Subscription{}, err
	}
	delete(store.leases, subscriptionID)
	store.subscriptions[subscriptionID] = subscription
	store.events = append(store.events, event)
	return subscription, nil
}

func (store *fakeStore) RecordDelivery(_ context.Context, subscriptionID, key string, at time.Time, event domain.Event) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.deliveries[[2]string{subscriptionID, key}]; ok {
		return false, nil
	}
	store.deliveries[[2]string{subscriptionID, key}] = at
	store.events = append(store.events, event)
	return true, nil
}

func (store *fakeStore) StartAction(_ context.Context, action domain.Action) (domain.Action, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := [3]string{action.ConnectionID, action.Name, action.IdempotencyKey}
	if id, ok := store.actionKeys[key]; ok {
		return store.actions[id], false, nil
	}
	store.actionKeys[key] = action.ID
	store.actions[action.ID] = action
	return action, true, nil
}

func (store *fakeStore) FinishAction(_ context.Context, action domain.Action, event domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	stored, ok := store.actions[action.ID]
	if !ok || stored.Status != domain.ActionRunning {
		return domain.ErrConflict
	}
	store.actions[action.ID] = action
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) DeleteActionsBefore(_ context.Context, before time.Time) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var deleted int64
	for id, action := range store.actions {
		if action.EndedAt != nil && action.EndedAt.Before(before) {
			delete(store.actions, id)
			deleted++
		}
	}
	return deleted, nil
}

func (store *fakeStore) ScheduleRenewal(_ context.Context, subscriptionID string, at time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	subscription, ok := store.subscriptions[subscriptionID]
	if !ok || subscription.Status != domain.SubscriptionActive || subscription.RegistrationID == "" {
		return nil
	}
	if subscription.RenewAt == nil || at.Before(*subscription.RenewAt) {
		subscription.RenewAt = &at
	}
	store.subscriptions[subscriptionID] = subscription
	return nil
}

func (store *fakeStore) DeleteDeliveriesBefore(_ context.Context, before time.Time) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var deleted int64
	for key, at := range store.deliveries {
		if at.Before(before) {
			delete(store.deliveries, key)
			deleted++
		}
	}
	return deleted, nil
}

func (store *fakeStore) ClaimRenewals(_ context.Context, now time.Time, limit int, leaseToken string, _ time.Time) ([]domain.Subscription, error) {
	return store.claim(limit, leaseToken, func(subscription domain.Subscription) bool {
		return subscription.Status == domain.SubscriptionActive && subscription.RenewAt != nil && !subscription.RenewAt.After(now)
	}), nil
}

func (store *fakeStore) SaveRenewal(_ context.Context, subscriptionID, leaseToken string, expiresAt, renewAt *time.Time, at time.Time) error {
	return store.leased(subscriptionID, leaseToken, true, func(subscription *domain.Subscription) {
		subscription.ExpiresAt, subscription.RenewAt, subscription.UpdatedAt = expiresAt, renewAt, at
	})
}

func (store *fakeStore) RetryRenewal(_ context.Context, subscriptionID, leaseToken string, next time.Time) error {
	return store.leased(subscriptionID, leaseToken, true, func(subscription *domain.Subscription) {
		subscription.RenewAt = &next
	})
}

func (store *fakeStore) ClaimCleanups(_ context.Context, now time.Time, limit int, leaseToken string, _ time.Time) ([]domain.Subscription, error) {
	return store.claim(limit, leaseToken, func(subscription domain.Subscription) bool {
		return subscription.CleanupAt != nil && !subscription.CleanupAt.After(now)
	}), nil
}

func (store *fakeStore) FinishCleanup(_ context.Context, subscriptionID, leaseToken string, at time.Time) error {
	return store.leased(subscriptionID, leaseToken, false, func(subscription *domain.Subscription) {
		subscription.CleanupAt, subscription.UpdatedAt = nil, at
	})
}

func (store *fakeStore) RetryCleanup(_ context.Context, subscriptionID, leaseToken string, next time.Time) error {
	return store.leased(subscriptionID, leaseToken, false, func(subscription *domain.Subscription) {
		subscription.CleanupAt = &next
	})
}

func (store *fakeStore) HoldsConnection(_ context.Context, organizationID, connectionID string) (bool, error) {
	return len(store.matching(func(subscription domain.Subscription) bool {
		return subscription.OrganizationID == organizationID && subscription.ConnectionID == connectionID &&
			(subscription.Status.Receives() || subscription.CleanupAt != nil)
	})) > 0, nil
}

func (store *fakeStore) PutAppWebhook(_ context.Context, webhook domain.AppWebhook) (domain.AppWebhook, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.webhooks[webhook.AppID]; ok {
		if existing.OrganizationID != webhook.OrganizationID || existing.Provider != webhook.Provider {
			return domain.AppWebhook{}, domain.ErrNotFound
		}
		webhook.Route, webhook.CreatedAt = existing.Route, existing.CreatedAt
	}
	store.webhooks[webhook.AppID] = webhook
	return webhook, nil
}

func (store *fakeStore) AppWebhook(_ context.Context, organizationID, appID string) (domain.AppWebhook, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	webhook, ok := store.webhooks[appID]
	if !ok || webhook.OrganizationID != organizationID {
		return domain.AppWebhook{}, domain.ErrNotFound
	}
	return webhook, nil
}

func (store *fakeStore) AppWebhookByRoute(_ context.Context, route string) (domain.AppWebhook, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, webhook := range store.webhooks {
		if webhook.Route == route {
			return webhook, nil
		}
	}
	return domain.AppWebhook{}, domain.ErrNotFound
}

func (store *fakeStore) DeleteAppWebhooks(_ context.Context, organizationID, appID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for id, webhook := range store.webhooks {
		if webhook.OrganizationID == organizationID && (appID == "" || id == appID) {
			delete(store.webhooks, id)
		}
	}
	return nil
}

// matching are the subscriptions a predicate picks, oldest first.
func (store *fakeStore) matching(pick func(domain.Subscription) bool) []domain.Subscription {
	store.mu.Lock()
	defer store.mu.Unlock()
	picked := []domain.Subscription{}
	for _, subscription := range store.subscriptions {
		if pick(subscription) {
			picked = append(picked, subscription)
		}
	}
	slices.SortFunc(picked, func(a, b domain.Subscription) int {
		if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return picked
}

func (store *fakeStore) claim(limit int, leaseToken string, due func(domain.Subscription) bool) []domain.Subscription {
	picked := store.matching(func(subscription domain.Subscription) bool {
		return due(subscription) && store.leases[subscription.ID] == ""
	})
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(picked) > limit {
		picked = picked[:limit]
	}
	for _, subscription := range picked {
		store.leases[subscription.ID] = leaseToken
	}
	return picked
}

func (store *fakeStore) leased(subscriptionID, leaseToken string, active bool, change func(*domain.Subscription)) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	subscription, ok := store.subscriptions[subscriptionID]
	if !ok || store.leases[subscriptionID] != leaseToken || (active && subscription.Status != domain.SubscriptionActive) {
		return domain.ErrConflict
	}
	change(&subscription)
	delete(store.leases, subscriptionID)
	store.subscriptions[subscriptionID] = subscription
	return nil
}

func (store *fakeStore) subscription(t *testing.T, id string) domain.Subscription {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	subscription, ok := store.subscriptions[id]
	if !ok {
		t.Fatalf("subscription %s is not stored", id)
	}
	return subscription
}

// put stores a subscription as a test arranges it.
func (store *fakeStore) put(subscription domain.Subscription) domain.Subscription {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.subscriptions[subscription.ID] = subscription
	return subscription
}

func (store *fakeStore) eventsOf(detailType string) []domain.Event {
	store.mu.Lock()
	defer store.mu.Unlock()
	var picked []domain.Event
	for _, event := range store.events {
		if event.DetailType == detailType {
			picked = append(picked, event)
		}
	}
	return picked
}

// fakeDirectory knows members by their access token.
type fakeDirectory struct {
	members map[string]domain.OrgMembership
}

func (directory *fakeDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	membership, ok := directory.members[credentials.AccessToken()]
	if !ok {
		return domain.OrgMembership{}, domain.ErrUnauthorized
	}
	if membership.OrganizationID != organizationID {
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	return membership, nil
}

// fakeConnections answers connections a member can see, issues one
// credential, and counts integrations' holds.
type fakeConnections struct {
	mu          sync.Mutex
	connections map[string]domain.Connection
	// hidden are the connections a member, by access token, may not see.
	hidden     map[string][]string
	apps       map[string]connectionsclient.App
	credential domain.Credential
	tokenErr   error
	addHoldErr error
	tokens     []connectionsclient.TokenRequest
	holds      map[string]bool
	holdCalls  []string
}

func (connections *fakeConnections) Connection(_ context.Context, organizationID, connectionID, accessToken string) (domain.Connection, error) {
	connections.mu.Lock()
	defer connections.mu.Unlock()
	connection, ok := connections.connections[connectionID]
	if !ok || connection.OrganizationID != organizationID {
		return domain.Connection{}, domain.ErrNotFound
	}
	if slices.Contains(connections.hidden[accessToken], connectionID) {
		return domain.Connection{}, domain.ErrForbidden
	}
	return connection, nil
}

func (connections *fakeConnections) Token(_ context.Context, request connectionsclient.TokenRequest) (domain.Credential, error) {
	connections.mu.Lock()
	defer connections.mu.Unlock()
	connections.tokens = append(connections.tokens, request)
	if connections.tokenErr != nil {
		return domain.Credential{}, connections.tokenErr
	}
	return connections.credential, nil
}

func (connections *fakeConnections) AddHold(_ context.Context, organizationID, connectionID string) error {
	connections.mu.Lock()
	defer connections.mu.Unlock()
	connections.holdCalls = append(connections.holdCalls, "add "+organizationID+"/"+connectionID)
	if connections.addHoldErr != nil {
		return connections.addHoldErr
	}
	connections.holds[connectionID] = true
	return nil
}

func (connections *fakeConnections) ReleaseHold(_ context.Context, organizationID, connectionID string) error {
	connections.mu.Lock()
	defer connections.mu.Unlock()
	connections.holdCalls = append(connections.holdCalls, "release "+organizationID+"/"+connectionID)
	delete(connections.holds, connectionID)
	return nil
}

func (connections *fakeConnections) App(_ context.Context, organizationID, appID, _ string) (connectionsclient.App, error) {
	app, ok := connections.apps[appID]
	if !ok || organizationID != testOrganization {
		return connectionsclient.App{}, domain.ErrNotFound
	}
	return app, nil
}

func (connections *fakeConnections) Connections(_ context.Context, organizationID, accessToken string) ([]domain.Connection, error) {
	connections.mu.Lock()
	defer connections.mu.Unlock()
	found := []domain.Connection{}
	for _, connection := range connections.connections {
		if connection.OrganizationID != organizationID || slices.Contains(connections.hidden[accessToken], connection.ID) {
			continue
		}
		found = append(found, connection)
	}
	slices.SortFunc(found, func(a, b domain.Connection) int { return strings.Compare(a.ID, b.ID) })
	return found, nil
}

func (connections *fakeConnections) held(connectionID string) bool {
	connections.mu.Lock()
	defer connections.mu.Unlock()
	return connections.holds[connectionID]
}

func (connections *fakeConnections) calls() []string {
	connections.mu.Lock()
	defer connections.mu.Unlock()
	return slices.Clone(connections.holdCalls)
}

// fakeSealer "seals" by keeping the plaintext beside its binding, and opens
// only with the same binding.
type fakeSealer struct{}

func (fakeSealer) Seal(_ context.Context, binding secrets.Binding, plaintext []byte) (domain.SealedData, error) {
	return domain.SealedData{Version: 1, Nonce: []byte(bindingKey(binding)), Ciphertext: slices.Clone(plaintext)}, nil
}

func (fakeSealer) Open(_ context.Context, binding secrets.Binding, sealed domain.SealedData) ([]byte, error) {
	if string(sealed.Nonce) != bindingKey(binding) {
		return nil, errors.New("sealed for another binding")
	}
	return slices.Clone(sealed.Ciphertext), nil
}

func bindingKey(binding secrets.Binding) string {
	return binding.OrganizationID + "/" + binding.ResourceType + "/" + binding.ResourceID
}

type received struct {
	delivery providers.Delivered
	secret   string
	now      time.Time
}

// fakeProvider records what integrations asks of a provider and answers what
// a test scripts.
type fakeProvider struct {
	mu            sync.Mutex
	registration  providers.Registration
	renewal       providers.Registration
	reception     providers.Reception
	registerErr   error
	renewErr      error
	unregisterErr error
	receiveErr    error
	// onRegister runs while the provider registers, as a provider's handshake
	// or a concurrent change would.
	onRegister   func(providers.RegisterRequest)
	result       providers.ActionResult
	actionErr    error
	ran          []providers.ActionRequest
	registered   []providers.RegisterRequest
	renewed      []providers.RenewRequest
	unregistered []providers.UnregisterRequest
	received     []received
}

func (fake *fakeProvider) webhook(name string) providers.Provider {
	return providers.Provider{
		Name:     name,
		Delivery: providers.DeliveryWebhook,
		Target:   fake.target,
		Register: func(_ context.Context, _ *http.Client, request providers.RegisterRequest) (providers.Registration, error) {
			if fake.onRegister != nil {
				fake.onRegister(request)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.registered = append(fake.registered, request)
			return fake.registration, fake.registerErr
		},
		Renew: func(_ context.Context, _ *http.Client, request providers.RenewRequest) (providers.Registration, error) {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.renewed = append(fake.renewed, request)
			return fake.renewal, fake.renewErr
		},
		Unregister: func(_ context.Context, _ *http.Client, request providers.UnregisterRequest) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.unregistered = append(fake.unregistered, request)
			return fake.unregisterErr
		},
		Receive: fake.receive,
		Actions: fake.actions(),
	}
}

func (fake *fakeProvider) app(name string) providers.Provider {
	return providers.Provider{
		Name:     name,
		Delivery: providers.DeliveryApp,
		Target:   fake.target,
		AccountKey: func(account domain.ProviderAccount) (string, error) {
			team, _, _ := strings.Cut(account.ID, ":")
			if team == "" {
				return "", domain.ErrInvalid
			}
			return team, nil
		},
		Receive: fake.receive,
		Actions: fake.actions(),
	}
}

// actions are one action that only reads and one that writes, so a test can
// see what each rule does.
func (fake *fakeProvider) actions() []providers.Action {
	run := func(_ context.Context, _ *http.Client, request providers.ActionRequest) (providers.ActionResult, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.ran = append(fake.ran, request)
		if fake.actionErr != nil {
			return providers.ActionResult{}, fake.actionErr
		}
		return fake.result, nil
	}
	return []providers.Action{
		{Name: "read_thing", Summary: "Read a thing.", Input: json.RawMessage(`{"type":"object"}`), Run: run},
		{Name: "write_thing", Summary: "Write a thing.", Input: json.RawMessage(`{"type":"object"}`), Writes: true, Run: run},
	}
}

func (fake *fakeProvider) target(target map[string]string, events []string) (map[string]string, []string, error) {
	if target["refuse"] != "" {
		return nil, nil, domain.ErrInvalid
	}
	return target, events, nil
}

func (fake *fakeProvider) receive(delivery providers.Delivered, secret []byte, now time.Time) (providers.Reception, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.received = append(fake.received, received{delivery: delivery, secret: string(secret), now: now})
	return fake.reception, fake.receiveErr
}

const (
	testOrganization = "org_1"
	testPublicURL    = "https://hooks.oryxa.test"
	adminToken       = "admin-token"
	memberToken      = "member-token"
	otherToken       = "other-token"
)

var testNow = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

type harness struct {
	service     *Service
	store       *fakeStore
	directory   *fakeDirectory
	connections *fakeConnections
	hooks       *fakeProvider
	chat        *fakeProvider
	now         time.Time
}

// newHarness is integrations with a webhook provider, hooks, and an app
// provider, chat; an admin, a member and another member; and a hooks and a
// chat connection the member owns.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		store: newFakeStore(),
		directory: &fakeDirectory{members: map[string]domain.OrgMembership{
			adminToken:  {OrganizationID: testOrganization, UserID: "user_admin", Role: domain.OrgRoleAdmin},
			memberToken: {OrganizationID: testOrganization, UserID: "user_member", Role: domain.OrgRoleMember},
			otherToken:  {OrganizationID: testOrganization, UserID: "user_other", Role: domain.OrgRoleMember},
		}},
		connections: &fakeConnections{
			connections: map[string]domain.Connection{
				"conn_hooks": {ID: "conn_hooks", OrganizationID: testOrganization, AppID: "app_hooks", Provider: "hooks", Scope: "member", MemberID: "user_member", ProviderAccount: domain.ProviderAccount{ID: "42", Name: "octo"}, Status: "active"},
				"conn_chat":  {ID: "conn_chat", OrganizationID: testOrganization, AppID: "app_chat", Provider: "chat", Scope: "member", MemberID: "user_member", ProviderAccount: domain.ProviderAccount{ID: "T1:U1", Name: "Acme"}, Status: "active"},
			},
			hidden: map[string][]string{},
			apps: map[string]connectionsclient.App{
				"app_hooks": {ID: "app_hooks", Provider: "hooks", Status: "active"},
				"app_chat":  {ID: "app_chat", Provider: "chat", Status: "active"},
				"app_other": {ID: "app_other", Provider: "unknown", Status: "active"},
			},
			credential: domain.Credential{AccessToken: "provider-token", In: "header", Name: "Authorization", Prefix: "Bearer "},
			holds:      map[string]bool{},
		},
		hooks: &fakeProvider{registration: providers.Registration{ID: "reg_1"}},
		chat:  &fakeProvider{},
		now:   testNow,
	}
	h.service = New(Dependencies{
		Store:       h.store,
		Directory:   h.directory,
		Connections: h.connections,
		Sealer:      fakeSealer{},
		Providers:   providers.Registry{"hooks": h.hooks.webhook("hooks"), "chat": h.chat.app("chat")},
		Clock:       func() time.Time { return h.now },
	}, Config{PublicBaseURL: testPublicURL + "/"})
	return h
}

func bearer(token string) authclient.Credentials {
	return authclient.Credentials{Authorization: "Bearer " + token}
}

// subscribe creates a hooks subscription for the member and fails the test
// if it cannot.
func (h *harness) subscribe(t *testing.T) domain.Subscription {
	t.Helper()
	subscription, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{
		ConnectionID: "conn_hooks", Target: map[string]string{"repository": "acme/api"}, Events: []string{"issues"},
	})
	if err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	return subscription
}

// setAppWebhook sets the chat app's signing secret.
func (h *harness) setAppWebhook(t *testing.T) AppWebhookView {
	t.Helper()
	view, err := h.service.PutAppWebhook(context.Background(), bearer(adminToken), testOrganization, "app_chat", "chat-signing-secret-1234")
	if err != nil {
		t.Fatalf("PutAppWebhook() error = %v", err)
	}
	return view
}

func timePointer(value time.Time) *time.Time { return &value }
