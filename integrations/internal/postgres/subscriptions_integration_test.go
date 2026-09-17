package postgres

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

func assertSubscriptionRoundTrip(t *testing.T, got, want domain.Subscription) {
	t.Helper()
	if got.ID != want.ID || got.OrganizationID != want.OrganizationID || got.ConnectionID != want.ConnectionID ||
		got.AppID != want.AppID || got.Provider != want.Provider {
		t.Fatalf("Subscription() identity = %+v, want %+v", got, want)
	}
	if got.Scope != want.Scope || got.TeamID != want.TeamID || got.MemberID != want.MemberID {
		t.Fatalf("Subscription() placement = scope=%s team=%s member=%s, want scope=%s team=%s member=%s",
			got.Scope, got.TeamID, got.MemberID, want.Scope, want.TeamID, want.MemberID)
	}
	if got.ProviderAccount != want.ProviderAccount {
		t.Fatalf("Subscription().ProviderAccount = %+v, want %+v", got.ProviderAccount, want.ProviderAccount)
	}
	if !maps.Equal(got.Target, want.Target) {
		t.Fatalf("Subscription().Target = %v, want %v", got.Target, want.Target)
	}
	if !slices.Equal(got.Events, want.Events) {
		t.Fatalf("Subscription().Events = %v, want %v", got.Events, want.Events)
	}
	if got.AccountKey != want.AccountKey {
		t.Fatalf("Subscription().AccountKey = %q, want %q", got.AccountKey, want.AccountKey)
	}
	if got.RegistrationID != want.RegistrationID {
		t.Fatalf("Subscription().RegistrationID = %q, want %q", got.RegistrationID, want.RegistrationID)
	}
	if !sealedEqual(got.EncryptedSecret, want.EncryptedSecret) {
		t.Fatalf("Subscription().EncryptedSecret = %+v, want %+v", got.EncryptedSecret, want.EncryptedSecret)
	}
	if got.Status != want.Status {
		t.Fatalf("Subscription().Status = %s, want %s", got.Status, want.Status)
	}
	if got.CreatedBy != want.CreatedBy {
		t.Fatalf("Subscription().CreatedBy = %q, want %q", got.CreatedBy, want.CreatedBy)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("Subscription() created/updated = %v/%v, want %v/%v", got.CreatedAt, got.UpdatedAt, want.CreatedAt, want.UpdatedAt)
	}
	if !timePtrEqual(got.ExpiresAt, want.ExpiresAt) {
		t.Fatalf("Subscription().ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
	if !timePtrEqual(got.RenewAt, want.RenewAt) {
		t.Fatalf("Subscription().RenewAt = %v, want %v", got.RenewAt, want.RenewAt)
	}
}

func TestCreateSubscription(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_create"

	t.Run("pending without event writes no outbox row", func(t *testing.T) {
		sub := newSubscription(org, "sub_pending", "con_1", "app_1", "user_1", domain.SubscriptionPending, now)
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}
		claimed, err := store.ClaimOutbox(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimOutbox() error = %v", err)
		}
		if len(claimed) != 0 {
			t.Fatalf("ClaimOutbox() after a pending create = %d events, want 0", len(claimed))
		}
	})

	t.Run("active with event writes exactly one outbox row", func(t *testing.T) {
		sub := newSubscription(org, "sub_active", "con_2", "app_1", "user_1", domain.SubscriptionActive, now)
		event := testEvent("integrations.subscription.created.v1", now)
		if err := store.CreateSubscription(ctx, sub, &event); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}
		claimed, err := store.ClaimOutbox(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimOutbox() error = %v", err)
		}
		if len(claimed) != 1 || claimed[0].ID != event.ID {
			t.Fatalf("ClaimOutbox() = %+v, want exactly [%s]", claimed, event.ID)
		}
	})

	t.Run("placement check refuses member scope without member id", func(t *testing.T) {
		sub := newSubscription(org, "sub_bad_placement", "con_3", "app_1", "user_1", domain.SubscriptionPending, now)
		sub.Scope = "member"
		// MemberID and TeamID are left empty: the placement check must reject it.
		if err := store.CreateSubscription(ctx, sub, nil); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("CreateSubscription() with member scope and no member_id error = %v, want ErrInvalid", err)
		}
	})

	t.Run("duplicate route is a conflict", func(t *testing.T) {
		route := routeFor("shared")
		first := newSubscription(org, "sub_route_1", "con_4", "app_1", "user_1", domain.SubscriptionPending, now)
		first.Route = route
		if err := store.CreateSubscription(ctx, first, nil); err != nil {
			t.Fatalf("CreateSubscription() first error = %v", err)
		}
		second := newSubscription(org, "sub_route_2", "con_5", "app_1", "user_1", domain.SubscriptionPending, now)
		second.Route = route
		if err := store.CreateSubscription(ctx, second, nil); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("CreateSubscription() with a duplicate route error = %v, want ErrConflict", err)
		}
	})

	t.Run("Subscription and SubscriptionByRoute read back every field", func(t *testing.T) {
		expiresAt := now.Add(24 * time.Hour)
		renewAt := now.Add(20 * time.Hour)
		sub := newSubscription(org, "sub_full", "con_6", "app_2", "user_2", domain.SubscriptionActive, now)
		sub.Scope, sub.TeamID = "team", "team_1"
		sub.Target = map[string]string{"repo": "octo/hello", "branch": "main", "visibility": "private"}
		sub.Events = []string{"issue.created", "issue.updated", "issue.closed"}
		sub.AccountKey = "workspace-42"
		sub.RegistrationID = "reg_123"
		sub.ExpiresAt = &expiresAt
		sub.RenewAt = &renewAt
		event := testEvent("integrations.subscription.created.v1", now)
		if err := store.CreateSubscription(ctx, sub, &event); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}

		got, err := store.Subscription(ctx, org, sub.ID)
		if err != nil {
			t.Fatalf("Subscription() error = %v", err)
		}
		assertSubscriptionRoundTrip(t, got, sub)

		byRoute, err := store.SubscriptionByRoute(ctx, sub.Route)
		if err != nil {
			t.Fatalf("SubscriptionByRoute() error = %v", err)
		}
		assertSubscriptionRoundTrip(t, byRoute, sub)
	})
}

