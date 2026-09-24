package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

const (
	// pagesPerRun bounds one pass, so a source with an enormous backlog gives
	// the worker back rather than holding it for hours. The cursor is saved,
	// so the next tick carries on where this one stopped.
	pagesPerRun = 200
	// pageSize is what an extractor is asked for at a time.
	pageSize = 100
)

// outcome is what one pass did, carried out of the loop so the run and the
// source are both closed off the same way whether it finished or failed.
type outcome struct {
	seen      int
	published int
	removed   int
	// excluded is what the pass judged not worth indexing.
	excluded int
	// watermark is the newest change the whole pass saw. It is held here
	// rather than persisted per page, because a provider that answers newest
	// first would otherwise have the next run skip everything this one had
	// not reached yet.
	watermark *time.Time
	// token is watermark's counterpart for a provider whose watermark is an
	// opaque cursor rather than a moment — see domain.Cursor.Token. Unlike
	// watermark it does not need "newest wins": Drive and Gmail's own feeds
	// are read in order, so the last one a page sets is already the latest.
	token   string
	drained bool
}

// Sync reads a source once, under a lease it must keep holding.
//
// A pass either drains the source or stops somewhere it can resume from.
// Cursors are saved per page so nothing is read twice after a crash, but the
// watermark only moves when the source is drained: it describes the whole
// window, not how far one pass got.
func (s *Service) Sync(ctx context.Context, source domain.Source, lease string) error {
	if err := source.Extractable(); err != nil {
		return err
	}
	extractor, err := s.extractors.Extractor(source.Provider)
	if err != nil {
		return err
	}
	cursor, err := s.store.Cursor(ctx, source.ID)
	if err != nil {
		return err
	}
	startedAt := s.now()

	// A source with no watermark and no token has never been read through, so
	// this pass is the first full one. Token is watermark's counterpart for a
	// provider whose position is an opaque cursor rather than a moment —
	// Drive's changes.startPageToken, Gmail's historyId — and either one
	// being set means a pass has drained before. A source that has been read
	// through is swept in full again once its last sweep is missing or has
	// aged past the interval — starting over from the beginning, the same as
	// a first pass — but only when nothing is already mid-walk: a pass
	// already under way keeps what it saved rather than discarding progress.
	// Only a full pass may retract what it did not see.
	full := cursor.Watermark == nil && cursor.Token == ""
	if cursor.Value == "" && dueForFullSweep(source.FullSweepAt, s.config.FullSweepInterval, startedAt) {
		full = true
		cursor = domain.Cursor{SourceID: source.ID}
	}
	if cursor.Value == "" {
		// A walk is beginning now rather than resuming one already under
		// way. Recorded so that if this walk outlives the run that started
		// it, a later run can still retract against when the walk itself
		// began, not against whichever run happens to finish it.
		cursor.WalkStartedAt = &startedAt
	}
	kind := knowledgev1.SyncIncremental
	if full {
		kind = knowledgev1.SyncBackfill
	}

	run := domain.SyncRun{
		OrganizationID: source.OrganizationID, SourceID: source.ID,
		Kind: kind, Status: domain.RunRunning, StartedAt: startedAt,
	}
	if run.ID, err = newID("run"); err != nil {
		return err
	}
	if err := s.store.StartRun(ctx, run); err != nil {
		return err
	}

	// A member who changed what kinds the source indexes has what it already
	// holds decided again first, from what the judge answered before.
	var redecided outcome
	if source.KindsPending {
		if redecided, err = s.redecide(ctx, source, lease); err != nil {
			return s.runFailed(ctx, source, run, redecided, lease, err)
		}
	}
	result, err := s.walk(ctx, extractor, source, cursor, full, lease)
	result.published += redecided.published
	result.excluded += redecided.excluded
	run.DocumentsSeen, run.DocumentsPublished, run.DocumentsRemoved = result.seen, result.published, result.removed
	run.DocumentsExcluded = result.excluded
	if err != nil {
		return s.runFailed(ctx, source, run, result, lease, err)
	}
	return s.runCompleted(ctx, source, run, result, cursor, lease)
}

