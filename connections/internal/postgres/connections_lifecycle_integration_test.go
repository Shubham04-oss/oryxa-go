package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

func TestUpdateConnection(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_update_connection"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)

	first := createConnection(t, store, app, "con_first", "user-1", "account-1", now)
	second := createConnection(t, store, app, "con_second", "user-1", "account-2", now)
	if !first.Default || second.Default {
		t.Fatalf("fixture defaults = first:%v second:%v", first.Default, second.Default)
	}

	label := "Work Gmail"
	updated, err := store.UpdateConnection(ctx, org, first.ID, domain.ConnectionChange{Label: &label}, now.Add(time.Minute))
	if err != nil || updated.Label != label || !updated.UpdatedAt.Equal(now.Add(time.Minute)) || updated.Version != first.Version || !updated.Default {
		t.Fatalf("UpdateConnection(label) = %+v, %v", updated, err)
	}

	empty := ""
	cleared, err := store.UpdateConnection(ctx, org, first.ID, domain.ConnectionChange{Label: &empty}, now.Add(90*time.Second))
	if err != nil || cleared.Label != "" {
		t.Fatalf("UpdateConnection(clear label) = %+v, %v", cleared, err)
	}

	swapped, err := store.UpdateConnection(ctx, org, second.ID, domain.ConnectionChange{MakeDefault: true}, now.Add(2*time.Minute))
	if err != nil || !swapped.Default || swapped.Version != second.Version {
		t.Fatalf("UpdateConnection(default) = %+v, %v", swapped, err)
	}
	if reloadedFirst, err := store.Connection(ctx, org, first.ID); err != nil || reloadedFirst.Default {
		t.Fatalf("first connection after default swap = %+v, %v", reloadedFirst, err)
	}

	// Making the current default the default again is a harmless no-op.
	again, err := store.UpdateConnection(ctx, org, second.ID, domain.ConnectionChange{MakeDefault: true}, now.Add(150*time.Second))
	if err != nil || !again.Default {
		t.Fatalf("UpdateConnection(already default) = %+v, %v", again, err)
	}

	if _, err := store.UpdateConnection(ctx, org, "con_missing", domain.ConnectionChange{MakeDefault: true}, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateConnection(missing) error = %v", err)
	}

	disconnect(t, store, org, first.ID, domain.DisconnectRequested, now.Add(3*time.Minute))
	if _, err := store.UpdateConnection(ctx, org, first.ID, domain.ConnectionChange{MakeDefault: true}, now.Add(4*time.Minute)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("UpdateConnection(disconnected) error = %v", err)
	}
}

