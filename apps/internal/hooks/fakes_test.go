package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/github"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/gmail"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/googledrive"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/notion"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/slack"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// memoryStore is Store in memory, keeping the rules apps' Postgres store
// keeps: the unique active endpoints, the conditional updates, and a
// delivery recorded with its events or not at all.
type memoryStore struct {
	mu        sync.Mutex
	endpoints map[string]Endpoint
	handled   map[string]string
	events    []domain.Event
	counted   map[string]int
	lookups   int
	failNext  error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{endpoints: map[string]Endpoint{}, handled: map[string]string{}, counted: map[string]int{}}
}

func (m *memoryStore) fail() error {
	err := m.failNext
	m.failNext = nil
	return err
}

func (m *memoryStore) CreateHookEndpoint(_ context.Context, endpoint Endpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.insertLocked(endpoint)
}

func (m *memoryStore) insertLocked(endpoint Endpoint) error {
	if err := m.fail(); err != nil {
		return err
	}
	if _, taken := m.endpoints[endpoint.ID]; taken {
		return domain.ErrConflict
	}
	if endpoint.Scope != integration.HookScopeConnection && endpoint.Status == StatusActive {
		for _, other := range m.endpoints {
			if other.Scope == endpoint.Scope && other.Status == StatusActive &&
				other.Toolkit == endpoint.Toolkit && other.OrganizationID == endpoint.OrganizationID {
				return fmt.Errorf("unique: %w", domain.ErrConflict)
			}
		}
	}
	m.endpoints[endpoint.ID] = endpoint
	return nil
}

func (m *memoryStore) HookEndpoint(_ context.Context, id string) (Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lookups++
	if err := m.fail(); err != nil {
		return Endpoint{}, err
	}
	endpoint, ok := m.endpoints[id]
	if !ok {
		return Endpoint{}, domain.ErrNotFound
	}
	return endpoint, nil
}

func (m *memoryStore) ActiveHookEndpoint(_ context.Context, scope, organizationID, toolkit string) (Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, endpoint := range m.endpoints {
		if endpoint.Scope == scope && endpoint.OrganizationID == organizationID && endpoint.Toolkit == toolkit &&
			endpoint.Status == StatusActive {
			return endpoint, nil
		}
	}
	return Endpoint{}, domain.ErrNotFound
}