// dueForFullSweep says whether a source needs to be read from the beginning
// again: it never has been, or its last full sweep has aged past interval.
func dueForFullSweep(sweptAt *time.Time, interval time.Duration, now time.Time) bool {
	return sweptAt == nil || !sweptAt.Add(interval).After(now)
}

// walk reads page after page until the source is drained, this pass has done
// enough, or something stops it.
func (s *Service) walk(ctx context.Context, extractor sources.Extractor, source domain.Source, cursor domain.Cursor, full bool, lease string) (outcome, error) {
	result := outcome{watermark: cursor.Watermark, token: cursor.Token}
	credential, err := s.credential(ctx, source)
	if err != nil {
		return result, err
	}

	request := sources.Request{Source: source, Credential: credential, Full: full, Limit: pageSize}
	if !full {
		// An incremental pass may skip what the source already holds as it
		// is; a full sweep reads everything again, so what changed in how
		// knowledge reads a document still reaches it within a sweep.
		request.Held = func(ctx context.Context, externalID string) (domain.Document, bool, error) {
			held, err := s.store.DocumentByExternalID(ctx, source.ID, externalID)
			switch {
			case err == nil:
				return held, true, nil
			case errors.Is(err, domain.ErrNotFound):
				return domain.Document{}, false, nil
			default:
				return domain.Document{}, false, err
			}
		}
	}
	for range pagesPerRun {
		request.Cursor = cursor
		page, err := extractor.Fetch(ctx, request)
		if err != nil {
			return result, err
		}

		entries := make([]*entry, 0, len(page.Items))
		for _, item := range page.Items {
			result.seen++
			prepared, err := s.prepare(ctx, source, item)
			if err != nil {
				return result, err
			}
			entries = append(entries, prepared)
		}
		// Judged before anything of the page is written: a page the judge
		// cannot finish is read again whole, by the run that retries it.
		if err := s.judgeAll(ctx, source, lease, entries); err != nil {
			return result, err
		}
		for _, prepared := range entries {
			published, excluded, err := s.write(ctx, source, prepared)
			if err != nil {
				return result, err
			}
			if published {
				result.published++
			}
			if excluded {
				result.excluded++
			}
		}
		// A page's own feed — Drive's changes, Gmail's history — sometimes
		// says outright that something is gone, and that is acted on now
		// rather than left for the next full sweep to notice the slow way.
		for _, externalID := range page.Removed {
			removed, found, err := s.store.RetractDocument(ctx, source.ID, externalID, s.now(),
				func(removed domain.Document) (domain.Event, error) {
					return s.documentRemoved(source, removed, knowledgev1.RemovedReasonDeleted)
				})
			if err != nil {
				return result, err
			}
			if found {
				result.removed++
				s.deleteOriginal(ctx, removed.Original)
			}
		}
		if page.Watermark != nil && (result.watermark == nil || page.Watermark.After(*result.watermark)) {
			result.watermark = page.Watermark
		}
		if page.Token != "" {
			// Drive and Gmail's own feeds are read in order, so the latest
			// page's token is always the newest — unlike watermark, there is
			// no "is this newer" to check.
			result.token = page.Token
		}

		// The lease is renewed between pages rather than held for the whole
		// pass, because a pass can outlast any window worth leasing for. A
		// lease that has moved on means another replica owns this source now,
		// and nothing more may be written for it.
		held, err := s.store.RenewLease(ctx, source.ID, lease, s.now().Add(LeaseWindow))
		if err != nil {
			return result, err
		}
		if !held {
			return result, domain.ErrLeaseLost
		}

		// The page's cursor is saved, but not the watermark: a pass that stops
		// here must resume mid-window, not start a narrower one.
		cursor.Value = page.Cursor
		cursor.UpdatedAt = s.now()
		if err := s.store.SaveCursor(ctx, cursor); err != nil {
			return result, err
		}
		if page.Cursor == "" {
			result.drained = true
			return result, nil
		}
	}
	return result, nil
}

