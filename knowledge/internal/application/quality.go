package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/sync/errgroup"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// What a member connects holds a great deal nobody would ever ask Krude
// about: promotions, automated notifications, empty and untitled files. Every
// document a connected app yields is judged before it is published, and what
// is judged noise is kept out of the index rather than left to crowd what
// matters out of every answer.
//
// A document the member put there themselves (the files source) is never
// judged: they chose it.

// Judge decides whether a document is worth indexing. An error that wraps
// domain.ErrQualityCheckUnavailable stops the run, to be tried again later:
// nothing is published unjudged.
type Judge interface {
	Judge(ctx context.Context, candidate domain.Candidate) (domain.Verdict, error)
	// Redecide draws a verdict again from a kept judgment for a source's
	// choice of kinds, without asking the judge. False: it cannot, and the
	// document must be judged again.
	Redecide(judgment []byte, include []string) (domain.Verdict, bool)
}

// qualityPolicy is the version of what "worth indexing" means: the judge's
// instructions and the rules below. A document judged under an older policy
// is judged again the next time a pass reads it, so raising this re-judges
// every source over its next full sweep.
const qualityPolicy = 2

const (
	// judgeConcurrency is how many documents of a page are judged at once.
	judgeConcurrency = 8
	// judgeBatch is how many are judged between renewals of the source's
	// lease: a page of large PDFs judged in one go could outlast it.
	judgeBatch = 2 * judgeConcurrency
	// maxExcludedReason bounds the reason an excluded document keeps.
	maxExcludedReason = 200
)

// entry is one item of a page on its way to being written: read and hashed
// first, judged together with the rest of its page, then written in order.
type entry struct {
	item sources.Item
	// document is what is written, without an id until it is.
	document domain.Document
	// existing is what the source already holds under the item's id, if
	// anything, removed or not.
	existing *domain.Document
	// original marks a document kept as its own bytes rather than its text.
	original bool
	// skip: nothing here can hold it, a type with no text or bytes too large.
	skip bool
	// settled: the same content as before, already judged under this policy
	// (or never to be judged). The run only notes that it saw it.
	settled bool
	// verdict decides a document that is not settled. Nil keeps it unjudged:
	// a source that is not judged, or no judge configured.
	verdict *domain.Verdict
}

// policy is the quality policy the entry was decided under: the current one
// when a rule or the judge decided it, zero when it was kept unjudged, so it
// is judged once there is a judge.
func (e *entry) policy() int {
	if e.verdict != nil {
		return qualityPolicy
	}
	return 0
}

// judgment is the judge's answer the entry was decided by, to keep with it;
// nil when a rule decided it, or nothing did.
func (e *entry) judgment() []byte {
	if e.verdict == nil {
		return nil
	}
	return e.verdict.Judgment
}

// gated says whether a source's documents are judged at all.
func gated(source domain.Source) bool {
	return source.Provider != "files"
}

// prepare reads one item into what would be written: its text or its bytes'
// hash, and what the source already holds for it. What needs no model to
// decide is decided here.
func (s *Service) prepare(ctx context.Context, source domain.Source, item sources.Item) (*entry, error) {
	e := &entry{item: item}
	document := domain.Document{
		OrganizationID: source.OrganizationID, SourceID: source.ID, ExternalID: item.ExternalID,
		Kind: item.Kind, Title: item.Title, URL: item.URL,
		Metadata: item.Metadata, SourceUpdatedAt: item.UpdatedAt, ExtractedAt: s.now(),
	}
	if s.originals != nil && transform.IsPassthroughMIME(item.MIME) {
		// Nothing in transform reads text out of a PDF or an Office file, so
		// its bytes are kept instead, when there is somewhere to keep them.
		if len(item.Content) > sources.MaxOriginalBytes {
			// Too large to keep; the extractor already counts this as skipped.
			e.skip = true
			return e, nil
		}
		sum := sha256.Sum256(item.Content)
		document.MIME, document.ContentHash, document.ByteSize = item.MIME, hex.EncodeToString(sum[:]), len(item.Content)
		e.original = true
	} else {
		text, err := transform.Normalize(item.Content, item.MIME)
		if err != nil {
			if errors.Is(err, domain.ErrUnsupportedType) {
				// A provider holds things that are not text. Skipping one is not
				// a failure of the run.
				e.skip = true
				return e, nil
			}
			return nil, err
		}
		document.MIME, document.ContentHash, document.Body, document.ByteSize = text.MIME, text.Hash, text.Body, text.Bytes
	}
	e.document = document

	existing, err := s.store.DocumentByExternalID(ctx, source.ID, item.ExternalID)
	switch {
	case err == nil:
		e.existing = &existing
	case errors.Is(err, domain.ErrNotFound):
	default:
		return nil, err
	}
	if e.existing != nil && e.existing.ContentHash == document.ContentHash && s.judgedEnough(source, *e.existing) &&
		(e.existing.RemovedAt == nil || e.existing.Excluded != "") {
		// Live and unchanged, or excluded and unchanged: a weekly full sweep
		// over the same content neither judges nor writes it again. One that
		// was removed for another reason and has come back is not settled: it
		// is published again.
		e.settled = true
		return e, nil
	}
	if gated(source) && !e.original && !hasWords(document.Body) {
		// A blank document, a spreadsheet exported as nothing but its commas,
		// a message with no words: no model needs to read these.
		e.verdict = &domain.Verdict{Keep: false, Reason: "empty: nothing in it to read"}
	}
	return e, nil
}

