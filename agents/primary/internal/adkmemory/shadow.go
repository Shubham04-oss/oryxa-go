package adkmemory

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
)

// Shadow returns a [Backend] that answers from primary and also writes to
// secondary, for running a new backend alongside the one in use before
// moving to it: secondary fills up with what is remembered from now on while
// every search is still answered as before.
//
// Forget runs on both, so the secondary never keeps what the primary was
// told to forget. Nothing secondary does fails a call: its errors are
// logged, and the call's outcome is the primary's alone.
func Shadow(primary, secondary Backend, logger *slog.Logger) (Backend, error) {
	if primary == nil || secondary == nil {
		return nil, errors.New("adkmemory: a shadow needs a primary and a secondary backend")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &shadow{primary: primary, secondary: secondary, logger: logger}, nil
}

type shadow struct {
	primary, secondary Backend
	logger             *slog.Logger
}

var _ Backend = (*shadow)(nil)

// Write implements [Backend]. It writes to the secondary whether or not the
// primary took the conversation, so a primary outage does not leave a gap in
// the secondary as well.
func (s *shadow) Write(ctx context.Context, scope Scope, conversation session.Session) error {
	err := s.primary.Write(ctx, scope, conversation)
	if shadowErr := s.secondary.Write(ctx, scope, conversation); shadowErr != nil {
		s.logger.WarnContext(ctx, "the shadow memory did not take a conversation",
			"organization_id", scope.OrganizationID, "error", shadowErr)
	}
	return err
}

// Search implements [Backend]. Only the primary answers.
func (s *shadow) Search(ctx context.Context, scope Scope, query string) ([]memory.Entry, error) {
	return s.primary.Search(ctx, scope, query)
}

// Forget implements [Backend].
func (s *shadow) Forget(ctx context.Context, organizationID, userID string) error {
	err := s.primary.Forget(ctx, organizationID, userID)
	if shadowErr := s.secondary.Forget(ctx, organizationID, userID); shadowErr != nil {
		s.logger.WarnContext(ctx, "the shadow memory did not forget everything it was asked to",
			"organization_id", organizationID, "error", shadowErr)
	}
	return err
}