func (m *memoryStore) sorted(keep func(Endpoint) bool) []Endpoint {
	var endpoints []Endpoint
	for _, endpoint := range m.endpoints {
		if keep(endpoint) {
			endpoints = append(endpoints, endpoint)
		}
	}
	slices.SortFunc(endpoints, func(a, b Endpoint) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return endpoints
}

func (m *memoryStore) HookEndpointsForConnection(_ context.Context, connectionID string) ([]Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sorted(func(e Endpoint) bool { return e.ConnectionID == connectionID }), nil
}

func (m *memoryStore) SetHookEndpointRemote(_ context.Context, id, remoteID string, expiresAt *time.Time, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	endpoint, ok := m.endpoints[id]
	if !ok {
		return domain.ErrNotFound
	}
	endpoint.RemoteID, endpoint.ExpiresAt, endpoint.UpdatedAt = remoteID, expiresAt, now
	m.endpoints[id] = endpoint
	return nil
}

func (m *memoryStore) RotateHookSecret(_ context.Context, id string, version int, sealed secrets.SealedData, previousUntil, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	endpoint, ok := m.endpoints[id]
	if !ok || endpoint.Secret.Version != version || endpoint.Secret.Current == nil {
		return false, nil
	}
	until := previousUntil
	endpoint.Secret.Previous, endpoint.Secret.PreviousUntil = endpoint.Secret.Current, &until
	endpoint.Secret.Current, endpoint.Secret.Version = &sealed, version+1
	endpoint.UpdatedAt = now
	m.endpoints[id] = endpoint
	return true, nil
}

func (m *memoryStore) CaptureHookSecret(_ context.Context, id string, sealed secrets.SealedData, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	endpoint, ok := m.endpoints[id]
	if !ok || endpoint.Secret.Current != nil {
		return false, nil
	}
	endpoint.Secret.Current, endpoint.Secret.Version = &sealed, endpoint.Secret.Version+1
	endpoint.UpdatedAt = now
	m.endpoints[id] = endpoint
	return true, nil
}

func (m *memoryStore) MarkHookSecretRevealed(_ context.Context, id string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	endpoint, ok := m.endpoints[id]
	if !ok || endpoint.Secret.Current == nil || endpoint.Secret.RevealedAt != nil {
		return false, nil
	}
	endpoint.Secret.RevealedAt = &now
	m.endpoints[id] = endpoint
	return true, nil
}

func (m *memoryStore) MarkHookValidated(_ context.Context, id string, createdAfter, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	endpoint, ok := m.endpoints[id]
	if !ok || endpoint.ValidatedAt != nil || !endpoint.CreatedAt.After(createdAfter) {
		return false, nil
	}
	endpoint.ValidatedAt = &now
	m.endpoints[id] = endpoint
	return true, nil
}

func (m *memoryStore) ReplaceHookEndpoint(_ context.Context, oldID string, replacement Endpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.endpoints[oldID]
	if !ok || old.Status != StatusActive {
		return domain.ErrConflict
	}
	if err := m.insertLocked(replacement); err != nil {
		return err
	}
	old.Status, old.ReplacedBy = StatusReplaced, replacement.ID
	m.endpoints[oldID] = old
	return nil
}

func (m *memoryStore) DeleteHookEndpoint(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for otherID, other := range m.endpoints {
		if other.ReplacedBy == id {
			other.Status, other.ReplacedBy = StatusActive, ""
			m.endpoints[otherID] = other
		}
	}
	_, ok := m.endpoints[id]
	delete(m.endpoints, id)
	return ok, nil
}

func (m *memoryStore) DeleteHookEndpointsForConnection(_ context.Context, connectionID string) ([]Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deleted := m.sorted(func(e Endpoint) bool { return e.ConnectionID == connectionID })
	for _, endpoint := range deleted {
		delete(m.endpoints, endpoint.ID)
	}
	return deleted, nil
}

func (m *memoryStore) HookEndpointsExpiring(_ context.Context, before time.Time, limit int) ([]Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	due := m.sorted(func(e Endpoint) bool {
		return e.Status == StatusActive && e.ExpiresAt != nil && !e.ExpiresAt.After(before)
	})
	slices.SortStableFunc(due, func(a, b Endpoint) int { return a.ExpiresAt.Compare(*b.ExpiresAt) })
	if len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}

func (m *memoryStore) DeleteExpiredHookEndpoints(_ context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var deleted int64
	for id, endpoint := range m.endpoints {
		if endpoint.ExpiresAt != nil && !endpoint.ExpiresAt.After(now) {
			delete(m.endpoints, id)
			deleted++
			continue
		}
		if endpoint.Secret.PreviousUntil != nil && !endpoint.Secret.PreviousUntil.After(now) {
			endpoint.Secret.Previous, endpoint.Secret.PreviousUntil = nil, nil
			m.endpoints[id] = endpoint
		}
	}
	return deleted, nil
}

func (m *memoryStore) HookDeliveryHandled(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail(); err != nil {
		return false, err
	}
	_, ok := m.handled[key]
	return ok, nil
}

func (m *memoryStore) RecordHookDelivery(_ context.Context, delivery Delivery) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail(); err != nil {
		return false, err
	}
	if _, ok := m.handled[delivery.Key]; ok {
		return false, nil
	}
	m.handled[delivery.Key] = "delivery"
	for _, event := range delivery.Events {
		if event.Key != "" {
			if _, ok := m.handled[event.Key]; ok {
				continue
			}
			m.handled[event.Key] = "event"
		}
		m.counted[event.TriggerID]++
		m.events = append(m.events, event.Event)
	}
	return true, nil
}