// judgedEnough says whether a document's verdict still stands for the same
// content.
func (s *Service) judgedEnough(source domain.Source, existing domain.Document) bool {
	if !gated(source) || s.judge == nil {
		return true
	}
	return existing.JudgedPolicy >= qualityPolicy
}

// hasWords says whether text holds anything to read at all.
func hasWords(text string) bool {
	return strings.IndexFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

// judgeAll asks the judge about every entry of a page not already decided,
// several at once, renewing the source's lease between batches.
func (s *Service) judgeAll(ctx context.Context, source domain.Source, lease string, entries []*entry) error {
	if s.judge == nil || !gated(source) {
		return nil
	}
	var pending []*entry
	for _, e := range entries {
		if !e.skip && !e.settled && e.verdict == nil {
			pending = append(pending, e)
		}
	}
	for start := 0; start < len(pending); start += judgeBatch {
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(judgeConcurrency)
		for _, e := range pending[start:min(start+judgeBatch, len(pending))] {
			group.Go(func() error {
				verdict, err := s.judge.Judge(groupCtx, candidate(source, e))
				if err != nil {
					return fmt.Errorf("judge %q: %w", e.item.ExternalID, err)
				}
				e.verdict = &verdict
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return err
		}
		held, err := s.store.RenewLease(ctx, source.ID, lease, s.now().Add(LeaseWindow))
		if err != nil {
			return err
		}
		if !held {
			return domain.ErrLeaseLost
		}
	}
	return nil
}

// candidate is what the judge is shown of an entry.
func candidate(source domain.Source, e *entry) domain.Candidate {
	c := domain.Candidate{
		Provider: source.Provider, Kind: e.document.Kind, Title: e.document.Title, MIME: e.document.MIME,
		Metadata: e.document.Metadata, Size: e.document.ByteSize, Include: source.IncludeKinds,
	}
	if e.original {
		c.Original = e.item.Content
	} else {
		c.Text = e.document.Body
	}
	return c
}

// write records one entry as it was decided, and answers whether it was
// published and whether it was excluded.
func (s *Service) write(ctx context.Context, source domain.Source, e *entry) (bool, bool, error) {
	switch {
	case e.skip:
		return false, false, nil
	case e.settled:
		if e.existing.RemovedAt != nil {
			// Excluded, and still the same: there is nothing live to keep
			// seen.
			return false, false, nil
		}
		// Seen, so the full sweep this may be part of does not retract it.
		return false, false, s.store.TouchDocument(ctx, source.ID, e.item.ExternalID, s.now())
	case e.verdict != nil && !e.verdict.Keep:
		return false, true, s.exclude(ctx, source, e)
	case e.original:
		published, err := s.keepOriginal(ctx, source, e)
		return published, false, err
	default:
		published, err := s.keepText(ctx, source, e)
		return published, false, err
	}
}

// keepText writes a document knowledge read as text, publishing it only if it
// changed or had been removed.
func (s *Service) keepText(ctx context.Context, source domain.Source, e *entry) (bool, error) {
	document := e.document
	document.JudgedPolicy, document.Judgment = e.policy(), e.judgment()
	var err error
	if document.ID, err = newID("doc"); err != nil {
		return false, err
	}
	return s.store.UpsertDocument(ctx, document, func(written domain.Document) (domain.Event, error) {
		return s.documentPublished(source, written)
	})
}

// exclude records a document judged not worth indexing, retracting it if it
// was live until now.
func (s *Service) exclude(ctx context.Context, source domain.Source, e *entry) error {
	document := e.document
	document.JudgedPolicy, document.Judgment = qualityPolicy, e.judgment()
	document.Excluded = excludedReason(e.verdict.Reason)
	var err error
	if document.ID, err = newID("doc"); err != nil {
		return err
	}
	retracted, err := s.store.ExcludeDocument(ctx, document, s.now(), func(written domain.Document) (domain.Event, error) {
		return s.documentRemoved(source, written, knowledgev1.RemovedReasonExcluded)
	})
	if err != nil {
		return err
	}
	if retracted && e.existing != nil {
		s.deleteOriginal(ctx, e.existing.Original)
	}
	return nil
}

// excludedReason is the reason an excluded document keeps: the judge's, never
// empty, and bounded.
func excludedReason(reason string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		return "not worth indexing"
	}
	if runes := []rune(reason); len(runes) > maxExcludedReason {
		return string(runes[:maxExcludedReason])
	}
	return reason
}

// redecidePage is how many of a source's documents are decided again at once.
const redecidePage = 100

// redecide decides a source's judged documents again for its member's new
// choice of kinds: from their kept judgments where those settle it, from their
// kept text otherwise. A document now of a kind the source indexes is
// published from its kept text; one no longer is, retracted.
func (s *Service) redecide(ctx context.Context, source domain.Source, lease string) (outcome, error) {
	var result outcome
	if s.judge == nil || !gated(source) {
		return result, s.store.ClearKindsPending(ctx, source.ID, lease)
	}
	after := ""
	for {
		documents, err := s.store.DocumentsToRedecide(ctx, source.ID, after, redecidePage)
		if err != nil {
			return result, err
		}
		if len(documents) == 0 {
			break
		}
		for _, document := range documents {
			after = document.ID
			verdict, ok := s.judge.Redecide(document.Judgment, source.IncludeKinds)
			if !ok {
				if document.Body == "" {
					// Nothing kept to judge it by: its next full sweep does.
					continue
				}
				if verdict, err = s.judge.Judge(ctx, candidateOfDocument(source, document)); err != nil {
					return result, err
				}
			}
			live := document.RemovedAt == nil
			switch {
			case verdict.Keep && !live:
				if document.Body == "" {
					// Only its original's bytes were ever read, and they were not
					// kept: its next full sweep reads them again.
					continue
				}
				document.Judgment = verdict.Judgment
				published, err := s.store.UpsertDocument(ctx, document, func(written domain.Document) (domain.Event, error) {
					return s.documentPublished(source, written)
				})
				if err != nil {
					return result, err
				}
				if published {
					result.published++
				}
			case !verdict.Keep && live:
				document.Excluded, document.Judgment = excludedReason(verdict.Reason), verdict.Judgment
				retracted, err := s.store.ExcludeDocument(ctx, document, s.now(), func(written domain.Document) (domain.Event, error) {
					return s.documentRemoved(source, written, knowledgev1.RemovedReasonExcluded)
				})
				if err != nil {
					return result, err
				}
				if retracted {
					s.deleteOriginal(ctx, document.Original)
				}
				result.excluded++
			}
		}
		held, err := s.store.RenewLease(ctx, source.ID, lease, s.now().Add(LeaseWindow))
		if err != nil {
			return result, err
		}
		if !held {
			return result, domain.ErrLeaseLost
		}
	}
	return result, s.store.ClearKindsPending(ctx, source.ID, lease)
}

// candidateOfDocument is what the judge is shown of a kept document: its
// kept text, for a source's current choice of kinds.
func candidateOfDocument(source domain.Source, document domain.Document) domain.Candidate {
	return domain.Candidate{
		Provider: source.Provider, Kind: document.Kind, Title: document.Title, MIME: document.MIME,
		Metadata: document.Metadata, Text: document.Body, Size: document.ByteSize, Include: source.IncludeKinds,
	}
}
