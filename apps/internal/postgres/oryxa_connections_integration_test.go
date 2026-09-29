package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
)

// oryxaConnection records a pending Oryxa connection, which carries no state
// hash: its sign-in's state lives in signin's flow.
func oryxaConnection(t *testing.T, store *Store, organizationID, memberID, toolkit string, createdAt time.Time) domain.Connection {
	t.Helper()
	connection := domain.Connection{
		ID: "apc_" + randomHex(8), OrganizationID: organizationID, MemberID: memberID, Toolkit: toolkit,
		Broker: domain.BrokerOryxa, Status: domain.ConnectionPending, AutoSubscribe: true,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := store.CreateConnection(context.Background(), connection, "", testEvent(t, "apps.connection.initiated.v1")); err != nil {
		t.Fatalf("CreateConnection() = %v", err)
	}
	return connection
}

func outboxCount(t *testing.T, store *Store, detailType string) int {
	t.Helper()
	var count int
	if err := store.pool.QueryRow(context.Background(), `SELECT count(*) FROM apps_outbox WHERE detail_type = $1`, detailType).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestOryxaConnectionsCarryTheirBrokerAndCredentialKind(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	composio, _ := testConnection(t, store, "org_1", "mem_1")
	// Two Oryxa sign-ins at once, neither with a state hash, do not collide.
	first := oryxaConnection(t, store, "org_1", "mem_2", "github", testClock)
	second := oryxaConnection(t, store, "org_1", "mem_3", "github", testClock)

	read, err := store.Connection(ctx, "org_1", composio.ID)
	if err != nil || read.Broker != domain.BrokerComposio || read.CredentialKind != "" {
		t.Fatalf("composio = %+v, %v", read, err)
	}
	read, err = store.Connection(ctx, "org_1", first.ID)
	if err != nil || !read.Oryxa() || read.CredentialKind != "" || read.ComposioAccountID != "" {
		t.Fatalf("oryxa = %+v, %v", read, err)
	}

	// Once signin keeps a credential, the connection says what kind.
	if _, err := store.ReplaceCredential(ctx, signin.Credential{
		ConnectionID: second.ID, OrganizationID: "org_1", Kind: integration.KindAPIToken,
		Settings: map[string]string{}, NextCheckAt: testClock.Add(24 * time.Hour), UpdatedAt: testClock,
	}); err != nil {
		t.Fatal(err)
	}
	activated, changed, err := store.ActivateConnection(ctx, second.ID, "octocat", testClock, testEvent(t, "apps.connection.connected.v1"))
	if err != nil || !changed || activated.CredentialKind != integration.KindAPIToken || !activated.Oryxa() {
		t.Fatalf("activated = %+v, %v, %v", activated, changed, err)
	}
	listed, err := store.Connections(ctx, "org_1", domain.ConnectionQuery{MemberID: "mem_3"})
	if err != nil || len(listed) != 1 || listed[0].CredentialKind != integration.KindAPIToken {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
}

func TestAutoSubscribeClaimsOnlyComposiosConnections(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	oryxa := oryxaConnection(t, store, "org_1", "mem_1", "github", testClock)
	if _, _, err := store.ActivateConnection(ctx, oryxa.ID, "octocat", testClock, testEvent(t, "apps.connection.connected.v1")); err != nil {
		t.Fatal(err)
	}
	composio := activateForAutoSubscribe(t, store, "org_1", "mem_2")

	claimed, err := store.ConnectionsAwaitingSubscribe(ctx, 3, 10, testClock.Add(time.Hour), testClock.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != composio.ID {
		t.Fatalf("claimed = %+v", claimed)
	}
}

func TestFailPendingOryxaConnectionsBeforeFailsOnlyAbandonedSignins(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	now := testClock.Add(time.Hour)
	abandoned := oryxaConnection(t, store, "org_1", "mem_1", "github", now.Add(-20*time.Minute))
	fresh := oryxaConnection(t, store, "org_1", "mem_2", "github", now.Add(-2*time.Minute))
	composio, _ := testConnection(t, store, "org_1", "mem_3") // pending since testClock, an hour ago

	var built []domain.Connection
	failed, err := store.FailPendingOryxaConnectionsBefore(ctx, now.Add(-15*time.Minute), now, func(connections []domain.Connection) ([]domain.Event, error) {
		built = connections
		events := make([]domain.Event, 0, len(connections))
		for range connections {
			events = append(events, testEvent(t, "apps.connection.failed.v1"))
		}
		return events, nil
	})
	if err != nil || failed != 1 || len(built) != 1 || built[0].ID != abandoned.ID {
		t.Fatalf("failed %d = %+v, %v", failed, built, err)
	}
	if built[0].Status != domain.ConnectionFailed || built[0].StatusReason != "link expired" {
		t.Fatalf("abandoned = %+v", built[0])
	}
	for id, want := range map[string]domain.ConnectionStatus{fresh.ID: domain.ConnectionPending, composio.ID: domain.ConnectionPending} {
		read, err := store.Connection(ctx, "org_1", id)
		if err != nil || read.Status != want {
			t.Fatalf("%s = %+v, %v", id, read, err)
		}
	}
	if count := outboxCount(t, store, "apps.connection.failed.v1"); count != 1 {
		t.Fatalf("failed events = %d", count)
	}
	// The member's slot is free again.
	oryxaConnection(t, store, "org_1", "mem_1", "github", now)
}

func TestRelabelConnectionOnlyChangesAnActiveOne(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	connection := oryxaConnection(t, store, "org_1", "mem_1", "github", testClock)

	if _, changed, err := store.RelabelConnection(ctx, connection.ID, "hubot", testClock, testEvent(t, "apps.credential.replaced.v1")); err != nil || changed {
		t.Fatalf("a pending one = %v, %v", changed, err)
	}
	if _, _, err := store.ActivateConnection(ctx, connection.ID, "octocat", testClock, testEvent(t, "apps.connection.connected.v1")); err != nil {
		t.Fatal(err)
	}
	relabelled, changed, err := store.RelabelConnection(ctx, connection.ID, "hubot", testClock.Add(time.Minute), testEvent(t, "apps.credential.replaced.v1"))
	if err != nil || !changed || relabelled.Label != "hubot" || !relabelled.UpdatedAt.Equal(testClock.Add(time.Minute)) {
		t.Fatalf("relabelled = %+v, %v, %v", relabelled, changed, err)
	}
	if count := outboxCount(t, store, "apps.credential.replaced.v1"); count != 1 {
		t.Fatalf("replaced events = %d, want only the one that changed something", count)
	}
}

func TestRecordEventsWritesThemToTheOutbox(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	if err := store.RecordEvents(context.Background()); err != nil {
		t.Fatalf("no events = %v", err)
	}
	if err := store.RecordEvents(context.Background(),
		testEvent(t, "apps.oauth_client.saved.v1"), testEvent(t, "apps.oauth_client.removed.v1")); err != nil {
		t.Fatal(err)
	}
	if saved, removed := outboxCount(t, store, "apps.oauth_client.saved.v1"), outboxCount(t, store, "apps.oauth_client.removed.v1"); saved != 1 || removed != 1 {
		t.Fatalf("saved %d, removed %d", saved, removed)
	}
}
