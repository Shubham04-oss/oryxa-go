package application

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

func TestMemberRemovedTakesTheirConnectionsWithThem(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	theirs := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.activeTrigger("apt_1", theirs.ID, "ti_1")
	kept := h.activeConnection("apc_2", "user-2", "github", "ca_2")

	err := h.service.MemberRemoved(context.Background(), authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.store.connections[theirs.ID]; got.Status != domain.ConnectionRemoved || got.StatusReason != appsv1.RemovedMemberLeft {
		t.Fatalf("connection = %+v", got)
	}
	if got := h.store.triggers["apt_1"]; got.Status != domain.TriggerRemoved {
		t.Fatalf("trigger = %+v", got)
	}
	// The credential is revoked at the app, not merely forgotten here.
	if !slices.Contains(h.composio.deleted, "ca_1") || !h.composio.revoked[0] {
		t.Fatalf("deleted = %v, revoked = %v", h.composio.deleted, h.composio.revoked)
	}
	if slices.Contains(h.composio.deleted, "ca_2") {
		t.Fatal("another member's account was deleted")
	}
	if got := h.store.connections[kept.ID]; got.Status != domain.ConnectionActive {
		t.Fatalf("another member's connection = %q", got.Status)
	}
}

func TestOrganizationDeletedTakesEveryConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.activeConnection("apc_2", "user-2", "slack", "ca_2")
	elsewhere := h.activeConnection("apc_3", "user-9", "github", "ca_3")
	elsewhere.OrganizationID = "org_2"
	h.store.connections[elsewhere.ID] = elsewhere

	err := h.service.OrganizationDeleted(context.Background(), authv1.OrganizationDeleted{OrganizationID: "org_1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"apc_1", "apc_2"} {
		if got := h.store.connections[id]; got.Status != domain.ConnectionRemoved || got.StatusReason != appsv1.RemovedOrganizationDeleted {
			t.Fatalf("%s = %+v", id, got)
		}
	}
	if got := h.store.connections[elsewhere.ID]; got.Status != domain.ConnectionActive {
		t.Fatalf("another organization's connection = %q", got.Status)
	}
	if len(h.store.published(appsv1.ConnectionRemovedDetailType)) != 2 {
		t.Fatalf("removed events = %d", len(h.store.published(appsv1.ConnectionRemovedDetailType)))
	}
}

func TestMemberRemovedKeepsGoingWhenOneConnectionCannotBeDeleted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.activeConnection("apc_2", "user-1", "slack", "ca_2")
	h.composio.deleteErr = composio.ErrUnavailable

	err := h.service.MemberRemoved(context.Background(), authv1.MemberRemoved{OrganizationID: "org_1", UserID: "user-1"})
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
	// Both were tried, so the retry has less left to do, and neither is
	// recorded removed while its credential still lives.
	if len(h.composio.deleted) != 2 {
		t.Fatalf("deleted = %v", h.composio.deleted)
	}
	for _, id := range []string{"apc_1", "apc_2"} {
		if got := h.store.connections[id]; got.Status != domain.ConnectionActive {
			t.Fatalf("%s = %q, want active", id, got.Status)
		}
	}
}

func TestHandledEventsAreRememberedOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	handled, err := h.service.AlreadyHandled(context.Background(), "evt_1")
	if err != nil || handled {
		t.Fatalf("already handled = %v, %v", handled, err)
	}
	if err := h.service.MarkHandled(context.Background(), "evt_1", authv1.MemberRemovedDetailType); err != nil {
		t.Fatal(err)
	}
	if handled, err = h.service.AlreadyHandled(context.Background(), "evt_1"); err != nil || !handled {
		t.Fatalf("already handled = %v, %v", handled, err)
	}
}

func TestMaintenanceFailsConnectionsNobodyCameBackFrom(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	fresh, _ := h.pending(t, "github")
	stale, staleState := h.pending(t, "slack")
	old := h.store.connections[stale.ID]
	old.CreatedAt = testClock.Add(-2 * pendingLinkLifetime)
	h.store.connections[stale.ID] = old

	h.service.maintain(context.Background())

	if got := h.store.connections[stale.ID]; got.Status != domain.ConnectionFailed {
		t.Fatalf("abandoned connection = %q", got.Status)
	}
	if got := h.store.connections[fresh.ID]; got.Status != domain.ConnectionPending {
		t.Fatalf("recent connection = %q", got.Status)
	}
	// Its state is spent too, so a late callback cannot revive it.
	if _, err := h.service.CompleteConnection(context.Background(), ConnectCallback{State: staleState, CookieState: staleState, Status: "success"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("late callback = %v, want not found", err)
	}
}

func TestRunMaintenanceStopsWithItsContext(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.service.RunMaintenance(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance did not stop")
	}
}
