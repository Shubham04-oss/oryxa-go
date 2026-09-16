package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"
	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// slackSend posts a body to a Slack app's Request URL, signed with a signing
// secret at a time, as Slack does, and answers integrations' reply.
func slackSend(t *testing.T, requestURL, signingSecret string, at time.Time, body string) (int, http.Header, []byte) {
	t.Helper()
	timestamp := strconv.FormatInt(at.Unix(), 10)
	return deliver(t, requestURL, http.Header{
		"Content-Type":              {"application/json"},
		"User-Agent":                {"Slackbot 1.0 (+https://api.slack.com/robots)"},
		"X-Slack-Request-Timestamp": {timestamp},
		"X-Slack-Signature":         {"v0=" + hmacHex([]byte(signingSecret), []byte("v0:"+timestamp+":"+body))},
	}, []byte(body))
}

// slackEvent is an Events API callback for a workspace.
func slackEvent(team, eventID string, event map[string]any) string {
	encoded, _ := json.Marshal(map[string]any{
		"token": "legacy-verification-token", "team_id": team, "api_app_id": "A0APP", "type": "event_callback",
		"event_id": eventID, "event_time": 1789992000, "event": event,
		"authorizations": []map[string]any{{"team_id": team, "user_id": "U0BOT", "is_bot": true}},
	})
	return string(encoded)
}

