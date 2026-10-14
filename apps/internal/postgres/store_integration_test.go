package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// openTestStore migrates a fresh schema and drops it afterwards, so the test
// never leaves tables in a shared development database.
// APPS_TEST_DATABASE_URL must be a postgres:// URL.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("APPS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("APPS_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schemaName := "apps_test_" + randomHex(6)
	schema := pgx.Identifier{schemaName}.Sanitize()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		_ = admin.Close(cleanup)
	})

	scoped, err := url.Parse(databaseURL)
	if err != nil || scoped.Scheme == "" {
		t.Fatalf("APPS_TEST_DATABASE_URL must be a postgres:// URL")
	}
	query := scoped.Query()
	query.Set("search_path", schemaName+",public")
	scoped.RawQuery = query.Encode()

	store, err := Open(ctx, scoped.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate() error = %v", err)
		}
	}
	return store
}

func randomHex(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

func testEvent(t *testing.T, detailType string) domain.Event {
	t.Helper()
	data, err := json.Marshal(map[string]string{"detail_type": detailType})
	if err != nil {
		t.Fatal(err)
	}
	return domain.Event{ID: "evt_" + randomHex(8), DetailType: detailType, OccurredAt: testClock, Data: data}
}

// testConnection creates and records a pending connection, answering it and
// the state hash its browser round trip would carry.
func testConnection(t *testing.T, store *Store, organizationID, memberID string) (domain.Connection, string) {
	t.Helper()
	stateHash := "state_" + randomHex(8)
	connection := domain.Connection{
		ID: "con_" + randomHex(8), OrganizationID: organizationID, MemberID: memberID, Toolkit: "github",
		AuthConfigID: "ac_1", ComposioUserID: domain.ComposioUserID(organizationID, memberID),
		ComposioAccountID: "acc_" + randomHex(8), Status: domain.ConnectionPending,
		ReturnTo: "https://example.test/return", CreatedAt: testClock, UpdatedAt: testClock,
	}
	if err := store.CreateConnection(context.Background(), connection, stateHash,
		testEvent(t, "apps.connection.initiated.v1")); err != nil {
		t.Fatalf("CreateConnection() error = %v", err)
	}
	return connection, stateHash
}

// activate makes a connection testConnection created active, the way a
// completed callback would. CreateTrigger now refuses anything else, so
// every test that puts a trigger on a connection needs this first.
func activate(t *testing.T, store *Store, connection domain.Connection) domain.Connection {
	t.Helper()
	activated, changed, err := store.ActivateConnection(context.Background(), connection.ID, "label",
		testClock.Add(time.Minute), testEvent(t, "apps.connection.connected.v1"))
	if err != nil || !changed {
		t.Fatalf("ActivateConnection() = %+v, %v, %v, want changed", activated, changed, err)
	}
	return activated
}

// testTrigger creates a live trigger on connection, backed by
// composioTriggerID.
func testTrigger(t *testing.T, store *Store, connection domain.Connection, composioTriggerID string) domain.Trigger {
	t.Helper()
	trigger := domain.Trigger{
		ID: "trg_" + randomHex(8), OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		ConnectionID: connection.ID, Toolkit: connection.Toolkit, Slug: "GITHUB_ISSUE_CREATED",
		Config: json.RawMessage(`{"repo":"acme/api"}`), ComposioTriggerID: composioTriggerID,
		Status: domain.TriggerActive, CreatedAt: testClock, UpdatedAt: testClock,
	}
	created, ok, err := store.CreateTrigger(context.Background(), trigger, testEvent(t, "apps.trigger.subscribed.v1"))
	if err != nil || !ok {
		t.Fatalf("CreateTrigger() = %+v, %v, %v, want created", created, ok, err)
	}
	return created
}

func TestConnectionPendingActivateWritesEventOnceAndClearsTheStateHash(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, stateHash := testConnection(t, store, "org_1", "member_1")

	pending, err := store.PendingConnection(ctx, stateHash)
	if err != nil {
		t.Fatalf("PendingConnection() error = %v", err)
	}
	if pending.ID != connection.ID || pending.Status != domain.ConnectionPending {
		t.Fatalf("PendingConnection() = %+v", pending)
	}

	event := testEvent(t, "apps.connection.connected.v1")
	activated, changed, err := store.ActivateConnection(ctx, connection.ID, "github_red-castle", testClock.Add(time.Minute), event)
	if err != nil || !changed {
		t.Fatalf("ActivateConnection() = %+v, %v, %v, want changed", activated, changed, err)
	}
	if activated.Status != domain.ConnectionActive || activated.Label != "github_red-castle" {
		t.Fatalf("ActivateConnection() = %+v", activated)
	}
	if activated.ConnectedAt == nil || !activated.ConnectedAt.Equal(testClock.Add(time.Minute)) {
		t.Fatalf("ActivateConnection() connected_at = %v, want %v", activated.ConnectedAt, testClock.Add(time.Minute))
	}

	var outboxCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM apps_outbox WHERE id = $1`, event.ID).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("outbox events for %s = %d, error = %v, want 1", event.ID, outboxCount, err)
	}

	// Activating again finds nothing pending, so nothing changes and no
	// second event is written.
	again := testEvent(t, "apps.connection.connected.v1")
	_, changed, err = store.ActivateConnection(ctx, connection.ID, "second-label", testClock.Add(2*time.Minute), again)
	if err != nil || changed {
		t.Fatalf("ActivateConnection(again) = %v, %v, want unchanged", changed, err)
	}
	var totalEvents int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.connection.connected.v1'`).Scan(&totalEvents); err != nil || totalEvents != 1 {
		t.Fatalf("outbox events = %d, error = %v, want still 1", totalEvents, err)
	}

	// The state hash no longer finds anything: it was cleared on activation.
	if _, err := store.PendingConnection(ctx, stateHash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("PendingConnection(after activation) error = %v, want ErrNotFound", err)
	}
}

