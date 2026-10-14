package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

// fakeStore keeps apps' tables in memory, with the same conditional
// semantics the Store interface promises.
type fakeStore struct {
	mu                  sync.Mutex
	createConnectionErr error
	connections         map[string]domain.Connection
	states              map[string]string // state hash → connection id
	authConfigs         map[string]AuthConfig
	triggers            map[string]domain.Trigger
	actions             map[string]domain.Action
	handled             map[string]bool
	events              []domain.Event
	// subscribeAttempts counts what ConnectionsAwaitingSubscribe has claimed,
	// the way the column does.
	subscribeAttempts map[string]int
	// signingKeys are Pipedream triggers' sealed signing keys, by trigger.
	signingKeys map[string][]byte
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		connections: map[string]domain.Connection{}, states: map[string]string{},
		authConfigs: map[string]AuthConfig{}, triggers: map[string]domain.Trigger{},
		actions: map[string]domain.Action{}, handled: map[string]bool{},
		subscribeAttempts: map[string]int{},
	}
}

func (store *fakeStore) published(detailType string) []domain.Event {
	store.mu.Lock()
	defer store.mu.Unlock()
	var matched []domain.Event
	for _, event := range store.events {
		if event.DetailType == detailType {
			matched = append(matched, event)
		}
	}
	return matched
}

func (store *fakeStore) CreateConnection(_ context.Context, connection domain.Connection, stateHash string, event domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.createConnectionErr != nil {
		return store.createConnectionErr
	}
	if _, taken := store.states[stateHash]; taken {
		return domain.ErrConflict
	}
	for _, existing := range store.connections {
		if existing.OrganizationID == connection.OrganizationID && existing.MemberID == connection.MemberID &&
			existing.Toolkit == connection.Toolkit &&
			(existing.Status == domain.ConnectionPending || existing.Status == domain.ConnectionActive) &&
			(connection.Status == domain.ConnectionPending || connection.Status == domain.ConnectionActive) {
			return domain.ErrConflict
		}
	}
	store.connections[connection.ID] = connection
	store.states[stateHash] = connection.ID
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) PendingConnection(_ context.Context, stateHash string) (domain.Connection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	id, ok := store.states[stateHash]
	if !ok || store.connections[id].Status != domain.ConnectionPending {
		return domain.Connection{}, domain.ErrNotFound
	}
	return store.connections[id], nil
}

func (store *fakeStore) clearState(id string) {
	for hash, owner := range store.states {
		if owner == id {
			delete(store.states, hash)
		}
	}
}

func (store *fakeStore) ActivateConnection(_ context.Context, id, label string, now time.Time, event domain.Event) (domain.Connection, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	connection := store.connections[id]
	if connection.Status != domain.ConnectionPending {
		return connection, false, nil
	}
	connection.Status, connection.Label, connection.UpdatedAt, connection.ConnectedAt = domain.ConnectionActive, label, now, &now
	store.connections[id] = connection
	store.clearState(id)
	store.events = append(store.events, event)
	return connection, true, nil
}

func (store *fakeStore) ActivatePipedreamConnection(_ context.Context, id, accountID, label string, now time.Time, event domain.Event) (domain.Connection, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	connection := store.connections[id]
	if connection.Status != domain.ConnectionPending || !connection.Pipedream() {
		return connection, false, nil
	}
	for _, other := range store.connections {
		if other.PipedreamAccountID == accountID {
			return domain.Connection{}, false, domain.ErrConflict
		}
	}
	connection.Status, connection.Label, connection.PipedreamAccountID = domain.ConnectionActive, label, accountID
	connection.UpdatedAt, connection.ConnectedAt = now, &now
	store.connections[id] = connection
	store.clearState(id)
	store.events = append(store.events, event)
	return connection, true, nil
}

func (store *fakeStore) PipedreamConnections(_ context.Context, afterID string, limit int) ([]domain.Connection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var listed []domain.Connection
	for _, connection := range store.connections {
		if connection.Pipedream() && connection.Status == domain.ConnectionActive && connection.PipedreamAccountID != "" && connection.ID > afterID {
			listed = append(listed, connection)
		}
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].ID < listed[j].ID })
	if len(listed) > limit {
		listed = listed[:limit]
	}
	return listed, nil
}

