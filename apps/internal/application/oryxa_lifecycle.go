package application

import (
	"context"
	"errors"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
)

// removeOryxa disconnects an Oryxa connection: signin revokes its credential
// at the app and forgets it, its webhook endpoints are deleted, and the
// connection and its triggers are recorded removed, announcing whether the
// app confirmed the revocation. An app that does not answer never blocks the
// removal: signin keeps the sealed credential and tries again once the
// connection has ended.
func (s *Service) removeOryxa(ctx context.Context, connection domain.Connection, reason string) error {
	var providerRevoked *bool
	if s.oryxa == nil {
		// Nothing here can revoke it now. The connection goes all the same,
		// and signin revokes what it still holds for any removed connection
		// once this deployment runs it again.
		s.logger.ErrorContext(ctx, "an Oryxa connection was removed where Oryxa's sign-in is not configured; its credential is revoked once it is",
			"connection_id", connection.ID, "toolkit", connection.Toolkit)
	} else {
		revocation, err := s.oryxa.Signin.Revoke(ctx, connection.ID)
		if err != nil {
			return err
		}
		revoked := revocation.ProviderRevoked
		providerRevoked = &revoked
		if revocation.Pending {
			s.logger.WarnContext(ctx, "the app did not answer a revocation; it is tried again once the connection is removed",
				"connection_id", connection.ID, "toolkit", connection.Toolkit)
		}
		endpoints, err := s.oryxa.Hooks.EndpointsForConnection(ctx, connection.ID)
		if err != nil {
			return err
		}
		// Their subscriptions at the app are the integration's to remove, and
		// only it can: the endpoint is an id, not something the app knows
		// about.
		s.unsubscribeEndpoints(ctx, connection.ID, connection.Toolkit, endpoints, "the connection was removed")
		deleted, err := s.oryxa.Hooks.DeleteForConnection(ctx, connection.ID)
		if err != nil {
			return err
		}
		if len(deleted) > 0 {
			s.logger.InfoContext(ctx, "an Oryxa connection's webhook endpoints were deleted with it",
				"connection_id", connection.ID, "count", len(deleted))
		}
	}
	now := s.now().UTC()
	removed, err := s.store.RemoveConnection(ctx, connection.ID, reason, now, func(removed domain.Connection, _ []domain.Trigger) ([]domain.Event, error) {
		event, err := s.event(appsv1.ConnectionRemovedDetailType, appsv1.ConnectionRemoved{
			ConnectionID: removed.ID, OrganizationID: removed.OrganizationID, MemberID: removed.MemberID,
			Toolkit: removed.Toolkit, Broker: appsv1.BrokerOryxa, Reason: reason,
			ProviderRevoked: providerRevoked, RemovedAt: now,
		})
		if err != nil {
			return nil, err
		}
		return []domain.Event{event}, nil
	})
	if err != nil {
		return err
	}
	if removed {
		revokedAtApp := providerRevoked != nil && *providerRevoked
		s.logger.InfoContext(ctx, "app connection removed", "connection_id", connection.ID,
			"organization_id", connection.OrganizationID, "reason", reason, "broker", domain.BrokerOryxa,
			"provider_revoked", revokedAtApp)
	}
	return nil
}

const (
	// oryxaEvery is how often RunOryxa looks for work.
	oryxaEvery = time.Minute
	// abandonAfter is how long a pending Oryxa connection waits before it is
	// failed as abandoned: its sign-in's flow lasts signin.FlowLifetime, and
	// a key is kept within the request that brought it.
	abandonAfter = signin.FlowLifetime + 5*time.Minute
	// checkBatch and checkPasses bound one pass over due health checks.
	checkBatch  = 50
	checkPasses = 10
	// revokeBatch bounds one pass over revocations to try again.
	revokeBatch = 50
)