// TestUpdateConnectionDefaultSwapIsSerialized makes many connections of the
// same owner race to become the default at once. The per-owner advisory
// lock must serialize them: none may fail, and connections_default_unique
// must end up satisfied by exactly one row.
func TestUpdateConnectionDefaultSwapIsSerialized(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_default_swap"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)

	const n = 8
	ids := make([]string, n)
	for i := range n {
		id := fmt.Sprintf("con_%d", i)
		ids[i] = id
		createConnection(t, store, app, id, "user-1", fmt.Sprintf("account-%d", i), now)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			_, err := store.UpdateConnection(ctx, org, id, domain.ConnectionChange{MakeDefault: true}, now.Add(time.Duration(i+1)*time.Second))
			errs[i] = err
		}(i, id)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("UpdateConnection() goroutine %d error = %v", i, err)
		}
	}

	connections, err := store.Connections(ctx, org, domain.ConnectionQuery{MemberID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	for _, connection := range connections {
		if connection.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly one default connection after the race, got %d among %d", defaults, len(connections))
	}
}

func TestDisconnectConnections(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_disconnect_all"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)

	memberConn := createConnection(t, store, app, "con_member", "user-1", "account-1", now)
	teamConn := createScopedConnection(t, store, app, "con_team", domain.ScopeTeam, "team-1", "", "account-2", now)
	orgConn := createScopedConnection(t, store, app, "con_org", domain.ScopeOrganization, "", "", "account-3", now)
	otherMember := createConnection(t, store, app, "con_other_member", "user-2", "account-4", now)

	events := 0
	event := func(domain.Connection) (domain.Event, error) {
		events++
		return testEvent("connections.connection.disconnected.v1", now), nil
	}

	if _, err := store.DisconnectConnections(ctx, org, domain.OwnerFilter{MemberID: "user-1", TeamID: "team-1"}, domain.DisconnectMemberLeft, now, event); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("DisconnectConnections(both member and team) error = %v", err)
	}
	if _, err := store.DisconnectConnections(ctx, org, domain.OwnerFilter{MemberID: "user-1"}, "bogus", now, event); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("DisconnectConnections(invalid reason) error = %v", err)
	}
	if events != 0 {
		t.Fatalf("rejected calls should not record events, got %d", events)
	}

	count, err := store.DisconnectConnections(ctx, org, domain.OwnerFilter{MemberID: "user-1"}, domain.DisconnectMemberLeft, now.Add(time.Minute), event)
	if err != nil || count != 1 || events != 1 {
		t.Fatalf("DisconnectConnections(member) = %d, %v (events=%d)", count, err, events)
	}
	if reloaded, err := store.Connection(ctx, org, memberConn.ID); err != nil || reloaded.Status != domain.StatusDisconnected || reloaded.Reason != string(domain.DisconnectMemberLeft) {
		t.Fatalf("member connection after disconnect = %+v %v", reloaded, err)
	}
	if reloaded, err := store.Connection(ctx, org, otherMember.ID); err != nil || reloaded.Status != domain.StatusActive {
		t.Fatalf("other member's connection was touched: %+v %v", reloaded, err)
	}
	if count, err := store.DisconnectConnections(ctx, org, domain.OwnerFilter{MemberID: "user-1"}, domain.DisconnectMemberLeft, now.Add(2*time.Minute), event); err != nil || count != 0 {
		t.Fatalf("second DisconnectConnections(member) = %d, %v, want 0 (idempotent)", count, err)
	}

	events = 0
	count, err = store.DisconnectConnections(ctx, org, domain.OwnerFilter{TeamID: "team-1"}, domain.DisconnectTeamDeleted, now.Add(3*time.Minute), event)
	if err != nil || count != 1 || events != 1 {
		t.Fatalf("DisconnectConnections(team) = %d, %v (events=%d)", count, err, events)
	}
	if reloaded, err := store.Connection(ctx, org, teamConn.ID); err != nil || reloaded.Status != domain.StatusDisconnected || reloaded.Reason != string(domain.DisconnectTeamDeleted) {
		t.Fatalf("team connection after disconnect = %+v %v", reloaded, err)
	}

	events = 0
	count, err = store.DisconnectConnections(ctx, org, domain.OwnerFilter{}, domain.DisconnectOrganizationDeleted, now.Add(4*time.Minute), event)
	if err != nil || count != 2 || events != 2 {
		t.Fatalf("DisconnectConnections(organization) = %d, %v (events=%d), want 2 (org + other member)", count, err, events)
	}
	if reloaded, err := store.Connection(ctx, org, orgConn.ID); err != nil || reloaded.Status != domain.StatusDisconnected {
		t.Fatalf("organization connection after disconnect = %+v %v", reloaded, err)
	}
	if reloaded, err := store.Connection(ctx, org, otherMember.ID); err != nil || reloaded.Status != domain.StatusDisconnected {
		t.Fatalf("other member connection after org-wide disconnect = %+v %v", reloaded, err)
	}
	if count, err := store.DisconnectConnections(ctx, org, domain.OwnerFilter{}, domain.DisconnectOrganizationDeleted, now.Add(5*time.Minute), event); err != nil || count != 0 {
		t.Fatalf("second DisconnectConnections(organization) = %d, %v, want 0 (idempotent)", count, err)
	}
}

