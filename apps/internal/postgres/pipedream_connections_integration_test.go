package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

func pipedreamConnection(t *testing.T, store *Store, memberID string) domain.Connection {
	t.Helper()
	connection := domain.Connection{
		ID: "apc_" + randomHex(8), OrganizationID: "org_1", MemberID: memberID, Toolkit: "linear",
		Broker: domain.BrokerPipedream, ComposioUserID: domain.ComposioUserID("org_1", memberID),
		Status: domain.ConnectionPending, CreatedAt: testClock, UpdatedAt: testClock,
	}
	if err := store.CreateConnection(context.Background(), connection, randomHex(16), testEvent(t, "apps.connection.initiated.v1")); err != nil {
		t.Fatalf("CreateConnection() = %v", err)
	}
	return connection
}

// A Pipedream connection activates with the account it was read back as, one
// account is never two connections', and the health sweep pages through
// exactly the active ones that have an account.
func TestPipedreamConnectionsRecordTheirAccount(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	first := pipedreamConnection(t, store, "mem_1")
	second := pipedreamConnection(t, store, "mem_2")
	pending := pipedreamConnection(t, store, "mem_3")
	composio, _ := testConnection(t, store, "org_1", "mem_4")

	activated, changed, err := store.ActivatePipedreamConnection(ctx, first.ID, "apn_1", "ada@acme.test", testClock.Add(time.Minute),
		testEvent(t, "apps.connection.connected.v1"))
	if err != nil || !changed || activated.Status != domain.ConnectionActive || activated.PipedreamAccountID != "apn_1" ||
		activated.Label != "ada@acme.test" || !activated.Pipedream() {
		t.Fatalf("ActivatePipedreamConnection() = %+v, %v, %v", activated, changed, err)
	}
	if _, _, err := store.ActivatePipedreamConnection(ctx, second.ID, "apn_1", "", testClock, testEvent(t, "apps.connection.connected.v1")); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second connection for one account = %v, want conflict", err)
	}
	if _, changed, err := store.ActivatePipedreamConnection(ctx, composio.ID, "apn_9", "", testClock, testEvent(t, "apps.connection.connected.v1")); err != nil || changed {
		t.Fatalf("a Composio connection activated as Pipedream's: %v, %v", changed, err)
	}
	if _, _, err := store.ActivatePipedreamConnection(ctx, second.ID, "apn_2", "", testClock, testEvent(t, "apps.connection.connected.v1")); err != nil {
		t.Fatal(err)
	}

	page, err := store.PipedreamConnections(ctx, "", 1)
	if err != nil || len(page) != 1 {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	rest, err := store.PipedreamConnections(ctx, page[0].ID, 10)
	if err != nil || len(rest) != 1 || rest[0].ID == page[0].ID {
		t.Fatalf("second page = %+v, %v", rest, err)
	}
	for _, listed := range append(page, rest...) {
		if listed.ID == pending.ID || listed.ID == composio.ID || listed.PipedreamAccountID == "" {
			t.Fatalf("listed %+v", listed)
		}
	}
}
