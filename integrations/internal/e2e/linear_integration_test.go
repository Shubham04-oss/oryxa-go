package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

const (
	linearAPIKey = "lin_api_e2e_0123456789abcdef"
	linearTeamID = "9cfb482a-81e3-4154-b5b9-2c805e70a02d"
)

type linearWebhook struct {
	id             string
	url, secret    string
	resourceTypes  []string
	teamID, label  string
	allPublicTeams bool
}

// fakeLinear answers the GraphQL operations integrations sends, and delivers
// signed changes to the webhook it creates.
type fakeLinear struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	webhooks map[string]linearWebhook
	deleted  []string
	keys     []string
	// authorizations are the Authorization headers Linear was called with.
	authorizations []string
}

func newFakeLinear(t *testing.T) *fakeLinear {
	t.Helper()
	linear := &fakeLinear{t: t, webhooks: map[string]linearWebhook{}}
	linear.server = httptest.NewServer(http.HandlerFunc(linear.graphql))
	t.Cleanup(linear.server.Close)
	return linear
}

func (linear *fakeLinear) graphql(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || request.URL.Path != "/graphql" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	linear.mu.Lock()
	defer linear.mu.Unlock()
	linear.authorizations = append(linear.authorizations, request.Header.Get("Authorization"))
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(body.Query, "teams("):
		key, _ := body.Variables["key"].(string)
		linear.keys = append(linear.keys, key)
		if key != "ENG" {
			_, _ = writer.Write([]byte(`{"data":{"teams":{"nodes":[]}}}`))
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{"teams": map[string]any{"nodes": []map[string]string{{"id": linearTeamID}}}}})
	case strings.Contains(body.Query, "webhookCreate"):
		input, _ := body.Variables["input"].(map[string]any)
		webhook := linearWebhook{id: "wh_e2e_1"}
		webhook.url, _ = input["url"].(string)
		webhook.secret, _ = input["secret"].(string)
		webhook.teamID, _ = input["teamId"].(string)
		webhook.label, _ = input["label"].(string)
		webhook.allPublicTeams, _ = input["allPublicTeams"].(bool)
		for _, resourceType := range input["resourceTypes"].([]any) {
			webhook.resourceTypes = append(webhook.resourceTypes, resourceType.(string))
		}
		linear.webhooks[webhook.id] = webhook
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": map[string]any{"webhookCreate": map[string]any{"success": true, "webhook": map[string]string{"id": webhook.id}}},
		})
	case strings.Contains(body.Query, "webhookDelete"):
		id, _ := body.Variables["id"].(string)
		if _, ok := linear.webhooks[id]; !ok {
			_, _ = writer.Write([]byte(`{"errors":[{"message":"Entity not found: Webhook - Could not find referenced Webhook."}]}`))
			return
		}
		delete(linear.webhooks, id)
		linear.deleted = append(linear.deleted, id)
		_, _ = writer.Write([]byte(`{"data":{"webhookDelete":{"success":true}}}`))
	default:
		writer.WriteHeader(http.StatusBadRequest)
	}
}

func (linear *fakeLinear) only() linearWebhook {
	linear.t.Helper()
	linear.mu.Lock()
	defer linear.mu.Unlock()
	if len(linear.webhooks) != 1 {
		linear.t.Fatalf("Linear holds %d webhooks, want 1", len(linear.webhooks))
	}
	for _, webhook := range linear.webhooks {
		return webhook
	}
	return linearWebhook{}
}

// deliver sends a change to a webhook, signed with its secret, as Linear does.
func (linear *fakeLinear) deliver(webhook linearWebhook, deliveryID string, at time.Time, payload map[string]any) int {
	payload["webhookTimestamp"] = at.UnixMilli()
	payload["webhookId"] = webhook.id
	body, err := json.Marshal(payload)
	if err != nil {
		linear.t.Fatal(err)
	}
	return linear.send(webhook.url, deliveryID, body, hmacHex([]byte(webhook.secret), body))
}

