package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestStopSubscriptionsInvalidSelector(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	if _, err := store.StopSubscriptions(ctx, domain.SubscriptionSelector{}, "reason", "user_admin", now, neverCalled(t)); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("StopSubscriptions() with no organization error = %v, want ErrInvalid", err)
	}
}

func TestStopSubscriptionsSelectorVariants(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_stop_selector"

	create := func(id, connectionID, createdBy, scope, teamID string) domain.Subscription {
		t.Helper()
		sub := newSubscription(org, id, connectionID, "app_1", createdBy, domain.SubscriptionPending, now)
		if scope != "" {
			sub.Scope, sub.TeamID = scope, teamID
		}
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription(%s) error = %v", id, err)
		}
		return sub
	}

	byID := create("sub_sel_by_id", "con_shared", "user_x", "", "")
	byConn1 := create("sub_sel_by_conn_1", "con_target", "user_y", "", "")
	byConn2 := create("sub_sel_by_conn_2", "con_target", "user_z", "", "")
	byCreator := create("sub_sel_by_creator", "con_other1", "user_target", "", "")
	byCreatorTeamMatch := create("sub_sel_by_creator_team", "con_other2", "user_team", "team", "team_target")
	byCreatorTeamOther := create("sub_sel_by_creator_other_team", "con_other3", "user_team", "team", "team_other")
	decoy := create("sub_sel_decoy", "con_decoy", "user_decoy", "", "")

	assertStopped := func(t *testing.T, selector domain.SubscriptionSelector, reason string, want ...string) {
		t.Helper()
		var events []domain.Subscription
		callback := func(subscription domain.Subscription) (domain.Event, error) {
			events = append(events, subscription)
			return testEvent("integrations.subscription.stopped.v1", now), nil
		}
		stopped, err := store.StopSubscriptions(ctx, selector, reason, "user_admin", now, callback)
		if err != nil {
			t.Fatalf("StopSubscriptions(%+v) error = %v", selector, err)
		}
		if !slices.Equal(idsOf(stopped), want) {
			t.Fatalf("StopSubscriptions(%+v) = %v, want %v", selector, idsOf(stopped), want)
		}
		if !slices.Equal(idsOf(events), want) {
			t.Fatalf("StopSubscriptions(%+v) events = %v, want %v", selector, idsOf(events), want)
		}
	}

	assertStopped(t, domain.SubscriptionSelector{OrganizationID: org, SubscriptionID: byID.ID}, "by_id", byID.ID)
	assertStopped(t, domain.SubscriptionSelector{OrganizationID: org, ConnectionID: "con_target"}, "by_connection", byConn1.ID, byConn2.ID)
	assertStopped(t, domain.SubscriptionSelector{OrganizationID: org, CreatedBy: "user_target"}, "by_creator", byCreator.ID)
	assertStopped(t, domain.SubscriptionSelector{OrganizationID: org, CreatedBy: "user_team", TeamID: "team_target"}, "by_creator_team", byCreatorTeamMatch.ID)

	// Nothing else was touched: the other team, and the decoy, are still live.
	if got, err := store.Subscription(ctx, org, byCreatorTeamOther.ID); err != nil || got.Status != domain.SubscriptionPending {
		t.Fatalf("Subscription(other team) = %+v, %v, want it to stay pending", got, err)
	}
	if got, err := store.Subscription(ctx, org, decoy.ID); err != nil || got.Status != domain.SubscriptionPending {
		t.Fatalf("Subscription(decoy) = %+v, %v, want it to stay pending", got, err)
	}
}

