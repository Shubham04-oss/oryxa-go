package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func TestScheduleRenewal(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_schedule_renewal"

	activate := func(id string, renewAt *time.Time) domain.Subscription {
		t.Helper()
		sub := newSubscription(org, id, "con_1", "app_1", "user_1", domain.SubscriptionPending, now)
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription(%s) error = %v", id, err)
		}
		activated, err := store.ActivateSubscription(ctx, org, id, "reg_"+id, nil, nil, renewAt, now, eventFor("integrations.subscription.activated.v1", now))
		if err != nil {
			t.Fatalf("ActivateSubscription(%s) error = %v", id, err)
		}
		return activated
	}
	reload := func(id string) domain.Subscription {
		t.Helper()
		got, err := store.Subscription(ctx, org, id)
		if err != nil {
			t.Fatalf("Subscription(%s) error = %v", id, err)
		}
		return got
	}

	t.Run("sets renew_at when it was unset", func(t *testing.T) {
		sub := activate("sub_sched_unset", nil)
		at := now.Add(time.Hour)
		if err := store.ScheduleRenewal(ctx, sub.ID, at); err != nil {
			t.Fatalf("ScheduleRenewal() error = %v", err)
		}
		if got := reload(sub.ID); !timePtrEqual(got.RenewAt, &at) {
			t.Fatalf("Subscription().RenewAt = %v, want %v", got.RenewAt, at)
		}
	})

	t.Run("brings an existing later renewal forward", func(t *testing.T) {
		later := now.Add(48 * time.Hour)
		sub := activate("sub_sched_later", &later)
		sooner := now.Add(time.Hour)
		if err := store.ScheduleRenewal(ctx, sub.ID, sooner); err != nil {
			t.Fatalf("ScheduleRenewal() error = %v", err)
		}
		if got := reload(sub.ID); !timePtrEqual(got.RenewAt, &sooner) {
			t.Fatalf("Subscription().RenewAt = %v, want %v", got.RenewAt, sooner)
		}
	})

	t.Run("never moves an earlier renewal later", func(t *testing.T) {
		sooner := now.Add(time.Hour)
		sub := activate("sub_sched_sooner", &sooner)
		later := now.Add(48 * time.Hour)
		if err := store.ScheduleRenewal(ctx, sub.ID, later); err != nil {
			t.Fatalf("ScheduleRenewal() error = %v", err)
		}
		if got := reload(sub.ID); !timePtrEqual(got.RenewAt, &sooner) {
			t.Fatalf("Subscription().RenewAt = %v, want it to stay %v", got.RenewAt, sooner)
		}
	})

	t.Run("does not affect a pending subscription", func(t *testing.T) {
		sub := newSubscription(org, "sub_sched_pending", "con_1", "app_1", "user_1", domain.SubscriptionPending, now)
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}
		if err := store.ScheduleRenewal(ctx, sub.ID, now.Add(time.Hour)); err != nil {
			t.Fatalf("ScheduleRenewal() error = %v", err)
		}
		if got := reload(sub.ID); got.RenewAt != nil {
			t.Fatalf("Subscription(pending).RenewAt = %v, want nil", got.RenewAt)
		}
	})

	t.Run("does not affect an active subscription without a registration", func(t *testing.T) {
		sub := newSubscription(org, "sub_sched_unregistered", "con_1", "app_1", "user_1", domain.SubscriptionActive, now)
		event := testEvent("integrations.subscription.created.v1", now)
		if err := store.CreateSubscription(ctx, sub, &event); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}
		if err := store.ScheduleRenewal(ctx, sub.ID, now.Add(time.Hour)); err != nil {
			t.Fatalf("ScheduleRenewal() error = %v", err)
		}
		if got := reload(sub.ID); got.RenewAt != nil {
			t.Fatalf("Subscription(unregistered).RenewAt = %v, want nil", got.RenewAt)
		}
	})
}

