package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
)

func completedEvents(t *testing.T, h *harness) []integrationsv1.ActionCompleted {
	t.Helper()
	var decoded []integrationsv1.ActionCompleted
	for _, event := range h.store.eventsOf(integrationsv1.ActionCompletedDetailType) {
		var completed integrationsv1.ActionCompleted
		if err := json.Unmarshal(event.Data, &completed); err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, completed)
	}
	return decoded
}

func failedActions(t *testing.T, h *harness) []integrationsv1.ActionFailed {
	t.Helper()
	var decoded []integrationsv1.ActionFailed
	for _, event := range h.store.eventsOf(integrationsv1.ActionFailedDetailType) {
		var failed integrationsv1.ActionFailed
		if err := json.Unmarshal(event.Data, &failed); err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, failed)
	}
	return decoded
}

func TestRunActionCarriesItOutAndPublishesIt(t *testing.T) {
	h := newHarness(t)
	h.hooks.result = providers.ActionResult{
		Resource: integrationsv1.Resource{Type: "issue", ID: "12", Name: "Broken build", URL: "https://example.test/12"},
		Result:   json.RawMessage(`{"number":12}`),
	}
	h.now = testNow.Add(time.Hour)

	action, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
		ConnectionID: "conn_hooks", Action: "write_thing", Input: json.RawMessage(`{"title":"Broken build"}`), IdempotencyKey: "chat-turn-7788",
	})

	if err != nil {
		t.Fatalf("RunAction() error = %v", err)
	}
	if !strings.HasPrefix(action.ID, "act_") || action.Status != domain.ActionCompleted || action.Name != "write_thing" ||
		action.Provider != "hooks" || action.ConnectionID != "conn_hooks" || action.RequestedBy != "user_member" {
		t.Fatalf("RunAction() = %+v", action)
	}
	if action.Resource == nil || *action.Resource != (domain.ActionResource{Type: "issue", ID: "12", Name: "Broken build", URL: "https://example.test/12"}) {
		t.Fatalf("RunAction() resource = %+v", action.Resource)
	}
	if string(action.Result) != `{"number":12}` || action.EndedAt == nil || !action.EndedAt.Equal(h.now) || !action.StartedAt.Equal(h.now) {
		t.Fatalf("RunAction() = %+v", action)
	}
	if len(h.hooks.ran) != 1 {
		t.Fatalf("provider ran %d times, want 1", len(h.hooks.ran))
	}
	if request := h.hooks.ran[0]; request.Credential.AccessToken != "provider-token" || string(request.Input) != `{"title":"Broken build"}` || !request.Now.Equal(h.now) {
		t.Fatalf("Run() request = %+v", request)
	}
	if calls := tokenCalls(h); len(calls) != 1 || calls[0].purpose != domain.PurposeAction || calls[0].accessToken != memberToken {
		t.Fatalf("Token calls = %+v, want one action credential for the member", calls)
	}
	completed := completedEvents(t, h)
	if len(completed) != 1 {
		t.Fatalf("completed events = %d, want 1", len(completed))
	}
	event := completed[0]
	if event.ActionID != action.ID || event.Action != "hooks.write_thing" || event.Service != "integrations" || event.RequestedBy != "user_member" ||
		event.Scope != "member" || event.MemberID != "user_member" || event.ProviderAccount.ID != "42" {
		t.Fatalf("completed event = %+v", event)
	}
	if event.Resource == nil || event.Resource.ID != "12" || string(event.Result) != `{"number":12}` || event.ResultOmitted ||
		!event.StartedAt.Equal(h.now) || !event.CompletedAt.Equal(h.now) {
		t.Fatalf("completed event = %+v", event)
	}
}

func TestRunActionUnderTheSameKeyRunsOnce(t *testing.T) {
	h := newHarness(t)
	h.hooks.result = providers.ActionResult{Result: json.RawMessage(`{"number":12}`)}
	input := RunActionInput{ConnectionID: "conn_hooks", Action: "write_thing", Input: json.RawMessage(`{"title":"once"}`), IdempotencyKey: "chat-turn-7788"}

	first, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, input)
	if err != nil {
		t.Fatal(err)
	}
	// The caller asks again, as it would after a timeout, with a different
	// input under the same key.
	input.Input = json.RawMessage(`{"title":"twice"}`)
	again, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, input)
	if err != nil {
		t.Fatal(err)
	}

	if again.ID != first.ID || string(again.Result) != `{"number":12}` {
		t.Fatalf("RunAction() again = %+v, want the first action", again)
	}
	if len(h.hooks.ran) != 1 {
		t.Fatalf("provider ran %d times, want once", len(h.hooks.ran))
	}
	if completed := completedEvents(t, h); len(completed) != 1 {
		t.Fatalf("completed events = %d, want 1", len(completed))
	}
	if calls := tokenCalls(h); len(calls) != 1 {
		t.Fatalf("credentials issued = %d, want 1: a repeat asks for nothing", len(calls))
	}
}