// RunOryxa keeps Oryxa's connections in order until ctx is cancelled: it
// fails sign-ins their members never finished, runs the daily health checks
// that are due and expires the connections an app refused, tries again the
// revocations an app did not answer, deletes webhook endpoints whose
// subscriptions have lapsed, and asks the apps that push nothing for the
// events they owe. Every replica may run it: signin claims each check and
// revocation first, and the store claims each poll.
func (s *Service) RunOryxa(ctx context.Context) {
	if s.oryxa == nil {
		return
	}
	for {
		s.maintainOryxa(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(oryxaEvery):
		}
	}
}

func (s *Service) maintainOryxa(ctx context.Context) {
	s.saveDefaultClients(ctx)
	s.failAbandonedSignins(ctx)
	s.checkOryxaConnections(ctx)
	if tried, err := s.oryxa.Signin.RetryRevocations(ctx, revokeBatch); err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "retry app revocations", "error", err)
		}
	} else if tried > 0 {
		s.logger.InfoContext(ctx, "app revocations tried again", "count", tried)
	}
	if _, err := s.oryxa.Hooks.SweepExpired(ctx); err != nil && ctx.Err() == nil {
		s.logger.ErrorContext(ctx, "sweep lapsed webhook endpoints", "error", err)
	}
	s.pollTriggers(ctx)
}

// saveDefaultClients saves Oryxa's own OAuth apps as configured, until every
// one has been saved once: saving one unchanged changes nothing, a new
// secret for the same client id is saved over it, and another client id
// makes a new live client while connections made through the old one keep
// refreshing and revoking through it. A default no longer configured is left
// as it is, for an operator to retire.
func (s *Service) saveDefaultClients(ctx context.Context) {
	s.defaultsMu.Lock()
	defer s.defaultsMu.Unlock()
	if s.defaultsSaved {
		return
	}
	saved := true
	for _, client := range s.oryxa.DefaultClients {
		view, changed, err := s.oryxa.Signin.PutClient(ctx, signin.ClientInput{
			Client: client.Client, ClientID: client.ClientID, ClientSecret: client.ClientSecret,
			Fields: client.Fields, ActorID: defaultClientActor,
		})
		if errors.Is(err, domain.ErrConflict) {
			// Another replica saved it first; saving again finds it.
			view, changed, err = s.oryxa.Signin.PutClient(ctx, signin.ClientInput{
				Client: client.Client, ClientID: client.ClientID, ClientSecret: client.ClientSecret,
				Fields: client.Fields, ActorID: defaultClientActor,
			})
		}
		if err != nil {
			saved = false
			if ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "save one of Oryxa's own OAuth apps", "client", client.Client, "error", err)
			}
			continue
		}
		if changed {
			s.logger.InfoContext(ctx, "one of Oryxa's own OAuth apps saved", "client", client.Client, "client_id", view.ClientID)
			if err := s.announceClient(ctx, appsv1.OAuthClientSavedDetailType, "", client.Client, view.ClientID, defaultClientActor); err != nil {
				s.logger.ErrorContext(ctx, "announce one of Oryxa's own OAuth apps", "client", client.Client, "error", err)
			}
		}
	}
	s.defaultsSaved = saved
}

// defaultClientActor is who saves Oryxa's own OAuth apps: the deployment's
// configuration, not a member.
const defaultClientActor = "deployment"

