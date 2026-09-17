package application

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/connections/internal/providers"
)

const (
	workerBatch = 20
	// workerLease bounds how long a claimed check or deletion stays with one
	// worker before another may take it over.
	workerLease       = 2 * time.Minute
	heartbeatInterval = 30 * time.Second
	cleanupInterval   = time.Minute
	// sessionRetention keeps expired sessions around briefly, so a late
	// callback still finds its session and answers session_expired.
	sessionRetention = time.Hour
	sessionSweep     = 500
	// outboxRetention is how long a published event is kept. Nothing reads a
	// published row again — delivery has already happened — so it is kept only
	// long enough to explain recent deliveries, and the sweep is what stops
	// the table growing once per connection change for ever.
	outboxRetention = 7 * 24 * time.Hour
	outboxSweep     = 500
	// revokeGiveUp is how long past domain.MaxHold a provider that cannot
	// answer is asked to revoke before the credentials are deleted anyway.
	revokeGiveUp = 24 * time.Hour
)

// RunHeartbeat checks due connections until ctx ends.
func (s *Service) RunHeartbeat(ctx context.Context) {
	s.runEvery(ctx, "heartbeat", heartbeatInterval, s.CheckHealth)
}

// RunCleanup revokes and deletes the credentials of disconnected connections,
// and removes expired sessions, until ctx ends.
func (s *Service) RunCleanup(ctx context.Context) {
	s.runEvery(ctx, "cleanup", cleanupInterval, s.CleanUp)
}

// runEvery runs work in full batches until one comes back short, then waits
// about interval.
func (s *Service) runEvery(ctx context.Context, name string, interval time.Duration, work func(context.Context) (int, error)) {
	for {
		for {
			handled, err := work(ctx)
			if err != nil {
				if ctx.Err() == nil {
					s.logger.ErrorContext(ctx, name+" failed", "error", err)
				}
				break
			}
			if handled < workerBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(jitter(interval)):
		}
	}
}

// CheckHealth claims connections whose check is due and checks them. It
// returns how many it claimed.
func (s *Service) CheckHealth(ctx context.Context) (int, error) {
	claim, err := s.randomToken(18)
	if err != nil {
		return 0, err
	}
	now := s.clock()
	checks, err := s.store.ClaimHealthChecks(ctx, now, workerBatch, claim, now.Add(workerLease))
	if err != nil {
		return 0, err
	}
	for _, check := range checks {
		result, event := s.checkConnection(ctx, check)
		if err := s.store.RecordHealthCheck(ctx, check, claim, result, event); err != nil && !errors.Is(err, domain.ErrConflict) {
			s.logger.ErrorContext(ctx, "record health check", "connection_id", check.ConnectionID, "error", err)
		}
	}
	return len(checks), nil
}

