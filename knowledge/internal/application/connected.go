package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	filesv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// Connecting an app makes what it holds searchable.
//
// A member who connects GitHub means Krude to know about their repositories.
// They used to have to come back afterwards, find the indexing panel, and
// type a repository name into a box — and until they did, connecting an app
// made nothing searchable at all. Apps announces a connection; this registers
// what it holds.
//
// It is the member's own work, done while they are away: the sources are
// theirs, read as them, and the standing grant they gave auth is what makes
// that possible. A member who never granted gets nothing registered, and is
// no worse off than before.

const (
	// autoRegisterLimit bounds how many sources connecting an app registers.
	// Every one is a scheduled pass against the provider for as long as it
	// lives, so a member with four hundred repositories is choosing which
	// matter, not having all of them indexed on their behalf.
	autoRegisterLimit = 10
	// autoRegisterInterval is how often an automatically registered source
	// is swept. It is the middle of what knowledge accepts: often enough to
	// be useful, seldom enough that ten of them are not a burden.
	autoRegisterInterval = time.Hour
)

// ConnectionConnected registers what a newly connected app holds, so it is
// searchable without the member naming anything.
//
// Nothing here is essential to the connection, which is already made and
// already usable, so most of what can go wrong is reported and dropped
// rather than retried: the member can still register a source by hand, and
// an event retried for ever would be worse than one that did nothing.
func (s *Service) ConnectionConnected(ctx context.Context, event appsv1.ConnectionConnected) error {
	if s.grants == nil || s.apps == nil {
		// Without a way to act as the member there is nothing to do, and
		// saying so on every connection would be noise: the mount already
		// warned once.
		return nil
	}
	if event.OrganizationID == "" || event.MemberID == "" || event.ConnectionID == "" {
		return fmt.Errorf("a connected app needs an organization, a member and a connection: %w", domain.ErrInvalid)
	}
	// An app nothing here can read still delivers events; it is decided
	// before anything is asked of auth or apps.
	provider := providerFor(event.Toolkit)
	if provider == "" {
		return nil
	}
	if _, mounted := s.extractors[provider]; !mounted {
		return nil
	}
	if err := s.allowProvider(ctx, event.OrganizationID, provider); err != nil {
		if errors.Is(err, domain.ErrForbidden) {
			return nil
		}
		return err
	}
	logger := s.logger.With("organization_id", event.OrganizationID,
		"connection_id", event.ConnectionID, "toolkit", event.Toolkit)

	token, err := s.grants.MemberToken(ctx, event.OrganizationID, event.MemberID)
	if err != nil {
		return withoutGrant(ctx, logger, "nothing indexed for a newly connected app", err)
	}
	credentials := authclient.Credentials{Authorization: "Bearer " + token}

	targets, err := s.apps.Targets(ctx, event.OrganizationID, event.ConnectionID, credentials)
	if err != nil {
		if errors.Is(err, domain.ErrProviderUnavailable) || errors.Is(err, domain.ErrRateLimited) {
			// Apps or the app itself may answer next time, and the bus will
			// redeliver this.
			return fmt.Errorf("read what the connection holds: %w", err)
		}
		logger.InfoContext(ctx, "nothing indexed for a newly connected app", "error", err)
		return nil
	}
	if len(targets) == 0 {
		// An app nothing can be indexed for. It still delivers events.
		return nil
	}

	// What the connection already has counts. A redelivered event, or a
	// member who registered something themselves first, must not take it
	// past the ceiling; and a target a member removed stays removed, since
	// removing it was them saying no.
	existing, err := s.store.Sources(ctx, event.OrganizationID, domain.SourceQuery{
		ConnectionID: event.ConnectionID, Admin: true, IncludeRemoved: true,
	})
	if err != nil {
		return fmt.Errorf("look for what the connection already indexes: %w", err)
	}
	live := 0
	for _, source := range existing {
		if source.Status != domain.StatusRemoved {
			live++
		}
	}

	registered, present, leftOver := 0, 0, 0
	for _, target := range targets {
		if holdsTarget(existing, target.Target) {
			present++
			continue
		}
		if live >= autoRegisterLimit {
			leftOver++
			continue
		}
		_, err := s.registerFor(ctx, credentials, event.OrganizationID, event.MemberID, RegisterInput{
			ConnectionID: event.ConnectionID, Provider: provider,
			Target: target.Target, Interval: autoRegisterInterval,
		})
		switch {
		case err == nil:
			registered++
			live++
		case errors.Is(err, domain.ErrConflict):
			// Registered by someone else in the meantime; it is done, and it
			// counts against the ceiling like any other.
			present++
			live++
		default:
			// One target failing says nothing about the rest: a repository
			// whose name the extractor will not take should not cost a
			// member every other one.
			logger.InfoContext(ctx, "one thing could not be indexed",
				"target", target.Name, "error", err)
		}
	}
	logger.InfoContext(ctx, "indexed what a newly connected app holds",
		"registered", registered, "already_there", present, "found", len(targets), "left_for_the_member", leftOver)
	return nil
}

