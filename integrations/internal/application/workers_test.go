package application

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
)

// renewing stores an active hooks subscription whose renewal is due, holding
// its connection.
func (h *harness) renewing(expiresIn time.Duration) domain.Subscription {
	h.connections.holds["conn_hooks"] = true
	return h.store.put(domain.Subscription{
		ID: "sub_renew", OrganizationID: testOrganization, ConnectionID: "conn_hooks", AppID: "app_hooks", Provider: "hooks", Scope: "member",
		MemberID: "user_member", Target: map[string]string{"resource": "me/messages"}, Events: []string{"created"}, Status: domain.SubscriptionActive,
		CreatedBy: "user_member", CreatedAt: h.now.Add(-72 * time.Hour), UpdatedAt: h.now.Add(-72 * time.Hour), Route: "route-renew",
		RegistrationID: "reg_1", ExpiresAt: timePointer(h.now.Add(expiresIn)), RenewAt: timePointer(h.now.Add(-time.Minute)),
	})
}

func TestRenewDueRenewsWithTheCreatorsCredential(t *testing.T) {
	h := newHarness(t)
	subscription := h.renewing(2 * time.Hour)
	renewed := h.now.Add(72 * time.Hour)
	h.hooks.renewal = providers.Registration{ID: "reg_1", ExpiresAt: renewed}

	h.service.RenewDue(context.Background())

	if calls := tokenCalls(h); !slices.Equal(calls, []connectionsclientTokenCall{{purpose: domain.PurposeTrigger, actingMemberID: "user_member"}}) {
		t.Fatalf("Token calls = %+v, want a trigger credential for the creator", calls)
	}
	if len(h.hooks.renewed) != 1 {
		t.Fatalf("Renew calls = %d, want 1", len(h.hooks.renewed))
	}
	request := h.hooks.renewed[0]
	if request.RegistrationID != "reg_1" || request.Credential.AccessToken != "provider-token" || request.Target["resource"] != "me/messages" || !slices.Equal(request.Events, []string{"created"}) || !request.Now.Equal(h.now) {
		t.Fatalf("Renew() request = %+v", request)
	}
	stored := h.store.subscription(t, subscription.ID)
	if !stored.ExpiresAt.Equal(renewed) || !stored.RenewAt.Equal(renewed.Add(-24*time.Hour)) || stored.Status != domain.SubscriptionActive {
		t.Fatalf("renewed subscription = %+v", stored)
	}
	if lease := h.store.leases[subscription.ID]; lease != "" {
		t.Fatalf("lease = %q, want it released", lease)
	}
}

func TestRenewDueLeavesSubscriptionsNotYetDue(t *testing.T) {
	h := newHarness(t)
	subscription := h.renewing(48 * time.Hour)
	subscription.RenewAt = timePointer(h.now.Add(time.Second))
	h.store.put(subscription)

	h.service.RenewDue(context.Background())

	if len(h.hooks.renewed) != 0 || len(tokenCalls(h)) != 0 {
		t.Fatal("a renewal that is not due ran")
	}
}