func TestStopSubscriptionsFieldsRecorded(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_stop_fields"

	// registered has a registration and a scheduled renewal: stopping it must
	// clear renew_at and queue a cleanup.
	registered := newSubscription(org, "sub_stop_registered", "con_stop_fields", "app_1", "user_1", domain.SubscriptionPending, now)
	if err := store.CreateSubscription(ctx, registered, nil); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	expiresAt, renewAt := now.Add(48*time.Hour), now.Add(24*time.Hour)
	activated, err := store.ActivateSubscription(ctx, org, registered.ID, "reg_stop", nil, &expiresAt, &renewAt, now,
		eventFor("integrations.subscription.activated.v1", now))
	if err != nil {
		t.Fatalf("ActivateSubscription() error = %v", err)
	}
	if activated.RenewAt == nil {
		t.Fatalf("ActivateSubscription() RenewAt = nil, want it set so stopping can be shown to clear it")
	}

	// neverRegistered has no registration: stopping it must not queue a cleanup.
	neverRegistered := newSubscription(org, "sub_stop_unregistered", "con_stop_fields", "app_1", "user_1", domain.SubscriptionPending, now)
	if err := store.CreateSubscription(ctx, neverRegistered, nil); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}

	stopAt := now.Add(time.Hour)
	stopped, err := store.StopSubscriptions(ctx, domain.SubscriptionSelector{OrganizationID: org, ConnectionID: "con_stop_fields"},
		"team_member_left", "user_admin", stopAt, eventFor("integrations.subscription.stopped.v1", stopAt))
	if err != nil {
		t.Fatalf("StopSubscriptions() error = %v", err)
	}
	if len(stopped) != 2 {
		t.Fatalf("StopSubscriptions() stopped %d subscriptions, want 2", len(stopped))
	}
	byID := map[string]domain.Subscription{}
	for _, subscription := range stopped {
		byID[subscription.ID] = subscription
	}

	withRegistration := byID[activated.ID]
	if withRegistration.Status != domain.SubscriptionStopped || withRegistration.StoppedBy != "user_admin" ||
		withRegistration.Reason != "team_member_left" || withRegistration.StoppedAt == nil || !withRegistration.StoppedAt.Equal(stopAt) {
		t.Fatalf("stopped(with registration) = %+v", withRegistration)
	}
	if withRegistration.RenewAt != nil {
		t.Fatalf("stopped(with registration).RenewAt = %v, want nil", withRegistration.RenewAt)
	}
	if withRegistration.CleanupAt == nil {
		t.Fatalf("stopped(with registration).CleanupAt = nil, want it set since registration_id is non-empty")
	}

	withoutRegistration := byID[neverRegistered.ID]
	if withoutRegistration.Status != domain.SubscriptionStopped || withoutRegistration.StoppedBy != "user_admin" ||
		withoutRegistration.Reason != "team_member_left" || withoutRegistration.StoppedAt == nil || !withoutRegistration.StoppedAt.Equal(stopAt) {
		t.Fatalf("stopped(without registration) = %+v", withoutRegistration)
	}
	if withoutRegistration.CleanupAt != nil {
		t.Fatalf("stopped(without registration).CleanupAt = %v, want nil since it never registered", withoutRegistration.CleanupAt)
	}

	due, err := store.ClaimCleanups(ctx, stopAt.Add(time.Second), 10, "lease_stop_fields", stopAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimCleanups() error = %v", err)
	}
	if want := []string{activated.ID}; !slices.Equal(idsOf(due), want) {
		t.Fatalf("ClaimCleanups() = %v, want %v", idsOf(due), want)
	}
}

func TestStopSubscriptionsLeavesEndedSubscriptionsAlone(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_stop_ended"
	connection := "con_stop_ended"

	alreadyStopped := newSubscription(org, "sub_already_stopped", connection, "app_1", "user_1", domain.SubscriptionStopped, now)
	if err := store.CreateSubscription(ctx, alreadyStopped, nil); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	alreadyFailed := newSubscription(org, "sub_already_failed", connection, "app_1", "user_1", domain.SubscriptionFailed, now)
	if err := store.CreateSubscription(ctx, alreadyFailed, nil); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	live := newSubscription(org, "sub_stop_ended_live", connection, "app_1", "user_1", domain.SubscriptionPending, now)
	if err := store.CreateSubscription(ctx, live, nil); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}

	var events []domain.Subscription
	callback := func(subscription domain.Subscription) (domain.Event, error) {
		events = append(events, subscription)
		return testEvent("integrations.subscription.stopped.v1", now), nil
	}
	stopped, err := store.StopSubscriptions(ctx, domain.SubscriptionSelector{OrganizationID: org, ConnectionID: connection},
		"disconnected", "user_admin", now, callback)
	if err != nil {
		t.Fatalf("StopSubscriptions() error = %v", err)
	}
	if want := []string{live.ID}; !slices.Equal(idsOf(stopped), want) {
		t.Fatalf("StopSubscriptions() = %v, want only the live one %v", idsOf(stopped), want)
	}
	if want := []string{live.ID}; !slices.Equal(idsOf(events), want) {
		t.Fatalf("StopSubscriptions() events = %v, want only the live one %v", idsOf(events), want)
	}

	if got, err := store.Subscription(ctx, org, alreadyStopped.ID); err != nil || got.Status != domain.SubscriptionStopped || got.Reason != "" {
		t.Fatalf("Subscription(already stopped) = %+v, %v, want it untouched", got, err)
	}
	if got, err := store.Subscription(ctx, org, alreadyFailed.ID); err != nil || got.Status != domain.SubscriptionFailed || got.Reason != "" {
		t.Fatalf("Subscription(already failed) = %+v, %v, want it untouched", got, err)
	}
}

