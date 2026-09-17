package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
)

func triggers(t *testing.T, h *harness) []integrationsv1.TriggerReceived {
	t.Helper()
	var decoded []integrationsv1.TriggerReceived
	for _, event := range h.store.eventsOf(integrationsv1.TriggerReceivedDetailType) {
		var trigger integrationsv1.TriggerReceived
		if err := json.Unmarshal(event.Data, &trigger); err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, trigger)
	}
	return decoded
}

func TestReceiveWebhookPublishesATriggerOnce(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	occurred := testNow.Add(30 * time.Minute)
	h.now = testNow.Add(time.Hour)
	h.hooks.reception = providers.Reception{
		Reply: &providers.Reply{Status: http.StatusAccepted},
		Events: []providers.Event{{
			Name: "issues.opened", Type: "issues", DeliveryID: "delivery-1", Key: "delivery-1", RegistrationID: "reg_1",
			Resource:   integrationsv1.Resource{Type: "issue", ID: "7", Name: "Broken build", URL: "https://example.test/7"},
			Actor:      &integrationsv1.Actor{ID: "9", Name: "octocat"},
			OccurredAt: occurred, Payload: json.RawMessage(`{"action":"opened"}`),
		}},
	}
	delivery := providers.Delivered{Header: http.Header{"X-Test": {"1"}}, Query: url.Values{"q": {"v"}}, Body: []byte(`{"action":"opened"}`)}

	reply, err := h.service.Receive(context.Background(), "hooks", subscription.Route, delivery)

	if err != nil || reply.Status != http.StatusAccepted {
		t.Fatalf("Receive() = %+v, %v, want 202", reply, err)
	}
	if len(h.hooks.received) != 1 {
		t.Fatalf("provider Receive calls = %d, want 1", len(h.hooks.received))
	}
	got := h.hooks.received[0]
	if string(got.delivery.Body) != string(delivery.Body) || got.delivery.Header.Get("X-Test") != "1" || got.delivery.Query.Get("q") != "v" || !got.now.Equal(h.now) {
		t.Fatalf("provider received %+v", got)
	}
	if got.secret != h.hooks.registered[0].Secret {
		t.Fatalf("provider verified with %q, want the registered secret", got.secret)
	}
	published := triggers(t, h)
	if len(published) != 1 {
		t.Fatalf("triggers = %d, want 1", len(published))
	}
	want := integrationsv1.TriggerReceived{
		SubscriptionID: subscription.ID, OrganizationID: testOrganization, ConnectionID: "conn_hooks", AppID: "app_hooks", Provider: "hooks",
		Event: "hooks.issues.opened", Scope: "member", MemberID: "user_member", ProviderAccount: integrationsv1.ProviderAccount{ID: "42", Name: "octo"},
		CreatedBy:  "user_member",
		Resource:   integrationsv1.Resource{Type: "issue", ID: "7", Name: "Broken build", URL: "https://example.test/7"},
		DeliveryID: "delivery-1", OccurredAt: occurred, ReceivedAt: h.now,
	}
	trigger := published[0]
	if trigger.Actor == nil || *trigger.Actor != (integrationsv1.Actor{ID: "9", Name: "octocat"}) || string(trigger.Payload) != `{"action":"opened"}` || trigger.PayloadOmitted {
		t.Fatalf("trigger = %+v", trigger)
	}
	trigger.Actor, trigger.Payload = nil, nil
	if fmt.Sprintf("%+v", trigger) != fmt.Sprintf("%+v", want) {
		t.Fatalf("trigger = %+v\nwant %+v", trigger, want)
	}
	if event := h.store.eventsOf(integrationsv1.TriggerReceivedDetailType)[0]; !event.OccurredAt.Equal(occurred) || !strings.HasPrefix(event.ID, "evt_") {
		t.Fatalf("outbox event = %+v", event)
	}

	// The provider delivers the same event again.
	if reply, err := h.service.Receive(context.Background(), "hooks", subscription.Route, delivery); err != nil || reply.Status != http.StatusAccepted {
		t.Fatalf("Receive() again = %+v, %v", reply, err)
	}
	if published := triggers(t, h); len(published) != 1 {
		t.Fatalf("triggers after a repeated delivery = %d, want 1", len(published))
	}
}