func (store *fakeStore) FailConnection(_ context.Context, id, reason string, now time.Time, event domain.Event) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	connection := store.connections[id]
	if connection.Status != domain.ConnectionPending {
		return false, nil
	}
	connection.Status, connection.StatusReason, connection.UpdatedAt = domain.ConnectionFailed, reason, now
	store.connections[id] = connection
	store.clearState(id)
	store.events = append(store.events, event)
	return true, nil
}

func (store *fakeStore) Connection(_ context.Context, organizationID, id string) (domain.Connection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	connection, ok := store.connections[id]
	if !ok || connection.OrganizationID != organizationID {
		return domain.Connection{}, domain.ErrNotFound
	}
	return connection, nil
}

func (store *fakeStore) ConnectionByAccount(_ context.Context, accountID string) (domain.Connection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, connection := range store.connections {
		if connection.ComposioAccountID == accountID {
			return connection, nil
		}
	}
	return domain.Connection{}, domain.ErrNotFound
}

func (store *fakeStore) Connections(_ context.Context, organizationID string, query domain.ConnectionQuery) ([]domain.Connection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var listed []domain.Connection
	for _, connection := range store.connections {
		if connection.OrganizationID != organizationID ||
			(query.MemberID != "" && connection.MemberID != query.MemberID) ||
			(query.Toolkit != "" && connection.Toolkit != query.Toolkit) {
			continue
		}
		if len(query.Statuses) > 0 && !slices.Contains(query.Statuses, connection.Status) {
			continue
		}
		if len(query.Statuses) == 0 && connection.Status == domain.ConnectionRemoved {
			continue
		}
		listed = append(listed, connection)
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].ID < listed[j].ID })
	return listed, nil
}

func (store *fakeStore) ExpireConnection(_ context.Context, id, reason string, now time.Time, event domain.Event) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	connection := store.connections[id]
	if connection.Status != domain.ConnectionActive {
		return false, nil
	}
	connection.Status, connection.StatusReason, connection.UpdatedAt = domain.ConnectionExpired, reason, now
	store.connections[id] = connection
	store.events = append(store.events, event)
	return true, nil
}

func (store *fakeStore) RemoveConnection(_ context.Context, id, reason string, now time.Time, events func(domain.Connection, []domain.Trigger) ([]domain.Event, error)) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	connection, ok := store.connections[id]
	if !ok {
		return false, domain.ErrNotFound
	}
	if connection.Status == domain.ConnectionRemoved {
		return false, nil
	}
	connection.Status, connection.StatusReason, connection.UpdatedAt, connection.RemovedAt = domain.ConnectionRemoved, reason, now, &now
	store.connections[id] = connection
	var removed []domain.Trigger
	for triggerID, trigger := range store.triggers {
		if trigger.ConnectionID == id && trigger.Status != domain.TriggerRemoved {
			trigger.Status, trigger.UpdatedAt = domain.TriggerRemoved, now
			store.triggers[triggerID] = trigger
			removed = append(removed, trigger)
		}
	}
	built, err := events(connection, removed)
	if err != nil {
		return false, err
	}
	store.events = append(store.events, built...)
	return true, nil
}

func (store *fakeStore) FailPendingConnectionsBefore(_ context.Context, cutoff, now time.Time, events func([]domain.Connection) ([]domain.Event, error)) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var swept []domain.Connection
	for id, connection := range store.connections {
		if connection.Status == domain.ConnectionPending && connection.CreatedAt.Before(cutoff) {
			connection.Status, connection.UpdatedAt = domain.ConnectionFailed, now
			store.connections[id] = connection
			store.clearState(id)
			swept = append(swept, connection)
		}
	}
	if len(swept) == 0 {
		return 0, nil
	}
	built, err := events(swept)
	if err != nil {
		return 0, err
	}
	store.events = append(store.events, built...)
	return int64(len(swept)), nil
}