// keepOriginal stores a document whose bytes knowledge does not read itself
// in the configured bucket, and publishes it with an Original rather than a
// Body: empty Body, MIME the real type, ContentHash the sha256 of its bytes
// hex encoded, which is also what makes the object's key content addressed,
// so a changed document is a new object and a reader never sees one half
// written.
//
// The object's key uses the document's own stable id when it has one, rather
// than one thrown away the moment this turns out to be an update. The bytes
// are written only when the object is not already there: a live document
// judged again under a new policy, but unchanged, keeps the one it has.
func (s *Service) keepOriginal(ctx context.Context, source domain.Source, e *entry) (bool, error) {
	existing := domain.Document{}
	if e.existing != nil {
		existing = *e.existing
	}
	document := withCategory(e.document, e.verdict)
	document.JudgedPolicy, document.Judgment = e.policy(), e.judgment()
	document.ID = existing.ID
	if document.ID == "" {
		var err error
		if document.ID, err = newID("doc"); err != nil {
			return false, err
		}
	}
	key := originalKey(source.OrganizationID, source.ID, document.ID, document.ContentHash)
	if existing.RemovedAt != nil || existing.Original == nil || existing.Original.Key != key {
		if err := s.originals.Put(ctx, key, e.item.Content, e.item.MIME); err != nil {
			return false, fmt.Errorf("store the document's original bytes: %w", err)
		}
	}
	document.Original = &domain.Original{
		Bucket: s.originalsBucket, Key: key, Size: int64(document.ByteSize), SHA256: document.ContentHash,
	}
	published, err := s.store.UpsertDocument(ctx, document, func(written domain.Document) (domain.Event, error) {
		return s.documentPublished(source, written)
	})
	if err != nil {
		return false, err
	}
	if published && existing.Original != nil && existing.Original.Key != key {
		// The new object is already published and safe to read; a failure to
		// clean up the one it replaced is logged rather than failing a run
		// that has otherwise succeeded, over a byte nothing will read again.
		s.deleteOriginal(ctx, existing.Original)
	}
	return published, nil
}

// originalKey is where a document's own bytes live in the configured bucket:
// namespaced by organization and source, so a retired source's objects are
// easy to find, and content addressed by sha256, so a changed document is a
// new object.
func originalKey(organizationID, sourceID, documentID, sha256Hex string) string {
	return "originals/" + organizationID + "/" + sourceID + "/" + documentID + "/" + sha256Hex
}

// pendingOriginals are the originals of documents a transaction is removing,
// kept to delete once it has committed. Deleting them inside it would leave a
// rolled-back removal's documents naming bytes that are gone.
type pendingOriginals struct {
	originals []*domain.Original
}

func (pending *pendingOriginals) add(original *domain.Original) {
	if original != nil {
		pending.originals = append(pending.originals, original)
	}
}

// deleteOriginals deletes what pending collected, after the removal that
// collected it has committed.
func (s *Service) deleteOriginals(ctx context.Context, pending *pendingOriginals) {
	for _, original := range pending.originals {
		s.deleteOriginal(ctx, original)
	}
}

// deleteOriginal removes a superseded or retracted document's bytes from the
// configured bucket. Best effort: the database write it follows has already
// committed to the row it describes, and an object nothing will read again is
// a smaller problem than failing a sync run, or a removal, over one
// straggling byte.
func (s *Service) deleteOriginal(ctx context.Context, original *domain.Original) {
	if s.originals == nil || original == nil {
		return
	}
	if err := s.originals.Delete(ctx, original.Key); err != nil {
		s.logger.WarnContext(ctx, "could not delete an original from the configured bucket",
			"key", original.Key, "error", err)
	}
}