// holdsTarget reports whether one of sources, live or removed, reads target.
func holdsTarget(sources []domain.Source, target map[string]string) bool {
	checked, err := domain.CheckTarget(target)
	if err != nil {
		return false
	}
	for _, source := range sources {
		if maps.Equal(source.Target, checked) {
			return true
		}
	}
	return false
}

// withoutGrant decides what a missing token means for work knowledge chose
// to do for a member. A member who has not granted, or a deployment that
// may not ask, is reported and the event is done: no retry changes either.
// Anything else is auth not answering, and the bus delivers the event again.
func withoutGrant(ctx context.Context, logger *slog.Logger, what string, err error) error {
	switch {
	case errors.Is(err, domain.ErrGrantRequired):
		logger.InfoContext(ctx, what+": the member has not granted standing access", "error", err)
		return nil
	case errors.Is(err, domain.ErrForbidden):
		logger.WarnContext(ctx, what+": this deployment may not act as a member", "error", err)
		return nil
	default:
		return fmt.Errorf("%s: act as the member: %w", what, err)
	}
}

// triggerSweepFloor is how soon after a sweep a trigger may ask for another.
// A busy channel delivers faster than any provider should be read, and a
// trigger says something changed rather than read this now.
const triggerSweepFloor = 2 * time.Minute

// TriggerReceived makes what an app just reported searchable.
//
// A trigger carries the app's own payload, in a shape that differs for every
// one of Composio's thousands of triggers. Reading those shapes here would
// mean a parser per trigger, each a place to be wrong about scope, about
// what counts as the same document, and about what is text. The extractor
// for that provider already knows all of it.
//
// So a delivery is not read. It is taken as what it is — the app saying
// something in this connection changed — and the sources on that connection
// are swept now rather than at their next hour. What the trigger reported
// becomes searchable through the path everything else does.
func (s *Service) TriggerReceived(ctx context.Context, event appsv1.TriggerReceived) error {
	if event.ConnectionID == "" {
		// Nothing names what changed, so there is nothing to sweep.
		return nil
	}
	moved, err := s.store.HastenSources(ctx, event.ConnectionID, s.now(), triggerSweepFloor)
	if err != nil {
		return fmt.Errorf("bring a connection's sources forward: %w", err)
	}
	if moved > 0 {
		s.logger.InfoContext(ctx, "an app reported a change, so its sources are swept now",
			"connection_id", event.ConnectionID, "toolkit", event.Toolkit,
			"trigger", event.Trigger, "sources", moved)
	}
	return nil
}

// providerFor is the knowledge source that reads a Composio toolkit. They
// are spelled the same today; an app with no source reads as none rather
// than as a provider that does not exist.
func providerFor(toolkit string) string {
	switch toolkit {
	case "github", "notion", "slack", "googledrive", "gmail":
		return toolkit
	default:
		return ""
	}
}

