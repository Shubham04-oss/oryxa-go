package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
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

// readsThroughApps says whether the extractor mounted for a toolkit reads
// through apps' own reader rather than through Composio's proxy. Only such an
// extractor can serve a source registered on an oryxa connection.
//
// This is asked of the registry rather than kept in a set beside it, so the
// answer cannot fall behind what is mounted: a toolkit's extractor is what
// says whether it reads through apps, and mounting a reader for one is the
// single thing that lets that toolkit's oryxa connections register.
func readsThroughApps(extractor sources.Extractor) bool {
	reader, ok := extractor.(sources.AppsReader)
	return ok && reader.ReadsThroughApps()
}

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
	// A new source cannot be in a grant that names sources, so a specialist
	// limited to some could not read what it registered.
	if caller.KnowledgeLimited() {
		return domain.Source{}, fmt.Errorf("a specialist limited to some sources cannot register one: %w", domain.ErrForbidden)
	}
	provider := strings.TrimSpace(input.Provider)
	if err := s.allowProvider(ctx, organizationID, provider); err != nil {
		return domain.Source{}, err
	}
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
		if target["member"] == "self" {
			source.Scope = domain.ScopeMember
			source.MemberID = caller.UserID
		} else if team := strings.TrimSpace(target["team"]); team != "" {
			source.Scope = domain.ScopeTeam
			source.TeamID = team
		}
	} else {
		connection, err := s.usableConnection(ctx, credentials, organizationID, connectionID, provider, caller.UserID, extractor)
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
	// A source reads as the member who registers it. One they cannot read
	// would be refused on every pass, and would hold the placement against
	// anyone who can, so it is asked now, while a member is present to be
	// told. Files is asked for the listing every pass of it would make, and
	// an apps-backed source for a single item of the target it names. A
	// Composio source is not asked, as before the apps reader was mounted.
	if checker, ok := extractor.(sources.Checker); ok &&
		(provider == filesProvider || (readsThroughApps(extractor) && source.ComposioAccountID == "")) {
		credential, err := memberCredential(credentials)
		if err != nil {
			return domain.Source{}, err
		}
		if err := checker.Check(ctx, sources.Request{Source: source, Credential: credential}); err != nil {
			return domain.Source{}, err
		}
	}

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
	s.sourceRegistered(ctx, source)
	s.logger.InfoContext(ctx, "source registered",
		"source_id", source.ID, "organization_id", organizationID, "provider", provider)
	return source, nil
}

// memberCredential is the caller's own token, as an internal source is read
// with: a bearer token, or the one the web app's access cookie carries.
func memberCredential(credentials authclient.Credentials) (domain.Credential, error) {
	token := credentials.AccessCookie
	if scheme, value, found := strings.Cut(credentials.Authorization, " "); found && strings.EqualFold(scheme, "bearer") {
		token = strings.TrimSpace(value)
	}
	if token == "" {
		return domain.Credential{}, domain.ErrUnauthorized
	}
	return domain.Credential{AccessToken: token, In: "header", Name: "Authorization", Prefix: "Bearer "}, nil
}

// usableConnection refuses now what a background run could not do later: a
// connection for another toolkit, one that is not active, or one that is not
// the caller's own. A member indexes only through their own connection —
// there is no admin override, unlike an internal source — since a Composio
// connection is one member's credential, not the organization's.
func (s *Service) usableConnection(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID, provider, callerID string, extractor sources.Extractor) (domain.AppConnection, error) {
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
	if connection.Broker == appsv1.BrokerOryxa && !readsThroughApps(extractor) {
		// The extractor mounted for this toolkit reads through Composio's own
		// proxy, which cannot see a connection Oryxa's own sign-in holds
		// instead. Nothing could ever complete a pass on it until a reader is
		// mounted for this toolkit, and the check below would refuse it then.
		return domain.AppConnection{}, fmt.Errorf(
			"%s reads through this connection once its own integration arrives: %w", provider, domain.ErrConflict)
	}
	if connection.MemberID != callerID {
		return domain.AppConnection{}, fmt.Errorf(
			"a member indexes only through their own connection: %w", domain.ErrForbidden)
	}
	return connection, nil
}

// Sources lists what an organization has asked to index. An org admin sees
// all of it; anyone else sees what they registered.
//
// The query only narrows what is read; each source listed is decided by
// mayRead, the check a single read makes, so a listing can never answer what
// reading that source would refuse (docs/design/permissions, G3). There is
// no paging to keep: the listing answers every match at once.
func (s *Service) Sources(ctx context.Context, credentials authclient.Credentials, organizationID string, query domain.SourceQuery) ([]domain.Source, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	query.Admin = query.Admin && caller.ActsAsAdmin()
	query.CreatedBy = caller.UserID
	listed, err := s.store.Sources(ctx, organizationID, query)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(listed, func(source domain.Source) bool { return s.mayRead(ctx, caller, source, access.ActionRead) != nil }), nil
}

// mayRead is the one place every read of a source, of its runs and of its
// documents, and every change to it, decides whether the caller may reach
// it: its creator or an org admin may, and a specialist only when its grant
// names it, so the grant is enforced here once. Answering not-found tells
// the caller nothing they did not know, and a source outside a specialist's
// grant looks the same as one that does not exist.
//
// SpiceDB is asked the same, as action: in shadow, or, with an engine, as a
// second gate the caller must also pass (decisions.go).
func (s *Service) mayRead(ctx context.Context, caller domain.OrgMembership, source domain.Source, action string) error {
	return s.decide(ctx, caller, source, action, decide(caller, source))
}