func TestRunActionKeysAreOnlyRequiredForWrites(t *testing.T) {
	h := newHarness(t)
	h.hooks.result = providers.ActionResult{Result: json.RawMessage(`{"things":[]}`)}

	if _, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
		ConnectionID: "conn_hooks", Action: "write_thing", Input: json.RawMessage(`{}`),
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RunAction() without an idempotency key = %v, want domain.ErrInvalid", err)
	}
	if len(h.hooks.ran) != 0 || len(h.store.actions) != 0 {
		t.Fatal("an action refused for want of a key ran or was recorded")
	}

	// Reading twice runs twice: nothing changes at the provider.
	for range 2 {
		if _, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
			ConnectionID: "conn_hooks", Action: "read_thing", Input: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.hooks.ran) != 2 {
		t.Fatalf("reads ran %d times, want 2", len(h.hooks.ran))
	}
	if _, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
		ConnectionID: "conn_hooks", Action: "write_thing", Input: json.RawMessage(`{}`), IdempotencyKey: "short",
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RunAction() with a key that is too short = %v, want domain.ErrInvalid", err)
	}
}

func TestRunActionFailures(t *testing.T) {
	tests := []struct {
		name      string
		arrange   func(*harness)
		want      error
		reason    string
		retryable bool
		ran       int
	}{
		{name: "the provider refuses the input", arrange: func(h *harness) { h.hooks.actionErr = domain.ErrInvalid },
			want: domain.ErrInvalid, reason: integrationsv1.ActionErrorInvalidRequest, ran: 1},
		{name: "the provider rejects the credential", arrange: func(h *harness) { h.hooks.actionErr = domain.ErrCredentialRejected },
			want: domain.ErrCredentialRejected, reason: integrationsv1.ActionErrorCredentialRejected, ran: 1},
		{name: "the provider has no such thing", arrange: func(h *harness) { h.hooks.actionErr = domain.ErrNotFound },
			want: domain.ErrNotFound, reason: integrationsv1.ActionErrorNotFound, ran: 1},
		{name: "the provider is unavailable", arrange: func(h *harness) { h.hooks.actionErr = domain.ErrProviderUnavailable },
			want: domain.ErrProviderUnavailable, reason: integrationsv1.ActionErrorProviderUnavailable, retryable: true, ran: 1},
		{name: "the connection needs reconnecting", arrange: func(h *harness) { h.connections.tokenErr = domain.ErrNeedsReconnect },
			want: domain.ErrNeedsReconnect, reason: integrationsv1.ActionErrorNeedsReconnect},
		{name: "connections refuses the member", arrange: func(h *harness) { h.connections.tokenErr = domain.ErrForbidden },
			want: domain.ErrForbidden, reason: integrationsv1.ActionErrorForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			test.arrange(h)

			_, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
				ConnectionID: "conn_hooks", Action: "write_thing", Input: json.RawMessage(`{}`), IdempotencyKey: "chat-turn-7788",
			})

			if !errors.Is(err, test.want) {
				t.Fatalf("RunAction() error = %v, want %v", err, test.want)
			}
			if len(h.hooks.ran) != test.ran {
				t.Fatalf("provider ran %d times, want %d", len(h.hooks.ran), test.ran)
			}
			failed := failedActions(t, h)
			if len(failed) != 1 || failed[0].Error != test.reason || failed[0].Retryable != test.retryable {
				t.Fatalf("failed events = %+v, want one %s (retryable %v)", failed, test.reason, test.retryable)
			}
			if failed[0].Action != "hooks.write_thing" || failed[0].RequestedBy != "user_member" || failed[0].FailedAt.IsZero() {
				t.Fatalf("failed event = %+v", failed[0])
			}
			if completed := completedEvents(t, h); len(completed) != 0 {
				t.Fatalf("completed events = %d, want none", len(completed))
			}
			// A failure is remembered under its key, so asking again with the
			// same key answers the failure rather than running it.
			action, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
				ConnectionID: "conn_hooks", Action: "write_thing", Input: json.RawMessage(`{}`), IdempotencyKey: "chat-turn-7788",
			})
			if err != nil || action.Status != domain.ActionFailed || action.Error != test.reason {
				t.Fatalf("RunAction() again = %+v, %v, want the recorded failure", action, err)
			}
			if len(h.hooks.ran) != test.ran {
				t.Fatal("asking again under the same key ran the action")
			}
		})
	}
}