func TestSlackSubscriptionEndToEnd(t *testing.T) {
	s := newStack(t, nil)
	ctx := context.Background()
	s.connections.add(domain.Connection{
		ID: "conn_slack", AppID: "app_slack", Provider: "slack", CredentialType: "oauth2", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "T0001:U0001", Name: "Acme"}, Status: "active",
	})

	// Until an org admin sets the Slack app's signing secret, members cannot
	// subscribe, and a member may not set it.
	status, body := s.do(http.MethodPost, "/subscriptions", memberToken, map[string]any{"connection_id": "conn_slack", "events": []string{"message"}})
	if status != http.StatusConflict || !strings.Contains(string(body), "webhook_secret_required") {
		t.Fatalf("POST subscriptions before the secret is set = %d %s", status, body)
	}
	const signingSecret = "8f742231b10e8888abcd99yyyzzz85a5"
	if status, body := s.do(http.MethodPut, "/apps/app_slack/webhook", memberToken, map[string]string{"secret": signingSecret}); status != http.StatusForbidden {
		t.Fatalf("PUT webhook as a member = %d %s", status, body)
	}
	status, body = s.do(http.MethodPut, "/apps/app_slack/webhook", adminToken, map[string]string{"secret": signingSecret})
	var webhook struct {
		URL      string `json:"url"`
		Provider string `json:"provider"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &webhook) != nil || !strings.HasPrefix(webhook.URL, s.publicURL+"/v1/webhooks/slack/") || strings.Contains(string(body), signingSecret) {
		t.Fatalf("PUT webhook as an admin = %d %s", status, body)
	}

	// Slack verifies the Request URL the admin sets on the app.
	status, header, reply := slackSend(t, webhook.URL, signingSecret, s.clock.Now(), `{"token":"legacy","challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P","type":"url_verification"}`)
	if status != http.StatusOK || string(reply) != "3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P" || !strings.HasPrefix(header.Get("Content-Type"), "text/plain") {
		t.Fatalf("url_verification = %d %q %q", status, header.Get("Content-Type"), reply)
	}

	// The member subscribes to messages in their workspace.
	created := s.subscribe("conn_slack", nil, []string{"message"})
	if created.Status != "active" {
		t.Fatalf("created subscription = %+v", created)
	}
	if s.connections.held("conn_slack") {
		t.Fatal("a Slack subscription registers nothing, so it holds nothing")
	}

	// Slack delivers a message in the workspace, a reaction the subscription
	// did not ask for, and a message in another workspace the app is installed
	// in: only the first is published.
	message := slackEvent("T0001", "Ev0001", map[string]any{"type": "message", "channel": "C0001", "user": "U0002", "text": "deploy is green", "ts": "1789992000.000100"})
	for _, delivery := range []string{
		message,
		slackEvent("T0001", "Ev0002", map[string]any{"type": "reaction_added", "user": "U0002", "reaction": "tada", "item": map[string]any{"type": "message", "channel": "C0001", "ts": "1789992000.000100"}}),
		slackEvent("T9999", "Ev0003", map[string]any{"type": "message", "channel": "C9999", "user": "U9999", "text": "elsewhere", "ts": "1789992001.000100"}),
	} {
		if status, _, reply := slackSend(t, webhook.URL, signingSecret, s.clock.Now(), delivery); status != http.StatusOK {
			t.Fatalf("event delivery = %d %s", status, reply)
		}
	}
	triggers := eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType)
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(triggers))
	}
	trigger := triggers[0]
	if trigger.SubscriptionID != created.ID || trigger.Event != "slack.message" || trigger.DeliveryID != "Ev0001" || trigger.ProviderAccount.ID != "T0001:U0001" ||
		trigger.Resource != (integrationsv1.Resource{Type: "message", ID: "1789992000.000100", Name: "C0001"}) || trigger.Actor == nil || trigger.Actor.ID != "U0002" {
		t.Fatalf("trigger = %+v", trigger)
	}
	if !trigger.OccurredAt.Equal(time.Unix(1789992000, 0)) {
		t.Fatalf("trigger occurred at %v, want the event time", trigger.OccurredAt)
	}
	if strings.Contains(string(trigger.Payload), "legacy-verification-token") || !strings.Contains(string(trigger.Payload), "deploy is green") {
		t.Fatalf("trigger payload = %s, want the event without Slack's verification token", trigger.Payload)
	}

	// A retry of the same event, a replay from ten minutes ago, and a request
	// signed with another secret are not published.
	if status, _, _ := slackSend(t, webhook.URL, signingSecret, s.clock.Now(), message); status != http.StatusOK {
		t.Fatalf("retried event = %d, want 200", status)
	}
	if status, _, _ := slackSend(t, webhook.URL, signingSecret, s.clock.Now().Add(-10*time.Minute), slackEvent("T0001", "Ev0004", map[string]any{"type": "message", "ts": "1"})); status != http.StatusUnauthorized {
		t.Fatalf("replayed request = %d, want 401", status)
	}
	if status, _, _ := slackSend(t, webhook.URL, "another-signing-secret-000000000", s.clock.Now(), slackEvent("T0001", "Ev0005", map[string]any{"type": "message", "ts": "2"})); status != http.StatusUnauthorized {
		t.Fatalf("request signed with another secret = %d, want 401", status)
	}
	if triggers := eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType); len(triggers) != 1 {
		t.Fatalf("triggers = %d after refused and repeated deliveries, want 1", len(triggers))
	}

	// Rotating the signing secret keeps the Request URL; the old secret no
	// longer verifies.
	const rotated = "0a7c3e5d9b1f2e4d6c8a0b2d4f6e8a1c"
	status, body = s.do(http.MethodPut, "/apps/app_slack/webhook", adminToken, map[string]string{"secret": rotated})
	if status != http.StatusOK || !strings.Contains(string(body), webhook.URL) {
		t.Fatalf("PUT webhook to rotate = %d %s, want the same URL", status, body)
	}
	if status, _, _ := slackSend(t, webhook.URL, signingSecret, s.clock.Now(), slackEvent("T0001", "Ev0006", map[string]any{"type": "message", "ts": "3"})); status != http.StatusUnauthorized {
		t.Fatalf("request signed with the old secret = %d, want 401", status)
	}
	if status, _, _ := slackSend(t, webhook.URL, rotated, s.clock.Now(), slackEvent("T0001", "Ev0007", map[string]any{"type": "message", "ts": "4"})); status != http.StatusOK {
		t.Fatalf("request signed with the new secret = %d, want 200", status)
	}
	s.noSecrets(signingSecret, rotated, providerToken)

	// When the connection is disconnected, its subscription stops and nothing
	// more is published for it.
	if err := s.service.ConnectionDisconnected(ctx, connectionseventsv1.ConnectionDisconnected{ConnectionID: "conn_slack", OrganizationID: organization, Reason: "disconnected"}); err != nil {
		t.Fatal(err)
	}
	if stored := s.stored(created.ID); stored.Status != domain.SubscriptionStopped || stored.CleanupAt != nil {
		t.Fatalf("subscription after disconnect = %+v", stored)
	}
	before := len(eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType))
	if status, _, _ := slackSend(t, webhook.URL, rotated, s.clock.Now(), slackEvent("T0001", "Ev0008", map[string]any{"type": "message", "ts": "5"})); status != http.StatusOK {
		t.Fatalf("event after disconnect = %d, want 200", status)
	}
	if after := len(eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType)); after != before {
		t.Fatalf("triggers = %d after the subscription stopped, want %d", after, before)
	}

	// Deleting the app forgets its signing secret: its Request URL then names
	// nothing.
	if err := s.service.AppDeleted(ctx, connectionseventsv1.AppDeleted{AppID: "app_slack", OrganizationID: organization, Provider: "slack", Reason: "deleted"}); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := slackSend(t, webhook.URL, rotated, s.clock.Now(), slackEvent("T0001", "Ev0009", map[string]any{"type": "message", "ts": "6"})); status != http.StatusNotFound {
		t.Fatalf("event after the app was deleted = %d, want 404", status)
	}
}