// ConnectionsAwaitingSubscribe does not simulate the lease real SQL takes
// (apps/internal/postgres/store_integration_test.go proves that against a
// real database instead): every test here drives a single, sequential
// maintenance pass, so there is no second replica for a lease to matter
// against, and a frozen test clock would make one indistinguishable from a
// connection still held by the first pass.
func (store *fakeStore) ConnectionsAwaitingSubscribe(_ context.Context, maxAttempts, limit int, now, _ time.Time) ([]domain.Connection, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var owed []domain.Connection
	for id, connection := range store.connections {
		if connection.Status != domain.ConnectionActive || !connection.AutoSubscribe || connection.AutoSubscribedAt != nil {
			continue
		}
		if store.subscribeAttempts[id] >= maxAttempts {
			continue
		}
		store.subscribeAttempts[id]++
		// The real UPDATE ... RETURNING hands back the row it just
		// incremented, attempts and all.
		connection.AutoSubscribeAttempts = store.subscribeAttempts[id]
		store.connections[id] = connection
		owed = append(owed, connection)
		if len(owed) == limit {
			break
		}
	}
	sort.Slice(owed, func(i, j int) bool { return owed[i].ID < owed[j].ID })
	return owed, nil
}

func (store *fakeStore) RefundAutoSubscribeAttempt(_ context.Context, id string, attempt int, _ time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.subscribeAttempts[id] == attempt && attempt > 0 {
		store.subscribeAttempts[id]--
		connection := store.connections[id]
		connection.AutoSubscribeAttempts = store.subscribeAttempts[id]
		store.connections[id] = connection
	}
	return nil
}

func (store *fakeStore) MarkSubscribed(_ context.Context, id string, now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	connection, ok := store.connections[id]
	if !ok || connection.AutoSubscribedAt != nil {
		return nil
	}
	marked := now.UTC()
	connection.AutoSubscribedAt = &marked
	store.connections[id] = connection
	return nil
}

func (store *fakeStore) AuthConfig(_ context.Context, toolkit string) (AuthConfig, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	config, ok := store.authConfigs[toolkit]
	if !ok {
		return AuthConfig{}, domain.ErrNotFound
	}
	return config, nil
}

func (store *fakeStore) SaveAuthConfig(_ context.Context, config AuthConfig) (AuthConfig, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.authConfigs[config.Toolkit]; ok {
		return existing, nil
	}
	store.authConfigs[config.Toolkit] = config
	return config, nil
}

func (store *fakeStore) CreateTrigger(_ context.Context, trigger domain.Trigger, event domain.Event) (domain.Trigger, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.triggers {
		if existing.ComposioTriggerID == trigger.ComposioTriggerID && existing.Status != domain.TriggerRemoved {
			return existing, false, nil
		}
	}
	store.triggers[trigger.ID] = trigger
	store.events = append(store.events, event)
	return trigger, true, nil
}

func (store *fakeStore) CreatePipedreamTrigger(_ context.Context, trigger domain.Trigger, sealedKey []byte, event domain.Event) (domain.Trigger, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if connection := store.connections[trigger.ConnectionID]; connection.Status != domain.ConnectionActive {
		return domain.Trigger{}, false, domain.ErrNotConnected
	}
	key := OwnKey(trigger.ConnectionID, trigger.Slug, trigger.Config)
	for _, existing := range store.triggers {
		if existing.PipedreamTriggerID != "" && existing.Status != domain.TriggerRemoved &&
			OwnKey(existing.ConnectionID, existing.Slug, existing.Config) == key {
			return existing, false, nil
		}
	}
	if store.signingKeys == nil {
		store.signingKeys = map[string][]byte{}
	}
	store.triggers[trigger.ID] = trigger
	store.signingKeys[trigger.ID] = sealedKey
	store.events = append(store.events, event)
	return trigger, true, nil
}

func (store *fakeStore) PipedreamTrigger(_ context.Context, id string) (domain.Trigger, []byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	trigger, ok := store.triggers[id]
	if !ok || trigger.PipedreamTriggerID == "" {
		return domain.Trigger{}, nil, domain.ErrNotFound
	}
	return trigger, store.signingKeys[id], nil
}

