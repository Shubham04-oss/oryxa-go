package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/github"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/slack"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// hookSealed is a stand-in sealed secret; the store keeps it as it is given.
func hookSealed(label string) *secrets.SealedData {
	return &secrets.SealedData{Version: 1, Nonce: []byte("nonce-" + label), Ciphertext: []byte("ciphertext-" + label), EncryptedDataKey: []byte("key-" + label)}
}

// hookEndpointID is an endpoint id in hooks' own shape.
func hookEndpointID() string { return "aph_" + randomHex(12)[:24] }

// hookEndpoint is an endpoint of a scope, made at testClock.
func hookEndpoint(scope, organizationID, connectionID, toolkit string) hooks.Endpoint {
	manifest := github.New().Manifest()
	if toolkit == "slack" {
		manifest = slack.New().Manifest()
	}
	return hooks.Endpoint{
		ID: hookEndpointID(), Toolkit: toolkit, Scope: scope, OrganizationID: organizationID, ConnectionID: connectionID,
		Scheme: *manifest.Webhook, Status: hooks.StatusActive, CreatedAt: testClock, UpdatedAt: testClock,
	}
}

// hookInUTC puts an endpoint's times in UTC, so two that mean the same
// instants compare equal whatever zone the driver answered in.
func hookInUTC(endpoint hooks.Endpoint) hooks.Endpoint {
	in := func(at *time.Time) *time.Time {
		if at == nil {
			return nil
		}
		value := at.UTC()
		return &value
	}
	endpoint.CreatedAt, endpoint.UpdatedAt = endpoint.CreatedAt.UTC(), endpoint.UpdatedAt.UTC()
	endpoint.ExpiresAt, endpoint.ValidatedAt = in(endpoint.ExpiresAt), in(endpoint.ValidatedAt)
	endpoint.Secret.PreviousUntil, endpoint.Secret.RevealedAt = in(endpoint.Secret.PreviousUntil), in(endpoint.Secret.RevealedAt)
	return endpoint
}

func createHookEndpoint(t *testing.T, store *Store, endpoint hooks.Endpoint) hooks.Endpoint {
	t.Helper()
	if err := store.CreateHookEndpoint(context.Background(), endpoint); err != nil {
		t.Fatalf("CreateHookEndpoint(%s) = %v", endpoint.Scope, err)
	}
	return endpoint
}

func TestHookEndpointsRoundTripEveryScope(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")

	perConnection := hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github")
	perConnection.Secret = hooks.Sealed{Current: hookSealed("current"), Version: 1}
	lapse := testClock.Add(7 * 24 * time.Hour)
	perConnection.ExpiresAt, perConnection.RemoteID = &lapse, "hook-123"
	organization := hookEndpoint(integration.HookScopeOrganization, "org_1", "", "slack")
	organization.Secret = hooks.Sealed{Current: hookSealed("org"), Version: 1}
	deployment := hookEndpoint(integration.HookScopeDeployment, "", "", "slack")

	for _, want := range []hooks.Endpoint{perConnection, organization, deployment} {
		createHookEndpoint(t, store, want)
		got, err := store.HookEndpoint(ctx, want.ID)
		if err != nil {
			t.Fatalf("HookEndpoint(%s) = %v", want.Scope, err)
		}
		gotJSON, _ := json.Marshal(hookInUTC(got))
		wantJSON, _ := json.Marshal(hookInUTC(want))
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("the %s endpoint read back as\n%s\nwant\n%s", want.Scope, gotJSON, wantJSON)
		}
		if !reflect.DeepEqual(got.Scheme, want.Scheme) {
			t.Fatalf("the %s endpoint's scheme = %+v, want %+v", want.Scope, got.Scheme, want.Scheme)
		}
		if gotSecret, wantSecret := hookInUTC(got).Secret, hookInUTC(want).Secret; !reflect.DeepEqual(gotSecret, wantSecret) {
			t.Fatalf("the %s endpoint's secret read back as %+v, want %+v", want.Scope, gotSecret, wantSecret)
		}
	}
	if found, err := store.ActiveHookEndpoint(ctx, integration.HookScopeDeployment, "", "slack"); err != nil || found.ID != deployment.ID {
		t.Fatalf("the active deployment endpoint = %s, %v; want %s", found.ID, err, deployment.ID)
	}
	if found, err := store.ActiveHookEndpoint(ctx, integration.HookScopeOrganization, "org_1", "slack"); err != nil || found.ID != organization.ID {
		t.Fatalf("org_1's endpoint = %s, %v; want %s", found.ID, err, organization.ID)
	}
	if _, err := store.ActiveHookEndpoint(ctx, integration.HookScopeOrganization, "org_2", "slack"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("org_2's endpoint = %v, want not found", err)
	}
	listed, err := store.HookEndpointsForConnection(ctx, connection.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != perConnection.ID {
		t.Fatalf("the connection's endpoints = %v, %v", listed, err)
	}
	if _, err := store.HookEndpoint(ctx, hookEndpointID()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an unknown endpoint = %v, want not found", err)
	}
}