// forgetOrganization retires what an organization kept for Oryxa's own
// sign-in once it is deleted: its own OAuth clients, which nobody can sign in
// through any more, and the endpoints its own apps delivered to. Its
// connections have gone already, each revoked at its app, and a retired
// client still revokes what signin retries. It is best effort: what cannot
// be retired now is logged, never a reason to handle the deletion again.
func (s *Service) forgetOrganization(ctx context.Context, organizationID string) {
	if s.oryxa == nil || organizationID == "" {
		return
	}
	registrations := map[string]bool{}
	for _, toolkit := range s.oryxa.Integrations.Toolkits() {
		registered, ok := s.oryxa.Integrations.Lookup(toolkit)
		if !ok {
			continue
		}
		manifest := registered.Manifest()
		for _, method := range manifest.Auth.Methods {
			if method.OAuth2 != nil && method.OAuth2.Client != "" {
				registrations[method.OAuth2.Client] = true
			}
		}
		if !organizationEndpoints(manifest.Webhook) {
			continue
		}
		endpoint, err := s.oryxa.Hooks.OrganizationEndpoint(ctx, organizationID, toolkit)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err == nil {
			err = s.oryxa.Hooks.Delete(ctx, endpoint.ID)
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.logger.ErrorContext(ctx, "delete a deleted organization's webhook endpoint",
				"organization_id", organizationID, "toolkit", toolkit, "error", err)
		}
	}
	for client := range registrations {
		if _, err := s.oryxa.Signin.RemoveClient(ctx, organizationID, client); err != nil {
			s.logger.ErrorContext(ctx, "retire a deleted organization's OAuth client",
				"organization_id", organizationID, "client", client, "error", err)
		}
	}
}

// failAbandonedSignins fails the Oryxa sign-ins whose flow has run out, so the
// member's one-current-connection slot is theirs again.
func (s *Service) failAbandonedSignins(ctx context.Context) {
	now := s.now().UTC()
	abandoned := func(connections []domain.Connection) ([]domain.Event, error) {
		events := make([]domain.Event, 0, len(connections))
		for _, connection := range connections {
			event, err := s.event(appsv1.ConnectionFailedDetailType, appsv1.ConnectionFailed{
				ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
				Toolkit: connection.Toolkit, Broker: appsv1.BrokerOryxa, Reason: appsv1.FailedAbandoned, FailedAt: now,
			})
			if err != nil {
				return nil, err
			}
			events = append(events, event)
		}
		return events, nil
	}
	failed, err := s.oryxa.Store.FailPendingOryxaConnectionsBefore(ctx, now.Add(-abandonAfter), now, abandoned)
	switch {
	case err != nil && ctx.Err() == nil:
		s.logger.ErrorContext(ctx, "fail abandoned app sign-ins", "error", err)
	case failed > 0:
		s.logger.InfoContext(ctx, "abandoned app sign-ins failed", "count", failed)
	}
}

// checkOryxaConnections runs the health checks that are due, and expires each
// connection whose app refused its credential for good.
func (s *Service) checkOryxaConnections(ctx context.Context) {
	for range checkPasses {
		checks, err := s.oryxa.Signin.CheckDue(ctx, checkBatch)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "run app connections' health checks", "error", err)
			}
			return
		}
		for _, check := range checks {
			if check.Outcome == signin.CheckRejected {
				s.expireOryxa(ctx, check)
			}
		}
		if len(checks) < checkBatch {
			return
		}
	}
}

// expireOryxa marks an Oryxa connection expired because its app refused its
// credential, and announces why.
func (s *Service) expireOryxa(ctx context.Context, check signin.Check) {
	reason := check.Reason
	if reason == "" {
		reason = appsv1.ExpiredCredentialRejected
	}
	now := s.now().UTC()
	event, err := s.event(appsv1.ConnectionExpiredDetailType, appsv1.ConnectionExpired{
		ConnectionID: check.ConnectionID, OrganizationID: check.OrganizationID, MemberID: check.MemberID,
		Toolkit: check.Toolkit, Broker: appsv1.BrokerOryxa, Reason: reason, ExpiredAt: now,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "announce an expired app connection", "connection_id", check.ConnectionID, "error", err)
		return
	}
	expired, err := s.store.ExpireConnection(ctx, check.ConnectionID, expiredProse(reason), now, event)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "expire an app connection", "connection_id", check.ConnectionID, "error", err)
		}
	case expired:
		s.logger.InfoContext(ctx, "app connection expired", "connection_id", check.ConnectionID,
			"toolkit", check.Toolkit, "reason", reason, "broker", domain.BrokerOryxa)
	}
}