func (store *fakeStore) Trigger(_ context.Context, organizationID, id string) (domain.Trigger, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	trigger, ok := store.triggers[id]
	if !ok || trigger.OrganizationID != organizationID {
		return domain.Trigger{}, domain.ErrNotFound
	}
	return trigger, nil
}

func (store *fakeStore) TriggerByComposioID(_ context.Context, composioTriggerID string) (domain.Trigger, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, trigger := range store.triggers {
		if trigger.ComposioTriggerID == composioTriggerID && trigger.Status != domain.TriggerRemoved {
			return trigger, nil
		}
	}
	return domain.Trigger{}, domain.ErrNotFound
}

func (store *fakeStore) Triggers(_ context.Context, organizationID string, query TriggerQuery) ([]domain.Trigger, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var listed []domain.Trigger
	for _, trigger := range store.triggers {
		if trigger.OrganizationID != organizationID ||
			(!query.IncludeRemoved && trigger.Status == domain.TriggerRemoved) ||
			(query.MemberID != "" && trigger.MemberID != query.MemberID) ||
			(query.ConnectionID != "" && trigger.ConnectionID != query.ConnectionID) {
			continue
		}
		listed = append(listed, trigger)
	}
	return listed, nil
}

func (store *fakeStore) RemoveTrigger(_ context.Context, id string, now time.Time, event domain.Event) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	trigger := store.triggers[id]
	if trigger.Status == domain.TriggerRemoved {
		return false, nil
	}
	trigger.Status, trigger.UpdatedAt = domain.TriggerRemoved, now
	store.triggers[id] = trigger
	store.events = append(store.events, event)
	return true, nil
}

func (store *fakeStore) DisableTrigger(_ context.Context, id, reason string, now time.Time, event domain.Event) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	trigger := store.triggers[id]
	if trigger.Status != domain.TriggerActive {
		return false, nil
	}
	trigger.Status, trigger.StatusReason, trigger.UpdatedAt = domain.TriggerDisabled, reason, now
	store.triggers[id] = trigger
	store.events = append(store.events, event)
	return true, nil
}

func (store *fakeStore) StartAction(_ context.Context, action domain.Action) (domain.Action, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if action.IdempotencyKey != "" {
		for _, existing := range store.actions {
			if existing.OrganizationID == action.OrganizationID && existing.MemberID == action.MemberID &&
				existing.IdempotencyKey == action.IdempotencyKey {
				return existing, false, nil
			}
		}
	}
	store.actions[action.ID] = action
	return action, true, nil
}

func (store *fakeStore) FinishAction(_ context.Context, action domain.Action, event *domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.actions[action.ID].Status != domain.ActionRunning {
		return domain.ErrConflict
	}
	store.actions[action.ID] = action
	if event != nil {
		store.events = append(store.events, *event)
	}
	return nil
}

func (store *fakeStore) RecordWebhook(_ context.Context, eventID, _ string, _ time.Time, events ...domain.Event) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.handled[eventID] {
		return false, nil
	}
	store.handled[eventID] = true
	store.events = append(store.events, events...)
	return true, nil
}

func (store *fakeStore) RecordTriggerDelivery(_ context.Context, eventID, _, triggerID string, now time.Time, event domain.Event) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.handled[eventID] {
		return false, nil
	}
	store.handled[eventID] = true
	if trigger, live := store.triggers[triggerID]; live && trigger.Status != domain.TriggerRemoved {
		delivered := now.UTC()
		trigger.DeliveryCount++
		trigger.LastDeliveryAt = &delivered
		store.triggers[triggerID] = trigger
	}
	store.events = append(store.events, event)
	return true, nil
}

func (store *fakeStore) EventHandled(_ context.Context, eventID string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.handled[eventID], nil
}

func (store *fakeStore) MarkEventHandled(_ context.Context, eventID, _ string, _ time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.handled[eventID] = true
	return nil
}