func (m *memoryStore) published() []domain.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.events)
}

func (m *memoryStore) endpoint(t *testing.T, id string) Endpoint {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	endpoint, ok := m.endpoints[id]
	if !ok {
		t.Fatalf("endpoint %s is not stored", id)
	}
	return endpoint
}

// fakeKeys stands in for KMS: a data key unwraps only with the encryption
// context it was made for, and every unwrap is counted.
type fakeKeys struct {
	mu       sync.Mutex
	decrypts int
	broken   bool
}

type wrappedKey struct {
	Context map[string]string `json:"context"`
	Key     []byte            `json:"key"`
}

func (k *fakeKeys) GenerateDataKey(_ context.Context, _ string, encryptionContext map[string]string) ([]byte, []byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.broken {
		return nil, nil, errors.New("kms is down")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, nil, err
	}
	wrapped, err := json.Marshal(wrappedKey{Context: maps.Clone(encryptionContext), Key: key})
	return append([]byte(nil), key...), wrapped, err
}

func (k *fakeKeys) DecryptDataKey(_ context.Context, encrypted []byte, encryptionContext map[string]string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.decrypts++
	if k.broken {
		return nil, errors.New("kms is down")
	}
	var wrapped wrappedKey
	if err := json.Unmarshal(encrypted, &wrapped); err != nil || !maps.Equal(wrapped.Context, encryptionContext) {
		return nil, errors.New("kms context mismatch")
	}
	return wrapped.Key, nil
}

func (k *fakeKeys) opened() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.decrypts
}

// fakeIntegration is an integration whose Parse reads what a test says, and
// remembers what it was handed.
type fakeIntegration struct {
	manifest integration.Manifest
	parse    func(header http.Header, body []byte) ([]integration.Event, error)

	mu      sync.Mutex
	headers []http.Header
}

func (f *fakeIntegration) Manifest() integration.Manifest         { return f.manifest }
func (f *fakeIntegration) Tools() map[string]integration.ToolFunc { return nil }

func (f *fakeIntegration) Subscribe(context.Context, integration.Session, string, json.RawMessage, integration.Hook) (string, error) {
	return "", errors.New("not in these tests")
}

func (f *fakeIntegration) Unsubscribe(context.Context, integration.Session, string) error {
	return errors.New("not in these tests")
}

func (f *fakeIntegration) Parse(_ context.Context, header http.Header, body []byte) ([]integration.Event, error) {
	f.mu.Lock()
	f.headers = append(f.headers, header)
	parse := f.parse
	f.mu.Unlock()
	if parse != nil {
		return parse(header, body)
	}
	return f.defaultParse(body)
}

// defaultParse is one event for the manifest's first push trigger: the
// body's /id and /account, and the body as its data.
func (f *fakeIntegration) defaultParse(body []byte) ([]integration.Event, error) {
	var fields struct {
		ID      string `json:"id"`
		Account string `json:"account"`
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, errors.New("the fake reads JSON bodies")
	}
	return []integration.Event{{
		ID: fields.ID, Trigger: f.manifest.Triggers[0].Slug, AccountRef: fields.Account, Data: body,
	}}, nil
}

func (f *fakeIntegration) parsed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.headers)
}

// notParsing is an integration with a webhook but no Pusher yet, as every
// phase 1 manifest is.
type notParsing struct{ manifest integration.Manifest }

func (n notParsing) Manifest() integration.Manifest         { return n.manifest }
func (n notParsing) Tools() map[string]integration.ToolFunc { return nil }

// fakeSubscriptions answers what a test registered, plus anything rogue: a
// Subscriptions that, wrongly, offers a subscription outside the delivery's
// reach, which hooks must drop.
type fakeSubscriptions struct {
	mu           sync.Mutex
	byConnection map[string][]Subscription
	byAccount    []Subscription
	rogue        []Subscription
	err          error
}

