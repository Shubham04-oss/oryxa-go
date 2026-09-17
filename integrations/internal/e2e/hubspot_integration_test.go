package e2e

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"
	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// hubspotSend posts a batch to a HubSpot app's webhook URL, signed the v3
// way: HMAC-SHA256, base64-encoded, over the method, the whole URL called, the
// raw body and the timestamp (epoch milliseconds), as HubSpot signs it.
func hubspotSend(t *testing.T, webhookURL, clientSecret string, at time.Time, body string) (int, http.Header, []byte) {
	t.Helper()
	if _, err := url.Parse(webhookURL); err != nil {
		t.Fatal(err)
	}
	timestamp := strconv.FormatInt(at.UnixMilli(), 10)
	message := http.MethodPost + webhookURL + body + timestamp
	mac := hmac.New(sha256.New, []byte(clientSecret))
	mac.Write([]byte(message))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return deliver(t, webhookURL, http.Header{
		"Content-Type":                {"application/json"},
		"X-HubSpot-Signature-Version": {"v3"},
		"X-HubSpot-Signature-V3":      {signature},
		"X-HubSpot-Request-Timestamp": {timestamp},
	}, []byte(body))
}

// hubspotItem is one event in a HubSpot v3 webhook delivery batch. portal,
// object and event are HubSpot's numeric IDs, sent as JSON numbers, which is
// what hubspotDelivery's json.Number fields require.
func hubspotItem(portal, object, event int64, eventType string, occurredAt time.Time, propertyName, propertyValue string) map[string]any {
	item := map[string]any{
		"portalId": portal, "objectId": object, "eventId": event, "subscriptionId": 55,
		"attemptNumber": 0, "occurredAt": occurredAt.UnixMilli(), "eventType": eventType,
		"changeSource": "CRM_UI",
	}
	if propertyName != "" {
		item["propertyName"], item["propertyValue"] = propertyName, propertyValue
	}
	return item
}

// hubspotBatch is the array of events HubSpot posts in one delivery.
func hubspotBatch(items ...map[string]any) string {
	encoded, _ := json.Marshal(items)
	return string(encoded)
}