func (store *fakeStore) DeleteHandledEventsBefore(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// fakeDirectory answers auth's membership, by bearer token.
type fakeDirectory struct {
	members map[string]domain.OrgMembership
}

func (directory *fakeDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	member, ok := directory.members[credentials.Authorization]
	if !ok {
		return domain.OrgMembership{}, domain.ErrUnauthorized
	}
	if member.OrganizationID != organizationID {
		return domain.OrgMembership{}, domain.ErrForbidden
	}
	return member, nil
}

// fakeComposio answers apps' Composio calls from what a test sets, and
// records what it was asked.
type fakeComposio struct {
	mu          sync.Mutex
	toolkits    map[string]composio.Toolkit
	toolkitsErr error
	authConfigs []composio.AuthConfig
	created     []string // auth config kinds created: managed or scheme
	links       []composio.LinkRequest
	accounts    map[string][]composio.ConnectedAccount // successive reads
	deleted     []string
	revoked     []bool
	tools       map[string]composio.Tool
	executed    []composio.ExecuteRequest
	result      composio.ExecuteResult
	executeErr  error
	proxied     []composio.ProxyRequest
	proxyResult composio.ProxyResponse
	proxyErr    error
	upserts     []string
	// upsertUsers is the Composio user each upsert named, in order.
	upsertUsers []string
	// upsertErr is what every upsert answers instead, when set.
	upsertErr error
	upsertID  string
	// upsertPerSlug answers a distinct instance id per trigger, the way
	// Composio does for different triggers on one account.
	upsertPerSlug   bool
	deletedTriggers []string
	deleteErr       error
	// triggerTypes is the catalog a subscription is checked against, by
	// toolkit. triggerPages splits one toolkit's answer over cursors, so the
	// walk over them is exercised.
	triggerTypes   map[string][]composio.TriggerType
	triggerPages   int
	triggerTypeErr error
	triggerLists   int
	// searches counts Tools calls and toolReads counts Tool calls, so a test
	// can say that Composio was never asked for a tool the registry has.
	searches  int
	toolReads int
}

func newFakeComposio() *fakeComposio {
	return &fakeComposio{
		toolkits: map[string]composio.Toolkit{}, accounts: map[string][]composio.ConnectedAccount{},
		tools: map[string]composio.Tool{}, result: composio.ExecuteResult{Successful: true, Data: json.RawMessage(`{"ok":true}`), LogID: "log_1"},
		proxyResult: composio.ProxyResponse{Status: 200, Data: json.RawMessage(`{"items":[]}`)},
		upsertID:    "ti_1",
		triggerTypes: map[string][]composio.TriggerType{
			// What a GitHub connection can be subscribed to in these tests: a
			// pushed event needing a repository, and a polled one needing
			// nothing.
			"github": {{
				Slug: "GITHUB_COMMIT_EVENT", Name: "Commit", Toolkit: "github", Kind: "webhook",
				Config: json.RawMessage(`{"type":"object","properties":{"owner":{"type":"string"},"repo":{"type":"string"}},"required":["owner","repo"]}`),
			}, {
				Slug: "GITHUB_STAR_ADDED_EVENT", Name: "Star", Toolkit: "github", Kind: "poll",
				Config: json.RawMessage(`{"type":"object","properties":{}}`),
			}},
		},
	}
}

func (fake *fakeComposio) Toolkits(context.Context, composio.ToolkitQuery) (composio.Page[composio.Toolkit], error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.toolkitsErr != nil {
		return composio.Page[composio.Toolkit]{}, fake.toolkitsErr
	}
	page := composio.Page[composio.Toolkit]{}
	for _, toolkit := range fake.toolkits {
		page.Items = append(page.Items, toolkit)
	}
	sort.Slice(page.Items, func(i, j int) bool { return page.Items[i].Slug < page.Items[j].Slug })
	return page, nil
}

func (fake *fakeComposio) Toolkit(_ context.Context, slug string) (composio.Toolkit, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.toolkitsErr != nil {
		return composio.Toolkit{}, fake.toolkitsErr
	}
	toolkit, ok := fake.toolkits[slug]
	if !ok {
		return composio.Toolkit{}, composio.ErrNotFound
	}
	return toolkit, nil
}

func (fake *fakeComposio) AuthConfigs(_ context.Context, query composio.AuthConfigQuery) (composio.Page[composio.AuthConfig], error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	page := composio.Page[composio.AuthConfig]{}
	for _, config := range fake.authConfigs {
		if config.Toolkit == query.Toolkit {
			page.Items = append(page.Items, config)
		}
	}
	return page, nil
}

func (fake *fakeComposio) CreateManagedAuthConfig(_ context.Context, toolkit, name string) (composio.AuthConfig, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.created = append(fake.created, "managed")
	return composio.AuthConfig{ID: "ac_managed_" + toolkit, Name: name, Toolkit: toolkit, AuthScheme: "OAUTH2", IsComposioManaged: true, Enabled: true}, nil
}

func (fake *fakeComposio) CreateUserSuppliedAuthConfig(_ context.Context, toolkit, scheme, name string) (composio.AuthConfig, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.created = append(fake.created, scheme)
	return composio.AuthConfig{ID: "ac_custom_" + toolkit, Name: name, Toolkit: toolkit, AuthScheme: scheme, Enabled: true}, nil
}

func (fake *fakeComposio) CreateConnectLink(_ context.Context, request composio.LinkRequest) (composio.ConnectLink, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.links = append(fake.links, request)
	return composio.ConnectLink{RedirectURL: "https://connect.composio.dev/link/lt_1", ConnectedAccountID: "ca_new"}, nil
}

func (fake *fakeComposio) ConnectedAccount(_ context.Context, id string) (composio.ConnectedAccount, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	reads := fake.accounts[id]
	if len(reads) == 0 {
		return composio.ConnectedAccount{}, composio.ErrNotFound
	}
	account := reads[0]
	if len(reads) > 1 {
		fake.accounts[id] = reads[1:]
	}
	return account, nil
}

func (fake *fakeComposio) DeleteConnectedAccount(_ context.Context, id string, revoke bool) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.deleted = append(fake.deleted, id)
	fake.revoked = append(fake.revoked, revoke)
	return fake.deleteErr
}

