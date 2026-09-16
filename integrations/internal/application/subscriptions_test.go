package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"
	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

func TestCreateWebhookSubscriptionRegistersThenActivates(t *testing.T) {
	h := newHarness(t)
	expires := testNow.Add(72 * time.Hour)
	h.hooks.registration = providers.Registration{ID: "reg_1", ExpiresAt: expires}
	h.hooks.onRegister = func(request providers.RegisterRequest) {
		// The provider's handshake during registration must find the route.
		route := strings.TrimPrefix(request.CallbackURL, testPublicURL+"/v1/webhooks/hooks/")
		pending, err := h.store.SubscriptionByRoute(context.Background(), route)
		if err != nil || pending.Status != domain.SubscriptionPending {
			t.Errorf("during registration SubscriptionByRoute() = %+v, %v, want the pending subscription", pending, err)
		}
		if !h.connections.held("conn_hooks") {
			t.Error("the connection is not held while its webhook registers")
		}
	}

	subscription := h.subscribe(t)

	if subscription.Status != domain.SubscriptionActive || subscription.RegistrationID != "reg_1" || subscription.CreatedBy != "user_member" {
		t.Fatalf("CreateSubscription() = %+v", subscription)
	}
	if !strings.HasPrefix(subscription.ID, "sub_") || subscription.Provider != "hooks" || subscription.AppID != "app_hooks" || subscription.ProviderAccount.ID != "42" {
		t.Fatalf("CreateSubscription() = %+v", subscription)
	}
	if subscription.ExpiresAt == nil || !subscription.ExpiresAt.Equal(expires) || subscription.RenewAt == nil || !subscription.RenewAt.Equal(expires.Add(-24*time.Hour)) {
		t.Fatalf("CreateSubscription() expires_at = %v, renew_at = %v", subscription.ExpiresAt, subscription.RenewAt)
	}
	if len(h.hooks.registered) != 1 {
		t.Fatalf("Register calls = %d, want 1", len(h.hooks.registered))
	}
	request := h.hooks.registered[0]
	if request.CallbackURL != testPublicURL+"/v1/webhooks/hooks/"+subscription.Route || len(subscription.Route) != 43 {
		t.Fatalf("Register() callback = %q for route %q", request.CallbackURL, subscription.Route)
	}
	if request.Credential.AccessToken != "provider-token" || request.Target["repository"] != "acme/api" || !slices.Equal(request.Events, []string{"issues"}) || !request.Now.Equal(testNow) {
		t.Fatalf("Register() request = %+v", request)
	}
	if secret, err := hex.DecodeString(request.Secret); err != nil || len(secret) != 32 {
		t.Fatalf("Register() secret = %q, want 32 random bytes in hex", request.Secret)
	}
	opened, err := fakeSealer{}.Open(context.Background(), secrets.Binding{OrganizationID: testOrganization, ResourceType: secrets.ResourceSubscription, ResourceID: subscription.ID}, *h.store.subscription(t, subscription.ID).EncryptedSecret)
	if err != nil || string(opened) != request.Secret {
		t.Fatalf("stored secret opens to %q, %v, want the registered secret", opened, err)
	}
	if want := []connectionsclientTokenCall{{purpose: domain.PurposeTrigger, accessToken: memberToken}}; !slices.Equal(tokenCalls(h), want) {
		t.Fatalf("Token calls = %+v, want %+v", tokenCalls(h), want)
	}
	if calls := h.connections.calls(); !slices.Equal(calls, []string{"add org_1/conn_hooks"}) {
		t.Fatalf("hold calls = %v", calls)
	}

	created := h.store.eventsOf(integrationsv1.SubscriptionCreatedDetailType)
	if len(created) != 1 {
		t.Fatalf("created events = %d, want 1", len(created))
	}
	var data integrationsv1.SubscriptionCreated
	if err := json.Unmarshal(created[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.SubscriptionID != subscription.ID || data.ConnectionID != "conn_hooks" || data.Provider != "hooks" || data.CreatedBy != "user_member" ||
		data.Target["repository"] != "acme/api" || !slices.Equal(data.Events, []string{"issues"}) || data.ExpiresAt == nil || !data.ExpiresAt.Equal(expires) {
		t.Fatalf("created event = %+v", data)
	}
}

type connectionsclientTokenCall struct {
	purpose        domain.Purpose
	accessToken    string
	actingMemberID string
}

func tokenCalls(h *harness) []connectionsclientTokenCall {
	h.connections.mu.Lock()
	defer h.connections.mu.Unlock()
	var calls []connectionsclientTokenCall
	for _, request := range h.connections.tokens {
		calls = append(calls, connectionsclientTokenCall{purpose: request.Purpose, accessToken: request.AccessToken, actingMemberID: request.ActingMemberID})
	}
	return calls
}

func TestCreateSubscriptionWithoutExpiryIsNotRenewed(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	if subscription.ExpiresAt != nil || subscription.RenewAt != nil {
		t.Fatalf("CreateSubscription() expires_at = %v, renew_at = %v, want neither", subscription.ExpiresAt, subscription.RenewAt)
	}
	var data map[string]any
	if err := json.Unmarshal(h.store.eventsOf(integrationsv1.SubscriptionCreatedDetailType)[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if _, ok := data["expires_at"]; ok {
		t.Fatalf("created event = %v, want no expires_at", data)
	}
}

func TestCreateSubscriptionAsksForEveryEventAsAnEmptyList(t *testing.T) {
	h := newHarness(t)
	if _, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_hooks"}); err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(h.store.eventsOf(integrationsv1.SubscriptionCreatedDetailType)[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if string(data["events"]) != "[]" || string(data["target"]) != "{}" {
		t.Fatalf("created event events = %s, target = %s, want [] and {}", data["events"], data["target"])
	}
}

func TestCreateSubscriptionRefusals(t *testing.T) {
	tests := []struct {
		name        string
		credentials authclient.Credentials
		input       CreateSubscriptionInput
		arrange     func(*harness)
		want        error
	}{
		{name: "no credentials", input: CreateSubscriptionInput{ConnectionID: "conn_hooks"}, want: domain.ErrUnauthorized},
		{name: "a member without an access token", credentials: authclient.Credentials{Authorization: "Basic dXNlcg=="}, input: CreateSubscriptionInput{ConnectionID: "conn_hooks"}, want: domain.ErrUnauthorized,
			arrange: func(h *harness) { h.directory.members[""] = h.directory.members[memberToken] }},
		{name: "no connection", credentials: bearer(memberToken), input: CreateSubscriptionInput{ConnectionID: " "}, want: domain.ErrInvalid},
		{name: "unknown connection", credentials: bearer(memberToken), input: CreateSubscriptionInput{ConnectionID: "conn_missing"}, want: domain.ErrNotFound},
		{name: "hidden connection", credentials: bearer(otherToken), input: CreateSubscriptionInput{ConnectionID: "conn_hooks"}, want: domain.ErrNotFound,
			arrange: func(h *harness) { h.connections.hidden[otherToken] = []string{"conn_hooks"} }},
		{name: "needs reconnect", credentials: bearer(memberToken), input: CreateSubscriptionInput{ConnectionID: "conn_hooks"}, want: domain.ErrNeedsReconnect,
			arrange: func(h *harness) { h.setStatus("conn_hooks", "needs_reconnect") }},
		{name: "disconnected", credentials: bearer(memberToken), input: CreateSubscriptionInput{ConnectionID: "conn_hooks"}, want: domain.ErrConflict,
			arrange: func(h *harness) { h.setStatus("conn_hooks", "disconnected") }},
		{name: "provider without events", credentials: bearer(memberToken), input: CreateSubscriptionInput{ConnectionID: "conn_hooks"}, want: domain.ErrInvalid,
			arrange: func(h *harness) { h.setProvider("conn_hooks", "notion") }},
		{name: "invalid target", credentials: bearer(memberToken), input: CreateSubscriptionInput{ConnectionID: "conn_hooks", Target: map[string]string{"Repository": "acme/api"}}, want: domain.ErrInvalid},
		{name: "invalid event", credentials: bearer(memberToken), input: CreateSubscriptionInput{ConnectionID: "conn_hooks", Events: []string{"issues opened"}}, want: domain.ErrInvalid},
		{name: "target the provider refuses", credentials: bearer(memberToken), input: CreateSubscriptionInput{ConnectionID: "conn_hooks", Target: map[string]string{"refuse": "yes"}}, want: domain.ErrInvalid},
		{name: "member may not use the connection", credentials: bearer(otherToken), input: CreateSubscriptionInput{ConnectionID: "conn_hooks"}, want: domain.ErrForbidden,
			arrange: func(h *harness) { h.connections.tokenErr = domain.ErrForbidden }},
		{name: "hold refused", credentials: bearer(memberToken), input: CreateSubscriptionInput{ConnectionID: "conn_hooks"}, want: domain.ErrProviderUnavailable,
			arrange: func(h *harness) { h.connections.addHoldErr = domain.ErrProviderUnavailable }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			if test.arrange != nil {
				test.arrange(h)
			}
			_, err := h.service.CreateSubscription(context.Background(), test.credentials, testOrganization, test.input)
			if !errors.Is(err, test.want) {
				t.Fatalf("CreateSubscription() error = %v, want %v", err, test.want)
			}
			if len(h.store.subscriptions) != 0 || len(h.store.events) != 0 || len(h.hooks.registered) != 0 {
				t.Fatalf("after a refusal: subscriptions = %d, events = %d, registrations = %d", len(h.store.subscriptions), len(h.store.events), len(h.hooks.registered))
			}
			if h.connections.held("conn_hooks") {
				t.Fatal("a refused subscription holds the connection")
			}
		})
	}
}

func (h *harness) setStatus(connectionID, status string) {
	connection := h.connections.connections[connectionID]
	connection.Status = status
	h.connections.connections[connectionID] = connection
}

func (h *harness) setProvider(connectionID, provider string) {
	connection := h.connections.connections[connectionID]
	connection.Provider = provider
	h.connections.connections[connectionID] = connection
}

func TestCreateSubscriptionThatFailsToRegisterIsForgotten(t *testing.T) {
	h := newHarness(t)
	h.hooks.registerErr = domain.ErrCredentialRejected

	_, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_hooks"})

	if !errors.Is(err, domain.ErrCredentialRejected) {
		t.Fatalf("CreateSubscription() error = %v, want the provider's", err)
	}
	if len(h.store.subscriptions) != 0 || len(h.store.events) != 0 {
		t.Fatalf("subscriptions = %d, events = %d, want none", len(h.store.subscriptions), len(h.store.events))
	}
	if h.connections.held("conn_hooks") {
		t.Fatal("the connection is still held")
	}
}

func TestCreateSubscriptionThatFailsToRegisterKeepsAHoldOthersNeed(t *testing.T) {
	h := newHarness(t)
	first := h.subscribe(t)
	h.hooks.registerErr = domain.ErrProviderUnavailable

	if _, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_hooks"}); err == nil {
		t.Fatal("CreateSubscription() error = nil")
	}
	if !h.connections.held("conn_hooks") {
		t.Fatalf("the hold %s needs was released", first.ID)
	}
}

func TestCreateSubscriptionRemovesARegistrationItCouldNotSave(t *testing.T) {
	h := newHarness(t)
	h.store.activateErr = errors.New("database unavailable")

	_, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_hooks", Target: map[string]string{"repository": "acme/api"}})

	if err == nil || errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CreateSubscription() error = %v, want the store's", err)
	}
	if len(h.hooks.unregistered) != 1 || h.hooks.unregistered[0].RegistrationID != "reg_1" || h.hooks.unregistered[0].Credential.AccessToken != "provider-token" || h.hooks.unregistered[0].Target["repository"] != "acme/api" {
		t.Fatalf("Unregister calls = %+v, want reg_1 with the issued credential", h.hooks.unregistered)
	}
	if len(h.store.subscriptions) != 0 || h.connections.held("conn_hooks") {
		t.Fatalf("subscriptions = %d, held = %v, want none", len(h.store.subscriptions), h.connections.held("conn_hooks"))
	}
}

