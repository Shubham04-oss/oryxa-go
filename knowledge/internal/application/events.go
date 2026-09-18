package application

import (
	"context"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	teamsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// What knowledge holds outlives the connections and people it was read
// through, so it has to hear when one of those goes away. A source whose
// connection expired is paused and keeps its documents, because reconnecting
// brings it back; a source whose connection was removed for good, or whose
// organization or owner is gone, is retired and its documents retracted,
// because nothing about it can return.

// ConnectionExpired pauses what reads through a connection whose credential
// can no longer be renewed, before its next run fails the same way.
func (s *Service) ConnectionExpired(ctx context.Context, event appsv1.ConnectionExpired) error {
	return s.pause(ctx, event.ConnectionID, knowledgev1.PausedReasonNeedsReconnect)
}

// ConnectionRemoved retires what read through a connection that is gone for
// good, and retracts what it held: unlike an expired one, this connection is
// not coming back.
func (s *Service) ConnectionRemoved(ctx context.Context, event appsv1.ConnectionRemoved) error {
	return s.retireByConnection(ctx, event.ConnectionID, knowledgev1.RemovedReasonConnectionRemoved)
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

// TeamDeleted retires what a deleted team held. Rag removes the team's
// corpora when it hears the same event; left running, the team's sources
// would publish their documents again and rag would build the deleted team a
// new corpus from them.
func (s *Service) TeamDeleted(ctx context.Context, event teamsv1.TeamDeleted) error {
	removed, err := s.store.RemoveSourcesByTeam(ctx, event.OrganizationID, event.TeamID, s.now(),
		s.retractions(knowledgev1.RemovedReasonTeamDeleted))
	if err != nil {
		return err
	}
	if len(removed) > 0 {
		s.logger.InfoContext(ctx, "sources retired", "count", len(removed), "reason", knowledgev1.RemovedReasonTeamDeleted)
	}
	return nil
}

func (s *Service) pause(ctx context.Context, connectionID, reason string) error {
	paused, err := s.store.PauseSources(ctx, connectionID, reason, s.now(),
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

func (s *Service) retireByConnection(ctx context.Context, connectionID, reason string) error {
	removed, err := s.store.RemoveSourcesByConnection(ctx, connectionID, s.now(), s.retractions(reason))
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