// checkConnection issues a heartbeat token, renewing it as needed, and asks
// the provider who the credential belongs to. A connection that issued a
// token recently with no failure reported since is skipped: real use is the
// heartbeat.
func (s *Service) checkConnection(ctx context.Context, check domain.HealthCheck) (domain.HealthResult, *domain.Event) {
	now := s.clock()
	next := now.Add(jitter(s.heartbeatInterval))
	connection, err := s.store.Connection(ctx, check.OrganizationID, check.ConnectionID)
	if err != nil {
		s.logger.WarnContext(ctx, "load connection for health check", "connection_id", check.ConnectionID, "error", err)
		return check.Skipped(now.Add(domain.MinHealthBackoff)), nil
	}
	recentlyUsed := connection.LastUsedAt != nil && now.Sub(*connection.LastUsedAt) < domain.RecentUse &&
		(connection.LastReportedAt == nil || connection.LastReportedAt.Before(*connection.LastUsedAt))
	if connection.Status != domain.StatusActive || recentlyUsed {
		return check.Skipped(next), nil
	}
	app, err := s.store.AnyApp(ctx, connection.OrganizationID, connection.AppID)
	if err != nil || app.Status != domain.AppStatusActive {
		// A disabled app blocks the connection; that is not the credential's
		// health.
		return check.Skipped(next), nil
	}
	provider, err := s.providerFor(app)
	if err != nil {
		return check.Skipped(next), nil
	}

	var result domain.HealthResult
	probed, err := s.probe(ctx, connection, app, provider)
	switch {
	case err == nil:
		result = check.Succeeded(now, next)
	case errors.Is(err, domain.ErrNeedsReconnect):
		// Renewal already recorded the permanent failure.
		return check.Skipped(next), nil
	case errors.Is(err, domain.ErrAccountChanged):
		_, _ = s.markNeedsReconnect(ctx, probed, domain.ReconnectAccountChanged)
		return check.Skipped(next), nil
	case errors.Is(err, domain.ErrCredentialRejected):
		_, _ = s.markNeedsReconnect(ctx, probed, checkRejection(connection.CredentialType, err))
		return check.Skipped(next), nil
	default:
		s.logger.WarnContext(ctx, "health check failed", "connection_id", connection.ID, "error", err)
		result = check.Failed(domain.ErrProviderUnavailable.Error(), now)
	}
	if !check.Changed(result) {
		return result, nil
	}
	event, err := s.event(connectionseventsv1.ConnectionHealthChangedDetailType, connectionseventsv1.ConnectionHealthChanged{
		ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, AppID: connection.AppID, Provider: connection.Provider,
		Health: string(result.State), PreviousHealth: string(check.State), Reason: result.Reason, CheckedAt: now,
	}, now)
	if err != nil {
		s.logger.ErrorContext(ctx, "encode health changed event", "connection_id", connection.ID, "error", err)
		return result, nil
	}
	return result, &event
}

// probe proves a connection's credential still works for the same account.
// It returns the connection as of the credential it checked, so a failure is
// recorded against that version: a renewal during the check moves it, and a
// reconnect meanwhile leaves the failure unrecorded.
func (s *Service) probe(ctx context.Context, connection domain.Connection, app domain.App, provider providers.Provider) (domain.Connection, error) {
	var account domain.ProviderAccount
	switch connection.CredentialType {
	case domain.CredentialOAuth2:
		grant, current, err := s.freshGrant(ctx, connection, app, provider)
		if err != nil {
			return connection, err
		}
		connection = current
		if account, err = s.oauth.Identity(ctx, *provider.OAuth2, grant); err != nil {
			return connection, err
		}
	case domain.CredentialOAuth2ClientCredentials:
		// Minting in the tenant is the check.
		_, current, err := s.freshGrant(ctx, connection, app, provider)
		if err != nil {
			return connection, err
		}
		return current, nil
	case domain.CredentialInstallation:
		sealed, err := s.connectionSecrets(ctx, connection)
		if err != nil {
			return connection, err
		}
		opened, err := s.appSecrets(ctx, app)
		if err != nil {
			return connection, err
		}
		if account, err = s.oauth.Installation(ctx, provider, githubApp(app, opened), sealed.InstallationID); err != nil {
			return connection, err
		}
	default:
		sealed, err := s.connectionSecrets(ctx, connection)
		if err != nil {
			return connection, err
		}
		if sealed.Key == nil {
			return connection, errors.New("key-based connection holds no key")
		}
		if account, err = s.oauth.KeyIdentity(ctx, provider, connection.CredentialType, *sealed.Key); err != nil {
			return connection, err
		}
	}
	if account.ID != connection.ProviderAccount.ID {
		return connection, domain.ErrAccountChanged
	}
	return connection, nil
}

func checkRejection(credentialType domain.CredentialType, err error) domain.ReconnectReason {
	switch {
	case errors.Is(err, providers.ErrInstallationNotFound):
		return domain.ReconnectInstallationRemoved
	case credentialType == domain.CredentialOAuth2:
		return domain.ReconnectUnauthorized
	default:
		return domain.ReconnectCredentialRejected
	}
}

