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

// filesProvider is the one source that reads the organization's own storage
// rather than a third-party app, so it takes no connection.
const filesProvider = "files"

// RegisterSource records something to index and schedules its first run.
//
// Everything that could make a later run impossible is checked now, while a
// member is present to be told: a provider nothing can read, a target the
// extractor would not understand, or a connection that is not usable. The
// worst outcome is a source that looks registered and quietly never produces
// anything.
func (s *Service) RegisterSource(ctx context.Context, credentials authclient.Credentials, organizationID string, input RegisterInput) (domain.Source, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Source{}, err
	}
	if caller.Agent {
		return domain.Source{}, fmt.Errorf("an agent cannot register a source: %w", domain.ErrForbidden)
	}
	return s.register(ctx, credentials, caller, organizationID, input)
}

// registerFor registers a source knowledge chose to index for a member —
// what their newly connected app holds, or their organization's files —
// with the token their standing grant redeems to.
//
// That token is an agent's: auth answers every grant-redeemed token as one.
// The rule RegisterSource keeps, that an agent may not choose what gets
// indexed, is about an agent choosing; nothing here was chosen by one. So it
// is the one rule not applied. The source is still registered as that
// member and checked as them, and a token that turns out to act as anyone
// else registers nothing.
func (s *Service) registerFor(ctx context.Context, credentials authclient.Credentials, organizationID, memberID string, input RegisterInput) (domain.Source, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Source{}, err
	}
	if caller.UserID != memberID {
		return domain.Source{}, fmt.Errorf("the grant redeemed for a member acts as someone else: %w", domain.ErrForbidden)
	}
	return s.register(ctx, credentials, caller, organizationID, input)
}

// register records a source for a caller whose right to it has been decided.
func (s *Service) register(ctx context.Context, credentials authclient.Credentials, caller domain.OrgMembership, organizationID string, input RegisterInput) (domain.Source, error) {
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
	connectionID := strings.TrimSpace(input.ConnectionID)
	if provider == filesProvider {
		if connectionID != "" {
			return domain.Source{}, fmt.Errorf(
				"files is the organization's own storage and takes no connection: %w", domain.ErrInvalid)
		}
		// What the source publishes carries the scope of what it reads. A
		// team's files are searchable by that team, never by the whole
		// organization, whoever registered the source: files would answer a
		// team member their team's files, and publishing those organization-
		// wide would share them further than anyone who stored them chose.
		if team := strings.TrimSpace(target["team"]); team != "" {
			source.Scope = domain.ScopeTeam
			source.TeamID = team
		}
	} else {
		connection, err := s.usableConnection(ctx, credentials, organizationID, connectionID, provider, caller.UserID)
		if err != nil {
			return domain.Source{}, err
		}
		source.ConnectionID = connection.ID
		source.ComposioAccountID = connection.ComposioAccountID
		// A Composio connection belongs to one member, full stop: nothing
		// here reads it as a team or the organization at large.
		source.Scope = domain.ScopeMember
		source.MemberID = caller.UserID
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

// usableConnection refuses now what a background run could not do later: a
// connection for another toolkit, one that is not active, or one that is not
// the caller's own. A member indexes only through their own connection —
// there is no admin override, unlike an internal source — since a Composio
// connection is one member's credential, not the organization's.
func (s *Service) usableConnection(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID, provider, callerID string) (domain.AppConnection, error) {
	if connectionID == "" {
		return domain.AppConnection{}, fmt.Errorf("%s needs a connection_id: %w", provider, domain.ErrInvalid)
	}
	if s.apps == nil {
		return domain.AppConnection{}, fmt.Errorf(
			"reading %s needs apps, which is not configured: %w", provider, domain.ErrProviderUnavailable)
	}
	connection, err := s.apps.Connection(ctx, organizationID, connectionID, credentials)
	if err != nil {
		return domain.AppConnection{}, err
	}
	if connection.Toolkit != provider {
		return domain.AppConnection{}, fmt.Errorf(
			"the connection is for %s, not %s: %w", connection.Toolkit, provider, domain.ErrInvalid)
	}
	if connection.Status != "active" {
		return domain.AppConnection{}, fmt.Errorf("the connection is %s: %w", connection.Status, domain.ErrConflict)
	}
	if connection.MemberID != callerID {
		return domain.AppConnection{}, fmt.Errorf(
			"a member indexes only through their own connection: %w", domain.ErrForbidden)
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
	pending := &pendingOriginals{}
	if err := s.store.RemoveSource(ctx, organizationID, sourceID, s.now(), s.retractions(knowledgev1.RemovedReasonSourceRemoved, pending)); err != nil {
		return err
	}
	s.deleteOriginals(ctx, pending)
	return nil
}

// SyncSource reads a source now rather than at its next interval.
//
// A schedule cannot know that a member just fixed a permission, or just
// changed something at the provider and wants to see it indexed. This is
// them saying so. It moves the next sweep forward and answers; the pass
// itself runs on the worker that was going to run it anyway, so nothing here
// waits on a provider.
//
// A source already due sooner is left alone rather than pushed back, and one
// that is not active answers what is wrong with it: a paused source needs
// reconnecting, not hurrying.
func (s *Service) SyncSource(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string) (domain.Source, error) {
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
	switch source.Status {
	case domain.StatusActive:
	case domain.StatusPaused:
		return domain.Source{}, fmt.Errorf("this source is paused and will not read until its connection is reconnected: %w",
			domain.ErrNeedsReconnect)
	default:
		return domain.Source{}, fmt.Errorf("this source is %s: %w", source.Status, domain.ErrConflict)
	}
	if _, err := s.store.HastenSource(ctx, organizationID, sourceID, s.now()); err != nil {
		return domain.Source{}, err
	}
	// Read back rather than answering what was passed in, so a caller sees
	// when the sweep will now be — which is now for a source that was
	// waiting, and unchanged for one already due.
	return s.store.Source(ctx, organizationID, sourceID)
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

// retractions builds the events that say a source's documents are gone, and
// notes their originals in pending, for the caller to delete once the
// retraction has committed: the builder runs inside the transaction, which
// may still roll back and keep the documents.
func (s *Service) retractions(reason string, pending *pendingOriginals) func(domain.Source, []domain.Document) ([]domain.Event, error) {
	return func(source domain.Source, documents []domain.Document) ([]domain.Event, error) {
		events := make([]domain.Event, 0, len(documents))
		for _, document := range documents {
			pending.add(document.Original)
			event, err := s.documentRemoved(source, document, reason)
			if err != nil {
				return nil, err
			}
			events = append(events, event)
		}
		return events, nil
	}
}

// pausedBy is the reason a source stopped, from what went wrong reading it,
// for a run that discovers its connection needs attention before the event
// saying so arrives.
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