func TestClaimRenewals(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_claim_renewals"

	activate := func(id string, renewAt *time.Time) domain.Subscription {
		t.Helper()
		sub := newSubscription(org, id, "con_1", "app_1", "user_1", domain.SubscriptionPending, now)
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription(%s) error = %v", id, err)
		}
		activated, err := store.ActivateSubscription(ctx, org, id, "reg_"+id, nil, nil, renewAt, now, eventFor("integrations.subscription.activated.v1", now))
		if err != nil {
			t.Fatalf("ActivateSubscription(%s) error = %v", id, err)
		}
		return activated
	}

	due1 := now.Add(-time.Hour)
	due2 := now
	future := now.Add(time.Hour)

	dueSub1 := activate("sub_renew_due_1", &due1)
	dueSub2 := activate("sub_renew_due_2", &due2)
	activate("sub_renew_future", &future)

	stoppable := activate("sub_renew_stopped", &due1)
	if _, err := store.StopSubscriptions(ctx, domain.SubscriptionSelector{OrganizationID: org, SubscriptionID: stoppable.ID},
		"stopped", "user_admin", now, eventFor("integrations.subscription.stopped.v1", now)); err != nil {
		t.Fatalf("StopSubscriptions() error = %v", err)
	}

	leaseUntil := now.Add(2 * time.Minute)
	claimed, err := store.ClaimRenewals(ctx, now, 10, "lease-renew-1", leaseUntil)
	if err != nil {
		t.Fatalf("ClaimRenewals() error = %v", err)
	}
	if want := []string{dueSub1.ID, dueSub2.ID}; !slices.Equal(idsOf(claimed), want) {
		t.Fatalf("ClaimRenewals() = %v, want %v (not the future or stopped ones)", idsOf(claimed), want)
	}

	if again, err := store.ClaimRenewals(ctx, now, 10, "lease-renew-2", leaseUntil); err != nil || len(again) != 0 {
		t.Fatalf("ClaimRenewals() second claim at the same time = %+v, %v, want none", again, err)
	}

	afterLease := leaseUntil.Add(time.Second)
	reclaimed, err := store.ClaimRenewals(ctx, afterLease, 10, "lease-renew-3", afterLease.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimRenewals() after the lease expired error = %v", err)
	}
	if want := []string{dueSub1.ID, dueSub2.ID}; !slices.Equal(idsOf(reclaimed), want) {
		t.Fatalf("ClaimRenewals() after the lease expired = %v, want %v", idsOf(reclaimed), want)
	}

	t.Run("SaveRenewal and RetryRenewal reject the wrong lease", func(t *testing.T) {
		expiresAt, renewAt := now.Add(72*time.Hour), now.Add(48*time.Hour)
		if err := store.SaveRenewal(ctx, dueSub1.ID, "not-the-lease", &expiresAt, &renewAt, now); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("SaveRenewal() with the wrong lease error = %v, want ErrConflict", err)
		}
		if err := store.RetryRenewal(ctx, dueSub1.ID, "not-the-lease", now.Add(time.Hour)); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("RetryRenewal() with the wrong lease error = %v, want ErrConflict", err)
		}
	})

	t.Run("SaveRenewal records times and releases the lease", func(t *testing.T) {
		savedAt := afterLease.Add(time.Minute) // equals the lease_until the third claim set
		expiresAt := savedAt.Add(72 * time.Hour)
		renewAt := savedAt // due immediately, so a reclaim only succeeds if the lease was actually released
		if err := store.SaveRenewal(ctx, dueSub1.ID, "lease-renew-3", &expiresAt, &renewAt, savedAt); err != nil {
			t.Fatalf("SaveRenewal() error = %v", err)
		}
		got, err := store.Subscription(ctx, org, dueSub1.ID)
		if err != nil {
			t.Fatalf("Subscription() error = %v", err)
		}
		if !timePtrEqual(got.ExpiresAt, &expiresAt) || !timePtrEqual(got.RenewAt, &renewAt) || !got.UpdatedAt.Equal(savedAt) {
			t.Fatalf("Subscription() after SaveRenewal = %+v", got)
		}
		reclaimed, err := store.ClaimRenewals(ctx, savedAt, 10, "lease-renew-4", savedAt.Add(time.Minute))
		if err != nil {
			t.Fatalf("ClaimRenewals() error = %v", err)
		}
		if !slices.Contains(idsOf(reclaimed), dueSub1.ID) {
			t.Fatalf("ClaimRenewals() = %v, want it to include %s now that the lease is released and it is due", idsOf(reclaimed), dueSub1.ID)
		}
	})

	t.Run("RetryRenewal moves renew_at and releases the lease", func(t *testing.T) {
		retryAt := afterLease.Add(time.Minute) // equals the lease_until the third claim set
		if err := store.RetryRenewal(ctx, dueSub2.ID, "lease-renew-3", retryAt); err != nil {
			t.Fatalf("RetryRenewal() error = %v", err)
		}
		got, err := store.Subscription(ctx, org, dueSub2.ID)
		if err != nil {
			t.Fatalf("Subscription() error = %v", err)
		}
		if !timePtrEqual(got.RenewAt, &retryAt) {
			t.Fatalf("Subscription().RenewAt after RetryRenewal = %v, want %v", got.RenewAt, retryAt)
		}
		reclaimed, err := store.ClaimRenewals(ctx, retryAt, 10, "lease-renew-5", retryAt.Add(time.Minute))
		if err != nil {
			t.Fatalf("ClaimRenewals() error = %v", err)
		}
		if !slices.Contains(idsOf(reclaimed), dueSub2.ID) {
			t.Fatalf("ClaimRenewals() = %v, want it to include %s now that the lease is released and it is due", idsOf(reclaimed), dueSub2.ID)
		}
	})
}