func TestRunActionRefusals(t *testing.T) {
	tests := []struct {
		name        string
		credentials authclient.Credentials
		input       RunActionInput
		arrange     func(*harness)
		want        error
	}{
		{name: "no credentials", input: RunActionInput{ConnectionID: "conn_hooks", Action: "read_thing"}, want: domain.ErrUnauthorized},
		{name: "no connection", credentials: bearer(memberToken), input: RunActionInput{Action: "read_thing"}, want: domain.ErrInvalid},
		{name: "unknown connection", credentials: bearer(memberToken), input: RunActionInput{ConnectionID: "conn_missing", Action: "read_thing"}, want: domain.ErrNotFound},
		{name: "a connection the member cannot see", credentials: bearer(otherToken), input: RunActionInput{ConnectionID: "conn_hooks", Action: "read_thing"}, want: domain.ErrNotFound,
			arrange: func(h *harness) { h.connections.hidden[otherToken] = []string{"conn_hooks"} }},
		{name: "an action the provider does not have", credentials: bearer(memberToken), input: RunActionInput{ConnectionID: "conn_hooks", Action: "delete_everything"}, want: domain.ErrInvalid},
		{name: "a provider with no actions", credentials: bearer(memberToken), input: RunActionInput{ConnectionID: "conn_hooks", Action: "read_thing"}, want: domain.ErrInvalid,
			arrange: func(h *harness) { h.setProvider("conn_hooks", "notion") }},
		{name: "a disconnected connection", credentials: bearer(memberToken), input: RunActionInput{ConnectionID: "conn_hooks", Action: "read_thing"}, want: domain.ErrConflict,
			arrange: func(h *harness) { h.setStatus("conn_hooks", "disconnected") }},
		{name: "a connection that needs reconnecting", credentials: bearer(memberToken), input: RunActionInput{ConnectionID: "conn_hooks", Action: "read_thing"}, want: domain.ErrNeedsReconnect,
			arrange: func(h *harness) { h.setStatus("conn_hooks", "needs_reconnect") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			if test.arrange != nil {
				test.arrange(h)
			}
			if _, err := h.service.RunAction(context.Background(), test.credentials, testOrganization, test.input); !errors.Is(err, test.want) {
				t.Fatalf("RunAction() error = %v, want %v", err, test.want)
			}
			if len(h.hooks.ran) != 0 || len(h.store.actions) != 0 || len(h.store.events) != 0 {
				t.Fatalf("a refused action ran %d times, recorded %d and published %d", len(h.hooks.ran), len(h.store.actions), len(h.store.events))
			}
		})
	}
}

func TestRunActionLeavesOutAResultThatDoesNotFit(t *testing.T) {
	h := newHarness(t)
	h.hooks.result = providers.ActionResult{Result: json.RawMessage(`"` + strings.Repeat("a", integrationsv1.MaxPayloadBytes) + `"`)}

	action, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
		ConnectionID: "conn_hooks", Action: "read_thing", Input: json.RawMessage(`{}`),
	})

	if err != nil {
		t.Fatal(err)
	}
	if action.Result != nil {
		t.Fatalf("RunAction() kept %d bytes of result, want it left out", len(action.Result))
	}
	completed := completedEvents(t, h)
	if len(completed) != 1 || !completed[0].ResultOmitted || completed[0].Result != nil {
		t.Fatalf("completed event = %+v, want the result left out and flagged", completed[0])
	}
}

