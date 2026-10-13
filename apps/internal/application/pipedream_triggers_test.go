package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/pipedream"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// triggerPipedream answers the part of Pipedream Connect triggers use; the
// rest is left to the nil interface, and would panic if it were called.
type triggerPipedream struct {
	Pipedream
	mu         sync.Mutex
	triggers   []PipedreamComponent
	listCalls  int
	deploys    []PipedreamDeployRequest
	deleted    []string
	deleteErr  error
	signingKey string
}

func (fake *triggerPipedream) ListTriggers(context.Context, string) ([]PipedreamComponent, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.listCalls++
	return fake.triggers, nil
}

func (fake *triggerPipedream) DeployTrigger(_ context.Context, request PipedreamDeployRequest) (PipedreamDeployedTrigger, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.deploys = append(fake.deploys, request)
	return PipedreamDeployedTrigger{ID: fmt.Sprintf("dc_%d", len(fake.deploys)), SigningKey: []byte(fake.signingKey)}, nil
}

func (fake *triggerPipedream) DeleteTrigger(_ context.Context, externalUserID, id string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.deleted = append(fake.deleted, externalUserID+"/"+id)
	return fake.deleteErr
}

func (fake *triggerPipedream) DeleteAccount(context.Context, string) error { return nil }

// countingKeys stands in for KMS: a wrapped key unwraps only with the
// context it was made for, and every unwrap is counted.
type countingKeys struct {
	mu     sync.Mutex
	opened int
}

func (*countingKeys) GenerateDataKey(_ context.Context, _ string, encryptionContext map[string]string) ([]byte, []byte, error) {
	key := bytes.Repeat([]byte{7}, 32)
	wrapped, err := json.Marshal(map[string]any{"key": key, "context": encryptionContext})
	return key, wrapped, err
}

func (keys *countingKeys) DecryptDataKey(_ context.Context, wrapped []byte, encryptionContext map[string]string) ([]byte, error) {
	keys.mu.Lock()
	keys.opened++
	keys.mu.Unlock()
	var unwrapped struct {
		Key     []byte            `json:"key"`
		Context map[string]string `json:"context"`
	}
	if err := json.Unmarshal(wrapped, &unwrapped); err != nil || !maps.Equal(unwrapped.Context, encryptionContext) {
		return nil, errors.New("the encryption context does not match")
	}
	return unwrapped.Key, nil
}

const testSigningKey = "whsk_test_key"

// gmailTriggers are a webhook trigger, a polling one whose own default is
// a minute, a polling one whose default is an hour, and two that cannot be
// set in one go.
func gmailTriggers() []PipedreamComponent {
	return []PipedreamComponent{
		{Key: "gmail-new-labeled-email", Name: "New Labeled Email", Description: "Emit on a label", Props: json.RawMessage(`[
			{"name":"gmail","type":"app","app":"gmail"},
			{"name":"http","type":"$.interface.http","customResponse":true},
			{"name":"db","type":"$.service.db"},
			{"name":"label","type":"string","label":"Label"},
			{"name":"withAttachments","type":"boolean","optional":true}
		]`)},
		{Key: "gmail-new-email-received", Name: "New Email Received", Props: json.RawMessage(`[
			{"name":"gmail","type":"app","app":"gmail"},
			{"name":"timer","type":"$.interface.timer","default":{"intervalSeconds":60}},
			{"name":"q","type":"string","optional":true}
		]`)},
		{Key: "gmail-new-sent-email", Name: "New Sent Email", Props: json.RawMessage(`[
			{"name":"gmail","type":"app","app":"gmail"},
			{"name":"timer","type":"$.interface.timer","default":{"intervalSeconds":3600}}
		]`)},
		{Key: "gmail-reloading", Name: "Reloading", Props: json.RawMessage(`[{"name":"kind","type":"string","reloadProps":true}]`)},
		{Key: "gmail-new-in-label", Name: "New In Label", Props: json.RawMessage(`[{"name":"label","type":"string","remoteOptions":true}]`)},
	}
}

