package application

import (
	"context"
	"errors"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	connectionseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// OrganizationDeleted disconnects every connection of a deleted organization
// and deletes its apps. Redelivery changes nothing.
func (s *Service) OrganizationDeleted(ctx context.Context, event autheventsv1.OrganizationDeleted) error {
	if event.OrganizationID == "" {
		return errors.New("organization deleted event names no organization")
	}
	now := s.clock()
	if _, err := s.store.DisconnectConnections(ctx, event.OrganizationID, domain.OwnerFilter{}, domain.DisconnectOrganizationDeleted, now,
		s.disconnectedEvent("", now)); err != nil {
		return err
	}
	_, err := s.store.DeleteOrganizationApps(ctx, event.OrganizationID, now, s.appDeletedEvent(connectionseventsv1.AppDeletedReasonOrganizationDeleted, now))
	return err
}

// MemberRemoved disconnects the member-scoped connections of a member who
// left or was removed. Team and organization connections they created stay.
// A removal because the organization was deleted is left to
// OrganizationDeleted.
func (s *Service) MemberRemoved(ctx context.Context, event autheventsv1.MemberRemoved) error {
	if event.Reason == autheventsv1.MemberRemovedReasonOrganizationDeleted {
		return nil
	}
	if event.OrganizationID == "" || event.UserID == "" {
		return errors.New("member removed event names no organization or member")
	}
	now := s.clock()
	_, err := s.store.DisconnectConnections(ctx, event.OrganizationID, domain.OwnerFilter{MemberID: event.UserID},
		domain.DisconnectMemberLeft, now, s.disconnectedEvent("", now))
	return err
}

// TeamDeleted disconnects a deleted team's connections.
func (s *Service) TeamDeleted(ctx context.Context, event teamseventsv1.TeamDeleted) error {
	if event.OrganizationID == "" || event.TeamID == "" {
		return errors.New("team deleted event names no organization or team")
	}
	now := s.clock()
	_, err := s.store.DisconnectConnections(ctx, event.OrganizationID, domain.OwnerFilter{TeamID: event.TeamID},
		domain.DisconnectTeamDeleted, now, s.disconnectedEvent("", now))
	return err
}

func (s *Service) appDeletedEvent(reason string, at time.Time) func(domain.App) (domain.Event, error) {
	return func(app domain.App) (domain.Event, error) {
		return s.event(connectionseventsv1.AppDeletedDetailType, connectionseventsv1.AppDeleted{
			AppID: app.ID, OrganizationID: app.OrganizationID, Provider: app.Provider, Reason: reason, DeletedAt: at,
		}, at)
	}
}