func TestActivateSubscription(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_activate"

	t.Run("pending becomes active with one created event", func(t *testing.T) {
		sub := newSubscription(org, "sub_act_1", "con_1", "app_1", "user_1", domain.SubscriptionPending, now)
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}
		expiresAt := now.Add(48 * time.Hour)
		renewAt := now.Add(24 * time.Hour)
		activatedAt := now.Add(time.Minute)
		var seen domain.Subscription
		callback := func(subscription domain.Subscription) (domain.Event, error) {
			seen = subscription
			return testEvent("integrations.subscription.activated.v1", activatedAt), nil
		}
		activated, err := store.ActivateSubscription(ctx, org, sub.ID, "reg_abc", nil, &expiresAt, &renewAt, activatedAt, callback)
		if err != nil {
			t.Fatalf("ActivateSubscription() error = %v", err)
		}
		if activated.Status != domain.SubscriptionActive || activated.RegistrationID != "reg_abc" ||
			!timePtrEqual(activated.ExpiresAt, &expiresAt) || !timePtrEqual(activated.RenewAt, &renewAt) {
			t.Fatalf("ActivateSubscription() = %+v", activated)
		}
		if seen.ID != sub.ID || seen.Status != domain.SubscriptionActive || seen.RegistrationID != "reg_abc" {
			t.Fatalf("eventFor received = %+v, want the activated subscription", seen)
		}
		claimed, err := store.ClaimOutbox(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimOutbox() error = %v", err)
		}
		if len(claimed) != 1 {
			t.Fatalf("ClaimOutbox() = %d events, want 1", len(claimed))
		}
	})

	t.Run("unknown id is not found", func(t *testing.T) {
		_, err := store.ActivateSubscription(ctx, org, "sub_missing", "reg", nil, nil, nil, now, neverCalled(t))
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("ActivateSubscription() for an unknown id error = %v, want ErrNotFound", err)
		}
	})

	// A provider that issues its own signing secret — ClickUp answers a create
	// with the webhook's secret, Asana hands one over during the handshake —
	// replaces the one the service generated. Every later delivery is signed
	// with the provider's, so the stored value has to become that one, or
	// Receive would refuse genuine deliveries.
	t.Run("a provider-issued secret replaces the one stored", func(t *testing.T) {
		sub := newSubscription(org, "sub_secret", "con_secret", "app_1", "user_1", domain.SubscriptionPending, now)
		sub.EncryptedSecret = sealedData("generated")
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}

		issued := sealedData("issued")
		activated, err := store.ActivateSubscription(ctx, org, sub.ID, "reg_issued", issued, nil, nil, now,
			eventFor("integrations.subscription.activated.v1", now))
		if err != nil {
			t.Fatalf("ActivateSubscription() error = %v", err)
		}
		if !sealedEqual(activated.EncryptedSecret, issued) {
			t.Fatalf("ActivateSubscription() stored secret = %+v, want the provider's", activated.EncryptedSecret)
		}
		// Reading it back shows the replacement was persisted, not just
		// returned.
		stored, err := store.Subscription(ctx, org, sub.ID)
		if err != nil {
			t.Fatalf("Subscription() error = %v", err)
		}
		if !sealedEqual(stored.EncryptedSecret, issued) {
			t.Fatalf("Subscription() secret = %+v, want the provider's", stored.EncryptedSecret)
		}
	})

	// A provider that takes the generated secret — GitHub stores what it is
	// given — leaves it alone.
	t.Run("no issued secret keeps the one stored", func(t *testing.T) {
		generated := sealedData("kept")
		sub := newSubscription(org, "sub_keeps", "con_keeps", "app_1", "user_1", domain.SubscriptionPending, now)
		sub.EncryptedSecret = generated
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}
		activated, err := store.ActivateSubscription(ctx, org, sub.ID, "reg_keeps", nil, nil, nil, now,
			eventFor("integrations.subscription.activated.v1", now))
		if err != nil {
			t.Fatalf("ActivateSubscription() error = %v", err)
		}
		if !sealedEqual(activated.EncryptedSecret, generated) {
			t.Fatalf("ActivateSubscription() stored secret = %+v, want the generated one kept", activated.EncryptedSecret)
		}
	})

	t.Run("already active is a conflict and changes nothing", func(t *testing.T) {
		sub := newSubscription(org, "sub_act_already", "con_2", "app_1", "user_1", domain.SubscriptionPending, now)
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}
		first, err := store.ActivateSubscription(ctx, org, sub.ID, "reg_1", nil, nil, nil, now, eventFor("integrations.subscription.activated.v1", now))
		if err != nil {
			t.Fatalf("ActivateSubscription() first error = %v", err)
		}
		if _, err := store.ClaimOutbox(ctx, 10); err != nil {
			t.Fatalf("ClaimOutbox() error = %v", err)
		}
		if _, err := store.ActivateSubscription(ctx, org, sub.ID, "reg_2", nil, nil, nil, now.Add(time.Minute), neverCalled(t)); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("ActivateSubscription() on an active subscription error = %v, want ErrConflict", err)
		}
		reloaded, err := store.Subscription(ctx, org, sub.ID)
		if err != nil {
			t.Fatalf("Subscription() error = %v", err)
		}
		if reloaded.RegistrationID != first.RegistrationID || reloaded.Version != first.Version {
			t.Fatalf("Subscription() after a conflicting activation = %+v, want unchanged from %+v", reloaded, first)
		}
	})

	t.Run("stopped while pending stays stopped but queues cleanup", func(t *testing.T) {
		sub := newSubscription(org, "sub_act_stopped", "con_3", "app_1", "user_1", domain.SubscriptionPending, now)
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription() error = %v", err)
		}
		stopAt := now.Add(30 * time.Second)
		stopped, err := store.StopSubscriptions(ctx, domain.SubscriptionSelector{OrganizationID: org, SubscriptionID: sub.ID},
			"stopped", "user_admin", stopAt, eventFor("integrations.subscription.stopped.v1", stopAt))
		if err != nil || len(stopped) != 1 {
			t.Fatalf("StopSubscriptions() = %+v, %v", stopped, err)
		}
		if _, err := store.ClaimOutbox(ctx, 10); err != nil {
			t.Fatalf("ClaimOutbox() error = %v", err)
		}

		activateAt := now.Add(time.Minute)
		if _, err := store.ActivateSubscription(ctx, org, sub.ID, "reg_late", nil, nil, nil, activateAt, neverCalled(t)); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("ActivateSubscription() of a subscription stopped while pending error = %v, want ErrConflict", err)
		}

		reloaded, err := store.Subscription(ctx, org, sub.ID)
		if err != nil {
			t.Fatalf("Subscription() error = %v", err)
		}
		if reloaded.Status != domain.SubscriptionStopped || reloaded.RegistrationID != "reg_late" || reloaded.CleanupAt == nil {
			t.Fatalf("Subscription() after activating a stopped one = %+v", reloaded)
		}
		due, err := store.ClaimCleanups(ctx, activateAt.Add(time.Second), 10, "lease_1", activateAt.Add(time.Minute))
		if err != nil {
			t.Fatalf("ClaimCleanups() error = %v", err)
		}
		if len(due) != 1 || due[0].ID != sub.ID {
			t.Fatalf("ClaimCleanups() = %+v, want [%s]", due, sub.ID)
		}
		claimed, err := store.ClaimOutbox(ctx, 10)
		if err != nil {
			t.Fatalf("ClaimOutbox() error = %v", err)
		}
		if len(claimed) != 0 {
			t.Fatalf("ClaimOutbox() after activating a stopped subscription = %d events, want 0", len(claimed))
		}
	})
}