// newTriggerHarness is a harness whose Pipedream has Gmail's triggers, with
// Gmail pinned to Pipedream, a sealer, and user-1 connected to Gmail through
// Pipedream as apc_pd.
func newTriggerHarness(t *testing.T) (harness, *triggerPipedream, *countingKeys) {
	t.Helper()
	h := newHarness(t)
	fake := &triggerPipedream{triggers: gmailTriggers(), signingKey: testSigningKey}
	keys := &countingKeys{}
	h.service.pipedream = fake
	h.service.sealer = secrets.Envelope{Keys: keys, KeyID: "key", Service: "oryxa-apps"}
	h.service.toolkitBrokers = map[string]string{"gmail": domain.BrokerPipedream}
	connection := h.activeConnection("apc_pd", "user-1", "gmail", "")
	connection.Broker, connection.PipedreamAccountID = domain.BrokerPipedream, "apn_1"
	h.store.connections[connection.ID] = connection
	return h, fake, keys
}

func (h harness) subscribe(t *testing.T, slug, config string) domain.Trigger {
	t.Helper()
	trigger, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_pd", Trigger: slug, Config: json.RawMessage(config),
	})
	if err != nil {
		t.Fatalf("CreateTrigger(%s) = %v", slug, err)
	}
	return trigger
}

func TestPipedreamTriggersAreTheCatalogsForAPipedreamApp(t *testing.T) {
	t.Parallel()
	h, _, _ := newTriggerHarness(t)
	views, next, err := h.service.CatalogTriggers(context.Background(), "gmail", "", 0)
	if err != nil || next != "" {
		t.Fatalf("CatalogTriggers() = %v, %q, %v", views, next, err)
	}
	byTrigger := map[string]TriggerTypeView{}
	for _, view := range views {
		byTrigger[view.Trigger] = view
	}
	// What reloads its props or needs remote options is not offered.
	if len(views) != 3 || byTrigger["PD_GMAIL_RELOADING"].Trigger != "" || byTrigger["PD_GMAIL_NEW_IN_LABEL"].Trigger != "" {
		t.Fatalf("views = %+v", views)
	}
	labeled := byTrigger["PD_GMAIL_NEW_LABELED_EMAIL"]
	if labeled.Kind != domain.TriggerKindWebhook || labeled.Toolkit != "gmail" || labeled.Name != "New Labeled Email" {
		t.Fatalf("labeled = %+v", labeled)
	}
	if byTrigger["PD_GMAIL_NEW_EMAIL_RECEIVED"].Kind != domain.TriggerKindPoll {
		t.Fatal("a trigger with a timer is not a poll")
	}
	// The schema offers only what a member sets: not the account, the
	// interfaces Pipedream fills, nor the timer apps sets.
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(labeled.ConfigSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties) != 2 || schema.Properties["label"] == nil || schema.Properties["withAttachments"] == nil ||
		len(schema.Required) != 1 || schema.Required[0] != "label" {
		t.Fatalf("schema = %s", labeled.ConfigSchema)
	}
	// The same schema is what a subscription is checked against.
	if err := domain.CheckTriggerConfig(labeled.ConfigSchema, json.RawMessage(`{}`)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a config missing label = %v", err)
	}

	page, next, err := h.service.CatalogTriggers(context.Background(), "gmail", "", 2)
	if err != nil || len(page) != 2 || next != "2" {
		t.Fatalf("first page = %d, %q, %v", len(page), next, err)
	}
	if rest, next, err := h.service.CatalogTriggers(context.Background(), "gmail", next, 2); err != nil || len(rest) != 1 || next != "" {
		t.Fatalf("second page = %d, %q, %v", len(rest), next, err)
	}
}