// runCompleted closes off a pass that did not fail.
func (s *Service) runCompleted(ctx context.Context, source domain.Source, run domain.SyncRun, result outcome, cursor domain.Cursor, lease string) error {
	now := s.now()
	var fullSweptAt *time.Time
	if result.drained {
		// Only a pass that read the whole source may say what is missing from
		// it, and only then does the watermark describe a closed window.
		if run.Kind == knowledgev1.SyncBackfill {
			// The threshold is when the walk began, not when this run of it
			// did: a walk spanning several runs stamped what earlier runs
			// saw before this run started, and using this run's own start
			// would retract that along with what is actually gone. A cursor
			// saved before walk_started_at existed has none, so this run's
			// start is the best available answer for it.
			before := run.StartedAt
			if cursor.WalkStartedAt != nil {
				before = *cursor.WalkStartedAt
			}
			pending := &pendingOriginals{}
			removed, err := s.store.RetractUnseen(ctx, source.ID, before, now,
				func(document domain.Document) (domain.Event, error) {
					pending.add(document.Original)
					return s.documentRemoved(source, document, knowledgev1.RemovedReasonDeleted)
				})
			if err != nil {
				return err
			}
			s.deleteOriginals(ctx, pending)
			// Added to, not replaced: an incremental-shaped feed inside a
			// full pass could in principle have already retracted some of
			// this source's documents directly through Page.Removed.
			result.removed += removed
			run.DocumentsRemoved = result.removed
			fullSweptAt = &now
		}
		if err := s.store.SaveCursor(ctx, domain.Cursor{
			SourceID: source.ID, Value: "", Watermark: result.watermark, Token: result.token, UpdatedAt: now,
		}); err != nil {
			return err
		}
	}

	run.Status = domain.RunCompleted
	run.FinishedAt = &now
	completed, err := s.event(knowledgev1.SyncCompletedDetailType, knowledgev1.SyncCompleted{
		RunID: run.ID, OrganizationID: source.OrganizationID, SourceID: source.ID,
		Provider: source.Provider, Kind: run.Kind,
		DocumentsSeen: result.seen, DocumentsPublished: result.published, DocumentsRemoved: result.removed,
		DocumentsExcluded: result.excluded, StartedAt: run.StartedAt, CompletedAt: now,
	})
	if err != nil {
		return err
	}
	if err := s.store.FinishRun(ctx, run, &completed); err != nil {
		return err
	}

	// A pass that stopped early comes straight back rather than waiting an
	// interval: there is more of this source to read. One asked for again
	// while it ran — an upload that may have landed after it listed — keeps
	// that sooner time: the store compares against when it was claimed due.
	next := now.Add(source.Interval)
	if !result.drained {
		next = now
	}
	if _, err := s.store.FinishSource(ctx, source.ID, lease, now, next, source.SyncAt, fullSweptAt, nil); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "source read",
		"source_id", source.ID, "kind", run.Kind, "seen", result.seen,
		"published", result.published, "removed", result.removed, "excluded", result.excluded,
		"drained", result.drained)
	return nil
}