func TestDeletePendingSubscription(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_delete"

	pending := newSubscription(org, "sub_del_pending", "con_1", "app_1", "user_1", domain.SubscriptionPending, now)
	if err := store.CreateSubscription(ctx, pending, nil); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	active := newSubscription(org, "sub_del_active", "con_2", "app_1", "user_1", domain.SubscriptionActive, now)
	event := testEvent("integrations.subscription.created.v1", now)
	if err := store.CreateSubscription(ctx, active, &event); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}

	if err := store.DeletePendingSubscription(ctx, org, pending.ID); err != nil {
		t.Fatalf("DeletePendingSubscription() error = %v", err)
	}
	if _, err := store.Subscription(ctx, org, pending.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Subscription() after deleting a pending subscription error = %v, want ErrNotFound", err)
	}

	if err := store.DeletePendingSubscription(ctx, org, active.ID); err != nil {
		t.Fatalf("DeletePendingSubscription() on an active subscription error = %v", err)
	}
	if got, err := store.Subscription(ctx, org, active.ID); err != nil || got.Status != domain.SubscriptionActive {
		t.Fatalf("Subscription() after deleting an active subscription = %+v, %v, want it to stay active", got, err)
	}
}

func TestSubscriptionsFilters(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_list"

	create := func(id, connectionID, createdBy string, status domain.SubscriptionStatus, at time.Time) domain.Subscription {
		t.Helper()
		sub := newSubscription(org, id, connectionID, "app_1", createdBy, status, at)
		var event *domain.Event
		if status == domain.SubscriptionActive {
			e := testEvent("integrations.subscription.created.v1", at)
			event = &e
		}
		if err := store.CreateSubscription(ctx, sub, event); err != nil {
			t.Fatalf("CreateSubscription(%s) error = %v", id, err)
		}
		return sub
	}

	subZ := create("sub_z", "con_1", "user_1", domain.SubscriptionActive, base)
	subA := create("sub_a", "con_1", "user_1", domain.SubscriptionActive, base.Add(time.Minute))
	subB := create("sub_b", "con_2", "user_1", domain.SubscriptionPending, base.Add(2*time.Minute))
	subC := create("sub_c", "con_1", "user_2", domain.SubscriptionStopped, base.Add(3*time.Minute))
	// subX and subY share a created_at, so ordering can only be settled by id.
	subY := create("sub_y", "con_3", "user_1", domain.SubscriptionActive, base.Add(4*time.Minute))
	subX := create("sub_x", "con_3", "user_1", domain.SubscriptionActive, base.Add(4*time.Minute))

	all, err := store.Subscriptions(ctx, org, domain.SubscriptionFilter{})
	if err != nil {
		t.Fatalf("Subscriptions() error = %v", err)
	}
	if want := []string{subZ.ID, subA.ID, subB.ID, subC.ID, subX.ID, subY.ID}; !slices.Equal(idsOf(all), want) {
		t.Fatalf("Subscriptions() order = %v, want %v", idsOf(all), want)
	}

	byConnection, err := store.Subscriptions(ctx, org, domain.SubscriptionFilter{ConnectionID: "con_1"})
	if err != nil {
		t.Fatalf("Subscriptions(connection) error = %v", err)
	}
	if want := []string{subZ.ID, subA.ID, subC.ID}; !slices.Equal(idsOf(byConnection), want) {
		t.Fatalf("Subscriptions(connection) = %v, want %v", idsOf(byConnection), want)
	}

	byCreator, err := store.Subscriptions(ctx, org, domain.SubscriptionFilter{CreatedBy: "user_2"})
	if err != nil {
		t.Fatalf("Subscriptions(created_by) error = %v", err)
	}
	if want := []string{subC.ID}; !slices.Equal(idsOf(byCreator), want) {
		t.Fatalf("Subscriptions(created_by) = %v, want %v", idsOf(byCreator), want)
	}

	live, err := store.Subscriptions(ctx, org, domain.SubscriptionFilter{Live: true})
	if err != nil {
		t.Fatalf("Subscriptions(live) error = %v", err)
	}
	if want := []string{subZ.ID, subA.ID, subB.ID, subX.ID, subY.ID}; !slices.Equal(idsOf(live), want) {
		t.Fatalf("Subscriptions(live) = %v, want %v", idsOf(live), want)
	}
}