func decide(caller domain.OrgMembership, source domain.Source) error {
	if err := caller.MayReadSource(source.ID); err != nil {
		return err
	}
	if source.CreatedBy != caller.UserID && !caller.ActsAsAdmin() {
		return domain.ErrNotFound
	}
	return nil
}

// Source reads one, for its creator or an org admin.
func (s *Service) Source(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string) (domain.Source, error) {
	caller, source, err := s.readable(ctx, credentials, organizationID, sourceID)
	if err != nil {
		return domain.Source{}, err
	}
	s.record(ctx, caller, access.ActionRead, resourceSource, source.ID, source)
	return source, nil
}

// readable reads one source the caller may reach, and who the caller is.
func (s *Service) readable(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string) (domain.OrgMembership, domain.Source, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.OrgMembership{}, domain.Source{}, err
	}
	source, err := s.store.Source(ctx, organizationID, sourceID)
	if err != nil {
		return domain.OrgMembership{}, domain.Source{}, err
	}
	if err := s.mayRead(ctx, caller, source, access.ActionRead); err != nil {
		return domain.OrgMembership{}, domain.Source{}, err
	}
	return caller, source, nil
}

// Runs reads how a source's recent passes went, so a member can see why
// nothing new arrived.
func (s *Service) Runs(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string, limit int) ([]domain.SyncRun, error) {
	caller, source, err := s.readable(ctx, credentials, organizationID, sourceID)
	if err != nil {
		return nil, err
	}
	s.record(ctx, caller, access.ActionRead, resourceSource, source.ID, source)
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
	if err := s.mayRead(ctx, caller, source, access.ActionDelete); err != nil {
		return err
	}
	pending := &pendingOriginals{}
	if err := s.store.RemoveSource(ctx, organizationID, sourceID, knowledgev1.RemovedReasonSourceRemoved, s.now(),
		s.retractions(knowledgev1.RemovedReasonSourceRemoved, pending)); err != nil {
		return err
	}
	s.record(ctx, caller, access.ActionDelete, resourceSource, source.ID, source)
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
	if err := s.mayRead(ctx, caller, source, access.ActionWrite); err != nil {
		return domain.Source{}, err
	}
	if err := s.allowProvider(ctx, organizationID, source.Provider); err != nil {
		return domain.Source{}, err
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
	s.record(ctx, caller, access.ActionWrite, resourceSource, source.ID, source)
	// Read back rather than answering what was passed in, so a caller sees
	// when the sweep will now be — which is now for a source that was
	// waiting, and unchanged for one already due.
	return s.store.Source(ctx, organizationID, sourceID)
}

// Document reads one document's body, for the caller fetching what an event
// was too small to carry.
//
// Registering a source says nothing about who may read an admin-only file it
// read: that is Files' access label, decided now. A creator who has since
// stopped being an admin, or an agent acting for one, is told the document
// does not exist, the same as anyone else it is not for.
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
	if err := s.mayRead(ctx, caller, source, access.ActionRead); err != nil {
		return domain.Document{}, err
	}
	if documentAccess(source, document) == accessAdmins && !caller.ActsAsAdmin() {
		return domain.Document{}, domain.ErrNotFound
	}
	s.record(ctx, caller, access.ActionRead, resourceDocument, document.ID, source)
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

// SetSourceKinds records what kinds of document a source indexes, for its
// creator or an org admin: include names kinds from domain.Kinds, nil
// returns it to the default. What the source already holds is decided again
// on its next pass, which is brought forward, from what the judge answered
// before rather than by asking it again.
func (s *Service) SetSourceKinds(ctx context.Context, credentials authclient.Credentials, organizationID, sourceID string, include []string) (domain.Source, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Source{}, err
	}
	if caller.Agent {
		return domain.Source{}, fmt.Errorf("an agent cannot change what a source indexes: %w", domain.ErrForbidden)
	}
	source, err := s.store.Source(ctx, organizationID, sourceID)
	if err != nil {
		return domain.Source{}, err
	}
	if err := s.mayRead(ctx, caller, source, access.ActionWrite); err != nil {
		return domain.Source{}, err
	}
	if s.judge == nil || !gated(source) {
		return domain.Source{}, fmt.Errorf("this source's documents are not judged, so there are no kinds to choose: %w", domain.ErrConflict)
	}
	if include != nil {
		if err := domain.ValidateKinds(include); err != nil {
			return domain.Source{}, err
		}
		chosen := map[string]bool{}
		for _, name := range include {
			chosen[name] = true
		}
		// Kept in the order kinds are shown, once each.
		include = include[:0:0]
		for _, name := range domain.KindNames() {
			if chosen[name] {
				include = append(include, name)
			}
		}
		if len(include) == 0 {
			return domain.Source{}, fmt.Errorf("choose at least one kind of document; to index nothing, remove the source: %w", domain.ErrInvalid)
		}
	}
	changed, err := s.store.SetSourceKinds(ctx, organizationID, sourceID, include, s.now())
	if err != nil {
		return domain.Source{}, err
	}
	if !changed {
		return domain.Source{}, domain.ErrNotFound
	}
	s.record(ctx, caller, access.ActionWrite, resourceSource, source.ID, source)
	return s.store.Source(ctx, organizationID, sourceID)
}
