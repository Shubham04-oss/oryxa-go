// Package sources is where documents come from: the one seam every extractor
// implements, and the HTTP behaviour bulk reading needs.
//
// A source is drained a page at a time. The caller passes back the cursor the
// last page returned and stops when one comes back empty, so a run that dies
// resumes rather than reading the whole provider again. An extractor reports
// what it read and never decides what to keep: normalizing, hashing and
// publishing belong to the caller.
package sources

import (
	"context"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// Item is one thing a source yielded, as the provider gave it.
type Item struct {
	// ExternalID is the provider's own identifier, stable across runs. It is
	// what makes a document the same document.
	ExternalID string
	Kind       string
	Title      string
	URL        string
	// MIME is what the provider says the content is. An extractor that knows
	// better says so; an empty value is read as plain text.
	MIME    string
	Content []byte
	// Metadata is what the provider said beyond the text: an author, a label,
	// a channel. Never a credential.
	Metadata map[string]string
	// UpdatedAt is when the provider last changed it, when it says.
	UpdatedAt *time.Time
}

// Page is one call's worth of a source.
type Page struct {
	Items []Item
	// Cursor is where to carry on. Empty means the source is drained.
	Cursor string
	// Watermark is the newest change this page saw, for a provider that reads
	// by time rather than by page.
	Watermark *time.Time
	// Token is Watermark's counterpart for a provider whose watermark is an
	// opaque cursor rather than a moment, such as Drive's
	// changes.startPageToken or Gmail's historyId. It is carried into
	// domain.Cursor.Token the same way Watermark is carried into
	// Cursor.Watermark: read back on the next incremental pass, and moved
	// only once this pass drains. An extractor that reads by time leaves it
	// empty and never sets it.
	Token string
	// Removed is the external ids an incremental page's own provider already
	// said are gone — Drive's changes feed, Gmail's history — so the caller
	// retracts them now rather than waiting for the next full sweep to
	// notice the same thing the slow way. Nil for an extractor that has no
	// such feed; a full pass never needs it, since it retracts everything it
	// did not see once it drains.
	Removed []string
}

// Request is what to read, and what to read it with.
type Request struct {
	Source     domain.Source
	Credential domain.Credential
	// Cursor is where the last run stopped. A zero cursor starts at the
	// beginning.
	Cursor domain.Cursor
	// Full says read everything the source holds, rather than only what
	// changed. Only a full pass may retract what it did not see.
	Full bool
	// Limit is how many items to ask the provider for in one call. An
	// extractor may ask for fewer if the provider allows less.
	Limit int
}

// Extractor reads documents out of one provider.
type Extractor interface {
	// Provider is what this reads, such as "files" for the organization's own
	// storage.
	Provider() string
	// Target says whether this extractor can read what a source names, before
	// a source is registered on it.
	Target(target map[string]string) error
	// Fetch returns one page. Calling it with the cursor the last page
	// returned walks the source; an empty cursor back means it is drained.
	Fetch(ctx context.Context, request Request) (Page, error)
}

// Checker is an extractor that can say, before a source is registered,
// whether the member it would read as can read what it names at all. A
// source its member cannot read would hold the name for nothing: every pass
// refused, and nobody who can read it able to register it instead.
type Checker interface {
	// Check answers nil when request.Credential can read what
	// request.Source names, and the refusal otherwise.
	Check(ctx context.Context, request Request) error
}

// Registry is the extractors this build knows, by provider.
type Registry map[string]Extractor

// Extractor answers the one that reads a provider.
func (registry Registry) Extractor(provider string) (Extractor, error) {
	extractor, ok := registry[provider]
	if !ok {
		return nil, fmt.Errorf("nothing reads %q: %w", provider, domain.ErrInvalid)
	}
	return extractor, nil
}

// Providers is what this build can read, for the caller that reads the
// registry aloud.
func (registry Registry) Providers() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// DefaultLimit is how many items a page asks for when the caller says
// nothing. It is deliberately modest: a page is the unit of work a lease has
// to cover, and a smaller one resumes more precisely.
const DefaultLimit = 100

// MaxOriginalBytes bounds a document whose own bytes are kept as they are
// rather than read as text — a PDF, an Office file. It is what an extractor
// checks before downloading one at all, so a file too large to keep is never
// pulled into memory just to be discarded, and it is also the ceiling this
// package's own HTTP client and Composio's binary indirection (composio.go)
// hold a downloaded file to, so the two can never disagree about how large
// is too large.
const MaxOriginalBytes = 50 << 20

// Limit keeps a caller's page size inside what a provider will answer.
func Limit(requested, maximum int) int {
	if requested <= 0 {
		requested = DefaultLimit
	}
	if maximum > 0 && requested > maximum {
		return maximum
	}
	return requested
}

// MissingTarget says a source did not name something the extractor needs.
func MissingTarget(field string) error {
	return fmt.Errorf("a target needs %s: %w", field, domain.ErrInvalid)
}

// watermarkAfter is whichever of current and candidate is newer, so a page's
// watermark only ever moves forward.
func watermarkAfter(current *time.Time, candidate time.Time) *time.Time {
	if current == nil || candidate.After(*current) {
		value := candidate.UTC()
		return &value
	}
	return current
}