func TestAccountSubscriptions(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_account"

	makeActive := func(id, appID, accountKey string) domain.Subscription {
		t.Helper()
		sub := newSubscription(org, id, "con_1", appID, "user_1", domain.SubscriptionActive, now)
		sub.AccountKey = accountKey
		event := testEvent("integrations.subscription.created.v1", now)
		if err := store.CreateSubscription(ctx, sub, &event); err != nil {
			t.Fatalf("CreateSubscription(%s) error = %v", id, err)
		}
		return sub
	}
	makePending := func(id, appID, accountKey string) domain.Subscription {
		t.Helper()
		sub := newSubscription(org, id, "con_1", appID, "user_1", domain.SubscriptionPending, now)
		sub.AccountKey = accountKey
		if err := store.CreateSubscription(ctx, sub, nil); err != nil {
			t.Fatalf("CreateSubscription(%s) error = %v", id, err)
		}
		return sub
	}

	match1 := makeActive("sub_acc_1", "app_slack", "workspace-1")
	match2 := makeActive("sub_acc_2", "app_slack", "workspace-1")
	makeActive("sub_acc_other_key", "app_slack", "workspace-2")
	makeActive("sub_acc_other_app", "app_github", "workspace-1")
	makePending("sub_acc_pending", "app_slack", "workspace-1")

	got, err := store.AccountSubscriptions(ctx, "app_slack", "workspace-1")
	if err != nil {
		t.Fatalf("AccountSubscriptions() error = %v", err)
	}
	if want := []string{match1.ID, match2.ID}; !slices.Equal(idsOf(got), want) {
		t.Fatalf("AccountSubscriptions() = %v, want %v", idsOf(got), want)
	}
}