func (fake *fakeComposio) Tools(_ context.Context, query composio.ToolQuery) (composio.Page[composio.Tool], error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.searches++
	page := composio.Page[composio.Tool]{}
	for _, tool := range fake.tools {
		if tool.Toolkit != query.Toolkit ||
			(query.Important && !slices.Contains(tool.Tags, "important")) ||
			(query.Search != "" && !strings.Contains(strings.ToLower(tool.Name), strings.ToLower(query.Search))) {
			continue
		}
		page.Items = append(page.Items, tool)
	}
	sort.Slice(page.Items, func(i, j int) bool { return page.Items[i].Slug < page.Items[j].Slug })
	return page, nil
}

func (fake *fakeComposio) Tool(_ context.Context, slug string) (composio.Tool, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.toolReads++
	tool, ok := fake.tools[slug]
	if !ok {
		return composio.Tool{}, composio.ErrNotFound
	}
	return tool, nil
}

func (fake *fakeComposio) ExecuteTool(_ context.Context, _ string, request composio.ExecuteRequest) (composio.ExecuteResult, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.executed = append(fake.executed, request)
	if fake.executeErr != nil {
		return composio.ExecuteResult{}, fake.executeErr
	}
	return fake.result, nil
}

func (fake *fakeComposio) Proxy(_ context.Context, request composio.ProxyRequest) (composio.ProxyResponse, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.proxied = append(fake.proxied, request)
	if fake.proxyErr != nil {
		return composio.ProxyResponse{}, fake.proxyErr
	}
	return fake.proxyResult, nil
}

