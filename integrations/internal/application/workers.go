package application

import (
	"context"
	"errors"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/connectionsclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
)

const (
	workerInterval = time.Minute
	workerBatch    = 20
	workerLease    = 2 * time.Minute
	// retryAfter is how long a renewal or removal the provider or connections
	// could not do waits before it is tried again.
	retryAfter = 15 * time.Minute
	// cleanupWindow is how long integrations keeps trying to remove a
	// registration, matching how long connections holds credentials past a
	// disconnect.
	cleanupWindow = 7 * 24 * time.Hour
	// deliveryMemory is how long a delivery is remembered, so a repeat of it is
	// not published again.
	deliveryMemory = 7 * 24 * time.Hour
)

// RunWorkers renews registrations before they expire, removes those of
// stopped and failed subscriptions, and forgets old deliveries, until ctx is
// cancelled. Work is claimed under leases, so every replica may run it.
func (s *Service) RunWorkers(ctx context.Context) {
	ticker := time.NewTicker(workerInterval)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		s.RenewDue(ctx)
		s.CleanupDue(ctx)
		if s.now().Sub(lastPrune) >= time.Hour {
			if _, err := s.store.DeleteDeliveriesBefore(ctx, s.now().Add(-deliveryMemory)); err != nil && ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "forget old deliveries", "error", err)
			}
			if _, err := s.store.DeleteActionsBefore(ctx, s.now().Add(-actionMemory)); err != nil && ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "forget old actions", "error", err)
			}
			lastPrune = s.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RenewDue renews the registrations whose renewal is due.
func (s *Service) RenewDue(ctx context.Context) {
	lease, err := newRoute()
	if err != nil {
		s.logger.ErrorContext(ctx, "generate a renewal lease", "error", err)
		return
	}
	now := s.now()
	due, err := s.store.ClaimRenewals(ctx, now, workerBatch, lease, now.Add(workerLease))
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "claim renewals", "error", err)
		}
		return
	}
	for _, subscription := range due {
		s.renew(ctx, subscription, lease)
	}
}

func (s *Service) renew(ctx context.Context, subscription domain.Subscription, lease string) {
	now := s.now()
	provider, ok := s.providers[subscription.Provider]
	if !ok || provider.Renew == nil {
		if err := s.store.SaveRenewal(ctx, subscription.ID, lease, subscription.ExpiresAt, nil, now); err != nil {
			s.logger.ErrorContext(ctx, "stop renewing", "subscription_id", subscription.ID, "error", err)
		}
		return
	}
	credential, err := s.connections.Token(ctx, connectionsclient.TokenRequest{
		OrganizationID: subscription.OrganizationID, ConnectionID: subscription.ConnectionID,
		Purpose: domain.PurposeTrigger, ActingMemberID: subscription.CreatedBy,
	})
	if err != nil {
		s.renewalFailed(ctx, subscription, lease, err, false)
		return
	}
	registration, err := provider.Renew(ctx, s.http, providers.RenewRequest{
		Credential: credential, RegistrationID: subscription.RegistrationID, Target: subscription.Target,
		Events: subscription.Events, Now: now,
	})
	if err != nil {
		s.renewalFailed(ctx, subscription, lease, err, true)
		return
	}
	expiresAt, renewAt := renewalTimes(registration.ExpiresAt, now)
	if err := s.store.SaveRenewal(ctx, subscription.ID, lease, expiresAt, renewAt, now); err != nil {
		s.logger.ErrorContext(ctx, "save a renewal", "subscription_id", subscription.ID, "error", err)
	}
}