func TestSubscriptionStoppedWhileRegisteringIsCleanedUpUnderItsHold(t *testing.T) {
	h := newHarness(t)
	h.hooks.onRegister = func(providers.RegisterRequest) {
		// The connection is disconnected while the provider registers.
		if err := h.service.ConnectionDisconnected(context.Background(), connectionseventsv1.ConnectionDisconnected{
			ConnectionID: "conn_hooks", OrganizationID: testOrganization, DisconnectedBy: "user_member",
		}); err != nil {
			t.Errorf("ConnectionDisconnected() error = %v", err)
		}
	}

	_, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_hooks"})

	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("CreateSubscription() error = %v, want domain.ErrConflict", err)
	}
	var subscription domain.Subscription
	for _, stored := range h.store.subscriptions {
		subscription = stored
	}
	if subscription.Status != domain.SubscriptionStopped || subscription.RegistrationID != "reg_1" || subscription.CleanupAt == nil {
		t.Fatalf("subscription = %+v, want stopped with reg_1 queued for removal", subscription)
	}
	if created := h.store.eventsOf(integrationsv1.SubscriptionCreatedDetailType); len(created) != 0 {
		t.Fatalf("created events = %d, want none", len(created))
	}
	if !h.connections.held("conn_hooks") {
		t.Fatal("the hold was released before the registration was removed")
	}

	h.service.CleanupDue(context.Background())

	if len(h.hooks.unregistered) != 1 || h.hooks.unregistered[0].RegistrationID != "reg_1" {
		t.Fatalf("Unregister calls = %+v, want reg_1", h.hooks.unregistered)
	}
	if calls := tokenCalls(h); calls[len(calls)-1] != (connectionsclientTokenCall{purpose: domain.PurposeCleanup, actingMemberID: "user_member"}) {
		t.Fatalf("cleanup Token call = %+v", calls[len(calls)-1])
	}
	if h.store.subscription(t, subscription.ID).CleanupAt != nil || h.connections.held("conn_hooks") {
		t.Fatal("after cleanup the subscription still has a removal due or holds the connection")
	}
}