func (linear *fakeLinear) send(webhookURL, deliveryID string, body []byte, signature string) int {
	status, _, _ := deliver(linear.t, webhookURL, http.Header{
		"Content-Type": {"application/json"}, "User-Agent": {"Linear-Webhook"},
		"Linear-Delivery": {deliveryID}, "Linear-Event": {"Issue"}, "Linear-Signature": {signature},
	}, body)
	return status
}

func issueCreated(title string) map[string]any {
	return map[string]any{
		"action": "create", "type": "Issue", "createdAt": "2026-09-15T12:30:00.000Z",
		"data": map[string]any{
			"id": "3f9c1e70-2a5e-4a52-9b4d-6a1e6b0a2e11", "title": title, "identifier": "ENG-42", "priority": 2,
			"team": map[string]any{"id": linearTeamID, "key": "ENG", "name": "Engineering"},
		},
		"url":            "https://linear.app/acme/issue/ENG-42/" + strings.ToLower(strings.ReplaceAll(title, " ", "-")),
		"organizationId": "b1a5e6c2-0f7a-4d1e-9a2b-3c4d5e6f7a8b",
		"actor":          map[string]any{"id": "c7b1f1a2-3d4e-5f60-7182-93a4b5c6d7e8", "name": "Megan", "type": "user"},
	}
}