// A provider that signs the whole URL it called, as HubSpot does, must be
// handed the public webhook URL: the request arrives with only its path, and
// a proxy in front may have rewritten the host it came to.
func TestReceiveHandsTheProviderThePublicURLItCalled(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	uri := "/v1/webhooks/hooks/" + subscription.Route + "?attempt=2"

	if _, err := h.service.Receive(context.Background(), "hooks", subscription.Route, providers.Delivered{Method: http.MethodPost, URI: uri}); err != nil {
		t.Fatal(err)
	}
	if len(h.hooks.received) != 1 {
		t.Fatalf("provider Receive calls = %d, want 1", len(h.hooks.received))
	}
	if got, want := h.hooks.received[0].delivery.URL, testPublicURL+uri; got != want {
		t.Fatalf("Delivered.URL = %q, want %q", got, want)
	}
}

func TestReceiveRefusesWhatARouteDoesNotName(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	h.store.put(domain.Subscription{ID: "sub_elsewhere", OrganizationID: testOrganization, Provider: "linear", Status: domain.SubscriptionActive, Route: "route-of-another-provider"})
	h.store.webhooks["app_elsewhere"] = domain.AppWebhook{AppID: "app_elsewhere", OrganizationID: testOrganization, Provider: "other", Route: "app-route-of-another-provider"}

	for _, test := range []struct{ provider, route string }{
		{"unknown", subscription.Route},
		{"hooks", ""},
		{"hooks", "route-that-names-nothing"},
		{"hooks", "route-of-another-provider"},
		{"chat", subscription.Route},
		{"chat", "app-route-of-another-provider"},
	} {
		if _, err := h.service.Receive(context.Background(), test.provider, test.route, providers.Delivered{}); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Receive(%s, %s) error = %v, want domain.ErrNotFound", test.provider, test.route, err)
		}
	}
	if len(h.hooks.received) != 0 || len(h.chat.received) != 0 {
		t.Fatal("a delivery to a route that names nothing reached a provider")
	}
}

func TestReceiveRefusesAnUnverifiedDelivery(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	h.hooks.receiveErr = fmt.Errorf("%w: signature", providers.ErrUnverified)
	h.hooks.reception = providers.Reception{Events: []providers.Event{{Name: "push", Key: "k"}}}

	_, err := h.service.Receive(context.Background(), "hooks", subscription.Route, providers.Delivered{})

	if !errors.Is(err, domain.ErrUnauthorized) || !errors.Is(err, providers.ErrUnverified) {
		t.Fatalf("Receive() error = %v, want domain.ErrUnauthorized", err)
	}
	if len(triggers(t, h)) != 0 {
		t.Fatal("an unverified delivery published a trigger")
	}

	h.hooks.receiveErr = fmt.Errorf("%w: not JSON", domain.ErrInvalid)
	if _, err := h.service.Receive(context.Background(), "hooks", subscription.Route, providers.Delivered{}); !errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Receive() error = %v, want domain.ErrInvalid", err)
	}
}

func TestReceiveAcknowledgesAndDropsDeliveriesToAStoppedSubscription(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	if err := h.service.StopSubscription(context.Background(), bearer(memberToken), testOrganization, subscription.ID); err != nil {
		t.Fatal(err)
	}
	h.hooks.reception = providers.Reception{Events: []providers.Event{{Name: "push", Key: "k"}}}

	reply, err := h.service.Receive(context.Background(), "hooks", subscription.Route, providers.Delivered{Body: []byte("forged")})

	if err != nil || reply.Status != http.StatusOK {
		t.Fatalf("Receive() = %+v, %v, want 200", reply, err)
	}
	if len(h.hooks.received) != 0 || len(triggers(t, h)) != 0 {
		t.Fatal("a delivery to a stopped subscription was read or published")
	}
}

