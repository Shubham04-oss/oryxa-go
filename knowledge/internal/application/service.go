// Package application is what knowledge does: register the sources an
// organization wants indexed, extract documents from them on a schedule, and
// publish what changed for a retrieval pipeline to hold.
//
// Sources are registered rather than discovered: a member says what to index
// with their own credentials, and every later run works from what was stored.
// That matches what indexing is: a choice, not a crawl of everything an
// organization holds.
package application

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

// Store is what the application needs of knowledge's own tables.
type Store interface {
	CreateSource(context.Context, domain.Source, *domain.Event) error
	Source(context.Context, string, string) (domain.Source, error)
	Sources(context.Context, string, domain.SourceQuery) ([]domain.Source, error)
	// Every removal records its reason on what it retires (one of
	// knowledgev1's RemovedReason values), so what decides whether to
	// register a source again can tell a member saying no from a member
	// leaving.
	RemoveSource(ctx context.Context, organizationID, sourceID, reason string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) error
	RemoveSources(ctx context.Context, organizationID, createdBy, reason string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error)
	// RemoveSourcesByConnection retires every source reading through one
	// connection: apps.connection.removed.v1, which is gone for good.
	RemoveSourcesByConnection(ctx context.Context, connectionID, reason string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error)
	// HastenSources brings forward the next sweep of a connection's live
	// sources, no sooner than floor after their last.
	HastenSources(ctx context.Context, connectionID string, at time.Time, floor time.Duration) (int64, error)
	// HastenSource brings forward one source's next sweep, because someone
	// asked. It answers false for a source that is not active or already due
	// sooner.
	HastenSource(ctx context.Context, organizationID, sourceID string, at time.Time) (bool, error)
	// RemoveSourcesByTeam retires every source a deleted team held, and
	// retracts what they held in the same transaction.
	RemoveSourcesByTeam(ctx context.Context, organizationID, teamID, reason string, now time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error)
	// PauseSources stops every live source reading through one connection:
	// apps.connection.expired.v1, or a run that just discovered the same
	// thing the hard way.
	PauseSources(context.Context, string, string, time.Time, func(domain.Source) (domain.Event, error)) ([]domain.Source, error)

	ClaimDueSources(context.Context, time.Time, int, string, time.Time) ([]domain.Source, error)
	RenewLease(context.Context, string, string, time.Time) (bool, error)
	FinishSource(context.Context, string, string, time.Time, time.Time, *time.Time, *domain.Event) (bool, error)
	FailSource(context.Context, string, string, string, time.Time, time.Time, *domain.Event) (bool, error)

	UpsertDocument(context.Context, domain.Document, func(domain.Document) (domain.Event, error)) (bool, error)
	RetractUnseen(context.Context, string, time.Time, time.Time, func(domain.Document) (domain.Event, error)) (int, error)
	Document(context.Context, string, string) (domain.Document, error)
	// DocumentByExternalID reads one document, whatever its removed_at, as a
	// source's own extractor names it. keepOriginal uses it to find a
	// document's stable id and its previous Original before deciding whether
	// its bytes are worth writing again.
	DocumentByExternalID(context.Context, string, string) (domain.Document, error)
	// RetractDocument forgets one document immediately, named by the
	// provider's own id for it: Page.Removed says a page's own incremental
	// feed already knows it is gone, so nothing here waits for the next full
	// sweep to notice the same thing the slow way. Answers the document
	// retracted and whether there was a live one to.
	RetractDocument(context.Context, string, string, time.Time, func(domain.Document) (domain.Event, error)) (domain.Document, bool, error)
	// TouchDocument records that a run saw a document it did not need to
	// write, so a full sweep does not retract it as gone.
	TouchDocument(context.Context, string, string, time.Time) error
	// ExcludeDocument records a document judged not worth indexing, removed
	// with its reason, retracting it with event's removal if it was live.
	// Answers whether it was.
	ExcludeDocument(context.Context, domain.Document, time.Time, func(domain.Document) (domain.Event, error)) (bool, error)
	// SetSourceKinds records what kinds of document a source indexes and
	// marks its documents to be decided again; ClearKindsPending records that
	// they were. DocumentsToRedecide pages through what a judge decided.
	SetSourceKinds(ctx context.Context, organizationID, sourceID string, include []string, at time.Time) (bool, error)
	ClearKindsPending(ctx context.Context, sourceID, lease string) error
	DocumentsToRedecide(ctx context.Context, sourceID, after string, limit int) ([]domain.Document, error)

	Cursor(context.Context, string) (domain.Cursor, error)
	SaveCursor(context.Context, domain.Cursor) error
	ClearCursor(context.Context, string) error

	StartRun(context.Context, domain.SyncRun) error
	FinishRun(context.Context, domain.SyncRun, *domain.Event) error
	Runs(context.Context, string, string, int) ([]domain.SyncRun, error)

	EventHandled(context.Context, string) (bool, error)
	MarkEventHandled(context.Context, string, string, time.Time) error
	DeleteHandledEventsBefore(context.Context, time.Time) (int64, error)
	DeleteRunsBefore(context.Context, time.Time) (int64, error)
}