func TestSubscriptionStoppedWhileRegisteringThatFailsReleasesTheHold(t *testing.T) {
	h := newHarness(t)
	h.hooks.registerErr = domain.ErrProviderUnavailable
	h.hooks.onRegister = func(providers.RegisterRequest) {
		if err := h.service.ConnectionDisconnected(context.Background(), connectionseventsv1.ConnectionDisconnected{ConnectionID: "conn_hooks", OrganizationID: testOrganization}); err != nil {
			t.Errorf("ConnectionDisconnected() error = %v", err)
		}
		if !h.connections.held("conn_hooks") {
			t.Error("stopping a subscription that is registering released its hold")
		}
	}

	if _, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_hooks"}); err == nil {
		t.Fatal("CreateSubscription() error = nil")
	}
	if h.connections.held("conn_hooks") {
		t.Fatal("the hold outlived a registration that failed")
	}
}

func TestCreateAppSubscriptionNeedsTheAppsSecret(t *testing.T) {
	h := newHarness(t)
	_, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_chat"})
	if !errors.Is(err, domain.ErrWebhookSecretRequired) {
		t.Fatalf("CreateSubscription() error = %v, want domain.ErrWebhookSecretRequired", err)
	}
}

func TestCreateAppSubscriptionIsActiveWithoutRegistering(t *testing.T) {
	h := newHarness(t)
	h.setAppWebhook(t)

	subscription, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_chat", Events: []string{"message"}})

	if err != nil {
		t.Fatal(err)
	}
	if subscription.Status != domain.SubscriptionActive || subscription.AccountKey != "T1" || subscription.EncryptedSecret != nil || subscription.RegistrationID != "" {
		t.Fatalf("CreateSubscription() = %+v", subscription)
	}
	if len(h.connections.calls()) != 0 {
		t.Fatalf("hold calls = %v, want none", h.connections.calls())
	}
	if calls := tokenCalls(h); len(calls) != 1 || calls[0].purpose != domain.PurposeTrigger || calls[0].accessToken != memberToken {
		t.Fatalf("Token calls = %+v, want the member's access checked once", calls)
	}
	if created := h.store.eventsOf(integrationsv1.SubscriptionCreatedDetailType); len(created) != 1 {
		t.Fatalf("created events = %d, want 1", len(created))
	}
}