// runFailed closes off a pass that stopped, backing the source off and
// pausing it outright when what went wrong will not fix itself.
func (s *Service) runFailed(ctx context.Context, source domain.Source, run domain.SyncRun, result outcome, lease string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		// The process is going away, not the source. Leave the lease to lapse
		// so another replica picks this up rather than recording a failure.
		return cause
	}
	now := s.now()
	reason, retryable := syncError(cause)

	run.Status = domain.RunFailed
	run.Error = reason
	run.Retryable = retryable
	run.FinishedAt = &now
	failed, err := s.event(knowledgev1.SyncFailedDetailType, knowledgev1.SyncFailed{
		RunID: run.ID, OrganizationID: source.OrganizationID, SourceID: source.ID,
		Provider: source.Provider, Kind: run.Kind, Error: reason, Retryable: retryable,
		DocumentsPublished: result.published, Resumable: !result.drained,
		StartedAt: run.StartedAt, FailedAt: now,
	})
	if err != nil {
		return err
	}
	if err := s.store.FinishRun(ctx, run, &failed); err != nil {
		return err
	}

	// A lease that moved on means another replica owns the source; touching
	// its schedule would fight that replica.
	if errors.Is(cause, domain.ErrLeaseLost) {
		s.logger.WarnContext(ctx, "source claimed by another worker mid-run", "source_id", source.ID)
		return nil
	}

	// A connection that needs attention will not read differently next
	// interval, so every source on it is paused now rather than discovering
	// the same thing one at a time. An internal source has no connection to
	// pause by, though nothing reads as one and gets this far in practice.
	if reason, ok := pausedBy(cause); ok && source.ConnectionID != "" {
		if err := s.pause(ctx, source.ConnectionID, reason); err != nil {
			return err
		}
		s.logger.WarnContext(ctx, "source paused", "source_id", source.ID, "reason", reason)
		return nil
	}

	// An internal source reads as a member, and one Files now refuses the
	// placement — they left the team, or stopped being an admin — is refused
	// on every pass from here on. It is retired with what it held rather
	// than backed off for ever, and the next member to upload there takes
	// the placement over: see FileStored.
	if errors.Is(cause, domain.ErrUnreadable) && source.ConnectionID == "" {
		pending := &pendingOriginals{}
		retired, err := s.store.RetireSource(ctx, source.ID, lease, knowledgev1.RemovedReasonNoLongerReadable, now,
			s.retractions(knowledgev1.RemovedReasonNoLongerReadable, pending))
		if err != nil {
			return err
		}
		s.deleteOriginals(ctx, pending)
		if retired {
			s.logger.WarnContext(ctx, "source retired: the member it reads as can no longer read it",
				"source_id", source.ID, "created_by", source.CreatedBy, "error", cause)
		}
		return nil
	}

	retryAt := now.Add(domain.RetryAfter(source.Attempts))
	if _, err := s.store.FailSource(ctx, source.ID, lease, reason, now, retryAt, nil); err != nil {
		return err
	}
	// The reason is what the event and the API say; the cause is what an
	// operator needs, and nothing else records it.
	s.logger.WarnContext(ctx, "source could not be read",
		"source_id", source.ID, "reason", reason, "retry_at", retryAt, "error", cause)
	return nil
}

// credential is what the source is read with.
//
// A provider source reads through Composio, which holds the credential
// itself: nothing here ever sees a token for it, only the connected account
// to proxy calls through. Internal storage has no service identity to
// accept, so a source with no Composio account is read as the member who
// registered it, under the grant they gave.
func (s *Service) credential(ctx context.Context, source domain.Source) (domain.Credential, error) {
	if source.ComposioAccountID != "" {
		return domain.Credential{Metadata: map[string]string{"composio_account_id": source.ComposioAccountID}}, nil
	}
	if s.grants == nil {
		return domain.Credential{}, fmt.Errorf(
			"reading %s acts as a member, and no grant is configured: %w", source.Provider, domain.ErrForbidden)
	}
	token, err := s.grants.MemberToken(ctx, source.OrganizationID, source.CreatedBy)
	if err != nil {
		return domain.Credential{}, err
	}
	return domain.Credential{
		AccessToken: token, In: "header", Name: "Authorization", Prefix: "Bearer ",
	}, nil
}

// syncError names what stopped a run in the caller's terms, and says whether
// running it again unchanged could do better.
func syncError(err error) (string, bool) {
	switch {
	case errors.Is(err, domain.ErrNeedsReconnect):
		return knowledgev1.SyncErrorNeedsReconnect, false
	case errors.Is(err, domain.ErrScopeRequired):
		return knowledgev1.SyncErrorScopeRequired, false
	case errors.Is(err, domain.ErrCredentialRejected):
		return knowledgev1.SyncErrorCredentialRejected, false
	case errors.Is(err, domain.ErrForbidden):
		return knowledgev1.SyncErrorForbidden, false
	case errors.Is(err, domain.ErrNotFound):
		return knowledgev1.SyncErrorNotFound, false
	case errors.Is(err, domain.ErrRateLimited):
		return knowledgev1.SyncErrorRateLimited, true
	case errors.Is(err, domain.ErrProviderUnavailable):
		return knowledgev1.SyncErrorProviderUnavailable, true
	case errors.Is(err, domain.ErrLeaseLost):
		return knowledgev1.SyncErrorLeaseLost, true
	case errors.Is(err, domain.ErrGrantRequired):
		return knowledgev1.SyncErrorGrantRequired, false
	case errors.Is(err, domain.ErrQualityCheckUnavailable):
		return knowledgev1.SyncErrorQualityCheckUnavailable, true
	default:
		return knowledgev1.SyncErrorProviderError, true
	}
}