func TestRenewalFailures(t *testing.T) {
	tests := []struct {
		name       string
		expiresIn  time.Duration
		tokenErr   error
		renewErr   error
		wantStatus domain.SubscriptionStatus
		wantReason string
		wantClean  bool
		wantRetry  bool
	}{
		{name: "the provider no longer has the registration", expiresIn: 2 * time.Hour, renewErr: domain.ErrNotFound,
			wantStatus: domain.SubscriptionFailed, wantReason: integrationsv1.SubscriptionFailedReasonProviderRemoved},
		{name: "the provider refuses to renew", expiresIn: 2 * time.Hour, renewErr: domain.ErrInvalid,
			wantStatus: domain.SubscriptionFailed, wantReason: integrationsv1.SubscriptionFailedReasonProviderRejected, wantClean: true},
		{name: "the provider is unavailable", expiresIn: 2 * time.Hour, renewErr: domain.ErrProviderUnavailable,
			wantStatus: domain.SubscriptionActive, wantRetry: true},
		{name: "the provider rejects the credential", expiresIn: 2 * time.Hour, renewErr: domain.ErrCredentialRejected,
			wantStatus: domain.SubscriptionActive, wantRetry: true},
		{name: "connections says the connection needs reconnecting", expiresIn: 2 * time.Hour, tokenErr: domain.ErrNeedsReconnect,
			wantStatus: domain.SubscriptionActive, wantRetry: true},
		{name: "connections has no such connection", expiresIn: 2 * time.Hour, tokenErr: domain.ErrNotFound,
			wantStatus: domain.SubscriptionActive, wantRetry: true},
		{name: "it expires just after the next try", expiresIn: retryAfter + time.Second, renewErr: domain.ErrProviderUnavailable,
			wantStatus: domain.SubscriptionActive, wantRetry: true},
		{name: "it expires by the next try", expiresIn: retryAfter, renewErr: domain.ErrProviderUnavailable,
			wantStatus: domain.SubscriptionFailed, wantReason: integrationsv1.SubscriptionFailedReasonExpired},
		{name: "it expires by the next try without a credential", expiresIn: time.Minute, tokenErr: domain.ErrNeedsReconnect,
			wantStatus: domain.SubscriptionFailed, wantReason: integrationsv1.SubscriptionFailedReasonExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			subscription := h.renewing(test.expiresIn)
			h.connections.tokenErr, h.hooks.renewErr = test.tokenErr, test.renewErr

			h.service.RenewDue(context.Background())

			stored := h.store.subscription(t, subscription.ID)
			if stored.Status != test.wantStatus || stored.Reason != test.wantReason || (stored.CleanupAt != nil) != test.wantClean {
				t.Fatalf("subscription = status %s, reason %q, cleanup %v; want %s, %q, %v", stored.Status, stored.Reason, stored.CleanupAt, test.wantStatus, test.wantReason, test.wantClean)
			}
			if test.wantRetry != (stored.RenewAt != nil && stored.RenewAt.Equal(h.now.Add(retryAfter))) {
				t.Fatalf("renew_at = %v, retry wanted = %v", stored.RenewAt, test.wantRetry)
			}
			if lease := h.store.leases[subscription.ID]; lease != "" {
				t.Fatalf("lease = %q, want it released", lease)
			}
			failed := h.store.eventsOf(integrationsv1.SubscriptionFailedDetailType)
			if (len(failed) == 1) != (test.wantStatus == domain.SubscriptionFailed) || len(failed) > 1 {
				t.Fatalf("failed events = %d", len(failed))
			}
			if len(failed) == 1 {
				var data integrationsv1.SubscriptionFailed
				if err := json.Unmarshal(failed[0].Data, &data); err != nil || data.Reason != test.wantReason || data.SubscriptionID != subscription.ID {
					t.Fatalf("failed event = %+v, %v", data, err)
				}
			}
			// A failed subscription with nothing left to remove lets go of the
			// connection; one that is retried or has a removal due keeps it.
			wantHeld := test.wantStatus == domain.SubscriptionActive || test.wantClean
			if h.connections.held("conn_hooks") != wantHeld {
				t.Fatalf("held = %v, want %v", h.connections.held("conn_hooks"), wantHeld)
			}
		})
	}
}

func TestRenewDueStopsRenewingForAProviderThatCannotRenew(t *testing.T) {
	h := newHarness(t)
	subscription := h.renewing(2 * time.Hour)
	subscription.Provider = "chat"
	h.store.put(subscription)

	h.service.RenewDue(context.Background())

	stored := h.store.subscription(t, subscription.ID)
	if stored.RenewAt != nil || !stored.ExpiresAt.Equal(*subscription.ExpiresAt) || stored.Status != domain.SubscriptionActive {
		t.Fatalf("subscription = %+v, want no renewal due", stored)
	}
	if len(tokenCalls(h)) != 0 {
		t.Fatal("a credential was issued for a renewal that cannot happen")
	}
}

// ending stores a stopped hooks subscription whose registration removal is
// due, holding its connection.
func (h *harness) ending(stoppedAgo time.Duration) domain.Subscription {
	h.connections.holds["conn_hooks"] = true
	stoppedAt := h.now.Add(-stoppedAgo)
	return h.store.put(domain.Subscription{
		ID: "sub_ended", OrganizationID: testOrganization, ConnectionID: "conn_hooks", AppID: "app_hooks", Provider: "hooks", Scope: "member",
		Target: map[string]string{"repository": "acme/api"}, Status: domain.SubscriptionStopped, CreatedBy: "user_member",
		CreatedAt: stoppedAt.Add(-time.Hour), UpdatedAt: stoppedAt, StoppedAt: &stoppedAt, Route: "route-ended",
		RegistrationID: "reg_1", CleanupAt: &stoppedAt,
	})
}