func TestCreateAppSubscriptionRefusesAnAccountWithoutAWorkspace(t *testing.T) {
	h := newHarness(t)
	h.setAppWebhook(t)
	connection := h.connections.connections["conn_chat"]
	connection.ProviderAccount.ID = ":U1"
	h.connections.connections["conn_chat"] = connection

	if _, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_chat"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CreateSubscription() error = %v, want domain.ErrInvalid", err)
	}
}

func TestListSubscriptions(t *testing.T) {
	h := newHarness(t)
	mine := h.subscribe(t)
	h.now = h.now.Add(time.Minute)
	others := h.store.put(domain.Subscription{ID: "sub_other", OrganizationID: testOrganization, ConnectionID: "conn_hooks", Provider: "hooks", CreatedBy: "user_other", CreatedAt: h.now, Status: domain.SubscriptionActive, Route: "route-other"})
	elsewhere := h.store.put(domain.Subscription{ID: "sub_chat", OrganizationID: testOrganization, ConnectionID: "conn_chat", Provider: "chat", CreatedBy: "user_other", CreatedAt: h.now, Status: domain.SubscriptionActive, Route: "route-chat"})

	tests := []struct {
		name         string
		token        string
		connectionID string
		want         []string
		err          error
	}{
		{name: "a member sees their own", token: memberToken, want: []string{mine.ID}},
		{name: "an admin sees the organization's", token: adminToken, want: []string{mine.ID, elsewhere.ID, others.ID}},
		{name: "who can see a connection sees its subscriptions", token: otherToken, connectionID: "conn_hooks", want: []string{mine.ID, others.ID}},
		{name: "who cannot see a connection is told it does not exist", token: memberToken, connectionID: "conn_secret", err: domain.ErrNotFound},
	}
	h.connections.hidden[memberToken] = []string{"conn_secret"}
	h.connections.connections["conn_secret"] = domain.Connection{ID: "conn_secret", OrganizationID: testOrganization, Provider: "hooks", Status: "active"}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listed, err := h.service.ListSubscriptions(context.Background(), bearer(test.token), testOrganization, test.connectionID)
			if !errors.Is(err, test.err) {
				t.Fatalf("ListSubscriptions() error = %v, want %v", err, test.err)
			}
			var ids []string
			for _, subscription := range listed {
				ids = append(ids, subscription.ID)
			}
			if !slices.Equal(ids, test.want) {
				t.Fatalf("ListSubscriptions() = %v, want %v", ids, test.want)
			}
		})
	}
}