func TestFailSubscription(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_fail"

	activateWithRegistration := func(id, connectionID string) domain.Subscription {
		t.Helper()
		sub := newSubscription(org, id, connectionID, "app_1", "user_1", domain.SubscriptionPending, now)
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription(%s) error = %v", id, err)
		}
		activated, err := store.ActivateSubscription(ctx, org, id, "reg_"+id, nil, nil, nil, now, eventFor("integrations.subscription.activated.v1", now))
		if err != nil {
			t.Fatalf("ActivateSubscription(%s) error = %v", id, err)
		}
		return activated
	}

	t.Run("marks failed with reason and event, cleanup true queues cleanup", func(t *testing.T) {
		sub := activateWithRegistration("sub_fail_cleanup", "con_1")
		var seen domain.Subscription
		var failedEvent domain.Event
		callback := func(subscription domain.Subscription) (domain.Event, error) {
			seen = subscription
			failedEvent = testEvent("integrations.subscription.failed.v1", now.Add(time.Minute))
			return failedEvent, nil
		}
		failed, err := store.FailSubscription(ctx, org, sub.ID, "", "provider_removed", true, now.Add(time.Minute), callback)
		if err != nil {
			t.Fatalf("FailSubscription() error = %v", err)
		}
		if failed.Status != domain.SubscriptionFailed || failed.Reason != "provider_removed" {
			t.Fatalf("FailSubscription() = %+v", failed)
		}
		if seen.ID != sub.ID || seen.Reason != "provider_removed" {
			t.Fatalf("eventFor received = %+v", seen)
		}
		assertOutboxHas(t, ctx, store, failedEvent.ID, true)
		due, err := store.ClaimCleanups(ctx, now.Add(2*time.Minute), 10, "lease_fail_1", now.Add(3*time.Minute))
		if err != nil {
			t.Fatalf("ClaimCleanups() error = %v", err)
		}
		if want := []string{sub.ID}; !slices.Equal(idsOf(due), want) {
			t.Fatalf("ClaimCleanups() = %v, want %v", idsOf(due), want)
		}
	})

	t.Run("cleanup false does not queue cleanup", func(t *testing.T) {
		sub := activateWithRegistration("sub_fail_no_cleanup", "con_2")
		failed, err := store.FailSubscription(ctx, org, sub.ID, "", "provider_rejected", false, now.Add(time.Minute),
			eventFor("integrations.subscription.failed.v1", now.Add(time.Minute)))
		if err != nil {
			t.Fatalf("FailSubscription() error = %v", err)
		}
		if failed.CleanupAt != nil {
			t.Fatalf("FailSubscription() with cleanup=false CleanupAt = %v, want nil", failed.CleanupAt)
		}
	})

	t.Run("wrong lease token is not found and changes nothing", func(t *testing.T) {
		sub := activateWithRegistration("sub_fail_lease", "con_3")
		lease := "correct-lease"
		if err := store.ScheduleRenewal(ctx, sub.ID, now); err != nil {
			t.Fatalf("ScheduleRenewal() error = %v", err)
		}
		leased, err := store.ClaimRenewals(ctx, now.Add(time.Second), 10, lease, now.Add(time.Hour))
		if err != nil || len(leased) != 1 {
			t.Fatalf("ClaimRenewals() = %+v, %v", leased, err)
		}
		if _, err := store.FailSubscription(ctx, org, sub.ID, "wrong-lease", "provider_removed", true, now.Add(time.Minute), neverCalled(t)); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("FailSubscription() with the wrong lease error = %v, want ErrNotFound", err)
		}
		reloaded, err := store.Subscription(ctx, org, sub.ID)
		if err != nil {
			t.Fatalf("Subscription() error = %v", err)
		}
		if reloaded.Status != domain.SubscriptionActive || reloaded.Version != leased[0].Version {
			t.Fatalf("Subscription() after a failed FailSubscription attempt = %+v, want it unchanged from %+v", reloaded, leased[0])
		}
	})

	t.Run("a stopped subscription cannot be failed", func(t *testing.T) {
		sub := newSubscription(org, "sub_fail_stopped", "con_4", "app_1", "user_1", domain.SubscriptionPending, now)
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}
		if _, err := store.StopSubscriptions(ctx, domain.SubscriptionSelector{OrganizationID: org, SubscriptionID: sub.ID},
			"stopped", "user_admin", now, eventFor("integrations.subscription.stopped.v1", now)); err != nil {
			t.Fatalf("StopSubscriptions() error = %v", err)
		}
		if _, err := store.FailSubscription(ctx, org, sub.ID, "", "provider_removed", true, now.Add(time.Minute), neverCalled(t)); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("FailSubscription() on a stopped subscription error = %v, want ErrNotFound", err)
		}
	})
}