// CleanUp removes expired sessions and long-published outbox events, then
// claims disconnected connections whose holds are released or expired, revokes
// their grants where the provider allows, and deletes their credentials. It
// returns how many connections it claimed.
func (s *Service) CleanUp(ctx context.Context) (int, error) {
	now := s.clock()
	if _, err := s.store.DeleteExpiredSessions(ctx, now.Add(-sessionRetention), sessionSweep); err != nil {
		s.logger.WarnContext(ctx, "delete expired sessions", "error", err)
	}
	if _, err := s.store.DeletePublishedOutbox(ctx, now.Add(-outboxRetention), outboxSweep); err != nil {
		s.logger.WarnContext(ctx, "delete published outbox events", "error", err)
	}
	lease, err := s.randomToken(18)
	if err != nil {
		return 0, err
	}
	connections, err := s.store.ClaimCredentialDeletions(ctx, now, workerBatch, lease, now.Add(workerLease))
	if err != nil {
		return 0, err
	}
	for _, connection := range connections {
		// A failed deletion keeps its lease, so it is tried again once the
		// lease runs out rather than on every run.
		if err := s.deleteCredentials(ctx, connection, lease); err != nil {
			s.logger.WarnContext(ctx, "delete connection credentials", "connection_id", connection.ID, "error", err)
		}
	}
	return len(connections), nil
}

func (s *Service) deleteCredentials(ctx context.Context, connection domain.Connection, lease string) error {
	if connection.CredentialType != domain.CredentialOAuth2 {
		return s.finishDeletion(ctx, connection, lease, false)
	}
	// Connections of one app and account share a grant at the provider: the
	// revocation decision and the deletion run one connection at a time, so the
	// last one deleted always sees it is the last and revokes.
	return s.store.WithProviderGrantLock(ctx, connection.OrganizationID, connection.AppID, connection.ProviderAccount.ID, func(ctx context.Context) error {
		revoked, err := s.revokeConnection(ctx, connection)
		if err != nil {
			if s.clock().Before(connection.StatusChangedAt.Add(domain.MaxHold + revokeGiveUp)) {
				return err
			}
			s.logger.WarnContext(ctx, "deleting credentials the provider did not revoke", "connection_id", connection.ID, "error", err)
			revoked = false
		}
		return s.finishDeletion(ctx, connection, lease, revoked)
	})
}

func (s *Service) finishDeletion(ctx context.Context, connection domain.Connection, lease string, revoked bool) error {
	now := s.clock()
	event, err := s.event(connectionseventsv1.ConnectionCredentialsDeletedDetailType, connectionseventsv1.ConnectionCredentialsDeleted{
		ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, AppID: connection.AppID, Provider: connection.Provider,
		Revoked: revoked, DeletedAt: now,
	}, now)
	if err != nil {
		return err
	}
	return s.store.DeleteCredentials(ctx, connection.OrganizationID, connection.ID, lease, now, event)
}

// revokeConnection revokes a disconnected connection's oauth2 grant at its
// provider. Keys cannot be revoked from here, and minted tokens expire on
// their own. A grant another connection of the same app and account still
// holds is left alone, because revoking it would cut that connection off.
func (s *Service) revokeConnection(ctx context.Context, connection domain.Connection) (bool, error) {
	if connection.CredentialType != domain.CredentialOAuth2 {
		return false, nil
	}
	app, err := s.store.AnyApp(ctx, connection.OrganizationID, connection.AppID)
	if err != nil {
		return false, err
	}
	provider, err := s.providerFor(app)
	if err != nil {
		return false, nil
	}
	shared, err := s.store.SharesProviderGrant(ctx, connection.OrganizationID, connection.AppID, connection.ProviderAccount.ID, connection.ID)
	if err != nil || shared {
		return false, err
	}
	sealed, err := s.connectionSecrets(ctx, connection)
	if err != nil {
		return false, err
	}
	if sealed.Grant == nil {
		return false, nil
	}
	opened, err := s.appSecrets(ctx, app)
	if err != nil {
		return false, err
	}
	revoked, err := s.oauth.Revoke(ctx, provider, providers.RevokeRequest{ClientID: app.ClientID, ClientSecret: opened.ClientSecret, Grant: *sealed.Grant})
	if err != nil {
		return false, fmt.Errorf("revoke grant: %w", err)
	}
	return revoked, nil
}

// jitter spreads work out by up to a tenth of interval either way.
func jitter(interval time.Duration) time.Duration {
	spread := int64(interval / 10)
	if spread <= 0 {
		return interval
	}
	return interval - time.Duration(spread) + time.Duration(rand.Int64N(2*spread+1))
}