func TestFailConnectionOnlyChangesAPendingOne(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")

	failed, err := store.FailConnection(ctx, connection.ID, "denied_by_provider", testClock.Add(time.Minute), testEvent(t, "apps.connection.failed.v1"))
	if err != nil || !failed {
		t.Fatalf("FailConnection() = %v, %v, want changed", failed, err)
	}
	read, err := store.Connection(ctx, "org_1", connection.ID)
	if err != nil {
		t.Fatalf("Connection() error = %v", err)
	}
	if read.Status != domain.ConnectionFailed || read.StatusReason != "denied_by_provider" {
		t.Fatalf("Connection() after failing = %+v", read)
	}

	failed, err = store.FailConnection(ctx, connection.ID, "denied_by_provider", testClock.Add(2*time.Minute), testEvent(t, "apps.connection.failed.v1"))
	if err != nil || failed {
		t.Fatalf("FailConnection(again) = %v, %v, want unchanged", failed, err)
	}
}

func TestConnectionHidesAnotherOrganizations(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")

	if _, err := store.Connection(ctx, "org_2", connection.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Connection(wrong org) error = %v, want ErrNotFound", err)
	}
	read, err := store.Connection(ctx, "org_1", connection.ID)
	if err != nil || read.ID != connection.ID {
		t.Fatalf("Connection(own org) = %+v, %v", read, err)
	}
}

func TestOneCurrentConnectionPerMemberAndApp(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	first, _ := testConnection(t, store, "org_1", "member_1")

	second := first
	second.ID = "con_" + randomHex(8)
	second.ComposioAccountID = "acc_" + randomHex(8)
	if err := store.CreateConnection(ctx, second, "state_"+randomHex(8), testEvent(t, "apps.connection.initiated.v1")); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second current connection error=%v, want conflict", err)
	}

	// Another member owns a separate connection slot to the same app.
	other := second
	other.ID = "con_" + randomHex(8)
	other.MemberID = "member_2"
	other.ComposioUserID = domain.ComposioUserID("org_1", other.MemberID)
	other.ComposioAccountID = "acc_" + randomHex(8)
	if err := store.CreateConnection(ctx, other, "state_"+randomHex(8), testEvent(t, "apps.connection.initiated.v1")); err != nil {
		t.Fatalf("other member connection error=%v", err)
	}

	// A failed attempt is history, not a permanent lock: the member can retry.
	if _, err := store.FailConnection(ctx, first.ID, "denied_at_provider", testClock.Add(time.Minute), testEvent(t, "apps.connection.failed.v1")); err != nil {
		t.Fatal(err)
	}
	replacement := second
	replacement.ID = "con_" + randomHex(8)
	replacement.ComposioAccountID = "acc_" + randomHex(8)
	if err := store.CreateConnection(ctx, replacement, "state_"+randomHex(8), testEvent(t, "apps.connection.initiated.v1")); err != nil {
		t.Fatalf("replacement connection error=%v", err)
	}
}

func TestConnectionsListsNewestFirstAndFilters(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	older, _ := testConnection(t, store, "org_1", "member_1")

	newer := older
	newer.ID = "con_" + randomHex(8)
	newer.MemberID = "member_2"
	newer.Toolkit = "slack"
	newer.ComposioAccountID = "acc_" + randomHex(8)
	newer.CreatedAt = testClock.Add(time.Minute)
	newer.UpdatedAt = newer.CreatedAt
	if err := store.CreateConnection(ctx, newer, "state_"+randomHex(8),
		testEvent(t, "apps.connection.initiated.v1")); err != nil {
		t.Fatalf("CreateConnection() error = %v", err)
	}

	removed := older
	removed.ID = "con_" + randomHex(8)
	removed.Toolkit = "notion"
	removed.ComposioAccountID = "acc_" + randomHex(8)
	removed.CreatedAt = testClock.Add(2 * time.Minute)
	removed.UpdatedAt = removed.CreatedAt
	if err := store.CreateConnection(ctx, removed, "state_"+randomHex(8),
		testEvent(t, "apps.connection.initiated.v1")); err != nil {
		t.Fatalf("CreateConnection() error = %v", err)
	}
	if _, err := store.RemoveConnection(ctx, removed.ID, "removed_by_member", removed.CreatedAt, nil); err != nil {
		t.Fatalf("RemoveConnection() error = %v", err)
	}

	all, err := store.Connections(ctx, "org_1", domain.ConnectionQuery{})
	if err != nil {
		t.Fatalf("Connections() error = %v", err)
	}
	if len(all) != 2 || all[0].ID != newer.ID || all[1].ID != older.ID {
		t.Fatalf("Connections() = %d connections, want [newer, older] excluding the removed one", len(all))
	}

	byMember, err := store.Connections(ctx, "org_1", domain.ConnectionQuery{MemberID: "member_2"})
	if err != nil {
		t.Fatalf("Connections(member) error = %v", err)
	}
	if len(byMember) != 1 || byMember[0].ID != newer.ID {
		t.Fatalf("Connections(member) = %d, want just the member's", len(byMember))
	}

	byToolkit, err := store.Connections(ctx, "org_1", domain.ConnectionQuery{Toolkit: "slack"})
	if err != nil {
		t.Fatalf("Connections(toolkit) error = %v", err)
	}
	if len(byToolkit) != 1 || byToolkit[0].ID != newer.ID {
		t.Fatalf("Connections(toolkit) = %d, want just that toolkit", len(byToolkit))
	}

	byStatus, err := store.Connections(ctx, "org_1", domain.ConnectionQuery{Statuses: []domain.ConnectionStatus{domain.ConnectionRemoved}})
	if err != nil {
		t.Fatalf("Connections(status=removed) error = %v", err)
	}
	if len(byStatus) != 1 || byStatus[0].ID != removed.ID {
		t.Fatalf("Connections(status=removed) = %d, want the removed one when asked for by name", len(byStatus))
	}
}