func TestReceiveAnswersAHandshakeWhileRegistering(t *testing.T) {
	h := newHarness(t)
	h.hooks.reception = providers.Reception{Reply: &providers.Reply{Status: http.StatusOK, ContentType: "text/plain", Body: []byte("validation-token")}}
	var reply providers.Reply
	h.hooks.onRegister = func(request providers.RegisterRequest) {
		var err error
		reply, err = h.service.Receive(context.Background(), "hooks", strings.TrimPrefix(request.CallbackURL, testPublicURL+"/v1/webhooks/hooks/"), providers.Delivered{})
		if err != nil {
			t.Errorf("Receive() during registration error = %v", err)
		}
	}

	h.subscribe(t)

	if reply.Status != http.StatusOK || reply.ContentType != "text/plain" || string(reply.Body) != "validation-token" {
		t.Fatalf("handshake reply = %+v", reply)
	}
	if h.hooks.received[0].secret != h.hooks.registered[0].Secret {
		t.Fatal("the handshake was not verified with the secret being registered")
	}
}

func TestReceiveSkipsEventsForAnotherRegistration(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	h.hooks.reception = providers.Reception{Events: []providers.Event{
		{Name: "message.created", Key: "old", RegistrationID: "reg_old"},
		{Name: "message.created", Key: "current", RegistrationID: "reg_1"},
		{Name: "message.created", Key: "unnamed"},
	}}

	reply, err := h.service.Receive(context.Background(), "hooks", subscription.Route, providers.Delivered{})

	if err != nil || reply.Status != http.StatusOK {
		t.Fatalf("Receive() = %+v, %v, want 200 when the provider asks for no answer", reply, err)
	}
	if _, ok := h.store.deliveries[[2]string{subscription.ID, "old"}]; ok || len(triggers(t, h)) != 2 {
		t.Fatalf("triggers = %d, want the current registration's and the unnamed event's", len(triggers(t, h)))
	}
}

