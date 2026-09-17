package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/connectionsclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
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
	// watermark is the newest change the whole pass saw. It is held here
	// rather than persisted per page, because a provider that answers newest
	// first would otherwise have the next run skip everything this one had
	// not reached yet.
	watermark *time.Time
	drained   bool
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

	// A source with no watermark has never been read through, so this pass is
	// the first full one. A source that has been read through is swept in
	// full again once its last sweep is missing or has aged past the
	// interval — starting over from the beginning, the same as a first pass
	// — but only when nothing is already mid-walk: a pass already under way
	// keeps what it saved rather than discarding progress. Only a full pass
	// may retract what it did not see.
	full := cursor.Watermark == nil
	if cursor.Value == "" && dueForFullSweep(source.FullSweepAt, s.config.FullSweepInterval, startedAt) {
		full = true
		cursor = domain.Cursor{SourceID: source.ID}
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

	result, err := s.walk(ctx, extractor, source, cursor, full, lease)
	run.DocumentsSeen, run.DocumentsPublished, run.DocumentsRemoved = result.seen, result.published, result.removed
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
	result := outcome{watermark: cursor.Watermark}
	credential, err := s.credential(ctx, source)
	if err != nil {
		return result, err
	}

	for range pagesPerRun {
		page, err := extractor.Fetch(ctx, sources.Request{
			Source: source, Credential: credential, Cursor: cursor, Full: full, Limit: pageSize,
		})
		if err != nil {
			return result, err
		}

		for _, item := range page.Items {
			result.seen++
			published, err := s.keep(ctx, source, item)
			if err != nil {
				return result, err
			}
			if published {
				result.published++
			}
		}
		if page.Watermark != nil && (result.watermark == nil || page.Watermark.After(*result.watermark)) {
			result.watermark = page.Watermark
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

// keep normalizes one item and writes it, publishing only what changed.
func (s *Service) keep(ctx context.Context, source domain.Source, item sources.Item) (bool, error) {
	text, err := transform.Normalize(item.Content, item.MIME)
	if err != nil {
		if errors.Is(err, domain.ErrUnsupportedType) {
			// A provider holds things that are not text. Skipping one is not a
			// failure of the run.
			return false, nil
		}
		return false, err
	}

	document := domain.Document{
		OrganizationID: source.OrganizationID, SourceID: source.ID, ExternalID: item.ExternalID,
		Kind: item.Kind, Title: item.Title, URL: item.URL, MIME: text.MIME,
		ContentHash: text.Hash, Body: text.Body, ByteSize: text.Bytes,
		Metadata: item.Metadata, SourceUpdatedAt: item.UpdatedAt, ExtractedAt: s.now(),
	}
	if document.ID, err = newID("doc"); err != nil {
		return false, err
	}
	return s.store.UpsertDocument(ctx, document, func(written domain.Document) (domain.Event, error) {
		return s.documentPublished(source, written)
	})
}

// runCompleted closes off a pass that did not fail.
func (s *Service) runCompleted(ctx context.Context, source domain.Source, run domain.SyncRun, result outcome, cursor domain.Cursor, lease string) error {
	now := s.now()
	var fullSweptAt *time.Time
	if result.drained {
		// Only a pass that read the whole source may say what is missing from
		// it, and only then does the watermark describe a closed window.
		if run.Kind == knowledgev1.SyncBackfill {
			removed, err := s.store.RetractUnseen(ctx, source.ID, run.StartedAt, now,
				func(document domain.Document) (domain.Event, error) {
					return s.documentRemoved(source, document, knowledgev1.RemovedReasonDeleted)
				})
			if err != nil {
				return err
			}
			result.removed = removed
			run.DocumentsRemoved = removed
			fullSweptAt = &now
		}
		if err := s.store.SaveCursor(ctx, domain.Cursor{
			SourceID: source.ID, Value: "", Watermark: result.watermark, UpdatedAt: now,
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
		StartedAt: run.StartedAt, CompletedAt: now,
	})
	if err != nil {
		return err
	}
	if err := s.store.FinishRun(ctx, run, &completed); err != nil {
		return err
	}

	// A pass that stopped early comes straight back rather than waiting an
	// interval: there is more of this source to read.
	next := now.Add(source.Interval)
	if !result.drained {
		next = now
	}
	if _, err := s.store.FinishSource(ctx, source.ID, lease, now, next, fullSweptAt, nil); err != nil {
		return err
	}
	s.logger.InfoContext(ctx, "source read",
		"source_id", source.ID, "kind", run.Kind, "seen", result.seen,
		"published", result.published, "removed", result.removed, "drained", result.drained)
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

	if pause, ok := pausedBy(cause); ok {
		paused, err := s.sourcePaused(source, pause)
		if err != nil {
			return err
		}
		if _, err := s.store.PauseSources(ctx, "connection_id", source.ConnectionID, pause, now,
			func(domain.Source) (domain.Event, error) { return paused, nil }); err != nil {
			return err
		}
		s.connections.Forget(source.ConnectionID)
		s.logger.WarnContext(ctx, "source paused", "source_id", source.ID, "reason", pause)
		return nil
	}

	retryAt := now.Add(domain.RetryAfter(source.Attempts))
	if _, err := s.store.FailSource(ctx, source.ID, lease, reason, now, retryAt, nil); err != nil {
		return err
	}
	s.logger.WarnContext(ctx, "source could not be read",
		"source_id", source.ID, "reason", reason, "retry_at", retryAt)
	return nil
}

// credential is what the source is read with.
//
// A source with no connection is internal, and internal storage has no
// service identity to accept: it is read as the member who registered it,
// under the grant they gave.
func (s *Service) credential(ctx context.Context, source domain.Source) (domain.Credential, error) {
	if source.ConnectionID == "" {
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
	return s.connections.Token(ctx, connectionsclient.TokenRequest{
		OrganizationID: source.OrganizationID,
		ConnectionID:   source.ConnectionID,
		Purpose:        domain.PurposeAction,
		ActingMemberID: source.CreatedBy,
	})
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
	default:
		return knowledgev1.SyncErrorProviderError, true
	}
}