func TestExpireConnectionOnlyChangesAnActiveOne(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	if _, _, err := store.ActivateConnection(ctx, connection.ID, "label", testClock.Add(time.Minute), testEvent(t, "apps.connection.connected.v1")); err != nil {
		t.Fatalf("ActivateConnection() error = %v", err)
	}

	expireEvent := testEvent(t, "apps.connection.expired.v1")
	expired, err := store.ExpireConnection(ctx, connection.ID, "refresh_failed", testClock.Add(2*time.Minute), expireEvent)
	if err != nil || !expired {
		t.Fatalf("ExpireConnection() = %v, %v, want changed", expired, err)
	}
	read, err := store.Connection(ctx, "org_1", connection.ID)
	if err != nil {
		t.Fatalf("Connection() error = %v", err)
	}
	if read.Status != domain.ConnectionExpired || read.StatusReason != "refresh_failed" {
		t.Fatalf("Connection() after expiring = %+v", read)
	}

	var recorded bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM apps_outbox WHERE id = $1)`, expireEvent.ID).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("the expiry event was not in the outbox: found = %v, err = %v", recorded, err)
	}

	expired, err = store.ExpireConnection(ctx, connection.ID, "refresh_failed", testClock.Add(3*time.Minute), testEvent(t, "apps.connection.expired.v1"))
	if err != nil || expired {
		t.Fatalf("ExpireConnection(again) = %v, %v, want unchanged", expired, err)
	}
}

func TestRemoveConnectionRemovesItsTriggersAndAnswersFalseTheSecondTime(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	if _, _, err := store.ActivateConnection(ctx, connection.ID, "label", testClock.Add(time.Minute), testEvent(t, "apps.connection.connected.v1")); err != nil {
		t.Fatalf("ActivateConnection() error = %v", err)
	}

	live := testTrigger(t, store, connection, "ct_"+randomHex(8))
	alreadyRemoved := testTrigger(t, store, connection, "ct_"+randomHex(8))
	if _, err := store.RemoveTrigger(ctx, alreadyRemoved.ID, testClock.Add(time.Minute), testEvent(t, "apps.trigger.unsubscribed.v1")); err != nil {
		t.Fatalf("RemoveTrigger() error = %v", err)
	}

	var callbackConnection domain.Connection
	var callbackTriggers []domain.Trigger
	removed, err := store.RemoveConnection(ctx, connection.ID, "removed_by_member", testClock.Add(2*time.Minute),
		func(gotConnection domain.Connection, gotTriggers []domain.Trigger) ([]domain.Event, error) {
			callbackConnection = gotConnection
			callbackTriggers = gotTriggers
			return []domain.Event{testEvent(t, "apps.connection.removed.v1")}, nil
		})
	if err != nil || !removed {
		t.Fatalf("RemoveConnection() = %v, %v, want changed", removed, err)
	}
	if callbackConnection.Status != domain.ConnectionRemoved {
		t.Fatalf("RemoveConnection() callback connection status = %s, want removed", callbackConnection.Status)
	}
	if len(callbackTriggers) != 1 || callbackTriggers[0].ID != live.ID {
		t.Fatalf("RemoveConnection() callback triggers = %+v, want just the live one", callbackTriggers)
	}

	readLive, err := store.Trigger(ctx, "org_1", live.ID)
	if err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}
	if readLive.Status != domain.TriggerRemoved {
		t.Fatalf("Trigger() after RemoveConnection = %s, want removed", readLive.Status)
	}

	var outboxCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.connection.connected.v1' OR detail_type = 'apps.connection.removed.v1'`).Scan(&outboxCount); err != nil || outboxCount != 2 {
		t.Fatalf("outbox events = %d, error = %v, want 2 (connected + removed)", outboxCount, err)
	}

	removed, err = store.RemoveConnection(ctx, connection.ID, "removed_by_member", testClock.Add(3*time.Minute), nil)
	if err != nil || removed {
		t.Fatalf("RemoveConnection(again) = %v, %v, want unchanged", removed, err)
	}
}

// activateForAutoSubscribe records and activates a connection that wants
// auto-subscribing, the way a completed callback leaves one. testConnection
// does not set AutoSubscribe, since most of its callers do not care about it.
func activateForAutoSubscribe(t *testing.T, store *Store, organizationID, memberID string) domain.Connection {
	t.Helper()
	ctx := context.Background()
	connection := domain.Connection{
		ID: "con_" + randomHex(8), OrganizationID: organizationID, MemberID: memberID, Toolkit: "github",
		AuthConfigID: "ac_1", ComposioUserID: domain.ComposioUserID(organizationID, memberID),
		ComposioAccountID: "acc_" + randomHex(8), Status: domain.ConnectionPending, AutoSubscribe: true,
		CreatedAt: testClock, UpdatedAt: testClock,
	}
	if err := store.CreateConnection(ctx, connection, "state_"+randomHex(8),
		testEvent(t, "apps.connection.initiated.v1")); err != nil {
		t.Fatalf("CreateConnection() error = %v", err)
	}
	activated, _, err := store.ActivateConnection(ctx, connection.ID, "label", testClock.Add(time.Minute),
		testEvent(t, "apps.connection.connected.v1"))
	if err != nil {
		t.Fatalf("ActivateConnection() error = %v", err)
	}
	return activated
}