// Directory is auth, which decides who the caller is.
type Directory interface {
	Membership(context.Context, string, authclient.Credentials) (domain.OrgMembership, error)
}

// Apps is apps, which owns a member's connections to third-party apps and
// the Composio accounts behind them. Nil when COMPOSIO_API_KEY is not
// configured, in which case nothing reads through a connection: see
// usableConnection.
type Apps interface {
	Connection(ctx context.Context, organizationID, connectionID string, credentials authclient.Credentials) (domain.AppConnection, error)
	// Targets is what a connection holds that could be indexed, as apps
	// discovered it from the app itself.
	Targets(ctx context.Context, organizationID, connectionID string, credentials authclient.Credentials) ([]domain.AppTarget, error)
}

// Grants mints a token that acts as a member while they are away.
//
// An internal source is read through a service that has no service identity
// to offer: files forwards the caller's credentials to auth and accepts
// nothing else, so there is no bare-service way in. Auth's agent grant — a
// member's standing consent for work to run as them — is what fills that gap.
// Without one configured, an internal source says so rather than failing
// somewhere less obvious.
type Grants interface {
	MemberToken(ctx context.Context, organizationID, memberID string) (string, error)
}

// Originals is where a document's own bytes are kept when knowledge does not
// read them itself — a PDF, an Office file — so a backend that can parse one
// still gets to. Nil (KNOWLEDGE_ORIGINALS_BUCKET unset) means such a document
// is skipped instead, the same as before this existed.
type Originals interface {
	// Put writes content under key, replacing whatever was there. mime is the
	// original's own content type, for the object's metadata.
	Put(ctx context.Context, key string, content []byte, mime string) error
	// Delete removes an object a document no longer needs: it was superseded
	// by a new version at a new, content-addressed key, or the document
	// itself was retracted. Deleting a key that is already gone is not an
	// error — the end state is what a caller of Delete wants, not a
	// particular starting one.
	Delete(ctx context.Context, key string) error
}

type Dependencies struct {
	Store      Store
	Directory  Directory
	Apps       Apps
	Grants     Grants
	Extractors sources.Registry
	// Originals is nil unless KNOWLEDGE_ORIGINALS_BUCKET is configured.
	Originals Originals
	// Judge decides whether a connected app's document is worth indexing. Nil
	// indexes what the rules alone do not exclude, unjudged.
	Judge  Judge
	Logger *slog.Logger
	Clock  func() time.Time
}

type Config struct {
	ServiceName string
	// FullSweepInterval is how long a full sweep's word about what a source
	// holds is trusted before a pass starts over from the beginning, so a
	// document removed at the provider is eventually noticed even long after
	// a source's first pass. Zero (the caller built Config directly, such as
	// a test) defaults to domain.DefaultFullSweepInterval.
	FullSweepInterval time.Duration
	// OriginalsBucket names the bucket Originals writes to, carried on every
	// published Original so a consumer knows where to read it from. Empty
	// unless Dependencies.Originals is set.
	OriginalsBucket string
}

type Service struct {
	store           Store
	directory       Directory
	apps            Apps
	grants          Grants
	extractors      sources.Registry
	originals       Originals
	originalsBucket string
	judge           Judge
	logger          *slog.Logger
	now             func() time.Time
	config          Config
}

func New(dependencies Dependencies, config Config) *Service {
	service := &Service{
		store:           dependencies.Store,
		directory:       dependencies.Directory,
		apps:            dependencies.Apps,
		grants:          dependencies.Grants,
		extractors:      dependencies.Extractors,
		originals:       dependencies.Originals,
		originalsBucket: config.OriginalsBucket,
		judge:           dependencies.Judge,
		logger:          dependencies.Logger,
		now:             dependencies.Clock,
		config:          config,
	}
	if service.logger == nil {
		service.logger = slog.New(slog.DiscardHandler)
	}
	if service.now == nil {
		service.now = time.Now
	}
	if service.extractors == nil {
		service.extractors = sources.Registry{}
	}
	if service.config.ServiceName == "" {
		service.config.ServiceName = "knowledge"
	}
	if service.config.FullSweepInterval <= 0 {
		service.config.FullSweepInterval = domain.DefaultFullSweepInterval
	}
	return service
}