func TestGetSubscription(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	for _, token := range []string{memberToken, adminToken, otherToken} {
		if got, err := h.service.GetSubscription(context.Background(), bearer(token), testOrganization, subscription.ID); err != nil || got.ID != subscription.ID {
			t.Fatalf("GetSubscription(%s) = %+v, %v", token, got, err)
		}
	}
	h.connections.hidden[otherToken] = []string{"conn_hooks"}
	if _, err := h.service.GetSubscription(context.Background(), bearer(otherToken), testOrganization, subscription.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetSubscription() by a member who cannot see the connection error = %v, want domain.ErrNotFound", err)
	}
	if _, err := h.service.GetSubscription(context.Background(), bearer(memberToken), testOrganization, "sub_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetSubscription(missing) error = %v, want domain.ErrNotFound", err)
	}
	if _, err := h.service.GetSubscription(context.Background(), bearer(memberToken), "org_2", subscription.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetSubscription(other organization) error = %v, want domain.ErrNotFound", err)
	}
	if _, err := h.service.GetSubscription(context.Background(), bearer(memberToken), " ", subscription.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetSubscription(no organization) error = %v, want domain.ErrNotFound", err)
	}
}

func TestStopSubscriptionQueuesRemovalAndKeepsTheHold(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)
	h.now = h.now.Add(time.Hour)

	if err := h.service.StopSubscription(context.Background(), bearer(memberToken), testOrganization, subscription.ID); err != nil {
		t.Fatal(err)
	}

	stored := h.store.subscription(t, subscription.ID)
	if stored.Status != domain.SubscriptionStopped || stored.Reason != integrationsv1.SubscriptionStoppedReasonStopped || stored.StoppedBy != "user_member" || stored.CleanupAt == nil {
		t.Fatalf("stopped subscription = %+v", stored)
	}
	if !h.connections.held("conn_hooks") {
		t.Fatal("the hold was released before the registration was removed")
	}
	stoppedEvents := h.store.eventsOf(integrationsv1.SubscriptionStoppedDetailType)
	if len(stoppedEvents) != 1 {
		t.Fatalf("stopped events = %d, want 1", len(stoppedEvents))
	}
	var data integrationsv1.SubscriptionStopped
	if err := json.Unmarshal(stoppedEvents[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.SubscriptionID != subscription.ID || data.Reason != "stopped" || data.StoppedBy != "user_member" || data.CreatedBy != "user_member" || !data.StoppedAt.Equal(h.now) {
		t.Fatalf("stopped event = %+v", data)
	}

	// Stopping again changes nothing.
	if err := h.service.StopSubscription(context.Background(), bearer(adminToken), testOrganization, subscription.ID); err != nil {
		t.Fatal(err)
	}
	if len(h.store.eventsOf(integrationsv1.SubscriptionStoppedDetailType)) != 1 {
		t.Fatal("stopping a stopped subscription wrote another event")
	}
}

func TestStopSubscriptionPermissions(t *testing.T) {
	h := newHarness(t)
	subscription := h.subscribe(t)

	if err := h.service.StopSubscription(context.Background(), bearer(otherToken), testOrganization, subscription.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("StopSubscription() by another member who can see the connection error = %v, want domain.ErrForbidden", err)
	}
	h.connections.hidden[otherToken] = []string{"conn_hooks"}
	if err := h.service.StopSubscription(context.Background(), bearer(otherToken), testOrganization, subscription.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("StopSubscription() by another member who cannot see the connection error = %v, want domain.ErrNotFound", err)
	}
	if h.store.subscription(t, subscription.ID).Status != domain.SubscriptionActive {
		t.Fatal("a refused stop stopped the subscription")
	}
	if err := h.service.StopSubscription(context.Background(), bearer(adminToken), testOrganization, subscription.ID); err != nil {
		t.Fatalf("StopSubscription() by an admin error = %v", err)
	}
	if stored := h.store.subscription(t, subscription.ID); stored.Status != domain.SubscriptionStopped || stored.StoppedBy != "user_admin" {
		t.Fatalf("stopped subscription = %+v", stored)
	}
}

func TestStoppingAStalePendingSubscriptionReleasesItsHold(t *testing.T) {
	h := newHarness(t)
	h.connections.holds["conn_hooks"] = true
	pending := h.store.put(domain.Subscription{ID: "sub_pending", OrganizationID: testOrganization, ConnectionID: "conn_hooks", Provider: "hooks",
		CreatedBy: "user_member", CreatedAt: testNow, Status: domain.SubscriptionPending, Route: "route-pending"})

	h.now = testNow.Add(registrationWindow - time.Second)
	if err := h.service.StopSubscription(context.Background(), bearer(memberToken), testOrganization, pending.ID); err != nil {
		t.Fatal(err)
	}
	if !h.connections.held("conn_hooks") {
		t.Fatal("stopping a subscription that may still be registering released its hold")
	}

	h.store.put(domain.Subscription{ID: "sub_stale", OrganizationID: testOrganization, ConnectionID: "conn_hooks", Provider: "hooks",
		CreatedBy: "user_member", CreatedAt: testNow, Status: domain.SubscriptionPending, Route: "route-stale"})
	h.now = testNow.Add(registrationWindow)
	if err := h.service.StopSubscription(context.Background(), bearer(memberToken), testOrganization, "sub_stale"); err != nil {
		t.Fatal(err)
	}
	if h.connections.held("conn_hooks") {
		t.Fatal("stopping a subscription whose registration is long over kept the hold")
	}
}

func TestStoppingAnAppSubscriptionTouchesNoHold(t *testing.T) {
	h := newHarness(t)
	h.setAppWebhook(t)
	subscription, err := h.service.CreateSubscription(context.Background(), bearer(memberToken), testOrganization, CreateSubscriptionInput{ConnectionID: "conn_chat"})
	if err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(time.Hour)
	if err := h.service.StopSubscription(context.Background(), bearer(memberToken), testOrganization, subscription.ID); err != nil {
		t.Fatal(err)
	}
	if stored := h.store.subscription(t, subscription.ID); stored.CleanupAt != nil {
		t.Fatalf("stopped app subscription = %+v, want nothing to remove", stored)
	}
	if calls := h.connections.calls(); len(calls) != 0 {
		t.Fatalf("hold calls = %v, want none", calls)
	}
}

func TestRenewalTimes(t *testing.T) {
	if expires, renew := renewalTimes(time.Time{}, testNow); expires != nil || renew != nil {
		t.Fatalf("renewalTimes(zero) = %v, %v, want nil", expires, renew)
	}
	week := testNow.Add(7 * 24 * time.Hour)
	if expires, renew := renewalTimes(week, testNow); !expires.Equal(week) || !renew.Equal(week.Add(-24*time.Hour)) {
		t.Fatalf("renewalTimes(a week) = %v, %v", expires, renew)
	}
	hour := testNow.Add(time.Hour)
	if expires, renew := renewalTimes(hour.In(time.FixedZone("IST", 19800)), testNow); !expires.Equal(hour) || expires.Location() != time.UTC || !renew.Equal(testNow.Add(30*time.Minute)) {
		t.Fatalf("renewalTimes(an hour) = %v, %v", expires, renew)
	}
}