func (f *fakeSubscriptions) ForEndpoint(_ context.Context, endpoint Endpoint, trigger string) ([]Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var found []Subscription
	for _, subscription := range f.byConnection[endpoint.ConnectionID] {
		if subscription.Trigger == trigger {
			found = append(found, subscription)
		}
	}
	return append(found, f.rogue...), nil
}

func (f *fakeSubscriptions) ForAccount(_ context.Context, toolkit, organizationID, accountRef, trigger string) ([]Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var found []Subscription
	for _, subscription := range f.byAccount {
		if subscription.Toolkit == toolkit && subscription.AccountRef == accountRef && subscription.Trigger == trigger &&
			(organizationID == "" || subscription.OrganizationID == organizationID) {
			found = append(found, subscription)
		}
	}
	return append(found, f.rogue...), nil
}

// testClock is a clock a test moves.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func (c *testClock) Advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

const (
	testBaseURL = "https://api.oryxa.test"
	testOrg     = "org_1"
)

var testStart = time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)

// Push triggers the tests add to real and test manifests.
const (
	githubTrigger = "GITHUB_ISSUE_EVENT"
	slackTrigger  = "SLACK_MESSAGE_EVENT"
	notionTrigger = "NOTION_PAGE_EVENT"
	gmailTrigger  = "GMAIL_NEW_MESSAGE"
	driveTrigger  = "GOOGLEDRIVE_CHANGE"
	graphTrigger  = "OUTLOOK_MESSAGE_EVENT"
	linearTrigger = "LINEAR_ISSUE_EVENT"
)

func withPushTrigger(manifest integration.Manifest, slug string) integration.Manifest {
	manifest.Triggers = append(manifest.Triggers, integration.TriggerSpec{
		Slug: slug, Name: "Event", Kind: integration.TriggerPush,
		ConfigSchema: json.RawMessage(`{"type":"object"}`), PayloadSchema: json.RawMessage(`{"type":"object"}`),
	})
	return manifest
}

// testManifest is an app signed in with an API token, for a webhook scheme
// no real manifest has yet.
func testManifest(toolkit string, webhook *integration.Webhook) integration.Manifest {
	return integration.Manifest{
		Toolkit: toolkit, Version: 1, Name: toolkit, APIBase: "https://api.example.com",
		Auth: integration.Auth{
			Methods: []integration.Method{{
				Kind:  integration.KindAPIToken,
				Place: &integration.Place{In: integration.InHeader, Name: "Authorization", Template: "Bearer {token}"},
			}},
			Test: integration.Test{Path: "/me", Label: "/name", AccountRef: "/id"},
		},
		Webhook: webhook,
	}
}

// graphWebhook is Microsoft Graph's change notifications, as the plan pins
// them for outlook.
func graphWebhook() *integration.Webhook {
	return &integration.Webhook{
		Scope: integration.HookScopeConnection,
		Verify: integration.Verify{
			Type: integration.VerifyStaticToken, Token: &integration.Locator{JSON: "/value/*/clientState"},
		},
		DeliveryID: integration.Locator{Hash: true},
		Handshake:  integration.HandshakeGraphValidation,
	}
}

// linearWebhook is Linear's, as the plan pins it: a hex HMAC of the body, and
// a millisecond timestamp inside the body.
func linearWebhook() *integration.Webhook {
	return &integration.Webhook{
		Scope: integration.HookScopeConnection,
		Verify: integration.Verify{
			Type: integration.VerifyHMACSHA256, Signature: &integration.Locator{Header: "Linear-Signature"},
			Encoding: integration.EncodingHex, Signed: "{body}",
			Timestamp: &integration.Locator{JSON: "/webhookTimestamp", Unit: integration.UnitMilliseconds},
			Tolerance: 60,
		},
		DeliveryID: integration.Locator{Header: "Linear-Delivery"},
	}
}