// Extractors is what this build can read, for a caller that reads the
// registry aloud before anything is registered on it.
func (s *Service) Extractors() []string { return s.extractors.Providers() }

// caller is who is asking, checked against auth on every call rather than
// trusted from anything held here.
func (s *Service) caller(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	if strings.TrimSpace(organizationID) == "" {
		return domain.OrgMembership{}, fmt.Errorf("an organization is required: %w", domain.ErrInvalid)
	}
	member, err := s.directory.Membership(ctx, organizationID, credentials)
	if err != nil {
		return domain.OrgMembership{}, err
	}
	if member.OrganizationID != organizationID || member.UserID == "" {
		// A membership for another organization must never authorize this
		// organization's sources or document bodies.
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	return member, nil
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newID is a prefixed random identifier, such as src_…
func newID(prefix string) (string, error) {
	value := make([]byte, 15)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(value)), nil
}

// event packages what happened for the outbox, in the envelope every Oryxa
// publisher uses.
func (s *Service) event(detailType string, data any) (domain.Event, error) {
	id, err := newID("evt")
	if err != nil {
		return domain.Event{}, err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: s.now(), Data: encoded}, nil
}

// documentPublished is the event a changed document travels as. A body over
// the contract's budget is left out and flagged; the document id still names
// what to fetch.
func (s *Service) documentPublished(source domain.Source, document domain.Document) (domain.Event, error) {
	published := knowledgev1.DocumentPublished{
		DocumentID: document.ID, OrganizationID: source.OrganizationID, SourceID: source.ID,
		ConnectionID: source.ConnectionID, Provider: source.Provider,
		Scope: string(source.Scope), TeamID: source.TeamID, MemberID: source.MemberID,
		Access:     documentAccess(source, document),
		ExternalID: document.ExternalID, Kind: document.Kind, Title: document.Title,
		URL: document.URL, MIME: document.MIME, ContentHash: document.ContentHash,
		Metadata: document.Metadata, SourceUpdatedAt: document.SourceUpdatedAt,
		ExtractedAt: document.ExtractedAt,
	}
	if len(document.Body) > knowledgev1.MaxBodyBytes {
		published.BodyOmitted = true
	} else {
		published.Body = document.Body
	}
	if original := document.Original; original != nil {
		// A document kept as its own bytes has no body at all; this is the
		// only way a consumer learns where to read it from.
		published.Original = &knowledgev1.Original{
			Bucket: original.Bucket, Key: original.Key, Size: original.Size, SHA256: original.SHA256,
		}
	}
	return s.event(knowledgev1.DocumentPublishedDetailType, published)
}

// Access labels a published document carries: who within its scope may read
// it.
const (
	accessMembers = "members"
	accessAdmins  = "admins"
)

// documentAccess is who within its scope may read a document. Only Files'
// own labels restrict one: a billing attachment, or a file shared with
// admins alone. Any other provider's metadata is the provider's say, and
// grants or revokes nothing.
func documentAccess(source domain.Source, document domain.Document) string {
	if source.Provider == filesProvider {
		if document.Metadata["purpose"] == "billing.attachment" || document.Metadata["access"] == accessAdmins {
			return accessAdmins
		}
	}
	return accessMembers
}

func (s *Service) documentRemoved(source domain.Source, document domain.Document, reason string) (domain.Event, error) {
	return s.event(knowledgev1.DocumentRemovedDetailType, knowledgev1.DocumentRemoved{
		DocumentID: document.ID, OrganizationID: source.OrganizationID, SourceID: source.ID,
		Provider: source.Provider, ExternalID: document.ExternalID,
		Reason: reason, RemovedAt: s.now(),
	})
}

// sourcePaused is the event a source travels as when it stops reading
// because what it reads through needs attention.
func (s *Service) sourcePaused(source domain.Source, reason string) (domain.Event, error) {
	return s.event(knowledgev1.SourcePausedDetailType, knowledgev1.SourcePaused{
		SourceID: source.ID, OrganizationID: source.OrganizationID,
		ConnectionID: source.ConnectionID, Provider: source.Provider,
		Reason: reason, PausedAt: s.now(),
	})
}
