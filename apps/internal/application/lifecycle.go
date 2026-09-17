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

// OrganizationDeleted removes every connection an organization had.
func (s *Service) OrganizationDeleted(ctx context.Context, event authv1.OrganizationDeleted) error {
	return s.removeAll(ctx, event.OrganizationID, "", appsv1.RemovedOrganizationDeleted)
}

// MemberRemoved removes the connections a departing member made.
func (s *Service) MemberRemoved(ctx context.Context, event authv1.MemberRemoved) error {
	return s.removeAll(ctx, event.OrganizationID, event.UserID, appsv1.RemovedMemberLeft)
}

func (s *Service) removeAll(ctx context.Context, organizationID, memberID, reason string) error {
	connections, err := s.store.Connections(ctx, organizationID, domain.ConnectionQuery{MemberID: memberID})
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
	return errors.Join(failures...)
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
// member never came back from Composio, and forgets deliveries old enough
// never to be retried.
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
	if failed, err := s.store.FailPendingConnectionsBefore(ctx, now.Add(-pendingLinkLifetime), now); err != nil {
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
}
