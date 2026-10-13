package application

import (
	"context"
	"errors"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

// A connection is a member's standing access to their app, so it must not
// outlive their place in the organization: when they leave, or the
// organization goes, every connection they made there is deleted at Composio
// and its credential revoked.

// OrganizationDeleted removes every connection an organization had, and
// retires what it kept for Oryxa's own sign-in.
func (s *Service) OrganizationDeleted(ctx context.Context, event authv1.OrganizationDeleted) error {
	if err := s.removeAll(ctx, event.OrganizationID, "", appsv1.RemovedOrganizationDeleted); err != nil {
		return err
	}
	s.forgetOrganization(ctx, event.OrganizationID)
	return nil
}

// MemberRemoved removes the connections a departing member made.
func (s *Service) MemberRemoved(ctx context.Context, event authv1.MemberRemoved) error {
	return s.removeAll(ctx, event.OrganizationID, event.UserID, appsv1.RemovedMemberLeft)
}

func (s *Service) removeAll(ctx context.Context, organizationID, memberID, reason string) error {
	// Removed connections are listed too, so a retry after a Pipedream user
	// could not be deleted still knows the member had one there.
	connections, err := s.store.Connections(ctx, organizationID, domain.ConnectionQuery{MemberID: memberID, Statuses: everyStatus})
	if err != nil {
		return err
	}
	var failures []error
	for _, connection := range connections {
		// One connection Composio will not delete right now must not keep the
		// rest; the event is retried, and removing is idempotent.
		if err := s.remove(ctx, connection, reason); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return s.forgetPipedreamUsers(ctx, organizationID, connections)
}

// AlreadyHandled says whether a bus event has already been acted on. It
// records nothing, so the caller can check before running a handler and
// record only once the handler succeeded.
func (s *Service) AlreadyHandled(ctx context.Context, eventID string) (bool, error) {
	return s.store.EventHandled(ctx, eventID)
}

// MarkHandled records that a bus event's handler succeeded.
func (s *Service) MarkHandled(ctx context.Context, eventID, detailType string) error {
	return s.store.MarkEventHandled(ctx, eventID, detailType, s.now().UTC())
}

// handledRetention is how long a handled delivery or event is remembered,
// comfortably longer than Composio and the bus retry one.
const handledRetention = 7 * 24 * time.Hour

// maintenanceEvery is how often the sweep runs.
const maintenanceEvery = 15 * time.Minute

// RunMaintenance sweeps until ctx is cancelled: it fails connections whose
// member never came back from Composio or Pipedream, forgets deliveries old
// enough never to be retried, and once a day checks Pipedream's accounts.
func (s *Service) RunMaintenance(ctx context.Context) {
	for {
		s.maintain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(maintenanceEvery):
		}
	}
}

func (s *Service) maintain(ctx context.Context) {
	now := s.now().UTC()
	// A member who started connecting and never came back is as much an
	// ending as a refusal, and the only one nothing else would report.
	abandoned := func(connections []domain.Connection) ([]domain.Event, error) {
		events := make([]domain.Event, 0, len(connections))
		for _, connection := range connections {
			event, err := s.event(appsv1.ConnectionFailedDetailType, appsv1.ConnectionFailed{
				ConnectionID: connection.ID, OrganizationID: connection.OrganizationID,
				MemberID: connection.MemberID, Toolkit: connection.Toolkit, Broker: eventBroker(connection),
				Reason: appsv1.FailedAbandoned, FailedAt: now,
			})
			if err != nil {
				return nil, err
			}
			events = append(events, event)
		}
		return events, nil
	}
	if failed, err := s.store.FailPendingConnectionsBefore(ctx, now.Add(-pendingLinkLifetime), now, abandoned); err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "fail abandoned connections", "error", err)
		}
	} else if failed > 0 {
		s.logger.InfoContext(ctx, "abandoned connections failed", "count", failed)
	}
	if forgotten, err := s.store.DeleteHandledEventsBefore(ctx, now.Add(-handledRetention)); err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "forget handled deliveries", "error", err)
		}
	} else if forgotten > 0 {
		s.logger.InfoContext(ctx, "handled deliveries forgotten", "count", forgotten)
	}
	s.checkPipedreamHealth(ctx)
}
