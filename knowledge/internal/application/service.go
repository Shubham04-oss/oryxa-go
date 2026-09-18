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
	RemoveSource(context.Context, string, string, time.Time, func(domain.Source, []domain.Document) ([]domain.Event, error)) error
	RemoveSources(context.Context, string, string, time.Time, func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error)
	// RemoveSourcesByConnection retires every source reading through one
	// connection: apps.connection.removed.v1, which is gone for good.
	RemoveSourcesByConnection(context.Context, string, time.Time, func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error)
	// RemoveSourcesByTeam retires every source a deleted team held, and
	// retracts what they held in the same transaction.
	RemoveSourcesByTeam(context.Context, string, string, time.Time, func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error)
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

type Dependencies struct {
	Store      Store
	Directory  Directory
	Apps       Apps
	Grants     Grants
	Extractors sources.Registry
	Logger     *slog.Logger
	Clock      func() time.Time
}

type Config struct {
	ServiceName string
	// FullSweepInterval is how long a full sweep's word about what a source
	// holds is trusted before a pass starts over from the beginning, so a
	// document removed at the provider is eventually noticed even long after
	// a source's first pass. Zero (the caller built Config directly, such as
	// a test) defaults to domain.DefaultFullSweepInterval.
	FullSweepInterval time.Duration
}

type Service struct {
	store      Store
	directory  Directory
	apps       Apps
	grants     Grants
	extractors sources.Registry
	logger     *slog.Logger
	now        func() time.Time
	config     Config
}

func New(dependencies Dependencies, config Config) *Service {
	service := &Service{
		store:      dependencies.Store,
		directory:  dependencies.Directory,
		apps:       dependencies.Apps,
		grants:     dependencies.Grants,
		extractors: dependencies.Extractors,
		logger:     dependencies.Logger,
		now:        dependencies.Clock,
		config:     config,
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
	return s.directory.Membership(ctx, organizationID, credentials)
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
	return s.event(knowledgev1.DocumentPublishedDetailType, published)
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