// standardWebhook is the Standard Webhooks scheme Composio signs with:
// several space-separated "v1,<base64>" signatures of "{id}.{timestamp}.{body}".
func standardWebhook() *integration.Webhook {
	return &integration.Webhook{
		Scope: integration.HookScopeDeployment,
		Verify: integration.Verify{
			Type:      integration.VerifyHMACSHA256,
			Signature: &integration.Locator{Header: "webhook-signature", Prefix: "v1,", Split: " "},
			Encoding:  integration.EncodingBase64, Signed: "{id}.{timestamp}.{body}",
			Timestamp: &integration.Locator{Header: "webhook-timestamp", Unit: integration.UnitSeconds},
		},
		DeliveryID: integration.Locator{Header: "webhook-id"},
	}
}

// harness is hooks assembled on fakes, mounted as apps mounts it.
type harness struct {
	t             *testing.T
	service       *Service
	mux           *http.ServeMux
	store         *memoryStore
	keys          *fakeKeys
	subscriptions *fakeSubscriptions
	clock         *testClock
	logs          *bytes.Buffer
	fakes         map[string]*fakeIntegration
}

type harnessOptions struct {
	config   Config
	tokens   TokenVerifier
	surfaces Surfaces
}

func newHarness(t *testing.T, options ...func(*harnessOptions)) *harness {
	t.Helper()
	settings := harnessOptions{config: Config{PublicBaseURL: testBaseURL}}
	for _, option := range options {
		option(&settings)
	}
	h := &harness{
		t: t, store: newMemoryStore(), keys: &fakeKeys{}, clock: &testClock{now: testStart},
		subscriptions: &fakeSubscriptions{byConnection: map[string][]Subscription{}},
		logs:          &bytes.Buffer{}, fakes: map[string]*fakeIntegration{},
	}
	registry := integration.NewRegistry()
	for _, manifest := range []integration.Manifest{
		withPushTrigger(github.New().Manifest(), githubTrigger),
		withPushTrigger(slack.New().Manifest(), slackTrigger),
		withPushTrigger(notion.New().Manifest(), notionTrigger),
		withPushTrigger(gmail.New().Manifest(), gmailTrigger),
		withPushTrigger(googledrive.New().Manifest(), driveTrigger),
		withPushTrigger(testManifest("outlook", graphWebhook()), graphTrigger),
		withPushTrigger(testManifest("linear", linearWebhook()), linearTrigger),
	} {
		fake := &fakeIntegration{manifest: manifest}
		if err := registry.Register(fake); err != nil {
			t.Fatalf("register %s: %v", manifest.Toolkit, err)
		}
		h.fakes[manifest.Toolkit] = fake
	}
	// A toolkit with a webhook and no Pusher, as every phase 1 manifest is.
	if err := registry.Register(notParsing{manifest: testManifest("zendesk", standardWebhook())}); err != nil {
		t.Fatalf("register zendesk: %v", err)
	}
	service, err := New(Dependencies{
		Store:        h.store,
		Sealer:       secrets.Envelope{Keys: h.keys, KeyID: "alias/test", Service: "oryxa-apps"},
		Integrations: registry, Subscriptions: h.subscriptions, Tokens: settings.tokens, Surfaces: settings.surfaces,
		Logger: slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock:  h.clock.Now,
	}, settings.config)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	h.service = service
	h.mux = http.NewServeMux()
	h.mux.Handle(Pattern, service.Handler())
	return h
}

func withDeploymentSecret(toolkit string, values ...string) func(*harnessOptions) {
	return func(o *harnessOptions) {
		if o.config.DeploymentSecrets == nil {
			o.config.DeploymentSecrets = map[string][]secrets.Secret{}
		}
		for _, value := range values {
			o.config.DeploymentSecrets[toolkit] = append(o.config.DeploymentSecrets[toolkit], secrets.Secret(value))
		}
	}
}

// create makes an endpoint, failing the test if it cannot.
func (h *harness) create(input CreateInput) Created {
	h.t.Helper()
	created, err := h.service.Create(context.Background(), input)
	if err != nil {
		h.t.Fatalf("Create(%s, %s) = %v", input.Toolkit, input.Scope, err)
	}
	return created
}