// TriggerTypes answers one toolkit's trigger definitions, in pages when
// triggerPages asks for them, so the catalog's walk over cursors is
// exercised rather than assumed.
func (fake *fakeComposio) TriggerTypes(_ context.Context, query composio.TriggerTypeQuery) (composio.Page[composio.TriggerType], error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.triggerLists++
	if fake.triggerTypeErr != nil {
		return composio.Page[composio.TriggerType]{}, fake.triggerTypeErr
	}
	var items []composio.TriggerType
	for _, toolkit := range query.Toolkits {
		items = append(items, fake.triggerTypes[toolkit]...)
	}
	size := fake.triggerPages
	if size <= 0 {
		return composio.Page[composio.TriggerType]{Items: items}, nil
	}
	start := 0
	if query.Cursor != "" {
		parsed, err := strconv.Atoi(query.Cursor)
		if err != nil {
			return composio.Page[composio.TriggerType]{}, fmt.Errorf("bad cursor %q", query.Cursor)
		}
		start = parsed
	}
	end := min(start+size, len(items))
	page := composio.Page[composio.TriggerType]{Items: items[start:end]}
	if end < len(items) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func (fake *fakeComposio) UpsertTrigger(_ context.Context, slug string, subscription composio.TriggerSubscription) (string, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	accountID := subscription.ConnectedAccountID
	fake.upserts = append(fake.upserts, slug+"@"+accountID)
	fake.upsertUsers = append(fake.upsertUsers, subscription.UserID)
	if fake.upsertErr != nil {
		return "", fake.upsertErr
	}
	if fake.upsertPerSlug {
		return "ti_" + slug + "@" + accountID, nil
	}
	return fake.upsertID, nil
}

func (fake *fakeComposio) DeleteTrigger(_ context.Context, id string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.deletedTriggers = append(fake.deletedTriggers, id)
	return fake.deleteErr
}

// Callers every test shares: a member, another member, an org admin, an
// agent acting for the first member, and an agent acting for the admin, all
// in org_1.
var (
	memberToken     = authclient.Credentials{Authorization: "Bearer member"}
	otherToken      = authclient.Credentials{Authorization: "Bearer other"}
	adminToken      = authclient.Credentials{Authorization: "Bearer admin"}
	agentToken      = authclient.Credentials{Authorization: "Bearer agent"}
	specialistToken = authclient.Credentials{Authorization: "Bearer specialist-agent"}
	adminAgentToken = authclient.Credentials{Authorization: "Bearer admin-agent"}
)

type harness struct {
	service  *Service
	store    *fakeStore
	composio *fakeComposio
}

func newHarness(t *testing.T) harness {
	t.Helper()
	store := newFakeStore()
	fake := newFakeComposio()
	service, err := New(Dependencies{
		Store: store,
		Directory: &fakeDirectory{members: map[string]domain.OrgMembership{
			memberToken.Authorization:     {OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember},
			otherToken.Authorization:      {OrganizationID: "org_1", UserID: "user-2", Role: domain.OrgRoleMember},
			adminToken.Authorization:      {OrganizationID: "org_1", UserID: "user-3", Role: domain.OrgRoleAdmin},
			agentToken.Authorization:      {OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember, Agent: true},
			specialistToken.Authorization: {OrganizationID: "org_1", UserID: "user-1", Role: domain.OrgRoleMember, Agent: true, AgentID: "agt_writer", AgentRole: "specialist"},
			adminAgentToken.Authorization: {OrganizationID: "org_1", UserID: "user-3", Role: domain.OrgRoleAdmin, Agent: true},
		}},
		Composio: fake,
		Clock:    func() time.Time { return testClock },
	}, Config{
		PublicBaseURL: "https://apps.oryxa.test", ReturnOrigins: []string{"https://app.oryxa.test"},
		WebhookSecret: "whsec_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	service.wait = func(context.Context, time.Duration) error { return nil }
	return harness{service: service, store: store, composio: fake}
}

// activeConnection records a member's active connection to toolkit.
func (h harness) activeConnection(id, memberID, toolkit, accountID string) domain.Connection {
	connected := testClock
	connection := domain.Connection{
		ID: id, OrganizationID: "org_1", MemberID: memberID, Toolkit: toolkit, AuthConfigID: "ac_1",
		ComposioUserID: domain.ComposioUserID("org_1", memberID), ComposioAccountID: accountID,
		Status: domain.ConnectionActive, CreatedAt: testClock, UpdatedAt: testClock, ConnectedAt: &connected,
	}
	h.store.connections[id] = connection
	return connection
}

var errBoom = errors.New("boom")