func TestCleanupDueRemovesTheRegistration(t *testing.T) {
	h := newHarness(t)
	subscription := h.ending(time.Minute)

	h.service.CleanupDue(context.Background())

	if calls := tokenCalls(h); !slices.Equal(calls, []connectionsclientTokenCall{{purpose: domain.PurposeCleanup, actingMemberID: "user_member"}}) {
		t.Fatalf("Token calls = %+v, want a cleanup credential for the creator", calls)
	}
	if len(h.hooks.unregistered) != 1 {
		t.Fatalf("Unregister calls = %d, want 1", len(h.hooks.unregistered))
	}
	if request := h.hooks.unregistered[0]; request.RegistrationID != "reg_1" || request.Target["repository"] != "acme/api" || request.Credential.AccessToken != "provider-token" {
		t.Fatalf("Unregister() request = %+v", request)
	}
	if stored := h.store.subscription(t, subscription.ID); stored.CleanupAt != nil || h.store.leases[subscription.ID] != "" {
		t.Fatalf("subscription = %+v, want its removal finished", stored)
	}
	if h.connections.held("conn_hooks") {
		t.Fatal("the hold outlived the last registration")
	}
}

func TestCleanupFailures(t *testing.T) {
	tests := []struct {
		name          string
		stoppedAgo    time.Duration
		failed        bool
		tokenErr      error
		unregisterErr error
		wantRetry     bool
	}{
		{name: "the provider is unavailable", stoppedAgo: time.Hour, unregisterErr: domain.ErrProviderUnavailable, wantRetry: true},
		{name: "the provider rejects the credential", stoppedAgo: time.Hour, unregisterErr: domain.ErrCredentialRejected, wantRetry: true},
		{name: "connections cannot issue a credential yet", stoppedAgo: time.Hour, tokenErr: domain.ErrProviderUnavailable, wantRetry: true},
		{name: "the registration is already gone", stoppedAgo: time.Hour, unregisterErr: domain.ErrNotFound},
		{name: "the connection's credentials are gone", stoppedAgo: time.Hour, tokenErr: domain.ErrNotFound},
		{name: "removal was tried for longer than the window", stoppedAgo: cleanupWindow + time.Minute, unregisterErr: domain.ErrProviderUnavailable},
		{name: "a failed subscription past the window", stoppedAgo: cleanupWindow + time.Minute, failed: true, tokenErr: domain.ErrNeedsReconnect},
		{name: "a failed subscription within the window", stoppedAgo: cleanupWindow - time.Minute, failed: true, tokenErr: domain.ErrNeedsReconnect, wantRetry: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			subscription := h.ending(test.stoppedAgo)
			if test.failed {
				subscription.Status, subscription.StoppedAt = domain.SubscriptionFailed, nil
				h.store.put(subscription)
			}
			h.connections.tokenErr, h.hooks.unregisterErr = test.tokenErr, test.unregisterErr

			h.service.CleanupDue(context.Background())

			stored := h.store.subscription(t, subscription.ID)
			if test.wantRetry {
				if stored.CleanupAt == nil || !stored.CleanupAt.Equal(h.now.Add(retryAfter)) || !h.connections.held("conn_hooks") {
					t.Fatalf("cleanup_at = %v, held = %v; want a retry under the hold", stored.CleanupAt, h.connections.held("conn_hooks"))
				}
			} else if stored.CleanupAt != nil || h.connections.held("conn_hooks") {
				t.Fatalf("cleanup_at = %v, held = %v; want removal given up and the hold released", stored.CleanupAt, h.connections.held("conn_hooks"))
			}
			if lease := h.store.leases[subscription.ID]; lease != "" {
				t.Fatalf("lease = %q, want it released", lease)
			}
		})
	}
}

func TestCleanupKeepsAHoldAnotherSubscriptionNeeds(t *testing.T) {
	h := newHarness(t)
	h.ending(time.Minute)
	h.store.put(domain.Subscription{ID: "sub_live", OrganizationID: testOrganization, ConnectionID: "conn_hooks", Provider: "hooks", Status: domain.SubscriptionActive, Route: "route-live", RegistrationID: "reg_2"})

	h.service.CleanupDue(context.Background())

	if !h.connections.held("conn_hooks") {
		t.Fatal("cleanup released a hold a live subscription needs")
	}
}

func TestRunWorkersForgetsOldDeliveries(t *testing.T) {
	h := newHarness(t)
	h.store.deliveries[[2]string{"sub_1", "old"}] = h.now.Add(-deliveryMemory - time.Second)
	h.store.deliveries[[2]string{"sub_1", "recent"}] = h.now.Add(-deliveryMemory + time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h.service.RunWorkers(ctx)

	if _, ok := h.store.deliveries[[2]string{"sub_1", "old"}]; ok {
		t.Fatal("a delivery older than the memory is still remembered")
	}
	if _, ok := h.store.deliveries[[2]string{"sub_1", "recent"}]; !ok {
		t.Fatal("a recent delivery was forgotten")
	}
}
