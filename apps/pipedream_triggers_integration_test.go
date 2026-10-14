package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/pipedream"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// pipedreamTriggerStub is the part of Pipedream Connect triggers use, as the
// real client calls it: one Gmail trigger that polls, deployments that
// answer a signing key, and deletions. It mirrors the chaos rig's
// FakePipedream, which lives under test/internal and so cannot be imported
// here.
type pipedreamTriggerStub struct {
	mu       sync.Mutex
	deployed map[string]map[string]any
	deleted  []string
}

const stubSigningKey = "whsk_stub_signing_key"

func newPipedreamTriggerStub(t *testing.T) (*pipedreamTriggerStub, *httptest.Server) {
	t.Helper()
	stub := &pipedreamTriggerStub{deployed: map[string]map[string]any{}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/oauth/token":
			_, _ = writer.Write([]byte(`{"access_token":"token","expires_in":3600}`))
		case request.Method == http.MethodGet && request.URL.Path == "/connect/proj_1/triggers":
			_, _ = writer.Write([]byte(`{"data":[{"key":"gmail-new-email-received","name":"New Email Received",
				"component_type":"source","configurable_props":[
					{"name":"gmail","type":"app","app":"gmail"},
					{"name":"timer","type":"$.interface.timer","default":{"intervalSeconds":60}},
					{"name":"q","type":"string","optional":true}]}],
				"page_info":{"count":1,"total_count":1,"end_cursor":null}}`))
		case request.Method == http.MethodPost && request.URL.Path == "/connect/proj_1/triggers/deploy":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			id := fmt.Sprintf("dc_%d", len(stub.deployed)+1)
			stub.deployed[id] = body
			writeJSON(writer, map[string]any{"data": map[string]any{"id": id, "active": true, "webhook_signing_key": stubSigningKey}})
		case request.Method == http.MethodDelete && strings.HasPrefix(request.URL.Path, "/connect/proj_1/deployed-triggers/"):
			stub.deleted = append(stub.deleted, request.URL.Query().Get("external_user_id")+"/"+
				strings.TrimPrefix(request.URL.Path, "/connect/proj_1/deployed-triggers/"))
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"error":"Not Found"}`))
		}
	}))
	t.Cleanup(server.Close)
	return stub, server
}

func (stub *pipedreamTriggerStub) only(t *testing.T) map[string]any {
	t.Helper()
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.deployed) != 1 {
		t.Fatalf("deployed %d triggers", len(stub.deployed))
	}
	for _, body := range stub.deployed {
		return body
	}
	return nil
}

// plainKeys stands in for KMS: a wrapped key unwraps only with the context
// it was made for.
type plainKeys struct{}

func (plainKeys) GenerateDataKey(_ context.Context, _ string, encryptionContext map[string]string) ([]byte, []byte, error) {
	key := []byte(randomHex(16))
	wrapped, err := json.Marshal(map[string]any{"key": key, "context": encryptionContext})
	return key, wrapped, err
}

func (plainKeys) DecryptDataKey(_ context.Context, wrapped []byte, encryptionContext map[string]string) ([]byte, error) {
	var unwrapped struct {
		Key     []byte            `json:"key"`
		Context map[string]string `json:"context"`
	}
	if err := json.Unmarshal(wrapped, &unwrapped); err != nil || !maps.Equal(unwrapped.Context, encryptionContext) {
		return nil, errors.New("the encryption context does not match")
	}
	return unwrapped.Key, nil
}

// TestPipedreamTriggerDeliversEndToEnd runs the whole service against a
// stand-in Pipedream through the real client: a member subscribes their
// Pipedream connection to a polling trigger, Pipedream delivers a signed
// event to the webhook URL apps gave it, the event reaches the bus once, and
// removing the trigger takes the deployment down and closes the URL.
func TestPipedreamTriggerDeliversEndToEnd(t *testing.T) {
	store := openTestStore(t)
	stub, pipedreamServer := newPipedreamTriggerStub(t)
	client, err := pipedream.New(pipedream.Config{
		ProjectID: "proj_1", Environment: pipedream.EnvironmentDevelopment,
		ClientID: "id", ClientSecret: "secret", BaseURL: pipedreamServer.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	service, err := New(Dependencies{
		Store: store, Directory: directoryClient(t, newDirectoryStub(t).URL), Composio: composioClient(t, newComposioStub(t).URL),
		Pipedream: client, Sealer: secrets.Envelope{Keys: plainKeys{}, KeyID: "key", Service: "oryxa-apps"},
		Sink: sink, Verifier: authHarness(t).Verifier(), Logger: slog.New(slog.DiscardHandler),
	}, Config{PublicBaseURL: "https://apps.oryxa.test", AllowedOrigins: []string{"https://app.oryxa.test"}, WebhookSecret: webhookSecret})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(service.Handler())
	t.Cleanup(api.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	dispatcher, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		service.dispatcher.Run(dispatcher)
		close(done)
	}()
	t.Cleanup(func() {
		stop()
		<-done
	})

	// The member's Gmail, connected through Pipedream.
	now := time.Now().UTC()
	connection := domain.Connection{
		ID: "apc_" + randomHex(8), OrganizationID: "org_1", MemberID: "user-1", Toolkit: "gmail",
		Broker: domain.BrokerPipedream, ComposioUserID: domain.ComposioUserID("org_1", "user-1"),
		Status: domain.ConnectionPending, AutoSubscribe: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateConnection(ctx, connection, randomHex(16), domain.Event{ID: "evt_" + randomHex(8), DetailType: appsv1.ConnectionInitiatedDetailType, OccurredAt: now, Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ActivatePipedreamConnection(ctx, connection.ID, "apn_1", "ada@acme.test", now,
		domain.Event{ID: "evt_" + randomHex(8), DetailType: appsv1.ConnectionConnectedDetailType, OccurredAt: now, Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	var trigger struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	post(t, api.URL+"/v1/organizations/org_1/apps/triggers",
		fmt.Sprintf(`{"connection_id":%q,"trigger":"PD_GMAIL_NEW_EMAIL_RECEIVED","config":{"q":"is:unread"}}`, connection.ID),
		http.StatusCreated, &trigger)
	if trigger.Kind != "poll" {
		t.Fatalf("trigger = %+v", trigger)
	}
	deploy := stub.only(t)
	props, _ := deploy["configured_props"].(map[string]any)
	timer, _ := props["timer"].(map[string]any)
	if deploy["webhook_url"] != "https://apps.oryxa.test/v1/apps/webhooks/pipedream/"+trigger.ID ||
		deploy["external_user_id"] != "oryxa.org_1.user-1" || deploy["emit_on_deploy"] != false ||
		timer["intervalSeconds"] != float64(900) || props["q"] != "is:unread" {
		t.Fatalf("deploy = %v", deploy)
	}
	if account, _ := props["gmail"].(map[string]any); account["authProvisionId"] != "apn_1" {
		t.Fatalf("app prop = %v", props["gmail"])
	}
	var sealed []byte
	if err := store.Pool().QueryRow(ctx, `SELECT pipedream_signing_key FROM apps_triggers WHERE id = $1`, trigger.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if len(sealed) == 0 || bytes.Contains(sealed, []byte(stubSigningKey)) {
		t.Fatal("the signing key is not stored sealed")
	}

	webhook, err := url.Parse(deploy["webhook_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(path, body, key string) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, api.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(pipedream.SignatureHeader, pipedream.SignWebhook(key, []byte(body), time.Now()))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		return response.StatusCode
	}

	event := `{"id":"18c1f0","subject":"Quarterly numbers"}`
	if status := deliver(webhook.Path, event, stubSigningKey); status != http.StatusOK {
		t.Fatalf("a signed delivery = %d", status)
	}
	received := decodeEvent(t, waitForEvent(t, sink, appsv1.TriggerReceivedDetailType))
	data, _ := received["data"].(map[string]any)
	if received["trigger_id"] != trigger.ID || received["trigger"] != "PD_GMAIL_NEW_EMAIL_RECEIVED" || data["subject"] != "Quarterly numbers" {
		t.Fatalf("received = %v", received)
	}
	if status := deliver(webhook.Path, event, stubSigningKey); status != http.StatusOK {
		t.Fatalf("a redelivery = %d", status)
	}
	if status := deliver(webhook.Path, `{"id":"forged"}`, "whsk_someone_else"); status != http.StatusUnauthorized {
		t.Fatalf("a forged delivery = %d", status)
	}
	if status := deliver("/v1/apps/webhooks/pipedream/apt_nothing", event, stubSigningKey); status != http.StatusNotFound {
		t.Fatalf("a delivery to no trigger = %d", status)
	}

	request, err := http.NewRequest(http.MethodDelete, api.URL+"/v1/organizations/org_1/apps/triggers/"+trigger.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", memberToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE trigger = %d", response.StatusCode)
	}
	stub.mu.Lock()
	deleted := append([]string(nil), stub.deleted...)
	stub.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "oryxa.org_1.user-1/dc_1" {
		t.Fatalf("deleted at Pipedream = %v", deleted)
	}
	if status := deliver(webhook.Path, `{"id":"late"}`, stubSigningKey); status != http.StatusNotFound {
		t.Fatalf("a delivery after removal = %d", status)
	}
	// Exactly one delivery reached the bus: the redelivery, the forgery and
	// the late one did not.
	waitFor(t, "the unsubscribe", func() bool { return sink.count(appsv1.TriggerUnsubscribedDetailType) == 1 })
	if count := sink.count(appsv1.TriggerReceivedDetailType); count != 1 {
		t.Fatalf("received events = %d", count)
	}
}
