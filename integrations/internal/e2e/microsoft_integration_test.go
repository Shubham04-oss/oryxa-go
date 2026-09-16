package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

type graphSubscription struct {
	ID                       string    `json:"id"`
	Resource                 string    `json:"resource"`
	ChangeType               string    `json:"changeType"`
	NotificationURL          string    `json:"notificationUrl"`
	LifecycleNotificationURL string    `json:"lifecycleNotificationUrl"`
	ClientState              string    `json:"clientState"`
	ExpirationDateTime       time.Time `json:"expirationDateTime"`
	LatestSupportedTLS       string    `json:"latestSupportedTlsVersion"`
}

// fakeGraph keeps change notification subscriptions as Microsoft Graph does:
// it validates each notification URL before it creates a subscription, and
// sends notifications and lifecycle events marked with the clientState.
type fakeGraph struct {
	t             *testing.T
	server        *httptest.Server
	mu            sync.Mutex
	subscriptions map[string]graphSubscription
	validated     []string
	renewals      []time.Time
	deleted       []string
	// unreachable makes every notification URL fail validation.
	unreachable bool
}

func newFakeGraph(t *testing.T) *fakeGraph {
	t.Helper()
	graph := &fakeGraph{t: t, subscriptions: map[string]graphSubscription{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1.0/subscriptions", graph.create)
	mux.HandleFunc("PATCH /v1.0/subscriptions/{id}", graph.renew)
	mux.HandleFunc("DELETE /v1.0/subscriptions/{id}", graph.delete)
	graph.server = httptest.NewServer(mux)
	t.Cleanup(graph.server.Close)
	return graph
}

func (graph *fakeGraph) authorized(writer http.ResponseWriter, request *http.Request) bool {
	if request.Header.Get("Authorization") != "Bearer "+providerToken {
		writer.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

// validate asks a notification URL to echo a validation token, as Graph does
// before it creates a subscription.
func (graph *fakeGraph) validate(notificationURL string) bool {
	token := fmt.Sprintf("Validation: Testing client application reachability for subscription Request-Id: %d", time.Now().UnixNano())
	status, header, body := deliver(graph.t, notificationURL+"?validationToken="+url.QueryEscape(token), http.Header{"Content-Type": {"text/plain; charset=utf-8"}}, nil)
	return status == http.StatusOK && strings.HasPrefix(header.Get("Content-Type"), "text/plain") && string(body) == token
}

func (graph *fakeGraph) create(writer http.ResponseWriter, request *http.Request) {
	if !graph.authorized(writer, request) {
		return
	}
	var subscription graphSubscription
	if err := json.NewDecoder(request.Body).Decode(&subscription); err != nil || subscription.ClientState == "" {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	graph.mu.Lock()
	unreachable := graph.unreachable
	graph.mu.Unlock()
	for _, notificationURL := range []string{subscription.NotificationURL, subscription.LifecycleNotificationURL} {
		if unreachable || !graph.validate(notificationURL) {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"code":"InvalidRequest","message":"Subscription validation request failed."}}`))
			return
		}
	}
	graph.mu.Lock()
	subscription.ID = fmt.Sprintf("7f105c7d-2dc5-4530-97cd-4e7ae6534c%02d", len(graph.subscriptions)+1)
	graph.subscriptions[subscription.ID] = subscription
	graph.validated = append(graph.validated, subscription.NotificationURL, subscription.LifecycleNotificationURL)
	graph.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(subscription)
}

func (graph *fakeGraph) renew(writer http.ResponseWriter, request *http.Request) {
	if !graph.authorized(writer, request) {
		return
	}
	var body struct {
		ExpirationDateTime time.Time `json:"expirationDateTime"`
	}
	_ = json.NewDecoder(request.Body).Decode(&body)
	graph.mu.Lock()
	defer graph.mu.Unlock()
	subscription, ok := graph.subscriptions[request.PathValue("id")]
	if !ok {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	subscription.ExpirationDateTime = body.ExpirationDateTime
	graph.subscriptions[subscription.ID] = subscription
	graph.renewals = append(graph.renewals, body.ExpirationDateTime)
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(subscription)
}

func (graph *fakeGraph) delete(writer http.ResponseWriter, request *http.Request) {
	if !graph.authorized(writer, request) {
		return
	}
	graph.mu.Lock()
	defer graph.mu.Unlock()
	if _, ok := graph.subscriptions[request.PathValue("id")]; !ok {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	delete(graph.subscriptions, request.PathValue("id"))
	graph.deleted = append(graph.deleted, request.PathValue("id"))
	writer.WriteHeader(http.StatusNoContent)
}

func (graph *fakeGraph) only() graphSubscription {
	graph.t.Helper()
	graph.mu.Lock()
	defer graph.mu.Unlock()
	if len(graph.subscriptions) != 1 {
		graph.t.Fatalf("Graph holds %d subscriptions, want 1", len(graph.subscriptions))
	}
	for _, subscription := range graph.subscriptions {
		return subscription
	}
	return graphSubscription{}
}

// notify sends a batch of notifications to a subscription's URL.
func (graph *fakeGraph) notify(target string, items ...map[string]any) int {
	body, _ := json.Marshal(map[string]any{"value": items})
	status, _, _ := deliver(graph.t, target, http.Header{"Content-Type": {"application/json; charset=utf-8"}}, body)
	return status
}

func messageCreated(subscription graphSubscription, clientState, messageID, etag string) map[string]any {
	return map[string]any{
		"subscriptionId": subscription.ID, "subscriptionExpirationDateTime": subscription.ExpirationDateTime, "changeType": "created",
		"resource": "Users/7a1c8e/Messages/" + messageID, "clientState": clientState, "tenantId": "84bd8158-6d4d-4958-8b9f-9d6445542f95",
		"resourceData": map[string]any{"@odata.type": "#Microsoft.Graph.Message", "@odata.id": "Users/7a1c8e/Messages/" + messageID, "@odata.etag": etag, "id": messageID},
	}
}

func lifecycleEvent(subscription graphSubscription, clientState, event string) map[string]any {
	return map[string]any{
		"subscriptionId": subscription.ID, "subscriptionExpirationDateTime": subscription.ExpirationDateTime, "lifecycleEvent": event,
		"clientState": clientState, "tenantId": "84bd8158-6d4d-4958-8b9f-9d6445542f95",
	}
}

func TestMicrosoftSubscriptionEndToEnd(t *testing.T) {
	graph := newFakeGraph(t)
	s := newStack(t, graph.server)
	ctx := context.Background()
	s.connections.add(domain.Connection{
		ID: "conn_microsoft", AppID: "app_microsoft", Provider: "microsoft", CredentialType: "oauth2", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "7a1c8e", Name: "megan@contoso.test"}, Status: "active",
	})

	// A member subscribes to new mail. Graph validates the notification URL
	// while the subscription is still pending, then creates it.
	created := s.subscribe("conn_microsoft", map[string]string{"resource": "me/messages"}, []string{"created"})
	subscription := graph.only()
	if len(graph.validated) != 2 || subscription.Resource != "me/messages" || subscription.ChangeType != "created" || subscription.LatestSupportedTLS != "v1_2" ||
		!strings.HasPrefix(subscription.NotificationURL, s.publicURL+"/v1/webhooks/microsoft/") || subscription.LifecycleNotificationURL != subscription.NotificationURL {
		t.Fatalf("Graph subscription = %+v, validated = %v", subscription, graph.validated)
	}
	wantExpiry := s.clock.Now().Add(10080*time.Minute - time.Hour)
	if !subscription.ExpirationDateTime.Equal(wantExpiry) || created.ExpiresAt == nil || !created.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("expiry asked = %v, recorded = %v, want %v", subscription.ExpirationDateTime, created.ExpiresAt, wantExpiry)
	}
	if stored := s.stored(created.ID); stored.RenewAt == nil || !stored.RenewAt.Equal(wantExpiry.Add(-24*time.Hour)) || stored.RegistrationID != subscription.ID {
		t.Fatalf("stored subscription = %+v", stored)
	}

	// Graph notifies of a new message, twice, as it may.
	notification := messageCreated(subscription, subscription.ClientState, "AAMkAGUAAAwqj", `W/"CQAAABYAAADkrWGo7bouTKlsgTZMr9KwAAAUWRHf"`)
	for range 2 {
		if status := graph.notify(subscription.NotificationURL, notification); status != http.StatusAccepted {
			t.Fatalf("notification = %d, want 202", status)
		}
	}
	triggers := eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType)
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(triggers))
	}
	if trigger := triggers[0]; trigger.Event != "microsoft.message.created" || trigger.Resource != (integrationsv1.Resource{Type: "message", ID: "AAMkAGUAAAwqj"}) ||
		strings.Contains(string(trigger.Payload), subscription.ClientState) || trigger.SubscriptionID != created.ID {
		t.Fatalf("trigger = %+v", trigger)
	}

	// A batch where any notification lacks the clientState is refused whole.
	forged := messageCreated(subscription, "not-the-client-state", "AAMkForged", `W/"forged"`)
	if status := graph.notify(subscription.NotificationURL, messageCreated(subscription, subscription.ClientState, "AAMkReal", `W/"real"`), forged); status != http.StatusUnauthorized {
		t.Fatalf("batch with a forged notification = %d, want 401", status)
	}
	if triggers := eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType); len(triggers) != 1 {
		t.Fatalf("triggers = %d after a forged batch, want 1", len(triggers))
	}

	// Graph asks for reauthorization: integrations renews at once.
	s.clock.Advance(time.Hour)
	if status := graph.notify(subscription.NotificationURL, lifecycleEvent(subscription, subscription.ClientState, "reauthorizationRequired")); status != http.StatusAccepted {
		t.Fatalf("reauthorizationRequired = %d, want 202", status)
	}
	s.service.RenewDue(ctx)
	renewedExpiry := s.clock.Now().Add(10080*time.Minute - time.Hour)
	if len(graph.renewals) != 1 || !graph.renewals[0].Equal(renewedExpiry) {
		t.Fatalf("renewals = %v, want one to %v", graph.renewals, renewedExpiry)
	}
	if stored := s.stored(created.ID); !stored.ExpiresAt.Equal(renewedExpiry) || !stored.RenewAt.Equal(renewedExpiry.Add(-24*time.Hour)) {
		t.Fatalf("renewed subscription = expires %v, renew %v", stored.ExpiresAt, stored.RenewAt)
	}
	if calls := s.connections.issued(); calls[len(calls)-1] != (tokenCall{service: "integrations", purpose: "trigger", member: "user-1"}) {
		t.Fatalf("renewal credential = %+v, want a trigger credential for the member", calls[len(calls)-1])
	}

	// Nothing is renewed early; the next renewal comes a day before expiry.
	s.service.RenewDue(ctx)
	s.clock.Advance(renewedExpiry.Add(-24 * time.Hour).Sub(s.clock.Now()))
	s.service.RenewDue(ctx)
	if len(graph.renewals) != 2 {
		t.Fatalf("renewals = %d, want 2", len(graph.renewals))
	}

	// Graph removes the subscription: integrations fails it, with nothing
	// left to remove, and lets go of the connection.
	if status := graph.notify(subscription.NotificationURL, lifecycleEvent(subscription, subscription.ClientState, "subscriptionRemoved")); status != http.StatusAccepted {
		t.Fatalf("subscriptionRemoved = %d, want 202", status)
	}
	stored := s.stored(created.ID)
	if stored.Status != domain.SubscriptionFailed || stored.Reason != integrationsv1.SubscriptionFailedReasonProviderRemoved || stored.CleanupAt != nil {
		t.Fatalf("subscription after removal = %+v", stored)
	}
	if s.connections.held("conn_microsoft") {
		t.Fatal("a removed subscription still holds the connection")
	}
	if failed := eventsOf[integrationsv1.SubscriptionFailed](s, integrationsv1.SubscriptionFailedDetailType); len(failed) != 1 || failed[0].Reason != "provider_removed" {
		t.Fatalf("failed events = %+v", failed)
	}
	if status := graph.notify(subscription.NotificationURL, messageCreated(subscription, subscription.ClientState, "AAMkLate", `W/"late"`)); status != http.StatusOK {
		t.Fatalf("notification after removal = %d, want 200", status)
	}
	s.noSecrets(subscription.ClientState, providerToken)
}

func TestMicrosoftRenewalGraphNoLongerHasFailsTheSubscription(t *testing.T) {
	graph := newFakeGraph(t)
	s := newStack(t, graph.server)
	ctx := context.Background()
	s.connections.add(domain.Connection{ID: "conn_microsoft", AppID: "app_microsoft", Provider: "microsoft", Scope: "member", MemberID: "user-1", ProviderAccount: domain.ProviderAccount{ID: "7a1c8e"}, Status: "active"})
	created := s.subscribe("conn_microsoft", map[string]string{"resource": "me/events"}, nil)
	if subscription := graph.only(); subscription.ChangeType != "created,updated,deleted" {
		t.Fatalf("change types asked = %q, want every change events report", subscription.ChangeType)
	}

	// Graph deleted the subscription without saying so; renewal finds out.
	graph.mu.Lock()
	graph.subscriptions = map[string]graphSubscription{}
	graph.mu.Unlock()
	s.clock.Advance(10080*time.Minute - time.Hour - 24*time.Hour)
	s.service.RenewDue(ctx)

	if stored := s.stored(created.ID); stored.Status != domain.SubscriptionFailed || stored.Reason != integrationsv1.SubscriptionFailedReasonProviderRemoved || stored.CleanupAt != nil {
		t.Fatalf("subscription after a renewal Graph refused = %+v", stored)
	}
	if s.connections.held("conn_microsoft") {
		t.Fatal("the hold outlived a subscription Graph no longer has")
	}
}

func TestMicrosoftSubscriptionWhoseURLFailsValidationIsNotCreated(t *testing.T) {
	graph := newFakeGraph(t)
	s := newStack(t, graph.server)
	s.connections.add(domain.Connection{ID: "conn_microsoft", AppID: "app_microsoft", Provider: "microsoft", Scope: "member", MemberID: "user-1", ProviderAccount: domain.ProviderAccount{ID: "7a1c8e"}, Status: "active"})
	// Graph cannot reach integrations, so validation fails.
	graph.mu.Lock()
	graph.unreachable = true
	graph.mu.Unlock()

	status, body := s.do(http.MethodPost, "/subscriptions", memberToken, map[string]any{"connection_id": "conn_microsoft", "target": map[string]string{"resource": "me/messages"}})

	if status != http.StatusBadRequest || !strings.Contains(string(body), "invalid_request") {
		t.Fatalf("POST subscriptions = %d %s, want 400", status, body)
	}
	if status, body := s.do(http.MethodGet, "/subscriptions", memberToken, nil); status != http.StatusOK || strings.Contains(string(body), "conn_microsoft") {
		t.Fatalf("GET subscriptions = %d %s, want none", status, body)
	}
	if s.connections.held("conn_microsoft") {
		t.Fatal("a subscription that was never created holds the connection")
	}
}