func TestSubscribingDeploysAtPipedreamAndSealsTheKey(t *testing.T) {
	t.Parallel()
	h, fake, _ := newTriggerHarness(t)
	trigger := h.subscribe(t, "PD_GMAIL_NEW_LABELED_EMAIL", `{"label":"inbox"}`)

	if len(fake.deploys) != 1 {
		t.Fatalf("deploys = %d", len(fake.deploys))
	}
	deploy := fake.deploys[0]
	if deploy.ExternalUserID != memberUser || deploy.ComponentKey != "gmail-new-labeled-email" ||
		deploy.WebhookURL != "https://apps.oryxa.test/v1/apps/webhooks/pipedream/"+trigger.ID || deploy.Timer != 0 {
		t.Fatalf("deploy = %+v", deploy)
	}
	if account, _ := json.Marshal(deploy.ConfiguredProps["gmail"]); string(account) != `{"authProvisionId":"apn_1"}` {
		t.Fatalf("app prop = %s", account)
	}
	if trigger.PipedreamTriggerID != "dc_1" || trigger.Status != domain.TriggerActive || trigger.Kind != domain.TriggerKindWebhook {
		t.Fatalf("trigger = %+v", trigger)
	}
	sealed := h.store.signingKeys[trigger.ID]
	if len(sealed) == 0 || bytes.Contains(sealed, []byte(testSigningKey)) {
		t.Fatalf("the signing key is not stored sealed: %q", sealed)
	}
	if subscribed := h.store.published(appsv1.TriggerSubscribedDetailType); len(subscribed) != 1 {
		t.Fatalf("subscribed events = %d", len(subscribed))
	}

	// Asking again answers the same subscription and deploys nothing more.
	again := h.subscribe(t, "PD_GMAIL_NEW_LABELED_EMAIL", `{ "label" : "inbox" }`)
	if again.ID != trigger.ID || len(fake.deploys) != 1 {
		t.Fatalf("again = %s, deploys = %d", again.ID, len(fake.deploys))
	}

	for name, config := range map[string]string{
		"a missing required prop": `{}`,
		"an unknown prop":         `{"label":"inbox","nope":1}`,
		"the account prop":        `{"label":"inbox","gmail":{"authProvisionId":"apn_other"}}`,
		"the timer":               `{"label":"inbox","timer":{"intervalSeconds":1}}`,
	} {
		if _, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
			ConnectionID: "apc_pd", Trigger: "PD_GMAIL_NEW_LABELED_EMAIL", Config: json.RawMessage(config),
		}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s = %v, want invalid", name, err)
		}
	}
	if _, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_pd", Trigger: "PD_GMAIL_RELOADING",
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a trigger not offered = %v, want not found", err)
	}
	if len(fake.deploys) != 1 {
		t.Fatalf("refused subscriptions deployed: %d", len(fake.deploys))
	}
}

func TestPollingTriggersPollNoMoreOftenThanTheFloor(t *testing.T) {
	t.Parallel()
	h, fake, _ := newTriggerHarness(t)
	h.subscribe(t, "PD_GMAIL_NEW_EMAIL_RECEIVED", `{}`)
	h.subscribe(t, "PD_GMAIL_NEW_SENT_EMAIL", `{}`)
	if fake.deploys[0].Timer != 15*time.Minute || fake.deploys[1].Timer != time.Hour {
		t.Fatalf("timers = %v, %v; want the floor, then the trigger's own longer one", fake.deploys[0].Timer, fake.deploys[1].Timer)
	}

	h.service.pipedreamMinPoll = 2 * time.Hour
	h.subscribe(t, "PD_GMAIL_NEW_EMAIL_RECEIVED", `{"q":"from:boss"}`)
	if fake.deploys[2].Timer != 2*time.Hour {
		t.Fatalf("timer = %v, want the configured floor", fake.deploys[2].Timer)
	}
}

func TestPipedreamTriggersNeedASealer(t *testing.T) {
	t.Parallel()
	h, fake, _ := newTriggerHarness(t)
	h.service.sealer = nil
	if _, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
		ConnectionID: "apc_pd", Trigger: "PD_GMAIL_NEW_SENT_EMAIL",
	}); !errors.Is(err, domain.ErrNotConnectable) || len(fake.deploys) != 0 {
		t.Fatalf("CreateTrigger() without a sealer = %v, deploys %d", err, len(fake.deploys))
	}
}