// TestConnectionsAwaitingSubscribeLeasesWhatItClaims proves the lease closes
// the window FOR UPDATE SKIP LOCKED leaves open: that lock protects only the
// claiming statement itself, so without a lease a second pass ticking right
// after the first commits would see the same connection as unclaimed and
// take it again - doubling the Composio calls the real bug spent.
func TestConnectionsAwaitingSubscribeLeasesWhatItClaims(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection := activateForAutoSubscribe(t, store, "org_1", "member_1")

	now := testClock.Add(time.Minute)
	leaseUntil := now.Add(10 * time.Minute)
	first, err := store.ConnectionsAwaitingSubscribe(ctx, 3, 10, now, leaseUntil)
	if err != nil {
		t.Fatalf("ConnectionsAwaitingSubscribe() error = %v", err)
	}
	if len(first) != 1 || first[0].ID != connection.ID {
		t.Fatalf("first claim = %+v, want just the one connection", first)
	}

	// A second pass at the same instant - the same race the bug allowed -
	// gets nothing: the lease this claim just took still holds.
	again, err := store.ConnectionsAwaitingSubscribe(ctx, 3, 10, now, leaseUntil)
	if err != nil {
		t.Fatalf("ConnectionsAwaitingSubscribe(same instant) error = %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("a second claim under the first's lease = %+v, want none", again)
	}

	// Once the lease has passed, and the connection is still unmarked and
	// under its attempt limit, it is fair game again.
	afterLease := leaseUntil.Add(time.Second)
	retried, err := store.ConnectionsAwaitingSubscribe(ctx, 3, 10, afterLease, afterLease.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("ConnectionsAwaitingSubscribe(after lease) error = %v", err)
	}
	if len(retried) != 1 || retried[0].ID != connection.ID {
		t.Fatalf("claim after the lease = %+v, want the connection back", retried)
	}

	// A connection already marked subscribed is never claimed, leased or not.
	if err := store.MarkSubscribed(ctx, connection.ID, afterLease); err != nil {
		t.Fatalf("MarkSubscribed() error = %v", err)
	}
	done, err := store.ConnectionsAwaitingSubscribe(ctx, 3, 10, afterLease.Add(time.Hour), afterLease.Add(time.Hour+10*time.Minute))
	if err != nil {
		t.Fatalf("ConnectionsAwaitingSubscribe(marked) error = %v", err)
	}
	if len(done) != 0 {
		t.Fatalf("claimed a connection already marked subscribed: %+v", done)
	}
}

func TestSaveAuthConfigAgreesUnderConcurrency(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, err := store.AuthConfig(ctx, "github"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("AuthConfig(before) error = %v, want ErrNotFound", err)
	}

	first := application.AuthConfig{Toolkit: "github", AuthConfigID: "ac_1", AuthScheme: "OAUTH2", ComposioManaged: true, CreatedAt: testClock}
	second := application.AuthConfig{Toolkit: "github", AuthConfigID: "ac_2", AuthScheme: "OAUTH2", ComposioManaged: true, CreatedAt: testClock}

	var wait sync.WaitGroup
	results := make([]application.AuthConfig, 2)
	errs := make([]error, 2)
	wait.Add(2)
	go func() { defer wait.Done(); results[0], errs[0] = store.SaveAuthConfig(ctx, first) }()
	go func() { defer wait.Done(); results[1], errs[1] = store.SaveAuthConfig(ctx, second) }()
	wait.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("SaveAuthConfig() errors = %v, %v", errs[0], errs[1])
	}
	if results[0].AuthConfigID != results[1].AuthConfigID {
		t.Fatalf("SaveAuthConfig() disagreed: %q vs %q", results[0].AuthConfigID, results[1].AuthConfigID)
	}

	read, err := store.AuthConfig(ctx, "github")
	if err != nil {
		t.Fatalf("AuthConfig() error = %v", err)
	}
	if read.AuthConfigID != results[0].AuthConfigID {
		t.Fatalf("AuthConfig() = %q, want %q", read.AuthConfigID, results[0].AuthConfigID)
	}
}