func TestHookEndpointRulesHoldInTheDatabase(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	createHookEndpoint(t, store, hookEndpoint(integration.HookScopeDeployment, "", "", "slack"))
	createHookEndpoint(t, store, hookEndpoint(integration.HookScopeOrganization, "org_1", "", "slack"))

	for name, test := range map[string]struct {
		endpoint hooks.Endpoint
		want     error
	}{
		"a second deployment endpoint for a toolkit": {hookEndpoint(integration.HookScopeDeployment, "", "", "slack"), domain.ErrConflict},
		"a second endpoint for org_1's own app":      {hookEndpoint(integration.HookScopeOrganization, "org_1", "", "slack"), domain.ErrConflict},
		"a deployment endpoint in an organization":   {hookEndpoint(integration.HookScopeDeployment, "org_1", "", "github"), domain.ErrInvalid},
		"an organization endpoint with a connection": {hookEndpoint(integration.HookScopeOrganization, "org_2", connection.ID, "slack"), domain.ErrInvalid},
		"a connection endpoint with no connection":   {hookEndpoint(integration.HookScopeConnection, "org_1", "", "github"), domain.ErrInvalid},
		"a connection endpoint with no organization": {hookEndpoint(integration.HookScopeConnection, "", connection.ID, "github"), domain.ErrInvalid},
		"a connection that does not exist":           {hookEndpoint(integration.HookScopeConnection, "org_1", "con_missing", "github"), domain.ErrNotFound},
		"a connection in another organization":       {hookEndpoint(integration.HookScopeConnection, "org_2", connection.ID, "github"), domain.ErrInvalid},
		"a scope nobody knows":                       {hookEndpoint("team", "org_1", "", "github"), domain.ErrInvalid},
	} {
		if err := store.CreateHookEndpoint(ctx, test.endpoint); !errors.Is(err, test.want) {
			t.Errorf("%s = %v, want %v", name, err, test.want)
		}
	}
	badStatus := hookEndpoint(integration.HookScopeDeployment, "", "", "github")
	badStatus.Status = "paused"
	if err := store.CreateHookEndpoint(ctx, badStatus); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("a status nobody knows = %v, want invalid", err)
	}
	// Another organization's own app, and many endpoints per connection, are
	// fine.
	createHookEndpoint(t, store, hookEndpoint(integration.HookScopeOrganization, "org_2", "", "slack"))
	createHookEndpoint(t, store, hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github"))
	createHookEndpoint(t, store, hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github"))
}

func TestHookSecretsRotateOnlyFromWhatWasReadAndAreCapturedAndShownOnce(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	endpoint := hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github")
	endpoint.Secret = hooks.Sealed{Current: hookSealed("first"), Version: 1}
	createHookEndpoint(t, store, endpoint)

	until := testClock.Add(24 * time.Hour)
	if rotated, err := store.RotateHookSecret(ctx, endpoint.ID, 0, *hookSealed("stale"), until, testClock); err != nil || rotated {
		t.Fatalf("a rotation from a version it did not read = %v, %v; want refused", rotated, err)
	}
	if rotated, err := store.RotateHookSecret(ctx, endpoint.ID, 1, *hookSealed("second"), until, testClock); err != nil || !rotated {
		t.Fatalf("a rotation = %v, %v", rotated, err)
	}
	got, err := store.HookEndpoint(ctx, endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret.Version != 2 || string(got.Secret.Current.Ciphertext) != "ciphertext-second" ||
		string(got.Secret.Previous.Ciphertext) != "ciphertext-first" || !got.Secret.PreviousUntil.Equal(until) {
		t.Fatalf("after rotating, the secret = %+v", got.Secret)
	}

	notion := hookEndpoint(integration.HookScopeDeployment, "", "", "notion")
	createHookEndpoint(t, store, notion)
	if rotated, _ := store.RotateHookSecret(ctx, notion.ID, 0, *hookSealed("x"), until, testClock); rotated {
		t.Fatal("rotated an endpoint that has no secret")
	}
	if revealed, _ := store.MarkHookSecretRevealed(ctx, notion.ID, testClock); revealed {
		t.Fatal("revealed a secret that never arrived")
	}
	if captured, err := store.CaptureHookSecret(ctx, notion.ID, *hookSealed("token"), testClock); err != nil || !captured {
		t.Fatalf("capturing the app's token = %v, %v", captured, err)
	}
	if captured, err := store.CaptureHookSecret(ctx, notion.ID, *hookSealed("second-token"), testClock); err != nil || captured {
		t.Fatalf("capturing a second token = %v, %v; want refused", captured, err)
	}
	if revealed, err := store.MarkHookSecretRevealed(ctx, notion.ID, testClock); err != nil || !revealed {
		t.Fatalf("revealing = %v, %v", revealed, err)
	}
	if revealed, err := store.MarkHookSecretRevealed(ctx, notion.ID, testClock.Add(time.Minute)); err != nil || revealed {
		t.Fatalf("revealing again = %v, %v; want refused", revealed, err)
	}
	got, err = store.HookEndpoint(ctx, notion.ID)
	if err != nil || string(got.Secret.Current.Ciphertext) != "ciphertext-token" || got.Secret.Version != 1 ||
		got.Secret.RevealedAt == nil || !got.Secret.RevealedAt.Equal(testClock) {
		t.Fatalf("the captured token = %+v, %v", got.Secret, err)
	}
}

func TestHookEndpointsValidateOnceAndOnlyInsideTheirWindow(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	endpoint := createHookEndpoint(t, store, hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github"))

	if validated, err := store.MarkHookValidated(ctx, endpoint.ID, testClock, testClock.Add(time.Minute)); err != nil || validated {
		t.Fatalf("validating an endpoint made at the window's start = %v, %v; want refused", validated, err)
	}
	if validated, err := store.MarkHookValidated(ctx, endpoint.ID, testClock.Add(-time.Second), testClock.Add(time.Minute)); err != nil || !validated {
		t.Fatalf("validating inside the window = %v, %v", validated, err)
	}
	if validated, err := store.MarkHookValidated(ctx, endpoint.ID, time.Time{}, testClock.Add(2*time.Minute)); err != nil || validated {
		t.Fatalf("validating twice = %v, %v; want refused", validated, err)
	}
	got, err := store.HookEndpoint(ctx, endpoint.ID)
	if err != nil || got.ValidatedAt == nil || !got.ValidatedAt.Equal(testClock.Add(time.Minute)) {
		t.Fatalf("validated at = %v, %v", got.ValidatedAt, err)
	}
}

func TestHookRenewalsHandOverAndADeletedReplacementHandsBack(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	old := hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github")
	lapse := testClock.Add(time.Hour)
	old.ExpiresAt = &lapse
	createHookEndpoint(t, store, old)

	replacement := hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github")
	if err := store.ReplaceHookEndpoint(ctx, old.ID, replacement); err != nil {
		t.Fatal(err)
	}
	got, err := store.HookEndpoint(ctx, old.ID)
	if err != nil || got.Status != hooks.StatusReplaced || got.ReplacedBy != replacement.ID {
		t.Fatalf("the old endpoint = %s replaced by %q, %v", got.Status, got.ReplacedBy, err)
	}
	if due, err := store.HookEndpointsExpiring(ctx, lapse, 10); err != nil || len(due) != 0 {
		t.Fatalf("due after the renewal = %v, %v; want nothing", due, err)
	}
	second := hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github")
	if err := store.ReplaceHookEndpoint(ctx, old.ID, second); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("renewing it again = %v, want conflict", err)
	}
	if _, err := store.HookEndpoint(ctx, second.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("a refused renewal still stored its endpoint")
	}

	if deleted, err := store.DeleteHookEndpoint(ctx, replacement.ID); err != nil || !deleted {
		t.Fatalf("deleting the replacement = %v, %v", deleted, err)
	}
	got, err = store.HookEndpoint(ctx, old.ID)
	if err != nil || got.Status != hooks.StatusActive || got.ReplacedBy != "" {
		t.Fatalf("after its replacement was deleted, the old endpoint = %s replaced by %q, %v", got.Status, got.ReplacedBy, err)
	}
	if due, err := store.HookEndpointsExpiring(ctx, lapse, 10); err != nil || len(due) != 1 || due[0].ID != old.ID {
		t.Fatalf("due once more = %v, %v", due, err)
	}
	if deleted, err := store.DeleteHookEndpoint(ctx, replacement.ID); err != nil || deleted {
		t.Fatalf("deleting it again = %v, %v; want nothing deleted", deleted, err)
	}
}

func TestHookEndpointsLapseAndAreSwept(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	soon := createHookEndpoint(t, store, hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github"))
	later := createHookEndpoint(t, store, hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github"))
	never := hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github")
	never.Secret = hooks.Sealed{Current: hookSealed("new"), Version: 2, Previous: hookSealed("old")}
	overlap := testClock.Add(24 * time.Hour)
	never.Secret.PreviousUntil = &overlap
	createHookEndpoint(t, store, never)

	inAnHour, inTwo := testClock.Add(time.Hour), testClock.Add(2*time.Hour)
	if err := store.SetHookEndpointRemote(ctx, soon.ID, "channel-1", &inAnHour, testClock); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHookEndpointRemote(ctx, later.ID, "channel-2", &inTwo, testClock); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHookEndpointRemote(ctx, hookEndpointID(), "x", nil, testClock); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("recording an unknown endpoint's subscription = %v", err)
	}
	due, err := store.HookEndpointsExpiring(ctx, testClock.Add(3*time.Hour), 10)
	if err != nil || len(due) != 2 || due[0].ID != soon.ID || due[1].ID != later.ID || due[0].RemoteID != "channel-1" {
		t.Fatalf("due = %v, %v; want the two channels, soonest first", due, err)
	}
	if due, _ := store.HookEndpointsExpiring(ctx, testClock.Add(3*time.Hour), 1); len(due) != 1 || due[0].ID != soon.ID {
		t.Fatalf("due, one at a time = %v", due)
	}

	if deleted, err := store.DeleteExpiredHookEndpoints(ctx, inAnHour); err != nil || deleted != 1 {
		t.Fatalf("sweeping at an hour = %d, %v; want 1", deleted, err)
	}
	if _, err := store.HookEndpoint(ctx, soon.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("the lapsed endpoint was not deleted")
	}
	if got, _ := store.HookEndpoint(ctx, never.ID); got.Secret.Previous == nil {
		t.Fatal("a previous secret was forgotten inside its overlap")
	}
	if _, err := store.DeleteExpiredHookEndpoints(ctx, overlap); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.HookEndpoint(ctx, never.ID); got.Secret.Previous != nil || got.Secret.PreviousUntil != nil || got.Secret.Current == nil {
		t.Fatalf("after the overlap, the secret = %+v; want only the current one", got.Secret)
	}
}

func TestDeletingAConnectionsHookEndpointsAnswersThem(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	other, _ := testConnection(t, store, "org_1", "member_2")
	old := createHookEndpoint(t, store, hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github"))
	replacement := hookEndpoint(integration.HookScopeConnection, "org_1", connection.ID, "github")
	if err := store.ReplaceHookEndpoint(ctx, old.ID, replacement); err != nil {
		t.Fatal(err)
	}
	kept := createHookEndpoint(t, store, hookEndpoint(integration.HookScopeConnection, "org_1", other.ID, "github"))

	deleted, err := store.DeleteHookEndpointsForConnection(ctx, connection.ID)
	if err != nil || len(deleted) != 2 {
		t.Fatalf("deleting the connection's endpoints, a renewal pair among them = %v, %v", deleted, err)
	}
	if _, err := store.HookEndpoint(ctx, kept.ID); err != nil {
		t.Fatalf("another connection's endpoint = %v, want kept", err)
	}
	if left, _ := store.HookEndpointsForConnection(ctx, connection.ID); len(left) != 0 {
		t.Fatalf("left behind %v", left)
	}
}

func TestRecordHookDeliveryPublishesEachEventOnceWithItsMark(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	connection = activate(t, store, connection)
	trigger := testTrigger(t, store, connection, "ct_"+randomHex(8))
	outbox := func() int {
		var count int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.trigger.received.v1'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	deliveries := func() int64 {
		var count int64
		if err := store.pool.QueryRow(ctx, `SELECT delivery_count FROM apps_triggers WHERE id = $1`, trigger.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	event := func(key string) hooks.DeliveredEvent {
		return hooks.DeliveredEvent{Key: key, TriggerID: trigger.ID, Event: testEvent(t, "apps.trigger.received.v1")}
	}
	first := hooks.Delivery{Key: "hook:aph_x:d-1", ReceivedAt: testClock, Events: []hooks.DeliveredEvent{
		event("trigger:" + trigger.ID + ":e1"), event("trigger:" + trigger.ID + ":e2"),
	}}

	if handled, err := store.HookDeliveryHandled(ctx, first.Key); err != nil || handled {
		t.Fatalf("handled before recording = %v, %v", handled, err)
	}
	if recorded, err := store.RecordHookDelivery(ctx, first); err != nil || !recorded {
		t.Fatalf("recording = %v, %v", recorded, err)
	}
	if outbox() != 2 || deliveries() != 2 {
		t.Fatalf("outbox %d, delivery count %d; want 2 and 2", outbox(), deliveries())
	}
	if handled, err := store.HookDeliveryHandled(ctx, first.Key); err != nil || !handled {
		t.Fatalf("handled after recording = %v, %v", handled, err)
	}

	// The same delivery again writes nothing, events and all.
	if recorded, err := store.RecordHookDelivery(ctx, first); err != nil || recorded {
		t.Fatalf("recording it again = %v, %v; want nothing", recorded, err)
	}
	// Another delivery repeating one event publishes only the new one. An
	// event without an id is published whatever came before.
	second := hooks.Delivery{Key: "hook:aph_x:d-2", ReceivedAt: testClock.Add(time.Second), Events: []hooks.DeliveredEvent{
		event("trigger:" + trigger.ID + ":e2"), event("trigger:" + trigger.ID + ":e3"), event(""),
	}}
	if recorded, err := store.RecordHookDelivery(ctx, second); err != nil || !recorded {
		t.Fatalf("recording another delivery = %v, %v", recorded, err)
	}
	if outbox() != 4 || deliveries() != 4 {
		t.Fatalf("outbox %d, delivery count %d; want 4 and 4: e2 once, e3 and the id-less event", outbox(), deliveries())
	}

	// A delivery that cannot write all of its events writes none of them,
	// and is not marked handled, so its retry is not dropped.
	broken := event("trigger:" + trigger.ID + ":e4")
	broken.Event.ID = ""
	third := hooks.Delivery{Key: "hook:aph_x:d-3", ReceivedAt: testClock, Events: []hooks.DeliveredEvent{event("trigger:" + trigger.ID + ":e5"), broken}}
	if _, err := store.RecordHookDelivery(ctx, third); err == nil {
		t.Fatal("recording a delivery with a broken event succeeded")
	}
	if handled, _ := store.HookDeliveryHandled(ctx, third.Key); handled || outbox() != 4 {
		t.Fatalf("a failed delivery left handled = %v, outbox %d", handled, outbox())
	}
	if handled, _ := store.HookDeliveryHandled(ctx, "trigger:"+trigger.ID+":e5"); handled {
		t.Fatal("a failed delivery left its event's mark behind")
	}

	// Maintenance forgets hook marks with every other handled event.
	if _, err := store.DeleteHandledEventsBefore(ctx, testClock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if handled, _ := store.HookDeliveryHandled(ctx, first.Key); handled {
		t.Fatal("maintenance did not forget an old hook delivery")
	}
}
