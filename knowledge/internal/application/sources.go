package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// RegisterInput is what a member says to have something indexed.
type RegisterInput struct {
	ConnectionID string
	Provider     string
	Target       map[string]string
	Interval     time.Duration
}

// RegisterSource records something to index and schedules its first run.
//
// Everything that could make a later run impossible is checked now, while a
// member is present to be told: a provider nothing can read, a target the
// extractor would not understand, a connection that is not usable. The worst
// outcome is a source that looks registered and quietly never produces
// anything.
func (s *Service) RegisterSource(ctx context.Context, credentials authclient.Credentials, organizationID string, input RegisterInput) (domain.Source, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Source{}, err
	}
	if caller.Agent {
		return domain.Source{}, fmt.Errorf("an agent cannot register a source: %w", domain.ErrForbidden)
	}

	provider := strings.TrimSpace(input.Provider)
	extractor, err := s.extractors.Extractor(provider)
	if err != nil {
		return domain.Source{}, err
	}
	target, err := domain.CheckTarget(input.Target)
	if err != nil {
		return domain.Source{}, err
	}
	if err := extractor.Target(target); err != nil {
		return domain.Source{}, err
	}
	interval, err := domain.CheckInterval(input.Interval)
	if err != nil {
		return domain.Source{}, err
	}

	source := domain.Source{
		OrganizationID: organizationID,
		Provider:       provider,
		Target:         target,
		Scope:          domain.ScopeOrganization,
		CreatedBy:      caller.UserID,
		Status:         domain.StatusActive,
		Interval:       interval,
	}
	if connectionID := strings.TrimSpace(input.ConnectionID); connectionID != "" {
		connection, err := s.usableConnection(ctx, organizationID, connectionID, credentials.Authorization, provider)
		if err != nil {
			return domain.Source{}, err
		}
		source.ConnectionID = connection.ID
		source.AppID = connection.AppID
		source.Scope = domain.Scope(connection.Scope)
		source.TeamID = connection.TeamID
		source.MemberID = connection.MemberID
	}
	teamID, memberID, err := domain.Place(source.Scope, source.TeamID, source.MemberID)
	if err != nil {
		return domain.Source{}, err
	}
	source.TeamID, source.MemberID = teamID, memberID

	if source.ID, err = newID("src"); err != nil {
		return domain.Source{}, err
	}
	now := s.now()
	source.CreatedAt, source.UpdatedAt = now, now
	// Due immediately: a member who just asked for something to be indexed
	// should not wait an interval for the first run.
	source.SyncAt = &now

	if err := s.store.CreateSource(ctx, source, nil); err != nil {
		return domain.Source{}, err
	}
	s.logger.InfoContext(ctx, "source registered",
		"source_id", source.ID, "organization_id", organizationID, "provider", provider)
	return source, nil
}

// usableConnection refuses now what a background run could not do later.
func (s *Service) usableConnection(ctx context.Context, organizationID, connectionID, authorization, provider string) (domain.Connection, error) {
	connection, err := s.connections.Connection(ctx, organizationID, connectionID, authorization)
	if err != nil {
		return domain.Connection{}, err
	}
	if connection.Provider != provider {
		return domain.Connection{}, fmt.Errorf("the connection is for %s, not %s: %w",
			connection.Provider, provider, domain.ErrInvalid)
	}
	if connection.Status != "active" {
		return domain.Connection{}, fmt.Errorf("the connection is %s: %w", connection.Status, domain.ErrConflict)
	}
	// Connections refuses a bare service every purpose but cleanup on an
	// organization connection its admins reserved. Extraction runs long after
	// the member has gone, with no token to forward, so such a source could
	// only ever fail.
	if connection.Scope == string(domain.ScopeOrganization) && connection.Usage == domain.UsageAdmins {
		return domain.Connection{}, fmt.Errorf(
			"this connection is reserved for admins, which a background reader cannot act as: %w", domain.ErrForbidden)
	}
	return connection, nil
}

// Sources lists what an organization has asked to index. An org admin sees
// all of it; anyone else sees what they registered.
func (s *Service) Sources(ctx context.Context, credentials authclient.Credentials, organizationID string, query domain.SourceQuery) ([]domain.Source, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	query.Admin = query.Admin && caller.Role.IsOrgAdmin()
	query.CreatedBy = caller.UserID
	return s.store.Sources(ctx, organizationID, query)
}

// Source reads one, for its creator or an org admin.
func (s *Service) Source(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string) (domain.Source, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Source{}, err
	}
	source, err := s.store.Source(ctx, organizationID, sourceID)
	if err != nil {
		return domain.Source{}, err
	}
	if source.CreatedBy != caller.UserID && !caller.Role.IsOrgAdmin() {
		// Answering not-found tells the caller nothing they did not know.
		return domain.Source{}, domain.ErrNotFound
	}
	return source, nil
}

// Runs reads how a source's recent passes went, so a member can see why
// nothing new arrived.
func (s *Service) Runs(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string, limit int) ([]domain.SyncRun, error) {
	if _, err := s.Source(ctx, credentials, organizationID, sourceID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxRuns {
		limit = maxRuns
	}
	return s.store.Runs(ctx, organizationID, sourceID, limit)
}

const maxRuns = 50

// RemoveSource stops indexing something and retracts what it held, so a
// retrieval pipeline forgets it too.
func (s *Service) RemoveSource(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string) error {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return err
	}
	if caller.Agent {
		return fmt.Errorf("an agent cannot remove a source: %w", domain.ErrForbidden)
	}
	source, err := s.store.Source(ctx, organizationID, sourceID)
	if err != nil {
		return err
	}
	if source.CreatedBy != caller.UserID && !caller.Role.IsOrgAdmin() {
		// Answering not-found tells the caller nothing they did not know.
		return domain.ErrNotFound
	}
	return s.store.RemoveSource(ctx, organizationID, sourceID, s.now(), s.retractions(knowledgev1.RemovedReasonSourceRemoved))
}

// Document reads one document's body, for the caller fetching what an event
// was too small to carry.
func (s *Service) Document(ctx context.Context, credentials authclient.Credentials, organizationID, documentID string) (domain.Document, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Document{}, err
	}
	document, err := s.store.Document(ctx, organizationID, documentID)
	if err != nil {
		return domain.Document{}, err
	}
	source, err := s.store.Source(ctx, organizationID, document.SourceID)
	if err != nil {
		return domain.Document{}, err
	}
	if source.CreatedBy != caller.UserID && !caller.Role.IsOrgAdmin() {
		return domain.Document{}, domain.ErrNotFound
	}
	return document, nil
}

// retractions builds the events that say a source's documents are gone.
func (s *Service) retractions(reason string) func(domain.Source, []domain.Document) ([]domain.Event, error) {
	return func(source domain.Source, documents []domain.Document) ([]domain.Event, error) {
		events := make([]domain.Event, 0, len(documents))
		for _, document := range documents {
			event, err := s.documentRemoved(source, document, reason)
			if err != nil {
				return nil, err
			}
			events = append(events, event)
		}
		return events, nil
	}
}

// pausedBy is the reason a source stopped, from what went wrong reading it.
func pausedBy(err error) (string, bool) {
	switch {
	case errors.Is(err, domain.ErrNeedsReconnect):
		return knowledgev1.PausedReasonNeedsReconnect, true
	case errors.Is(err, domain.ErrScopeRequired):
		return knowledgev1.PausedReasonScopeRequired, true
	default:
		return "", false
	}
}