func TestCreateTriggerDedupesByComposioIDAndAllowsRecreateAfterRemoval(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	connection = activate(t, store, connection)
	composioTriggerID := "ct_" + randomHex(8)

	first := testTrigger(t, store, connection, composioTriggerID)

	again := domain.Trigger{
		ID: "trg_" + randomHex(8), OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		ConnectionID: connection.ID, Toolkit: connection.Toolkit, Slug: "GITHUB_ISSUE_CREATED",
		Config: json.RawMessage(`{"repo":"acme/api"}`), ComposioTriggerID: composioTriggerID,
		Status: domain.TriggerActive, CreatedAt: testClock.Add(time.Minute), UpdatedAt: testClock.Add(time.Minute),
	}
	same, created, err := store.CreateTrigger(ctx, again, testEvent(t, "apps.trigger.subscribed.v1"))
	if err != nil || created {
		t.Fatalf("CreateTrigger(same instance) = %+v, %v, %v, want the existing one and false", same, created, err)
	}
	if same.ID != first.ID {
		t.Fatalf("CreateTrigger(same instance) = %s, want the original %s", same.ID, first.ID)
	}

	if _, err := store.RemoveTrigger(ctx, first.ID, testClock.Add(2*time.Minute), testEvent(t, "apps.trigger.unsubscribed.v1")); err != nil {
		t.Fatalf("RemoveTrigger() error = %v", err)
	}

	reborn, created, err := store.CreateTrigger(ctx, again, testEvent(t, "apps.trigger.subscribed.v1"))
	if err != nil || !created {
		t.Fatalf("CreateTrigger(after removal) = %+v, %v, %v, want a fresh row", reborn, created, err)
	}
	if reborn.ID == first.ID {
		t.Fatal("CreateTrigger(after removal) reused the removed row's id")
	}
}

// TestCreateTriggerRefusesARemovedConnection covers a pass racing a
// disconnect: once RemoveConnection has committed, nothing may attach a new
// trigger to that connection - it would sit there looking live, listed to
// the member forever, since removal's own cleanup has already run and will
// not run again.
func TestCreateTriggerRefusesARemovedConnection(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	connection = activate(t, store, connection)
	if _, err := store.RemoveConnection(ctx, connection.ID, "removed_by_member", testClock.Add(2*time.Minute), nil); err != nil {
		t.Fatalf("RemoveConnection() error = %v", err)
	}

	trigger := domain.Trigger{
		ID: "trg_" + randomHex(8), OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		ConnectionID: connection.ID, Toolkit: connection.Toolkit, Slug: "GITHUB_ISSUE_CREATED",
		Config: json.RawMessage(`{"repo":"acme/api"}`), ComposioTriggerID: "ct_" + randomHex(8),
		Status: domain.TriggerActive, CreatedAt: testClock.Add(3 * time.Minute), UpdatedAt: testClock.Add(3 * time.Minute),
	}
	if _, _, err := store.CreateTrigger(ctx, trigger, testEvent(t, "apps.trigger.subscribed.v1")); !errors.Is(err, domain.ErrNotConnected) {
		t.Fatalf("CreateTrigger(removed connection) error = %v, want ErrNotConnected", err)
	}

	var triggerCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM apps_triggers WHERE connection_id = $1`, connection.ID).Scan(&triggerCount); err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if triggerCount != 0 {
		t.Fatalf("triggers on a removed connection = %d, want 0", triggerCount)
	}
	var outboxCount int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.trigger.subscribed.v1'`,
	).Scan(&outboxCount); err != nil || outboxCount != 0 {
		t.Fatalf("outbox events = %d, error = %v, want 0", outboxCount, err)
	}
}

func TestDisableTriggerOnlyChangesAnActiveOne(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	connection = activate(t, store, connection)
	trigger := testTrigger(t, store, connection, "ct_"+randomHex(8))

	event := testEvent(t, "apps.trigger.disabled.v1")
	disabled, err := store.DisableTrigger(ctx, trigger.ID, "connection_expired", testClock.Add(time.Minute), event)
	if err != nil || !disabled {
		t.Fatalf("DisableTrigger() = %v, %v, want changed", disabled, err)
	}
	read, err := store.Trigger(ctx, "org_1", trigger.ID)
	if err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}
	if read.Status != domain.TriggerDisabled || read.StatusReason != "connection_expired" {
		t.Fatalf("Trigger() after disabling = %+v", read)
	}
	// Disabled is not removed, so it is still "live" by this store's own
	// definition (the same one CreateTrigger's dedupe uses) and still
	// answers TriggerByComposioID.
	byComposioID, err := store.TriggerByComposioID(ctx, trigger.ComposioTriggerID)
	if err != nil || byComposioID.ID != trigger.ID {
		t.Fatalf("TriggerByComposioID(disabled) = %+v, %v, want the disabled trigger", byComposioID, err)
	}

	disabled, err = store.DisableTrigger(ctx, trigger.ID, "connection_expired", testClock.Add(2*time.Minute), testEvent(t, "apps.trigger.disabled.v1"))
	if err != nil || disabled {
		t.Fatalf("DisableTrigger(again) = %v, %v, want unchanged", disabled, err)
	}
}

func TestTriggersListsLiveOnesFilteredByMemberOrConnection(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connectionA, _ := testConnection(t, store, "org_1", "member_1")
	connectionA = activate(t, store, connectionA)
	connectionB, _ := testConnection(t, store, "org_1", "member_2")
	connectionB = activate(t, store, connectionB)

	onA := testTrigger(t, store, connectionA, "ct_"+randomHex(8))
	onB := testTrigger(t, store, connectionB, "ct_"+randomHex(8))
	removedOnA := testTrigger(t, store, connectionA, "ct_"+randomHex(8))
	if _, err := store.RemoveTrigger(ctx, removedOnA.ID, testClock.Add(time.Minute), testEvent(t, "apps.trigger.unsubscribed.v1")); err != nil {
		t.Fatalf("RemoveTrigger() error = %v", err)
	}

	all, err := store.Triggers(ctx, "org_1", application.TriggerQuery{})
	if err != nil {
		t.Fatalf("Triggers() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Triggers() = %d, want 2 live triggers, excluding the removed one", len(all))
	}

	byConnection, err := store.Triggers(ctx, "org_1", application.TriggerQuery{ConnectionID: connectionB.ID})
	if err != nil {
		t.Fatalf("Triggers(connection) error = %v", err)
	}
	if len(byConnection) != 1 || byConnection[0].ID != onB.ID {
		t.Fatalf("Triggers(connection) = %+v, want just onB", byConnection)
	}

	byMember, err := store.Triggers(ctx, "org_1", application.TriggerQuery{MemberID: "member_1"})
	if err != nil {
		t.Fatalf("Triggers(member) error = %v", err)
	}
	if len(byMember) != 1 || byMember[0].ID != onA.ID {
		t.Fatalf("Triggers(member) = %+v, want just onA", byMember)
	}
}