func TestReceiveFailsASubscriptionTheProviderRemoved(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	h.now = testNow.Add(time.Hour)
	h.hooks.reception = providers.Reception{Lifecycle: providers.LifecycleRemoved, Reply: &providers.Reply{Status: http.StatusAccepted}}

	reply, err := h.service.Receive(context.Background(), "hooks", subscription.Route, providers.Delivered{})

	if err != nil || reply.Status != http.StatusAccepted {
		t.Fatalf("Receive() = %+v, %v", reply, err)
	}
	stored := h.store.subscription(t, subscription.ID)
	if stored.Status != domain.SubscriptionFailed || stored.Reason != integrationsv1.SubscriptionFailedReasonProviderRemoved || stored.CleanupAt != nil {
		t.Fatalf("subscription = %+v, want failed with nothing to remove", stored)
	}
	failed := h.store.eventsOf(integrationsv1.SubscriptionFailedDetailType)
	if len(failed) != 1 {
		t.Fatalf("failed events = %d, want 1", len(failed))
	}
	var data integrationsv1.SubscriptionFailed
	if err := json.Unmarshal(failed[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.SubscriptionID != subscription.ID || data.Reason != "provider_removed" || data.CreatedBy != "user_member" || !data.FailedAt.Equal(h.now) || data.Provider != "hooks" {
		t.Fatalf("failed event = %+v", data)
	}
	if h.connections.held("conn_hooks") {
		t.Fatal("a subscription with nothing left at the provider still holds the connection")
	}
}

func TestReceiveBringsRenewalForwardWhenTheProviderAsks(t *testing.T) {
	h := newHarness(t)
	h.hooks.registration = providers.Registration{ID: "reg_1", ExpiresAt: testNow.Add(72 * time.Hour)}
	subscription := h.subscribe(t)
	h.now = testNow.Add(time.Hour)
	h.hooks.reception = providers.Reception{Lifecycle: providers.LifecycleRenew}

	if _, err := h.service.Receive(context.Background(), "hooks", subscription.Route, providers.Delivered{}); err != nil {
		t.Fatal(err)
	}
	if stored := h.store.subscription(t, subscription.ID); stored.RenewAt == nil || !stored.RenewAt.Equal(h.now) || stored.Status != domain.SubscriptionActive {
		t.Fatalf("subscription = %+v, want its renewal due now", stored)
	}
}

func TestPublishBoundsWhatATriggerCarries(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	large := json.RawMessage(`"` + strings.Repeat("a", integrationsv1.MaxPayloadBytes) + `"`)
	fits := json.RawMessage(`"` + strings.Repeat("a", integrationsv1.MaxPayloadBytes-2) + `"`)
	longKey := strings.Repeat("k", maxDeliveryKey+1)
	h.hooks.reception = providers.Reception{Events: []providers.Event{
		{Name: "too.large", Key: "1", Payload: large},
		{Name: "not.json", Key: "2", Payload: json.RawMessage(`{"open":`)},
		{Name: "fits", Key: "3", Payload: fits},
		{Name: "no.payload", Key: "4"},
		{Name: "clipped", Key: longKey, DeliveryID: strings.Repeat("d", 300), Resource: integrationsv1.Resource{
			Type: strings.Repeat("t", 300), ID: strings.Repeat("é", 200), Name: strings.Repeat("n", 300), URL: strings.Repeat("u", 3000),
		}},
		{Name: "unkeyed", Payload: json.RawMessage(`{"n":1}`)},
		{Name: "unkeyed", Payload: json.RawMessage(`{"n":1}`)},
		{Name: "unkeyed", Payload: json.RawMessage(`{"n":2}`)},
	}}

	if _, err := h.service.Receive(context.Background(), "hooks", subscription.Route, providers.Delivered{}); err != nil {
		t.Fatal(err)
	}

	byEvent := map[string][]integrationsv1.TriggerReceived{}
	for _, trigger := range triggers(t, h) {
		byEvent[trigger.Event] = append(byEvent[trigger.Event], trigger)
	}
	if trigger := byEvent["hooks.too.large"][0]; !trigger.PayloadOmitted || trigger.Payload != nil {
		t.Errorf("an oversized payload = %d bytes, omitted = %v", len(trigger.Payload), trigger.PayloadOmitted)
	}
	if trigger := byEvent["hooks.not.json"][0]; !trigger.PayloadOmitted || trigger.Payload != nil {
		t.Errorf("an invalid payload = %s, omitted = %v", trigger.Payload, trigger.PayloadOmitted)
	}
	if trigger := byEvent["hooks.fits"][0]; trigger.PayloadOmitted || len(trigger.Payload) != integrationsv1.MaxPayloadBytes {
		t.Errorf("a payload that fits = %d bytes, omitted = %v", len(trigger.Payload), trigger.PayloadOmitted)
	}
	if trigger := byEvent["hooks.no.payload"][0]; trigger.PayloadOmitted || trigger.Payload != nil {
		t.Errorf("no payload = %s, omitted = %v", trigger.Payload, trigger.PayloadOmitted)
	}
	clipped := byEvent["hooks.clipped"][0]
	if len(clipped.Resource.Type) != maxResourceName || len(clipped.Resource.Name) != maxResourceName || len(clipped.Resource.URL) != maxResourceURL || len(clipped.DeliveryID) != maxResourceID {
		t.Errorf("clipped resource = type %d, name %d, url %d, delivery %d bytes", len(clipped.Resource.Type), len(clipped.Resource.Name), len(clipped.Resource.URL), len(clipped.DeliveryID))
	}
	if !utf8.ValidString(clipped.Resource.ID) || len(clipped.Resource.ID) != maxResourceID {
		t.Errorf("clipped resource ID = %d bytes, valid UTF-8 = %v", len(clipped.Resource.ID), utf8.ValidString(clipped.Resource.ID))
	}
	if len(byEvent["hooks.unkeyed"]) != 2 {
		t.Errorf("unkeyed triggers = %d, want the two different payloads", len(byEvent["hooks.unkeyed"]))
	}
	for key := range h.store.deliveries {
		if len(key[1]) > maxDeliveryKey || key[1] == longKey {
			t.Errorf("delivery key %q is longer than %d bytes", key[1], maxDeliveryKey)
		}
	}
}

func TestClip(t *testing.T) {
	for _, test := range []struct {
		text string
		size int
		want string
	}{
		{"short", 10, "short"},
		{"exact", 5, "exact"},
		{"longer", 4, "long"},
		{"aé", 2, "a"},
		{"éé", 3, "é"},
		{"日本", 5, "日"},
		{"é", 1, ""},
	} {
		if got := clip(test.text, test.size); got != test.want {
			t.Errorf("clip(%q, %d) = %q, want %q", test.text, test.size, got, test.want)
		}
	}
}

func TestReceiveAppDeliversToTheWorkspacesSubscriptions(t *testing.T) {
	h := newHarness(t)
	view := h.setAppWebhook(t)
	messages, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_chat", Events: []string{"message"}})
	if err != nil {
		t.Fatal(err)
	}
	everything := h.store.put(domain.Subscription{ID: "sub_t2", OrganizationID: testOrganization, ConnectionID: "conn_chat_2", AppID: "app_chat", Provider: "chat", AccountKey: "T2", Status: domain.SubscriptionActive, Route: "route-t2", CreatedBy: "user_other"})
	h.store.put(domain.Subscription{ID: "sub_t3", OrganizationID: testOrganization, ConnectionID: "conn_chat_3", AppID: "app_chat", Provider: "chat", AccountKey: "T3", Status: domain.SubscriptionActive, Route: "route-t3", CreatedBy: "user_other"})
	h.store.put(domain.Subscription{ID: "sub_t2_stopped", OrganizationID: testOrganization, ConnectionID: "conn_chat_2", AppID: "app_chat", Provider: "chat", AccountKey: "T2", Status: domain.SubscriptionStopped, Route: "route-t2-stopped", CreatedBy: "user_other"})
	h.chat.reception = providers.Reception{AccountKeys: []string{"T1", "T2"}, Events: []providers.Event{
		{Name: "message", Type: "message", Key: "Ev1"},
		{Name: "reaction_added", Type: "reaction_added", Key: "Ev2"},
	}}
	route := strings.TrimPrefix(view.URL, testPublicURL+"/v1/webhooks/chat/")

	reply, err := h.service.Receive(context.Background(), "chat", route, providers.Delivered{Body: []byte(`{}`)})

	if err != nil || reply.Status != http.StatusOK {
		t.Fatalf("Receive() = %+v, %v", reply, err)
	}
	if len(h.chat.received) != 1 || h.chat.received[0].secret != "chat-signing-secret-1234" {
		t.Fatalf("provider received %+v, want the app's signing secret", h.chat.received)
	}
	got := map[string]int{}
	for _, trigger := range triggers(t, h) {
		got[trigger.SubscriptionID+" "+trigger.Event]++
	}
	want := map[string]int{messages.ID + " chat.message": 1, everything.ID + " chat.message": 1, everything.ID + " chat.reaction_added": 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("triggers = %v, want %v", got, want)
	}
}

func TestReceiveAppAnswersTheURLVerification(t *testing.T) {
	h := newHarness(t)
	view := h.setAppWebhook(t)
	h.chat.reception = providers.Reception{Reply: &providers.Reply{Status: http.StatusOK, ContentType: "text/plain", Body: []byte("challenge")}}

	reply, err := h.service.Receive(context.Background(), "chat", strings.TrimPrefix(view.URL, testPublicURL+"/v1/webhooks/chat/"), providers.Delivered{})

	if err != nil || string(reply.Body) != "challenge" || reply.ContentType != "text/plain" {
		t.Fatalf("Receive() = %+v, %v", reply, err)
	}
}

func TestReceiveAppRefusesAnUnverifiedDelivery(t *testing.T) {
	h := newHarness(t)
	view := h.setAppWebhook(t)
	h.chat.receiveErr = providers.ErrUnverified

	_, err := h.service.Receive(context.Background(), "chat", strings.TrimPrefix(view.URL, testPublicURL+"/v1/webhooks/chat/"), providers.Delivered{})

	if !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Receive() error = %v, want domain.ErrUnauthorized", err)
	}
}