// renewalFailed decides what a failed renewal means. A registration the
// provider no longer has, or refuses to renew, fails the subscription; any
// other failure, such as the provider or connections not answering or the
// connection needing reconnecting, is tried again while the registration
// lasts. One that expires before the next try fails with nothing to remove,
// since providers delete expired registrations themselves.
func (s *Service) renewalFailed(ctx context.Context, subscription domain.Subscription, lease string, err error, fromProvider bool) {
	now := s.now()
	reason, cleanup := "", false
	switch {
	case fromProvider && errors.Is(err, domain.ErrNotFound):
		reason = integrationsv1.SubscriptionFailedReasonProviderRemoved
	case fromProvider && errors.Is(err, domain.ErrInvalid):
		reason, cleanup = integrationsv1.SubscriptionFailedReasonProviderRejected, true
	case subscription.ExpiresAt != nil && !now.Add(retryAfter).Before(*subscription.ExpiresAt):
		reason = integrationsv1.SubscriptionFailedReasonExpired
	default:
		s.logger.WarnContext(ctx, "renewal will be retried", "subscription_id", subscription.ID, "provider", subscription.Provider, "error", err)
		if err := s.store.RetryRenewal(ctx, subscription.ID, lease, now.Add(retryAfter)); err != nil {
			s.logger.ErrorContext(ctx, "schedule a renewal retry", "subscription_id", subscription.ID, "error", err)
		}
		return
	}
	s.logger.WarnContext(ctx, "subscription failed", "subscription_id", subscription.ID, "provider", subscription.Provider, "reason", reason, "error", err)
	if _, err := s.store.FailSubscription(ctx, subscription.OrganizationID, subscription.ID, lease, reason, cleanup, now, s.failedEvent(reason, now)); err != nil {
		s.logger.ErrorContext(ctx, "fail a subscription", "subscription_id", subscription.ID, "error", err)
		return
	}
	if !cleanup {
		s.releaseHoldIfUnused(ctx, subscription.OrganizationID, subscription.ConnectionID)
	}
}

// CleanupDue removes the registrations of stopped and failed subscriptions.
func (s *Service) CleanupDue(ctx context.Context) {
	lease, err := newRoute()
	if err != nil {
		s.logger.ErrorContext(ctx, "generate a cleanup lease", "error", err)
		return
	}
	now := s.now()
	due, err := s.store.ClaimCleanups(ctx, now, workerBatch, lease, now.Add(workerLease))
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "claim cleanups", "error", err)
		}
		return
	}
	for _, subscription := range due {
		s.cleanup(ctx, subscription, lease)
	}
}

// cleanup removes what a subscription registered at the provider, with a
// cleanup credential for the member who made it. A connection whose
// credentials are gone can remove nothing more, and after cleanupWindow
// removal is given up; either way the subscription stops holding the
// connection.
func (s *Service) cleanup(ctx context.Context, subscription domain.Subscription, lease string) {
	now := s.now()
	provider, ok := s.providers[subscription.Provider]
	if ok && provider.Unregister != nil && subscription.RegistrationID != "" {
		credential, err := s.connections.Token(ctx, connectionsclient.TokenRequest{
			OrganizationID: subscription.OrganizationID, ConnectionID: subscription.ConnectionID,
			Purpose: domain.PurposeCleanup, ActingMemberID: subscription.CreatedBy,
		})
		if err == nil {
			err = provider.Unregister(ctx, s.http, providers.UnregisterRequest{
				Credential: credential, RegistrationID: subscription.RegistrationID, Target: subscription.Target,
			})
		}
		ended := subscription.UpdatedAt
		if subscription.StoppedAt != nil {
			ended = *subscription.StoppedAt
		}
		switch {
		case err == nil:
		case errors.Is(err, domain.ErrNotFound), now.Sub(ended) > cleanupWindow:
			s.logger.WarnContext(ctx, "gave up removing a registration", "subscription_id", subscription.ID, "provider", subscription.Provider, "error", err)
		default:
			s.logger.WarnContext(ctx, "registration removal will be retried", "subscription_id", subscription.ID, "provider", subscription.Provider, "error", err)
			if err := s.store.RetryCleanup(ctx, subscription.ID, lease, now.Add(retryAfter)); err != nil {
				s.logger.ErrorContext(ctx, "schedule a cleanup retry", "subscription_id", subscription.ID, "error", err)
			}
			return
		}
	}
	if err := s.store.FinishCleanup(ctx, subscription.ID, lease, now); err != nil {
		s.logger.ErrorContext(ctx, "finish a cleanup", "subscription_id", subscription.ID, "error", err)
		return
	}
	s.releaseHoldIfUnused(ctx, subscription.OrganizationID, subscription.ConnectionID)
}