func TestStartActionIdempotencyKeyAnswersTheFirstRunAndDoesNotCollideAcrossMembers(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")

	key := "idem_" + randomHex(8)
	action := domain.Action{
		ID: "act_" + randomHex(8), OrganizationID: "org_1", MemberID: "member_1", ConnectionID: connection.ID,
		Toolkit: "github", Tool: "GITHUB_CREATE_AN_ISSUE", Agent: true, AgentID: "agt_writer", AgentRole: "specialist", IdempotencyKey: key, CreatedAt: testClock,
	}
	first, started, err := store.StartAction(ctx, action)
	if err != nil || !started {
		t.Fatalf("StartAction() = %+v, %v, %v, want started", first, started, err)
	}
	if first.Status != domain.ActionRunning || first.AgentID != "agt_writer" || first.AgentRole != "specialist" {
		t.Fatalf("StartAction() = %+v, want running, naming its agent", first)
	}

	retry := action
	retry.ID = "act_" + randomHex(8)
	retry.CreatedAt = testClock.Add(time.Minute)
	again, started, err := store.StartAction(ctx, retry)
	if err != nil || started {
		t.Fatalf("StartAction(retry) = %+v, %v, %v, want the first run and false", again, started, err)
	}
	if again.ID != first.ID {
		t.Fatalf("StartAction(retry) = %s, want the original %s", again.ID, first.ID)
	}

	otherMember := action
	otherMember.ID = "act_" + randomHex(8)
	otherMember.MemberID = "member_2"
	otherMember.CreatedAt = testClock.Add(2 * time.Minute)
	theirRun, started, err := store.StartAction(ctx, otherMember)
	if err != nil || !started {
		t.Fatalf("StartAction(other member, same key) = %+v, %v, %v, want started", theirRun, started, err)
	}
	if theirRun.ID == first.ID {
		t.Fatal("StartAction(other member, same key) collided with member_1's run")
	}
}

func TestFinishActionRefusesAnActionThatIsNotRunning(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	action := domain.Action{
		ID: "act_" + randomHex(8), OrganizationID: "org_1", MemberID: "member_1", ConnectionID: connection.ID,
		Toolkit: "github", Tool: "GITHUB_CREATE_AN_ISSUE", ReadOnly: true, CreatedAt: testClock,
	}
	if _, _, err := store.StartAction(ctx, action); err != nil {
		t.Fatalf("StartAction() error = %v", err)
	}

	finished := testClock.Add(time.Minute)
	action.Status = domain.ActionSucceeded
	action.Result = json.RawMessage(`{"issue_id":42}`)
	action.LogID = "log_1"
	action.FinishedAt = &finished
	event := testEvent(t, "apps.action.completed.v1")
	if err := store.FinishAction(ctx, action, &event); err != nil {
		t.Fatalf("FinishAction() error = %v", err)
	}

	var recorded bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM apps_outbox WHERE id = $1)`, event.ID).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("the completion event was not in the outbox: found = %v, err = %v", recorded, err)
	}
	var status string
	var result []byte
	if err := store.pool.QueryRow(ctx, `SELECT status, result FROM apps_actions WHERE id = $1`, action.ID).Scan(&status, &result); err != nil {
		t.Fatalf("read action row: %v", err)
	}
	// jsonb re-formats on the way back (a space after ':', for one), so
	// compare it decoded rather than as raw text.
	var decoded struct {
		IssueID int `json:"issue_id"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode stored result: %v", err)
	}
	if status != "succeeded" || decoded.IssueID != 42 {
		t.Fatalf("action row after finishing = status %q, result %q", status, result)
	}

	// It is no longer running, so finishing it again is a conflict rather
	// than a silent overwrite of what already happened.
	again := testEvent(t, "apps.action.completed.v1")
	err := store.FinishAction(ctx, action, &again)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("FinishAction(again) error = %v, want ErrConflict", err)
	}
	var outboxCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.action.completed.v1'`).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("outbox events = %d, error = %v, want still 1", outboxCount, err)
	}
}

func TestRecordWebhookDedupesByDeliveryID(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	eventID := "whd_" + randomHex(8)
	recorded, err := store.RecordWebhook(ctx, eventID, "github_issue_created", testClock, testEvent(t, "apps.trigger.received.v1"))
	if err != nil || !recorded {
		t.Fatalf("RecordWebhook() = %v, %v, want recorded", recorded, err)
	}
	var outboxCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.trigger.received.v1'`).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("outbox events = %d, error = %v, want 1", outboxCount, err)
	}

	recorded, err = store.RecordWebhook(ctx, eventID, "github_issue_created", testClock.Add(time.Second), testEvent(t, "apps.trigger.received.v1"))
	if err != nil || recorded {
		t.Fatalf("RecordWebhook(redelivered) = %v, %v, want not recorded", recorded, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.trigger.received.v1'`).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("outbox events after a redelivery = %d, error = %v, want still 1", outboxCount, err)
	}
}