func TestCatalogReadsTheRegistryAloud(t *testing.T) {
	h := newHarness(t)

	catalog := h.service.Catalog()

	if len(catalog) != 2 {
		t.Fatalf("Catalog() described %d providers, want the registry's 2", len(catalog))
	}
	if catalog[0].Name != "chat" || catalog[1].Name != "hooks" {
		t.Fatalf("Catalog() = %s, %s, want them in name order", catalog[0].Name, catalog[1].Name)
	}
	hooks := catalog[1]
	if hooks.Delivery != providers.DeliveryWebhook || len(hooks.Actions) != 2 {
		t.Fatalf("Catalog() hooks = %+v", hooks)
	}
	if hooks.Actions[0].Name != "read_thing" || hooks.Actions[0].Writes || !hooks.Actions[1].Writes {
		t.Fatalf("Catalog() actions = %+v", hooks.Actions)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) || strings.Contains(string(encoded), "provider-token") {
		t.Fatal("the catalogue does not encode, or carries a credential")
	}
}

func TestRunWorkersForgetsOldActions(t *testing.T) {
	h := newHarness(t)
	ended := h.now.Add(-actionMemory - time.Hour)
	h.store.actions["act_old"] = domain.Action{ID: "act_old", Status: domain.ActionCompleted, EndedAt: &ended}
	recent := h.now.Add(-time.Hour)
	h.store.actions["act_recent"] = domain.Action{ID: "act_recent", Status: domain.ActionCompleted, EndedAt: &recent}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h.service.RunWorkers(ctx)

	if _, ok := h.store.actions["act_old"]; ok {
		t.Fatal("an action older than the memory is still remembered")
	}
	if _, ok := h.store.actions["act_recent"]; !ok {
		t.Fatal("a recent action was forgotten")
	}
}

func TestRunActionFindsTheConnectionWhenOnlyTheProviderIsNamed(t *testing.T) {
	h := newHarness(t)
	h.hooks.result = providers.ActionResult{Result: json.RawMessage(`{"ok":true}`)}

	// The member has one connection to this provider, so naming the provider
	// is enough.
	action, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
		Provider: "hooks", Action: "read_thing", Input: json.RawMessage(`{}`),
	})
	if err != nil || action.ConnectionID != "conn_hooks" {
		t.Fatalf("RunAction() = %+v, %v, want it to find conn_hooks", action, err)
	}

	// With a second, the caller is asked which rather than acted with either.
	h.connections.connections["conn_hooks_2"] = domain.Connection{
		ID: "conn_hooks_2", OrganizationID: testOrganization, AppID: "app_hooks", Provider: "hooks", Scope: "organization",
		ProviderAccount: domain.ProviderAccount{ID: "43"}, Status: "active",
	}
	_, err = h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
		Provider: "hooks", Action: "read_thing", Input: json.RawMessage(`{}`),
	})
	if !errors.Is(err, domain.ErrInvalid) || !strings.Contains(err.Error(), "connection_id") {
		t.Fatalf("RunAction() with two connections error = %v, want it to ask which", err)
	}

	// A connection the member cannot see is not one of theirs to be found.
	h.connections.hidden[memberToken] = []string{"conn_hooks", "conn_hooks_2"}
	_, err = h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
		Provider: "hooks", Action: "read_thing", Input: json.RawMessage(`{}`),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("RunAction() with none the member can see error = %v, want domain.ErrNotFound", err)
	}
	if _, err := h.service.RunAction(context.Background(), bearer(memberToken), testOrganization, RunActionInput{
		Action: "read_thing", Input: json.RawMessage(`{}`),
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("RunAction() naming neither a connection nor a provider error = %v, want domain.ErrInvalid", err)
	}
}

func TestProviderConnectionsAreTheMembersOwn(t *testing.T) {
	h := newHarness(t)

	connections, err := h.service.ProviderConnections(context.Background(), bearer(memberToken), testOrganization, "hooks")
	if err != nil || len(connections) != 1 || connections[0].ID != "conn_hooks" {
		t.Fatalf("ProviderConnections() = %+v, %v", connections, err)
	}
	if all, err := h.service.ProviderConnections(context.Background(), bearer(memberToken), testOrganization, ""); err != nil || len(all) != 2 {
		t.Fatalf("ProviderConnections(every provider) = %d, %v, want 2", len(all), err)
	}
	h.connections.hidden[memberToken] = []string{"conn_hooks"}
	if connections, err := h.service.ProviderConnections(context.Background(), bearer(memberToken), testOrganization, "hooks"); err != nil || len(connections) != 0 {
		t.Fatalf("ProviderConnections() with it hidden = %+v, %v, want none", connections, err)
	}
	if _, err := h.service.ProviderConnections(context.Background(), authclient.Credentials{}, testOrganization, "hooks"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("ProviderConnections() without credentials error = %v, want domain.ErrUnauthorized", err)
	}
}