// The organization's own files are the obvious thing to index and the one
// thing with nothing to announce it: there is no connection to make, so
// nothing said "start indexing these". The first shared file does.

// FileDeleted forgets a file the moment it is deleted. The files source reads
// objects by id, so the document whose external id is the file's is that
// file, held by a source at the placement it lived in. Without this it stayed
// searchable until the next full sweep noticed it missing, up to a week
// later.
//
// The event names that placement, so only its sources are asked: with a
// source for every member and every team, asking all of an organization's
// was a transaction for each of them on every deletion. An event that names
// no placement this knows is looked for everywhere, as before.
func (s *Service) FileDeleted(ctx context.Context, event filesv1.ObjectDeleted) error {
	if event.OrganizationID == "" || event.FileID == "" {
		return fmt.Errorf("a deleted file needs an organization and an id: %w", domain.ErrInvalid)
	}
	query := domain.SourceQuery{Provider: filesProvider, Admin: true}
	if placement, ok := filePlacement(event.Scope, event.TeamID, event.MemberID); ok {
		query.Placement = &placement
	}
	sources, err := s.store.Sources(ctx, event.OrganizationID, query)
	if err != nil {
		return fmt.Errorf("find the files sources that could hold a deleted file: %w", err)
	}
	for _, source := range sources {
		if source.Status == domain.StatusRemoved {
			continue
		}
		removed, found, err := s.store.RetractDocument(ctx, source.ID, event.FileID, s.now(),
			func(document domain.Document) (domain.Event, error) {
				return s.documentRemoved(source, document, knowledgev1.RemovedReasonDeleted)
			})
		if err != nil {
			return fmt.Errorf("forget a deleted file: %w", err)
		}
		if found {
			s.deleteOriginal(ctx, removed.Original)
			s.logger.InfoContext(ctx, "a deleted file is no longer searchable",
				"organization_id", event.OrganizationID, "source_id", source.ID, "file_id", event.FileID)
		}
	}
	return nil
}

// filePlacement is the placement a file lives in, from what files says of
// it; false when that names none.
func filePlacement(scope, teamID, memberID string) (domain.Placement, bool) {
	switch domain.Scope(scope) {
	case domain.ScopeOrganization:
		return domain.Placement{Scope: domain.ScopeOrganization}, true
	case domain.ScopeTeam:
		return domain.Placement{Scope: domain.ScopeTeam, TeamID: teamID}, teamID != ""
	case domain.ScopeMember:
		return domain.Placement{Scope: domain.ScopeMember, MemberID: memberID}, memberID != ""
	default:
		return domain.Placement{}, false
	}
}