func TestClaimCleanupsLeaseLifecycle(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_claim_cleanups"

	stopWithRegistration := func(id string, stopAt time.Time) domain.Subscription {
		t.Helper()
		sub := newSubscription(org, id, "con_1", "app_1", "user_1", domain.SubscriptionPending, now)
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription(%s) error = %v", id, err)
		}
		if _, err := store.ActivateSubscription(ctx, org, id, "reg_"+id, nil, nil, nil, now, eventFor("integrations.subscription.activated.v1", now)); err != nil {
			t.Fatalf("ActivateSubscription(%s) error = %v", id, err)
		}
		stopped, err := store.StopSubscriptions(ctx, domain.SubscriptionSelector{OrganizationID: org, SubscriptionID: id},
			"stopped", "user_admin", stopAt, eventFor("integrations.subscription.stopped.v1", stopAt))
		if err != nil || len(stopped) != 1 {
			t.Fatalf("StopSubscriptions(%s) = %+v, %v", id, stopped, err)
		}
		return stopped[0]
	}

	dueSub := stopWithRegistration("sub_cleanup_due", now)

	// Never registered, so stopping it queues no cleanup; used to confirm
	// ClaimCleanups leaves it alone throughout.
	notRegistered := newSubscription(org, "sub_cleanup_not_registered", "con_1", "app_1", "user_1", domain.SubscriptionPending, now)
	if err := store.CreateSubscription(ctx, notRegistered, nil); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	if _, err := store.StopSubscriptions(ctx, domain.SubscriptionSelector{OrganizationID: org, SubscriptionID: notRegistered.ID},
		"stopped", "user_admin", now, eventFor("integrations.subscription.stopped.v1", now)); err != nil {
		t.Fatalf("StopSubscriptions() error = %v", err)
	}

	leaseUntil := now.Add(2 * time.Minute)
	claimed, err := store.ClaimCleanups(ctx, now, 10, "lease-cleanup-1", leaseUntil)
	if err != nil {
		t.Fatalf("ClaimCleanups() error = %v", err)
	}
	if want := []string{dueSub.ID}; !slices.Equal(idsOf(claimed), want) {
		t.Fatalf("ClaimCleanups() = %v, want %v", idsOf(claimed), want)
	}

	if again, err := store.ClaimCleanups(ctx, now, 10, "lease-cleanup-2", leaseUntil); err != nil || len(again) != 0 {
		t.Fatalf("ClaimCleanups() second claim at the same time = %+v, %v, want none", again, err)
	}

	afterLease := leaseUntil.Add(time.Second)
	reclaimed, err := store.ClaimCleanups(ctx, afterLease, 10, "lease-cleanup-3", afterLease.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimCleanups() after the lease expired error = %v", err)
	}
	if want := []string{dueSub.ID}; !slices.Equal(idsOf(reclaimed), want) {
		t.Fatalf("ClaimCleanups() after the lease expired = %v, want %v", idsOf(reclaimed), want)
	}

	if err := store.RetryCleanup(ctx, dueSub.ID, "wrong-lease", now.Add(time.Hour)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RetryCleanup() with the wrong lease error = %v, want ErrConflict", err)
	}
	if err := store.FinishCleanup(ctx, dueSub.ID, "wrong-lease", now.Add(time.Hour)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("FinishCleanup() with the wrong lease error = %v, want ErrConflict", err)
	}

	retryAt := afterLease.Add(time.Minute) // equals the lease_until the third claim set
	if err := store.RetryCleanup(ctx, dueSub.ID, "lease-cleanup-3", retryAt); err != nil {
		t.Fatalf("RetryCleanup() error = %v", err)
	}
	rereclaimed, err := store.ClaimCleanups(ctx, retryAt, 10, "lease-cleanup-4", retryAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimCleanups() error = %v", err)
	}
	if !slices.Contains(idsOf(rereclaimed), dueSub.ID) {
		t.Fatalf("ClaimCleanups() = %v, want it to include %s now that the lease is released and it is due", idsOf(rereclaimed), dueSub.ID)
	}

	finishAt := retryAt.Add(time.Minute)
	if err := store.FinishCleanup(ctx, dueSub.ID, "lease-cleanup-4", finishAt); err != nil {
		t.Fatalf("FinishCleanup() error = %v", err)
	}
	got, err := store.Subscription(ctx, org, dueSub.ID)
	if err != nil {
		t.Fatalf("Subscription() error = %v", err)
	}
	if got.CleanupAt != nil {
		t.Fatalf("Subscription().CleanupAt after FinishCleanup = %v, want nil", got.CleanupAt)
	}
	if !got.UpdatedAt.Equal(finishAt) {
		t.Fatalf("Subscription().UpdatedAt after FinishCleanup = %v, want %v", got.UpdatedAt, finishAt)
	}

	stillDue, err := store.ClaimCleanups(ctx, finishAt.Add(time.Hour), 10, "lease-cleanup-5", finishAt.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("ClaimCleanups() after FinishCleanup error = %v", err)
	}
	if slices.Contains(idsOf(stillDue), dueSub.ID) {
		t.Fatalf("ClaimCleanups() reclaimed %s after FinishCleanup, want it gone for good", dueSub.ID)
	}
}