func TestHoldsConnection(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	org := "org_holds"
	connection := "con_holds"

	if held, err := store.HoldsConnection(ctx, org, connection); err != nil || held {
		t.Fatalf("HoldsConnection() with nothing yet = %v, %v, want false", held, err)
	}

	pending := newSubscription(org, "sub_hold_pending", connection, "app_1", "user_1", domain.SubscriptionPending, now)
	if err := store.CreateSubscription(ctx, pending, nil); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	if held, err := store.HoldsConnection(ctx, org, connection); err != nil || !held {
		t.Fatalf("HoldsConnection() with a pending subscription = %v, %v, want true", held, err)
	}

	// Stopping it before it ever registered leaves nothing to clean up.
	stopAt := now.Add(time.Minute)
	if _, err := store.StopSubscriptions(ctx, domain.SubscriptionSelector{OrganizationID: org, SubscriptionID: pending.ID},
		"stopped", "user_admin", stopAt, eventFor("integrations.subscription.stopped.v1", stopAt)); err != nil {
		t.Fatalf("StopSubscriptions() error = %v", err)
	}
	if held, err := store.HoldsConnection(ctx, org, connection); err != nil || held {
		t.Fatalf("HoldsConnection() after stopping with nothing to clean up = %v, %v, want false", held, err)
	}

	// A second subscription that did register keeps the hold through cleanup.
	registered := newSubscription(org, "sub_hold_registered", connection, "app_1", "user_1", domain.SubscriptionPending, now)
	if err := store.CreateSubscription(ctx, registered, nil); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	activated, err := store.ActivateSubscription(ctx, org, registered.ID, "reg_hold", nil, nil, nil, now.Add(2*time.Minute),
		eventFor("integrations.subscription.activated.v1", now))
	if err != nil {
		t.Fatalf("ActivateSubscription() error = %v", err)
	}
	if held, err := store.HoldsConnection(ctx, org, connection); err != nil || !held {
		t.Fatalf("HoldsConnection() with an active subscription = %v, %v, want true", held, err)
	}

	stopAt2 := now.Add(3 * time.Minute)
	if _, err := store.StopSubscriptions(ctx, domain.SubscriptionSelector{OrganizationID: org, SubscriptionID: activated.ID},
		"stopped", "user_admin", stopAt2, eventFor("integrations.subscription.stopped.v1", stopAt2)); err != nil {
		t.Fatalf("StopSubscriptions() error = %v", err)
	}
	if held, err := store.HoldsConnection(ctx, org, connection); err != nil || !held {
		t.Fatalf("HoldsConnection() with a cleanup due = %v, %v, want true", held, err)
	}

	due, err := store.ClaimCleanups(ctx, stopAt2.Add(time.Second), 10, "lease_hold", stopAt2.Add(time.Minute))
	if err != nil || len(due) != 1 {
		t.Fatalf("ClaimCleanups() = %+v, %v", due, err)
	}
	if err := store.FinishCleanup(ctx, activated.ID, "lease_hold", stopAt2.Add(2*time.Second)); err != nil {
		t.Fatalf("FinishCleanup() error = %v", err)
	}
	if held, err := store.HoldsConnection(ctx, org, connection); err != nil || held {
		t.Fatalf("HoldsConnection() after the cleanup finished = %v, %v, want false", held, err)
	}
}