// FileStored starts indexing shared, team or private files when the first
// such file is uploaded. Each placement has its own source, read as the
// member who uploaded that first file.
//
// Every upload after the first finds the placement already held, so this
// costs one lookup by placement and mints no token. A source there that
// reads the new file is brought forward, so the file is indexed on the next
// pass rather than at the source's next interval.
func (s *Service) FileStored(ctx context.Context, event filesv1.ObjectCreated) error {
	if s.grants == nil || event.OrganizationID == "" {
		return nil
	}
	placement, target, uploader, ok := storedFilePlacement(event)
	if !ok {
		return nil
	}
	if _, mounted := s.extractors[filesProvider]; !mounted {
		// This deployment does not read files at all.
		return nil
	}
	existing, err := s.store.Sources(ctx, event.OrganizationID, domain.SourceQuery{
		Provider: filesProvider, Placement: &placement, Admin: true, IncludeRemoved: true,
	})
	if err != nil {
		return fmt.Errorf("look for the placement's files source: %w", err)
	}
	held := false
	for _, source := range existing {
		switch source.Status {
		case domain.StatusActive:
			held = true
			if !readsFile(source, event) || dueAndWaiting(source, s.now()) {
				continue
			}
			if _, err := s.store.HastenSource(ctx, event.OrganizationID, source.ID, s.now()); err != nil {
				return fmt.Errorf("bring the placement's files source forward: %w", err)
			}
		case domain.StatusRemoved:
			// A member removing a source said this placement should not be
			// indexed, and another upload does not reverse that. A source
			// retired for any reason nobody chose gives the placement back.
			if !replaceable(source.Reason) {
				held = true
			}
		default:
			held = true
		}
	}
	if held || uploader == "" {
		return nil
	}

	token, err := s.grants.MemberToken(ctx, event.OrganizationID, uploader)
	if err != nil {
		return withoutGrant(ctx, s.logger.With("organization_id", event.OrganizationID),
			"the files are not indexed", err)
	}
	// Read as whoever stored the file. Only an admin can share a file with
	// the whole organization, so that is an admin, whose view also carries
	// billing attachments; those are published with admin-only access.
	credentials := authclient.Credentials{Authorization: "Bearer " + token}
	source, err := s.registerFor(ctx, credentials, event.OrganizationID, uploader, RegisterInput{
		Provider: filesProvider, Target: target, Interval: autoRegisterInterval,
	})
	switch {
	case err == nil:
		s.logger.InfoContext(ctx, "files are now indexed",
			"organization_id", event.OrganizationID, "source_id", source.ID, "scope", event.Scope)
		return nil
	case errors.Is(err, domain.ErrConflict):
		// Two uploads at once, and the other won.
		return nil
	case errors.Is(err, domain.ErrProviderUnavailable), errors.Is(err, domain.ErrRateLimited):
		return fmt.Errorf("index the organization's files: %w", err)
	default:
		s.logger.InfoContext(ctx, "the organization's files could not be indexed",
			"organization_id", event.OrganizationID, "error", err)
		return nil
	}
}

// storedFilePlacement is where an uploaded file lives, the target a source
// registered for that placement takes, and who uploaded it; false for a file
// no source here reads. A private file is only ever indexed as its owner.
func storedFilePlacement(event filesv1.ObjectCreated) (domain.Placement, map[string]string, string, bool) {
	placement, ok := filePlacement(event.Scope, event.TeamID, event.MemberID)
	if !ok {
		return domain.Placement{}, nil, "", false
	}
	switch placement.Scope {
	case domain.ScopeTeam:
		if event.CreatedBy == "" {
			return domain.Placement{}, nil, "", false
		}
		return placement, map[string]string{"team": event.TeamID}, event.CreatedBy, true
	case domain.ScopeMember:
		if event.CreatedBy != "" && event.CreatedBy != event.MemberID {
			return domain.Placement{}, nil, "", false
		}
		return placement, map[string]string{"member": "self"}, event.MemberID, true
	default:
		return placement, nil, event.CreatedBy, true
	}
}

// dueAndWaiting says a source is already due and its pass has not begun, so
// that pass will list a file uploaded now, and there is nothing to write: a
// burst of uploads costs one pass, not a write each. A source whose pass is
// under way may already have listed, so it is asked for again.
func dueAndWaiting(source domain.Source, now time.Time) bool {
	return source.SyncAt != nil && !source.SyncAt.After(now) &&
		(source.LeaseUntil == nil || !source.LeaseUntil.After(now))
}

// readsFile says whether a source at a file's placement reads that file: one
// narrowed to a purpose reads only files of it.
func readsFile(source domain.Source, event filesv1.ObjectCreated) bool {
	purpose, narrowed := source.Target["purpose"]
	return !narrowed || purpose == event.Purpose
}

// replaceable says a retired files source gave its placement back: it went
// because its member left, or because Files stopped letting them read it,
// not because anyone chose to stop indexing it.
func replaceable(reason string) bool {
	return reason == knowledgev1.RemovedReasonMemberLeft || reason == knowledgev1.RemovedReasonNoLongerReadable
}