// TestRecordTriggerDeliveryCountsOnceAndOnlyWithItsEvent proves the one
// thing a trigger's tally is for: it counts exactly the deliveries whose
// events were published, so "this trigger has delivered n times" and the
// bus cannot disagree - not on a redelivery, and not on a trigger that has
// since been removed.
func TestRecordTriggerDeliveryCountsOnceAndOnlyWithItsEvent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	connection = activate(t, store, connection)
	trigger := testTrigger(t, store, connection, "ct_"+randomHex(8))

	if trigger.DeliveryCount != 0 || trigger.LastDeliveryAt != nil {
		t.Fatalf("a new trigger has already delivered: %d, %v", trigger.DeliveryCount, trigger.LastDeliveryAt)
	}

	eventID := "whd_" + randomHex(8)
	recorded, err := store.RecordTriggerDelivery(ctx, eventID, "composio.trigger.message", trigger.ID,
		testClock, testEvent(t, "apps.trigger.received.v1"))
	if err != nil || !recorded {
		t.Fatalf("RecordTriggerDelivery() = %v, %v, want recorded", recorded, err)
	}
	delivered, err := store.Trigger(ctx, "org_1", trigger.ID)
	if err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}
	if delivered.DeliveryCount != 1 || delivered.LastDeliveryAt == nil || !delivered.LastDeliveryAt.Equal(testClock) {
		t.Fatalf("after one delivery: count = %d, last = %v", delivered.DeliveryCount, delivered.LastDeliveryAt)
	}

	// Composio redelivers what it is unsure reached us. Nothing is written
	// twice: not the event, not the count.
	recorded, err = store.RecordTriggerDelivery(ctx, eventID, "composio.trigger.message", trigger.ID,
		testClock.Add(time.Minute), testEvent(t, "apps.trigger.received.v1"))
	if err != nil || recorded {
		t.Fatalf("RecordTriggerDelivery(redelivered) = %v, %v, want not recorded", recorded, err)
	}
	again, err := store.Trigger(ctx, "org_1", trigger.ID)
	if err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}
	if again.DeliveryCount != 1 || !again.LastDeliveryAt.Equal(testClock) {
		t.Fatalf("a redelivery counted again: count = %d, last = %v", again.DeliveryCount, again.LastDeliveryAt)
	}
	var outboxCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.trigger.received.v1'`).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("outbox events = %d, error = %v, want still 1", outboxCount, err)
	}

	// A delivery about a trigger removed in the meantime is still recorded
	// and still published - it describes something that happened - but
	// counts against nothing.
	if _, err := store.RemoveTrigger(ctx, trigger.ID, testClock.Add(time.Hour), testEvent(t, "apps.trigger.unsubscribed.v1")); err != nil {
		t.Fatalf("RemoveTrigger() error = %v", err)
	}
	recorded, err = store.RecordTriggerDelivery(ctx, "whd_"+randomHex(8), "composio.trigger.message", trigger.ID,
		testClock.Add(2*time.Hour), testEvent(t, "apps.trigger.received.v1"))
	if err != nil || !recorded {
		t.Fatalf("RecordTriggerDelivery(removed trigger) = %v, %v, want recorded", recorded, err)
	}
	removed, err := store.Trigger(ctx, "org_1", trigger.ID)
	if err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}
	if removed.DeliveryCount != 1 {
		t.Fatalf("a removed trigger counted a delivery: %d", removed.DeliveryCount)
	}
}

// TestCreateTriggerKeepsHowItDelivers proves the fields a member is told
// about survive the round trip, since they are the whole reason the catalog
// is read at subscribe time.
func TestCreateTriggerKeepsHowItDelivers(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection, _ := testConnection(t, store, "org_1", "member_1")
	connection = activate(t, store, connection)

	created, ok, err := store.CreateTrigger(ctx, domain.Trigger{
		ID: "trg_" + randomHex(8), OrganizationID: "org_1", MemberID: "member_1",
		ConnectionID: connection.ID, Toolkit: connection.Toolkit, Slug: "SLACK_RECEIVE_MESSAGE",
		Config: json.RawMessage(`{"channel":"C1"}`), ComposioTriggerID: "ct_" + randomHex(8),
		Status: domain.TriggerActive, Kind: domain.TriggerKindWebhook, ManualSetup: true,
		CreatedAt: testClock, UpdatedAt: testClock,
	}, testEvent(t, "apps.trigger.subscribed.v1"))
	if err != nil || !ok {
		t.Fatalf("CreateTrigger() = %+v, %v, %v, want created", created, ok, err)
	}
	if created.Kind != domain.TriggerKindWebhook || !created.ManualSetup {
		t.Fatalf("kind = %q, manual setup = %v", created.Kind, created.ManualSetup)
	}
	read, err := store.Trigger(ctx, "org_1", created.ID)
	if err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}
	if read.Kind != domain.TriggerKindWebhook || !read.ManualSetup {
		t.Fatalf("read back: kind = %q, manual setup = %v", read.Kind, read.ManualSetup)
	}
}