// Every deployed trigger costs credits, so connecting through Pipedream
// subscribes to nothing, even when the connection asked for it.
func TestAutoSubscribeLeavesPipedreamConnectionsAlone(t *testing.T) {
	t.Parallel()
	h, fake, _ := newTriggerHarness(t)
	connection := h.store.connections["apc_pd"]
	connection.AutoSubscribe = true
	h.store.connections["apc_pd"] = connection

	h.service.subscribeConnections(context.Background())
	if fake.listCalls != 0 || len(fake.deploys) != 0 || len(h.store.triggers) != 0 {
		t.Fatalf("auto-subscribe reached Pipedream: listed %d, deployed %d, triggers %d",
			fake.listCalls, len(fake.deploys), len(h.store.triggers))
	}
}

// untouchable fails the test if anything reads it: a body that must not be
// read before the trigger is known.
type untouchable struct{ t *testing.T }

func (body untouchable) Read([]byte) (int, error) {
	body.t.Error("the body was read")
	return 0, io.EOF
}

func signed(body string, at time.Time) http.Header {
	return signedWith(testSigningKey, body, at)
}

func signedWith(key, body string, at time.Time) http.Header {
	header := http.Header{}
	header.Set(pipedream.SignatureHeader, pipedream.SignWebhook(key, []byte(body), at))
	return header
}

