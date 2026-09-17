package application

import (
	"context"

	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	connectionsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/connections/v1"
	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// What knowledge holds outlives the connections and people it was read
// through, so it has to hear when one of those goes away. A source whose
// connection is gone is paused and keeps its documents, because the
// connection may come back; a source whose organization or owner is gone is
// retired and its documents retracted, because nothing about it can return.

// ConnectionDisconnected pauses what read through a connection that is gone.
func (s *Service) ConnectionDisconnected(ctx context.Context, event connectionsv1.ConnectionDisconnected) error {
	s.connections.Forget(event.ConnectionID)
	return s.pause(ctx, "connection_id", event.ConnectionID, knowledgev1.PausedReasonDisconnected)
}

// AppDeleted pauses everything read through an app's connections. Connections
// may not announce each one separately, so the app is followed as well.
func (s *Service) AppDeleted(ctx context.Context, event connectionsv1.AppDeleted) error {
	return s.pause(ctx, "app_id", event.AppID, knowledgev1.PausedReasonAppDeleted)
}

// ConnectionNeedsReconnect pauses a source before its next run fails, rather
// than letting it discover the same thing the hard way.
func (s *Service) ConnectionNeedsReconnect(ctx context.Context, event connectionsv1.ConnectionNeedsReconnect) error {
	s.connections.Forget(event.ConnectionID)
	return s.pause(ctx, "connection_id", event.ConnectionID, knowledgev1.PausedReasonNeedsReconnect)
}

// OrganizationDeleted retires everything an organization had indexed.
func (s *Service) OrganizationDeleted(ctx context.Context, event authv1.OrganizationDeleted) error {
	return s.retire(ctx, event.OrganizationID, "", knowledgev1.RemovedReasonOrganizationDeleted)
}

// MemberRemoved retires the sources a departing member registered. Extraction
// reads as them, so nothing they set up can keep running once they are gone.
func (s *Service) MemberRemoved(ctx context.Context, event authv1.MemberRemoved) error {
	return s.retire(ctx, event.OrganizationID, event.UserID, knowledgev1.RemovedReasonMemberLeft)
}

func (s *Service) pause(ctx context.Context, column, value, reason string) error {
	paused, err := s.store.PauseSources(ctx, column, value, reason, s.now(),
		func(source domain.Source) (domain.Event, error) { return s.sourcePaused(source, reason) })
	if err != nil {
		return err
	}
	if len(paused) > 0 {
		s.logger.InfoContext(ctx, "sources paused", "count", len(paused), "reason", reason)
	}
	return nil
}

func (s *Service) retire(ctx context.Context, organizationID, createdBy, reason string) error {
	removed, err := s.store.RemoveSources(ctx, organizationID, createdBy, s.now(), s.retractions(reason))
	if err != nil {
		return err
	}
	if len(removed) > 0 {
		s.logger.InfoContext(ctx, "sources retired", "count", len(removed), "reason", reason)
	}
	return nil
}

// AlreadyHandled says whether an event has already been acted on
// successfully. It records nothing itself, so the caller can check this
// before running a handler and record success only once the handler has
// returned it.
func (s *Service) AlreadyHandled(ctx context.Context, eventID string) (bool, error) {
	return s.store.EventHandled(ctx, eventID)
}

// MarkHandled records that an event's handler has succeeded, so a later
// redelivery finds it via AlreadyHandled and does not run the handler again.
// It must be called only after the handler succeeded: recording it first
// would mean a handler that then fails is never retried, since the
// redelivery would see the event already marked handled and skip it.
func (s *Service) MarkHandled(ctx context.Context, eventID, detailType string) error {
	return s.store.MarkEventHandled(ctx, eventID, detailType, s.now())
}
