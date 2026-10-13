package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// A Pipedream trigger is recorded once per connection, event and config with
// its sealed key, read back by id alone with that key, never claimed by
// auto-subscribe, and refused once its connection is gone.
func TestPipedreamTriggersKeepTheirSealedKey(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	connection := pipedreamConnection(t, store, "mem_1")
	if _, _, err := store.ActivatePipedreamConnection(ctx, connection.ID, "apn_"+randomHex(4), "", testClock,
		testEvent(t, "apps.connection.connected.v1")); err != nil {
		t.Fatal(err)
	}
	// Even a connection that asked to be auto-subscribed is not claimed.
	if _, err := store.pool.Exec(ctx, `UPDATE apps_connections SET auto_subscribe = true WHERE id = $1`, connection.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ConnectionsAwaitingSubscribe(ctx, 3, 100, testClock, testClock.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, owed := range claimed {
		if owed.ID == connection.ID {
			t.Fatal("auto-subscribe claimed a Pipedream connection")
		}
	}

	trigger := domain.Trigger{
		ID: "apt_" + randomHex(8), OrganizationID: "org_1", MemberID: "mem_1", ConnectionID: connection.ID,
		Toolkit: "linear", Slug: "PD_LINEAR_NEW_ISSUE", Config: []byte(`{"team":"eng"}`),
		PipedreamTriggerID: "dc_" + randomHex(4), Status: domain.TriggerActive, Kind: domain.TriggerKindWebhook,
		CreatedAt: testClock, UpdatedAt: testClock,
	}
	sealed := []byte(`{"version":1,"ciphertext":"c2VhbGVk"}`)
	created, fresh, err := store.CreatePipedreamTrigger(ctx, trigger, sealed, testEvent(t, "apps.trigger.subscribed.v1"))
	if err != nil || !fresh || created.PipedreamTriggerID != trigger.PipedreamTriggerID {
		t.Fatalf("CreatePipedreamTrigger() = %+v, %v, %v", created, fresh, err)
	}
	// The same subscription again, deployed again by a racing request,
	// answers the first and records nothing.
	twin := trigger
	twin.ID, twin.PipedreamTriggerID, twin.Config = "apt_"+randomHex(8), "dc_"+randomHex(4), []byte(`{ "team" : "eng" }`)
	if existing, fresh, err := store.CreatePipedreamTrigger(ctx, twin, sealed, testEvent(t, "apps.trigger.subscribed.v1")); err != nil || fresh || existing.ID != trigger.ID {
		t.Fatalf("a second identical subscription = %+v, %v, %v", existing, fresh, err)
	}

	read, key, err := store.PipedreamTrigger(ctx, trigger.ID)
	if err != nil || read.ID != trigger.ID || read.OrganizationID != "org_1" || string(key) != string(sealed) {
		t.Fatalf("PipedreamTrigger() = %+v, %q, %v", read, key, err)
	}
	composio, _ := testConnection(t, store, "org_1", "mem_2")
	if _, _, err := store.ActivateConnection(ctx, composio.ID, "", testClock, testEvent(t, "apps.connection.connected.v1")); err != nil {
		t.Fatal(err)
	}
	composioTrigger := domain.Trigger{
		ID: "apt_" + randomHex(8), OrganizationID: "org_1", MemberID: "mem_2", ConnectionID: composio.ID, Toolkit: "github",
		Slug: "GITHUB_STAR_ADDED_EVENT", Config: []byte(`{}`), ComposioTriggerID: "ti_" + randomHex(4),
		Status: domain.TriggerActive, CreatedAt: testClock, UpdatedAt: testClock,
	}
	if _, _, err := store.CreateTrigger(ctx, composioTrigger, testEvent(t, "apps.trigger.subscribed.v1")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{composioTrigger.ID, "apt_nothing"} {
		if _, _, err := store.PipedreamTrigger(ctx, id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("PipedreamTrigger(%s) = %v, want not found", id, err)
		}
	}

	if _, err := store.RemoveTrigger(ctx, trigger.ID, testClock, testEvent(t, "apps.trigger.unsubscribed.v1")); err != nil {
		t.Fatal(err)
	}
	if removed, _, err := store.PipedreamTrigger(ctx, trigger.ID); err != nil || removed.Status != domain.TriggerRemoved {
		t.Fatalf("a removed trigger reads as %+v, %v", removed, err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE apps_connections SET status = 'removed' WHERE id = $1`, connection.ID); err != nil {
		t.Fatal(err)
	}
	twin.ID = "apt_" + randomHex(8)
	if _, _, err := store.CreatePipedreamTrigger(ctx, twin, sealed, testEvent(t, "apps.trigger.subscribed.v1")); !errors.Is(err, domain.ErrNotConnected) {
		t.Fatalf("a trigger on a removed connection = %v", err)
	}
}