// connectionEndpoint makes a connection endpoint for a toolkit, on a
// connection whose member subscribed to trigger.
func (h *harness) connectionEndpoint(toolkit, connectionID, memberID, trigger string) Created {
	h.t.Helper()
	created := h.create(CreateInput{
		Toolkit: toolkit, Scope: integration.HookScopeConnection, OrganizationID: testOrg, ConnectionID: connectionID,
	})
	h.subscriptions.mu.Lock()
	h.subscriptions.byConnection[connectionID] = append(h.subscriptions.byConnection[connectionID], Subscription{
		TriggerID: "apt_" + connectionID, ConnectionID: connectionID, OrganizationID: testOrg, MemberID: memberID,
		Toolkit: toolkit, Trigger: trigger,
	})
	h.subscriptions.mu.Unlock()
	return created
}

// subscribeAccount registers a member's subscription reached by account ref.
func (h *harness) subscribeAccount(subscription Subscription) {
	h.subscriptions.mu.Lock()
	defer h.subscriptions.mu.Unlock()
	h.subscriptions.byAccount = append(h.subscriptions.byAccount, subscription)
}

// post sends a delivery through the mux, as apps would serve it.
func (h *harness) post(path string, header http.Header, body []byte) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	for name, values := range header {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

// deliver posts to an endpoint by id.
func (h *harness) deliver(id string, header http.Header, body []byte) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.post(PathPrefix+id, header, body)
}

// received decodes the trigger events published so far.
func (h *harness) received() []receivedEvent {
	h.t.Helper()
	var events []receivedEvent
	for _, event := range h.store.published() {
		if event.DetailType != "apps.trigger.received.v1" {
			h.t.Fatalf("published %s, want only apps.trigger.received.v1", event.DetailType)
		}
		var received receivedEvent
		if err := json.Unmarshal(event.Data, &received); err != nil {
			h.t.Fatal(err)
		}
		events = append(events, received)
	}
	return events
}

type receivedEvent struct {
	TriggerID      string          `json:"trigger_id"`
	ConnectionID   string          `json:"connection_id"`
	OrganizationID string          `json:"organization_id"`
	MemberID       string          `json:"member_id"`
	Toolkit        string          `json:"toolkit"`
	Trigger        string          `json:"trigger"`
	Data           json.RawMessage `json:"data"`
	DataOmitted    bool            `json:"data_omitted"`
	ReceivedAt     time.Time       `json:"received_at"`
}

// assertLogsLack fails if anything a delivery carried reached the logs.
func (h *harness) assertLogsLack(values ...string) {
	h.t.Helper()
	logs := h.logs.String()
	for _, value := range values {
		if value != "" && strings.Contains(logs, value) {
			h.t.Fatalf("the logs hold %q, which a delivery carried:\n%s", value, logs)
		}
	}
}

func hmacHex(secret string, parts ...string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	for _, part := range parts {
		mac.Write([]byte(part))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func hmacBase64(secret string, parts ...string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	for _, part := range parts {
		mac.Write([]byte(part))
	}
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// githubDelivery is a delivery GitHub would send: signed, with its id.
func githubDelivery(secret, deliveryID, event string, body []byte) http.Header {
	header := http.Header{}
	header.Set("X-Hub-Signature-256", "sha256="+hmacHex(secret, string(body)))
	header.Set("X-GitHub-Delivery", deliveryID)
	header.Set("X-GitHub-Event", event)
	header.Set("Content-Type", "application/json")
	return header
}

// slackDelivery is a delivery Slack would send at a moment.
func slackDelivery(secret string, at time.Time, body []byte) http.Header {
	timestamp := fmt.Sprint(at.Unix())
	header := http.Header{}
	header.Set("X-Slack-Request-Timestamp", timestamp)
	header.Set("X-Slack-Signature", "v0="+hmacHex(secret, "v0:", timestamp, ":", string(body)))
	header.Set("Content-Type", "application/json")
	return header
}