func TestHubSpotSubscriptionEndToEnd(t *testing.T) {
	s := newStack(t, nil)
	ctx := context.Background()
	s.connections.add(domain.Connection{
		ID: "conn_hubspot", AppID: "app_hubspot", Provider: "hubspot", CredentialType: "oauth2", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "12345678", Name: "Acme"}, Status: "active",
	})

	// Until an org admin sets the HubSpot app's client secret, members cannot
	// subscribe, and a member may not set it.
	status, body := s.do(http.MethodPost, "/subscriptions", memberToken, map[string]any{"connection_id": "conn_hubspot", "events": []string{"contact.creation"}})
	if status != http.StatusConflict || !strings.Contains(string(body), "webhook_secret_required") {
		t.Fatalf("POST subscriptions before the secret is set = %d %s", status, body)
	}
	const clientSecret = "8f742231-b10e-4888-abcd-99yyyzzz85a5"
	if status, body := s.do(http.MethodPut, "/apps/app_hubspot/webhook", memberToken, map[string]string{"secret": clientSecret}); status != http.StatusForbidden {
		t.Fatalf("PUT webhook as a member = %d %s", status, body)
	}
	status, body = s.do(http.MethodPut, "/apps/app_hubspot/webhook", adminToken, map[string]string{"secret": clientSecret})
	var webhook struct {
		URL      string `json:"url"`
		Provider string `json:"provider"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &webhook) != nil || !strings.HasPrefix(webhook.URL, s.publicURL+"/v1/webhooks/hubspot/") || strings.Contains(string(body), clientSecret) {
		t.Fatalf("PUT webhook as an admin = %d %s", status, body)
	}

	// A HubSpot app delivers every account's events to that one URL: a
	// subscription takes no target of its own, unlike a per-repository GitHub
	// hook or a per-channel target.
	if status, body := s.do(http.MethodPost, "/subscriptions", memberToken, map[string]any{
		"connection_id": "conn_hubspot", "target": map[string]string{"portal": "12345678"}, "events": []string{"contact.creation"},
	}); status != http.StatusBadRequest {
		t.Fatalf("POST subscriptions with a target = %d %s, want 400", status, body)
	}

	// The member subscribes to contact creations in their portal.
	created := s.subscribe("conn_hubspot", nil, []string{"contact.creation"})
	if created.Status != "active" {
		t.Fatalf("created subscription = %+v", created)
	}
	if s.connections.held("conn_hubspot") {
		t.Fatal("a HubSpot subscription registers nothing, so it holds nothing")
	}

	// HubSpot delivers a batch: a contact created in the connected portal, a
	// property change the subscription did not ask for, and a contact created
	// in another portal: only the first is published.
	now := s.clock.Now()
	batch := hubspotBatch(
		hubspotItem(12345678, 9001, 1001, "contact.creation", now, "", ""),
		hubspotItem(12345678, 9001, 1002, "contact.propertyChange", now.Add(time.Second), "email", "person@example.com"),
		hubspotItem(99999999, 1, 1003, "contact.creation", now.Add(2*time.Second), "", ""),
	)
	if status, _, reply := hubspotSend(t, webhook.URL, clientSecret, now, batch); status != http.StatusOK {
		t.Fatalf("batch delivery = %d %s", status, reply)
	}
	triggers := eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType)
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(triggers))
	}
	trigger := triggers[0]
	if trigger.SubscriptionID != created.ID || trigger.Event != "hubspot.contact.creation" || trigger.DeliveryID != "1001" ||
		trigger.ProviderAccount.ID != "12345678" || trigger.Resource != (integrationsv1.Resource{Type: "contact", ID: "9001"}) {
		t.Fatalf("trigger = %+v", trigger)
	}
	if !trigger.OccurredAt.Equal(now) {
		t.Fatalf("trigger occurred at %v, want %v", trigger.OccurredAt, now)
	}

	// A retry of the same batch, a replay from ten minutes ago, and a request
	// signed with another secret are not published.
	if status, _, _ := hubspotSend(t, webhook.URL, clientSecret, now, batch); status != http.StatusOK {
		t.Fatalf("retried batch = %d, want 200", status)
	}
	replay := hubspotBatch(hubspotItem(12345678, 9001, 1004, "contact.creation", now, "", ""))
	if status, _, _ := hubspotSend(t, webhook.URL, clientSecret, now.Add(-10*time.Minute), replay); status != http.StatusUnauthorized {
		t.Fatalf("replayed request = %d, want 401", status)
	}
	tampered := hubspotBatch(hubspotItem(12345678, 9001, 1005, "contact.creation", now, "", ""))
	if status, _, _ := hubspotSend(t, webhook.URL, "another-client-secret-000000000", now, tampered); status != http.StatusUnauthorized {
		t.Fatalf("request signed with another secret = %d, want 401", status)
	}
	if triggers := eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType); len(triggers) != 1 {
		t.Fatalf("triggers = %d after refused and repeated deliveries, want 1", len(triggers))
	}

	// Rotating the client secret keeps the webhook URL; the old secret no
	// longer verifies.
	const rotated = "0a7c3e5d-9b1f-42e4-96c8-a0b2d4f6e8a1"
	status, body = s.do(http.MethodPut, "/apps/app_hubspot/webhook", adminToken, map[string]string{"secret": rotated})
	if status != http.StatusOK || !strings.Contains(string(body), webhook.URL) {
		t.Fatalf("PUT webhook to rotate = %d %s, want the same URL", status, body)
	}
	oldSecret := hubspotBatch(hubspotItem(12345678, 9001, 1006, "contact.creation", now, "", ""))
	if status, _, _ := hubspotSend(t, webhook.URL, clientSecret, now, oldSecret); status != http.StatusUnauthorized {
		t.Fatalf("request signed with the old secret = %d, want 401", status)
	}
	newSecret := hubspotBatch(hubspotItem(12345678, 9001, 1007, "contact.creation", now, "", ""))
	if status, _, _ := hubspotSend(t, webhook.URL, rotated, now, newSecret); status != http.StatusOK {
		t.Fatalf("request signed with the new secret = %d, want 200", status)
	}
	s.noSecrets(clientSecret, rotated, providerToken)

	// When the connection is disconnected, its subscription stops and nothing
	// more is published for it.
	if err := s.service.ConnectionDisconnected(ctx, connectionseventsv1.ConnectionDisconnected{ConnectionID: "conn_hubspot", OrganizationID: organization, Reason: "disconnected"}); err != nil {
		t.Fatal(err)
	}
	if stored := s.stored(created.ID); stored.Status != domain.SubscriptionStopped || stored.CleanupAt != nil {
		t.Fatalf("subscription after disconnect = %+v", stored)
	}
	before := len(eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType))
	afterDisconnect := hubspotBatch(hubspotItem(12345678, 9001, 1008, "contact.creation", now, "", ""))
	if status, _, _ := hubspotSend(t, webhook.URL, rotated, now, afterDisconnect); status != http.StatusOK {
		t.Fatalf("event after disconnect = %d, want 200", status)
	}
	if after := len(eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType)); after != before {
		t.Fatalf("triggers = %d after the subscription stopped, want %d", after, before)
	}

	// Deleting the app forgets its client secret: its webhook URL then names
	// nothing.
	if err := s.service.AppDeleted(ctx, connectionseventsv1.AppDeleted{AppID: "app_hubspot", OrganizationID: organization, Provider: "hubspot", Reason: "deleted"}); err != nil {
		t.Fatal(err)
	}
	final := hubspotBatch(hubspotItem(12345678, 9001, 1009, "contact.creation", now, "", ""))
	if status, _, _ := hubspotSend(t, webhook.URL, rotated, now, final); status != http.StatusNotFound {
		t.Fatalf("event after the app was deleted = %d, want 404", status)
	}
}