func TestPipedreamDeliveriesArePublishedOnceAndOnlyWhenSigned(t *testing.T) {
	t.Parallel()
	h, _, keys := newTriggerHarness(t)
	trigger := h.subscribe(t, "PD_GMAIL_NEW_LABELED_EMAIL", `{"label":"inbox"}`)
	ctx := context.Background()
	deliver := func(body string, header http.Header) error {
		return h.service.ReceivePipedreamWebhook(ctx, trigger.ID, header, strings.NewReader(body))
	}

	body := `{"id":"msg_1","subject":"hello"}`
	if err := deliver(body, signed(body, testClock)); err != nil {
		t.Fatalf("a signed delivery = %v", err)
	}
	received := h.store.published(appsv1.TriggerReceivedDetailType)
	if len(received) != 1 {
		t.Fatalf("received events = %d", len(received))
	}
	var event appsv1.TriggerReceived
	if err := json.Unmarshal(received[0].Data, &event); err != nil {
		t.Fatal(err)
	}
	if event.TriggerID != trigger.ID || event.Trigger != "PD_GMAIL_NEW_LABELED_EMAIL" || event.MemberID != "user-1" ||
		string(event.Data) != body || event.DataOmitted {
		t.Fatalf("event = %+v", event)
	}

	// Redelivered, even re-signed later, it counts once.
	if err := deliver(body, signed(body, testClock.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	// Without an id, the bytes themselves are what is deduplicated.
	for range 2 {
		if err := deliver(`{"subject":"no id"}`, signed(`{"subject":"no id"}`, testClock)); err != nil {
			t.Fatal(err)
		}
	}
	if received := h.store.published(appsv1.TriggerReceivedDetailType); len(received) != 2 || h.store.triggers[trigger.ID].DeliveryCount != 2 {
		t.Fatalf("received %d, counted %d; want 2 each", len(received), h.store.triggers[trigger.ID].DeliveryCount)
	}
	// The key was opened once and then held.
	if keys.opened != 1 {
		t.Fatalf("the key was opened %d times", keys.opened)
	}

	for name, header := range map[string]http.Header{
		"unsigned":          {},
		"signed by another": signedWith("whsk_other", `{"id":"msg_2"}`, testClock),
		"stale":             signed(`{"id":"msg_2"}`, testClock.Add(-6*time.Minute)),
		"from the future":   signed(`{"id":"msg_2"}`, testClock.Add(6*time.Minute)),
	} {
		if err := deliver(`{"id":"msg_2"}`, header); !errors.Is(err, ErrUnverified) {
			t.Fatalf("%s = %v, want unverified", name, err)
		}
	}
	if err := deliver(`not json`, signed(`not json`, testClock)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a body that is not JSON = %v", err)
	}
	if received := h.store.published(appsv1.TriggerReceivedDetailType); len(received) != 2 {
		t.Fatalf("refused deliveries published: %d", len(received))
	}

	// A payload over the event's limit is published without its data.
	large := `{"id":"big","blob":"` + strings.Repeat("a", appsv1.MaxTriggerDataBytes) + `"}`
	if err := deliver(large, signed(large, testClock)); err != nil {
		t.Fatal(err)
	}
	received = h.store.published(appsv1.TriggerReceivedDetailType)
	var omitted appsv1.TriggerReceived
	if err := json.Unmarshal(received[len(received)-1].Data, &omitted); err != nil || !omitted.DataOmitted || omitted.Data != nil {
		t.Fatalf("large event = %+v, %v", omitted, err)
	}
	// Past the cap, nothing is used.
	oversized := strings.Repeat("a", MaxPipedreamDelivery+1)
	if err := deliver(oversized, signed(oversized, testClock)); !errors.Is(err, ErrDeliveryTooLarge) {
		t.Fatalf("an oversized delivery = %v", err)
	}
}

func TestPipedreamDeliveriesToNoLiveTriggerAreRefusedUnread(t *testing.T) {
	t.Parallel()
	h, _, _ := newTriggerHarness(t)
	ctx := context.Background()
	if err := h.service.ReceivePipedreamWebhook(ctx, "apt_unknown", http.Header{}, untouchable{t}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an unknown trigger = %v", err)
	}
	trigger := h.subscribe(t, "PD_GMAIL_NEW_LABELED_EMAIL", `{"label":"inbox"}`)
	if err := h.service.RemoveTrigger(ctx, memberToken, "org_1", trigger.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ReceivePipedreamWebhook(ctx, trigger.ID, http.Header{}, untouchable{t}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a removed trigger = %v", err)
	}
	// A Composio trigger's id is not a Pipedream one.
	h.store.triggers["apt_composio"] = domain.Trigger{ID: "apt_composio", OrganizationID: "org_1", ComposioTriggerID: "ti_1", Status: domain.TriggerActive}
	if err := h.service.ReceivePipedreamWebhook(ctx, "apt_composio", http.Header{}, untouchable{t}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a Composio trigger = %v", err)
	}
}

func TestRemovingAPipedreamTriggerDeletesItsDeployment(t *testing.T) {
	t.Parallel()
	h, fake, _ := newTriggerHarness(t)
	ctx := context.Background()
	first := h.subscribe(t, "PD_GMAIL_NEW_LABELED_EMAIL", `{"label":"inbox"}`)
	if err := h.service.RemoveTrigger(ctx, memberToken, "org_1", first.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != memberUser+"/dc_1" || h.store.triggers[first.ID].Status != domain.TriggerRemoved {
		t.Fatalf("deleted %v, status %s", fake.deleted, h.store.triggers[first.ID].Status)
	}
	if len(h.store.published(appsv1.TriggerUnsubscribedDetailType)) != 1 {
		t.Fatal("no unsubscribed event")
	}

	// One Pipedream no longer has is already gone, which is what was asked.
	second := h.subscribe(t, "PD_GMAIL_NEW_SENT_EMAIL", `{}`)
	fake.deleteErr = fmt.Errorf("record not found: %w", domain.ErrNotFound)
	if err := h.service.RemoveTrigger(ctx, memberToken, "org_1", second.ID); err != nil || h.store.triggers[second.ID].Status != domain.TriggerRemoved {
		t.Fatalf("RemoveTrigger(gone at Pipedream) = %v", err)
	}
	// Any other failure keeps the trigger, so removing it can be tried again.
	third := h.subscribe(t, "PD_GMAIL_NEW_EMAIL_RECEIVED", `{}`)
	fake.deleteErr = fmt.Errorf("down: %w", domain.ErrUnavailable)
	if err := h.service.RemoveTrigger(ctx, memberToken, "org_1", third.ID); err == nil || h.store.triggers[third.ID].Status != domain.TriggerActive {
		t.Fatalf("RemoveTrigger(Pipedream down) = %v", err)
	}

	// Removing the connection takes its deployed triggers down with it.
	fake.deleteErr = nil
	if err := h.service.RemoveConnection(ctx, memberToken, "org_1", "apc_pd"); err != nil {
		t.Fatal(err)
	}
	if last := fake.deleted[len(fake.deleted)-1]; last != memberUser+"/"+third.PipedreamTriggerID {
		t.Fatalf("deleted %v", fake.deleted)
	}
}