func TestLinearSubscriptionEndToEnd(t *testing.T) {
	linear := newFakeLinear(t)
	s := newStack(t, linear.server)
	ctx := context.Background()
	// A Linear API key is sent as connections placed it: bare, with no prefix.
	s.connections.issues(linearAPIKey, placement{In: "header", Name: "Authorization"})
	s.connections.add(domain.Connection{
		ID: "conn_linear", AppID: "app_linear", Provider: "linear", CredentialType: "api_key", Scope: "team", TeamID: "team_eng",
		ProviderAccount: domain.ProviderAccount{ID: "b1a5e6c2", Name: "Acme"}, Status: "active",
	})

	// A member subscribes a team's issues and comments. Integrations looks the
	// team key up and creates the webhook.
	created := s.subscribe("conn_linear", map[string]string{"team": "ENG"}, []string{"Issue", "Comment"})
	webhook := linear.only()
	if !slices.Equal(linear.keys, []string{"ENG"}) || webhook.teamID != linearTeamID || webhook.allPublicTeams {
		t.Fatalf("Linear webhook = %+v, team keys looked up = %v", webhook, linear.keys)
	}
	if !slices.Equal(webhook.resourceTypes, []string{"Issue", "Comment"}) || webhook.label != "Oryxa" || len(webhook.secret) != 64 {
		t.Fatalf("Linear webhook = %+v", webhook)
	}
	if !strings.HasPrefix(webhook.url, s.publicURL+"/v1/webhooks/linear/") {
		t.Fatalf("Linear webhook URL = %q", webhook.url)
	}
	for _, authorization := range linear.authorizations {
		if authorization != linearAPIKey {
			t.Fatalf("Linear was called with %q, want the bare API key", authorization)
		}
	}
	if !s.connections.held("conn_linear") {
		t.Fatal("the connection is not held while its webhook exists")
	}

	// Linear delivers an issue being created, then delivers it again.
	for range 2 {
		if status := linear.deliver(webhook, "4b0f9a0e-1f3a-4f8a-9a1e-6d2f0f6b1c33", s.clock.Now(), issueCreated("Retries are not backing off")); status != http.StatusOK {
			t.Fatalf("Issue delivery = %d, want 200", status)
		}
	}
	triggers := eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType)
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(triggers))
	}
	trigger := triggers[0]
	if trigger.Event != "linear.issue.create" || trigger.SubscriptionID != created.ID || trigger.Scope != "team" || trigger.TeamID != "team_eng" ||
		trigger.DeliveryID != "4b0f9a0e-1f3a-4f8a-9a1e-6d2f0f6b1c33" {
		t.Fatalf("trigger = %+v", trigger)
	}
	if trigger.Resource.Type != "issue" || trigger.Resource.ID != "3f9c1e70-2a5e-4a52-9b4d-6a1e6b0a2e11" || trigger.Resource.Name != "Retries are not backing off" ||
		!strings.HasPrefix(trigger.Resource.URL, "https://linear.app/acme/issue/ENG-42/") {
		t.Fatalf("trigger resource = %+v", trigger.Resource)
	}
	if trigger.Actor == nil || trigger.Actor.Name != "Megan" || !trigger.OccurredAt.Equal(time.Date(2026, 9, 15, 12, 30, 0, 0, time.UTC)) {
		t.Fatalf("trigger actor = %+v, occurred at %v", trigger.Actor, trigger.OccurredAt)
	}

	// A delivery from two minutes ago, one signed with another secret, and one
	// from a webhook this subscription does not have are not published.
	if status := linear.deliver(webhook, "stale-1", s.clock.Now().Add(-2*time.Minute), issueCreated("Old")); status != http.StatusUnauthorized {
		t.Fatalf("stale delivery = %d, want 401", status)
	}
	forged, _ := json.Marshal(issueCreated("Forged"))
	if status := linear.send(webhook.url, "forged-1", forged, hmacHex([]byte("not-the-secret"), forged)); status != http.StatusUnauthorized {
		t.Fatalf("forged delivery = %d, want 401", status)
	}
	elsewhere := issueCreated("From another webhook")
	elsewhere["webhookTimestamp"] = s.clock.Now().UnixMilli()
	elsewhere["webhookId"] = "wh_somewhere_else"
	body, _ := json.Marshal(elsewhere)
	if status := linear.send(webhook.url, "other-registration-1", body, hmacHex([]byte(webhook.secret), body)); status != http.StatusOK {
		t.Fatalf("delivery naming another webhook = %d, want 200", status)
	}
	if triggers := eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType); len(triggers) != 1 {
		t.Fatalf("triggers = %d after refused deliveries, want 1", len(triggers))
	}
	s.noSecrets(webhook.secret, linearAPIKey)

	// Stopping the subscription removes the webhook and releases the hold.
	if status, body := s.do(http.MethodDelete, "/subscriptions/"+created.ID, memberToken, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE subscription = %d %s", status, body)
	}
	s.service.CleanupDue(ctx)
	if !slices.Equal(linear.deleted, []string{webhook.id}) || s.connections.held("conn_linear") {
		t.Fatalf("deleted webhooks = %v, still held = %v", linear.deleted, s.connections.held("conn_linear"))
	}
	// Removing it again, as a retry would, is not an error.
	s.service.CleanupDue(ctx)
	if len(linear.deleted) != 1 {
		t.Fatalf("deleted webhooks = %v after a second cleanup", linear.deleted)
	}
}

func TestLinearSubscriptionForATeamLinearDoesNotHaveIsRefused(t *testing.T) {
	linear := newFakeLinear(t)
	s := newStack(t, linear.server)
	s.connections.issues(linearAPIKey, placement{In: "header", Name: "Authorization"})
	s.connections.add(domain.Connection{ID: "conn_linear", AppID: "app_linear", Provider: "linear", Scope: "organization", ProviderAccount: domain.ProviderAccount{ID: "b1a5e6c2"}, Status: "active"})

	status, body := s.do(http.MethodPost, "/subscriptions", memberToken, map[string]any{"connection_id": "conn_linear", "target": map[string]string{"team": "NOPE"}})

	if status != http.StatusBadRequest || !strings.Contains(string(body), "invalid_request") {
		t.Fatalf("POST subscriptions for an unknown team = %d %s, want 400", status, body)
	}
	if len(linear.webhooks) != 0 || s.connections.held("conn_linear") {
		t.Fatal("a refused subscription left a webhook or a hold behind")
	}
	if status, body := s.do(http.MethodGet, "/subscriptions", memberToken, nil); !strings.Contains(string(body), `"subscriptions":[]`) {
		t.Fatalf("GET subscriptions = %d %s, want none", status, body)
	}
}