func TestMarkNeedsReconnect(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_needs_reconnect"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	connection := createConnection(t, store, app, "con_a", "user-1", "account-1", now)

	if ok, err := store.AcquireLease(ctx, org, connection.ID, connection.Version, "lease-1", now, now.Add(10*time.Minute)); err != nil || !ok {
		t.Fatalf("AcquireLease() = %v, %v", ok, err)
	}

	events := 0
	event := func(c domain.Connection) (domain.Event, error) {
		events++
		if c.Status != domain.StatusNeedsReconnect || c.Reason != string(domain.ReconnectUnauthorized) {
			t.Errorf("connection marked as %s/%s", c.Status, c.Reason)
		}
		return testEvent("connections.connection.needs-reconnect.v1", now), nil
	}
	// A failure seen at an older version does not move the connection.
	changed, err := store.MarkNeedsReconnect(ctx, org, connection.ID, connection.Version-1, domain.ReconnectUnauthorized, now.Add(time.Minute), event)
	if err != nil || changed || events != 0 {
		t.Fatalf("MarkNeedsReconnect(stale version) = %v, %v (events=%d)", changed, err, events)
	}
	changed, err = store.MarkNeedsReconnect(ctx, org, connection.ID, connection.Version, domain.ReconnectUnauthorized, now.Add(time.Minute), event)
	if err != nil || !changed || events != 1 {
		t.Fatalf("MarkNeedsReconnect() = %v, %v (events=%d)", changed, err, events)
	}

	// An already needs_reconnect connection does not move again.
	changed, err = store.MarkNeedsReconnect(ctx, org, connection.ID, connection.Version+1, domain.ReconnectUnauthorized, now.Add(2*time.Minute), event)
	if err != nil || changed || events != 1 {
		t.Fatalf("second MarkNeedsReconnect() = %v, %v (events=%d)", changed, err, events)
	}

	reloaded, err := store.Connection(ctx, org, connection.ID)
	if err != nil || reloaded.Status != domain.StatusNeedsReconnect || reloaded.Version != connection.Version+1 {
		t.Fatalf("connection after MarkNeedsReconnect() = %+v %v", reloaded, err)
	}

	// The lease was cleared: acquiring at the new version succeeds well
	// before the original lease-until, proving it was released rather than
	// merely expired.
	acquired, err := store.AcquireLease(ctx, org, connection.ID, reloaded.Version, "lease-2", now.Add(3*time.Minute), now.Add(4*time.Minute))
	if err != nil || !acquired {
		t.Fatalf("AcquireLease() after MarkNeedsReconnect = %v, %v", acquired, err)
	}

	if _, err := store.MarkNeedsReconnect(ctx, org, connection.ID, connection.Version, "bogus", now, event); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("MarkNeedsReconnect(invalid reason) error = %v", err)
	}
}

func TestMarkUsedThrottles(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_mark_used"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	connection := createConnection(t, store, app, "con_a", "user-1", "account-1", now)
	if connection.LastUsedAt != nil {
		t.Fatalf("fresh connection LastUsedAt = %v, want nil", connection.LastUsedAt)
	}

	if err := store.MarkUsed(ctx, org, connection.ID, now); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Connection(ctx, org, connection.ID)
	if err != nil || reloaded.LastUsedAt == nil || !reloaded.LastUsedAt.Equal(now) {
		t.Fatalf("connection after first MarkUsed() = %+v %v", reloaded, err)
	}

	if err := store.MarkUsed(ctx, org, connection.ID, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if reloaded, err := store.Connection(ctx, org, connection.ID); err != nil || !reloaded.LastUsedAt.Equal(now) {
		t.Fatalf("connection after throttled MarkUsed() = %+v %v, want unchanged at %v", reloaded, err, now)
	}

	if err := store.MarkUsed(ctx, org, connection.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if reloaded, err := store.Connection(ctx, org, connection.ID); err != nil || !reloaded.LastUsedAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("connection after later MarkUsed() = %+v %v", reloaded, err)
	}

	if err := store.MarkUsed(ctx, org, "con_missing", now); err != nil {
		t.Fatalf("MarkUsed(missing connection) error = %v, want nil", err)
	}
}

func TestReportConnection(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	org := "org_report_connection"
	app := createApp(t, store, org, "app_google", domain.CredentialOAuth2, now)
	connection := createConnection(t, store, app, "con_a", "user-1", "account-1", now)
	if connection.LastReportedAt != nil {
		t.Fatalf("fresh connection LastReportedAt = %v, want nil", connection.LastReportedAt)
	}

	reportedAt := now.Add(time.Hour)
	if err := store.ReportConnection(ctx, org, connection.ID, reportedAt); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.Connection(ctx, org, connection.ID)
	if err != nil || reloaded.LastReportedAt == nil || !reloaded.LastReportedAt.Equal(reportedAt) {
		t.Fatalf("connection after ReportConnection() = %+v %v", reloaded, err)
	}

	// The health check, originally due 24h out, was brought forward to
	// reportedAt: it is claimable exactly there.
	checks, err := store.ClaimHealthChecks(ctx, reportedAt, 10, "claim-report", reportedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].ConnectionID != connection.ID {
		t.Fatalf("ClaimHealthChecks() after ReportConnection = %+v", checks)
	}

	if err := store.ReportConnection(ctx, org, "con_missing", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("ReportConnection(missing) error = %v", err)
	}
}