func TestFailPendingConnectionsBeforeFailsOnlyOldPendingOnes(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	old, _ := testConnection(t, store, "org_1", "member_1")
	fresh := old
	fresh.ID = "con_" + randomHex(8)
	fresh.Toolkit = "slack"
	fresh.ComposioAccountID = "acc_" + randomHex(8)
	fresh.CreatedAt = testClock.Add(time.Hour)
	fresh.UpdatedAt = fresh.CreatedAt
	if err := store.CreateConnection(ctx, fresh, "state_"+randomHex(8),
		testEvent(t, "apps.connection.initiated.v1")); err != nil {
		t.Fatalf("CreateConnection() error = %v", err)
	}

	cutoff := testClock.Add(30 * time.Minute)
	now := testClock.Add(2 * time.Hour)
	// The events are built from the rows the sweep actually changed, so it
	// announces the abandoned connection and only that one.
	var announced []domain.Connection
	abandoned := func(swept []domain.Connection) ([]domain.Event, error) {
		announced = swept
		events := make([]domain.Event, 0, len(swept))
		for range swept {
			events = append(events, testEvent(t, "apps.connection.failed.v1"))
		}
		return events, nil
	}
	count, err := store.FailPendingConnectionsBefore(ctx, cutoff, now, abandoned)
	if err != nil || count != 1 {
		t.Fatalf("FailPendingConnectionsBefore() = %d, %v, want 1", count, err)
	}
	if len(announced) != 1 || announced[0].ID != old.ID {
		t.Fatalf("announced = %+v, want just the abandoned connection", announced)
	}
	var outboxCount int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.connection.failed.v1'`,
	).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("failure events in the outbox = %d, error = %v, want 1", outboxCount, err)
	}

	readOld, err := store.Connection(ctx, "org_1", old.ID)
	if err != nil {
		t.Fatalf("Connection() error = %v", err)
	}
	if readOld.Status != domain.ConnectionFailed || readOld.StatusReason != "link expired" {
		t.Fatalf("Connection(old) = %+v, want failed/link expired", readOld)
	}

	readFresh, err := store.Connection(ctx, "org_1", fresh.ID)
	if err != nil {
		t.Fatalf("Connection() error = %v", err)
	}
	if readFresh.Status != domain.ConnectionPending {
		t.Fatalf("Connection(fresh) = %+v, want still pending", readFresh)
	}

	// Nothing is left to sweep, so nothing more is announced.
	count, err = store.FailPendingConnectionsBefore(ctx, cutoff, now, abandoned)
	if err != nil || count != 0 {
		t.Fatalf("FailPendingConnectionsBefore(again) = %d, %v, want 0", count, err)
	}
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM apps_outbox WHERE detail_type = 'apps.connection.failed.v1'`,
	).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("failure events after a second sweep = %d, error = %v, want still 1", outboxCount, err)
	}
}

func TestEventHandledAndDeleteHandledEventsBefore(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	handled, err := store.EventHandled(ctx, "evt_1")
	if err != nil || handled {
		t.Fatalf("EventHandled(before) = %v, %v, want false", handled, err)
	}
	if err := store.MarkEventHandled(ctx, "evt_1", "connections.connection.disconnected.v1", testClock); err != nil {
		t.Fatalf("MarkEventHandled() error = %v", err)
	}
	handled, err = store.EventHandled(ctx, "evt_1")
	if err != nil || !handled {
		t.Fatalf("EventHandled(after) = %v, %v, want true", handled, err)
	}
	// A redelivery racing another one marks the same event again; that must
	// be tolerated rather than treated as an error.
	if err := store.MarkEventHandled(ctx, "evt_1", "connections.connection.disconnected.v1", testClock); err != nil {
		t.Fatalf("MarkEventHandled(again) error = %v", err)
	}

	forgotten, err := store.DeleteHandledEventsBefore(ctx, testClock.Add(time.Hour))
	if err != nil || forgotten != 1 {
		t.Fatalf("DeleteHandledEventsBefore() = %d, %v, want 1", forgotten, err)
	}
	handled, err = store.EventHandled(ctx, "evt_1")
	if err != nil || handled {
		t.Fatalf("EventHandled(after forgetting) = %v, %v, want false", handled, err)
	}
}

func TestPolicyRefundPreservesLeaseAndCannotRefundLaterClaims(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	connection := activateForAutoSubscribe(t, store, "org_1", "member_1")
	now := testClock.Add(time.Minute)
	lease := now.Add(10 * time.Minute)
	first, err := store.ConnectionsAwaitingSubscribe(ctx, 3, 10, now, lease)
	if err != nil || len(first) != 1 {
		t.Fatalf("claim = %+v %v", first, err)
	}
	for range 2 {
		if err := store.RefundAutoSubscribeAttempt(ctx, connection.ID, first[0].AutoSubscribeAttempts, lease); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.Connection(ctx, "org_1", connection.ID)
	if err != nil || current.AutoSubscribeAttempts != 0 {
		t.Fatalf("refunded claim=%+v %v", current, err)
	}
	held, err := store.ConnectionsAwaitingSubscribe(ctx, 3, 10, now, lease)
	if err != nil || len(held) != 0 {
		t.Fatalf("policy backoff lost lease: %+v %v", held, err)
	}
	later := lease.Add(time.Second)
	nextLease := later.Add(10 * time.Minute)
	next, err := store.ConnectionsAwaitingSubscribe(ctx, 3, 10, later, nextLease)
	if err != nil || len(next) != 1 || next[0].AutoSubscribeAttempts != 1 {
		t.Fatalf("next claim=%+v %v", next, err)
	}
	if err := store.RefundAutoSubscribeAttempt(ctx, connection.ID, 1, lease); err != nil {
		t.Fatal(err)
	}
	current, err = store.Connection(ctx, "org_1", connection.ID)
	if err != nil || current.AutoSubscribeAttempts != 1 {
		t.Fatalf("stale refund changed next claim: %+v %v", current, err)
	}
}
