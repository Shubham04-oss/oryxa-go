package application

import (
	"context"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"
	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// These handle the events that end subscriptions. Each is safe to deliver
// again: a subscription already stopped is not stopped twice.

// OrganizationDeleted stops the organization's subscriptions and forgets its
// app webhooks.
func (s *Service) OrganizationDeleted(ctx context.Context, event autheventsv1.OrganizationDeleted) error {
	if _, err := s.stop(ctx, domain.SubscriptionSelector{OrganizationID: event.OrganizationID},
		integrationsv1.SubscriptionStoppedReasonOrganizationDeleted, ""); err != nil {
		return err
	}
	return s.store.DeleteAppWebhooks(ctx, event.OrganizationID, "")
}

// MemberRemoved stops the subscriptions a member made once they leave the
// organization, since their work runs as them. An organization's deletion is
// handled by OrganizationDeleted.
func (s *Service) MemberRemoved(ctx context.Context, event autheventsv1.MemberRemoved) error {
	if event.Reason == autheventsv1.MemberRemovedReasonOrganizationDeleted {
		return nil
	}
	_, err := s.stop(ctx, domain.SubscriptionSelector{OrganizationID: event.OrganizationID, CreatedBy: event.UserID},
		integrationsv1.SubscriptionStoppedReasonMemberLeft, "")
	return err
}

// TeamMemberRemoved stops the subscriptions a member made on a team's
// connections once they leave the team.
func (s *Service) TeamMemberRemoved(ctx context.Context, event teamseventsv1.MemberRemoved) error {
	_, err := s.stop(ctx, domain.SubscriptionSelector{OrganizationID: event.OrganizationID, CreatedBy: event.UserID, TeamID: event.TeamID},
		integrationsv1.SubscriptionStoppedReasonTeamMemberLeft, "")
	return err
}

// ConnectionDisconnected stops a disconnected connection's subscriptions.
// Connections holds its credentials until their registrations are removed.
func (s *Service) ConnectionDisconnected(ctx context.Context, event connectionseventsv1.ConnectionDisconnected) error {
	_, err := s.stop(ctx, domain.SubscriptionSelector{OrganizationID: event.OrganizationID, ConnectionID: event.ConnectionID},
		integrationsv1.SubscriptionStoppedReasonDisconnected, event.DisconnectedBy)
	return err
}

// AppDeleted forgets a deleted app's webhook. Connections disconnects the
// app's connections, which stops their subscriptions.
func (s *Service) AppDeleted(ctx context.Context, event connectionseventsv1.AppDeleted) error {
	return s.store.DeleteAppWebhooks(ctx, event.OrganizationID, event.AppID)
}
