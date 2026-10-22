package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

func nativeRow(id string, status domain.ConnectionStatus) domain.Connection {
	return domain.Connection{
		ID: "apc_" + randomHex(8), OrganizationID: "org_1", MemberID: "mem_1", Toolkit: "github",
		Broker: domain.BrokerNative, NativeConnectionID: id, Status: status,
	}
}

// nativeStatusOf reads the one row a native connection id names, and how many
// there are.
func nativeStatusOf(t *testing.T, store *Store, id string) (domain.ConnectionStatus, int) {
	t.Helper()
	var status string
	var count int
	if err := store.pool.QueryRow(context.Background(),
		`SELECT count(*), COALESCE(max(status), '') FROM apps_connections WHERE native_connection_id = $1`, id).Scan(&count, &status); err != nil {
		t.Fatal(err)
	}
	return domain.ConnectionStatus(status), count
}

func TestNativeConnectionsAreRecordedOnceAndFindable(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	at := testClock
	for range 2 { // a duplicate delivery is a no-op
		if err := store.ApplyNativeConnection(ctx, nativeRow("icn_1", domain.ConnectionActive), at); err != nil {
			t.Fatal(err)
		}
	}
	if status, count := nativeStatusOf(t, store, "icn_1"); status != domain.ConnectionActive || count != 1 {
		t.Fatalf("status = %s, rows = %d", status, count)
	}
	listed, err := store.Connections(ctx, "org_1", domain.ConnectionQuery{MemberID: "mem_1", Toolkit: "github"})
	if err != nil || len(listed) != 1 || !listed[0].Native() || listed[0].NativeConnectionID != "icn_1" || listed[0].ConnectedAt == nil {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
}

func TestNativeConnectionUpdatesApplyOnlyInOrder(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.ApplyNativeConnection(ctx, nativeRow("icn_1", domain.ConnectionActive), testClock); err != nil {
		t.Fatal(err)
	}
	// needs_reauth at t+2 arrives before the recovery at t+1: the older one is ignored.
	if err := store.ApplyNativeConnection(ctx, nativeRow("icn_1", domain.ConnectionExpired), testClock.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyNativeConnection(ctx, nativeRow("icn_1", domain.ConnectionActive), testClock.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if status, _ := nativeStatusOf(t, store, "icn_1"); status != domain.ConnectionExpired {
		t.Fatalf("status = %s, want expired: an older update was applied", status)
	}
	// The same timestamp again changes nothing either.
	if err := store.ApplyNativeConnection(ctx, nativeRow("icn_1", domain.ConnectionActive), testClock.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if status, _ := nativeStatusOf(t, store, "icn_1"); status != domain.ConnectionExpired {
		t.Fatalf("status = %s, want expired", status)
	}
	if err := store.ApplyNativeConnection(ctx, nativeRow("icn_1", domain.ConnectionActive), testClock.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if status, _ := nativeStatusOf(t, store, "icn_1"); status != domain.ConnectionActive {
		t.Fatalf("status = %s, want a newer update applied", status)
	}
}

func TestARemovedNativeConnectionIsNeverBroughtBack(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.ApplyNativeConnection(ctx, nativeRow("icn_1", domain.ConnectionActive), testClock); err != nil {
		t.Fatal(err)
	}
	// Removed applies whatever its time, and nothing after it does.
	if err := store.ApplyNativeConnection(ctx, nativeRow("icn_1", domain.ConnectionRemoved), testClock.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.ConnectionStatus{domain.ConnectionActive, domain.ConnectionExpired} {
		if err := store.ApplyNativeConnection(ctx, nativeRow("icn_1", status), testClock.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if status, count := nativeStatusOf(t, store, "icn_1"); status != domain.ConnectionRemoved || count != 1 {
		t.Fatalf("status = %s, rows = %d, want one removed row", status, count)
	}

	// Removed first, created after: the tombstone keeps it removed, and the
	// member may connect again under a new connection id.
	if err := store.ApplyNativeConnection(ctx, nativeRow("icn_2", domain.ConnectionRemoved), testClock); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyNativeConnection(ctx, nativeRow("icn_2", domain.ConnectionActive), testClock.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if status, count := nativeStatusOf(t, store, "icn_2"); status != domain.ConnectionRemoved || count != 1 {
		t.Fatalf("status = %s, rows = %d, want one removed row", status, count)
	}
	if err := store.ApplyNativeConnection(ctx, nativeRow("icn_3", domain.ConnectionActive), testClock.Add(2*time.Minute)); err != nil {
		t.Fatalf("a new connection after a removed one: %v", err)
	}
}
