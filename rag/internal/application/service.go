// Package application is where a request becomes work: who the caller is,
// what they may see, and which of the pieces below does the job.
//
// Privilege comes from auth and from teams, never from this service. A corpus
// hangs on a member, a team or the organization exactly as a file does, so a
// member who understands where their files live understands where their
// corpora live too. Anything the caller may not see reads as missing rather
// than forbidden, because saying "you may not see that" tells them it exists.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	rageventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/rag/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/access"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/chunking"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/generation"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/knowledgeclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

// Store is what this service needs from PostgreSQL.
type Store interface {
	CreateCorpus(ctx context.Context, corpus domain.Corpus, event domain.Event) error
	Corpus(ctx context.Context, organizationID, corpusID string) (domain.Corpus, error)
	CorpusBySlug(ctx context.Context, organizationID, slug string) (domain.Corpus, error)
	Corpora(ctx context.Context, organizationID string, query postgres.CorpusQuery) ([]domain.Corpus, error)
	CorpusCounts(ctx context.Context, corpusID string, includeAdminOnly bool) (int64, int64, error)
	CorporaForWipe(ctx context.Context, organizationID string, scope domain.Scope, ownerID string) ([]domain.Corpus, error)
	DeleteCorpus(ctx context.Context, organizationID, corpusID string, event domain.Event) error

	UpsertDocument(ctx context.Context, document domain.Document, body string, event domain.Event) (domain.Document, postgres.Upsert, error)
	Document(ctx context.Context, organizationID, documentID string) (domain.Document, error)
	Documents(ctx context.Context, organizationID, corpusID string, status domain.IngestStatus, cursor string, limit int, access []domain.DocumentAccess) ([]domain.Document, string, error)
	DocumentsBySource(ctx context.Context, organizationID string, kind domain.SourceKind, ref string) ([]domain.Document, error)
	ChunkIDs(ctx context.Context, organizationID, documentID string) ([]string, error)
	// DocumentIDsForCorpus is used only to remove a bedrock-kb corpus's
	// documents from the knowledge base when the corpus itself is dropped;
	// see dropKnowledgeBaseCorpus.
	DocumentIDsForCorpus(ctx context.Context, corpusID string) ([]string, error)
	DeleteDocument(ctx context.Context, organizationID, documentID string, event domain.Event) ([]string, error)

	UpsertOrganization(ctx context.Context, organizationID, name string, at time.Time) error
	RecordEvents(ctx context.Context, events []domain.Event) error

	// KnowledgeDocumentMapping and its writers back the bridge from knowledge:
	// see knowledge_events.go.
	KnowledgeDocumentMapping(ctx context.Context, organizationID, knowledgeDocumentID string) (domain.KnowledgeDocumentMapping, error)
	UpsertKnowledgeDocumentMapping(ctx context.Context, mapping domain.KnowledgeDocumentMapping) error
	DeleteKnowledgeDocumentMapping(ctx context.Context, organizationID, knowledgeDocumentID string) error
	KnowledgeDocumentRemovedAt(ctx context.Context, organizationID, knowledgeDocumentID string) (time.Time, error)
	RecordKnowledgeDocumentRemoval(ctx context.Context, organizationID, knowledgeDocumentID string, removedAt time.Time) error
	WithKnowledgeDocumentLock(ctx context.Context, organizationID, knowledgeDocumentID string, run func() error) error
}

// Directory is auth: who the caller is in an organization.
type Directory interface {
	Membership(ctx context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error)
}

// Teams is the team graph. Without it, team-scoped corpora cannot exist.
type Teams interface {
	Team(ctx context.Context, organizationID, teamID string, credentials authclient.Credentials) (domain.TeamAccess, error)
}

// Files fetches a document's bytes as the caller.
type Files interface {
	Fetch(ctx context.Context, organizationID, fileID string, credentials authclient.Credentials) (domain.SourceContent, error)
}

// Vectors routes a corpus to the store holding its vectors.
type Vectors interface {
	For(corpus domain.Corpus) (vectorstore.Store, error)
	Backends() []domain.Backend
}

// KnowledgeBase is how a bedrock-kb corpus's documents are removed from
// Amazon Bedrock. A corpus on that backend has no chunk ids for Vectors to
// delete and no per-corpus index for it to drop the way S3 Vectors has one —
// one knowledge base serves the whole deployment — so removal there is by
// document id instead, against the knowledge base directly.
type KnowledgeBase interface {
	Delete(ctx context.Context, documentIDs []string) error
	// DeleteStaged removes a document's staged text, when it has any. It is
	// best-effort from the caller's point of view: left-behind staged text
	// costs storage, not correctness, so a failure here is logged rather
	// than failing the removal it rode in on.
	DeleteStaged(ctx context.Context, corpus domain.Corpus, document domain.Document) error
}

// Retriever finds the passages a question should be answered from.
type Retriever interface {
	Search(ctx context.Context, corpus domain.Corpus, query domain.Query) (retrieval.Results, error)
}

// MaxDocumentsPerPage bounds a listing.
const MaxDocumentsPerPage = 200

// Documents reads back a knowledge document whose body was too large to
// travel on the bus. Nil where a deployment cannot reach knowledge's
// internal API, in which case the largest documents stay unindexed.
type Documents interface {
	Body(ctx context.Context, organizationID, documentID string) (knowledgeclient.Document, error)
}

type Service struct {
	store         Store
	directory     Directory
	teams         Teams
	files         Files
	documents     Documents
	vectors       Vectors
	retriever     Retriever
	generator     generation.Generator
	knowledgeBase KnowledgeBase
	defaults      Defaults
	// decisions keeps who was let into what, and by which rule; nil records
	// nothing. See decisions.go.
	decisions Recorder
	logger    *slog.Logger
	now       func() time.Time
}

// Defaults are what a corpus gets when its creator does not choose. They come
// from configuration so a deployment can prefer the provider it has quota on.
type Defaults struct {
	Provider domain.Provider
	Model    string
	Metric   domain.Metric
	Backend  domain.Backend
	// AnswerInstructions is appended to the grounding rules for every answer.
	AnswerInstructions string
}

// New builds a Service. knowledgeBase is nil in a deployment with no Bedrock
// knowledge base configured, in which case a bedrock-kb corpus's documents
// fail to remove by name rather than reaching a nil client — the same shape
// of failure Vectors.For already gives a corpus on a backend this deployment
// cannot serve. Corpus creation is what actually keeps such a corpus from
// existing in the first place; see requireBackendSpec.
func New(store Store, directory Directory, teams Teams, files Files, documents Documents, vectors Vectors,
	retriever Retriever, generator generation.Generator, knowledgeBase KnowledgeBase, defaults Defaults, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if defaults.Provider == "" {
		defaults.Provider = domain.ProviderBedrock
	}
	if defaults.Model == "" {
		defaults.Model = "amazon.titan-embed-text-v2:0"
	}
	if defaults.Metric == "" {
		defaults.Metric = domain.MetricCosine
	}
	if defaults.Backend == "" {
		defaults.Backend = domain.BackendPgvector
	}
	return &Service{
		store: store, directory: directory, teams: teams, files: files, documents: documents, vectors: vectors,
		retriever: retriever, generator: generator, knowledgeBase: knowledgeBase, defaults: defaults, logger: logger, now: time.Now,
	}
}

// CreateCorpusInput is what a caller asks for when making a corpus.
type CreateCorpusInput struct {
	Slug       string
	Name       string
	Scope      domain.Scope
	TeamID     string
	Provider   domain.Provider
	Model      string
	Dimensions int
	Metric     domain.Metric
	Backend    domain.Backend
	Chunking   domain.ChunkPolicy
	Metadata   domain.Metadata
}

func (service *Service) CreateCorpus(ctx context.Context, credentials authclient.Credentials, organizationID string, input CreateCorpusInput) (domain.Corpus, error) {
	member, err := service.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Corpus{}, err
	}
	// A new corpus cannot be in a grant that names corpora, so the specialist
	// could not read what it made.
	if member.CorporaLimited() {
		return domain.Corpus{}, domain.ErrForbidden
	}
	name, err := domain.Name(input.Name)
	if err != nil {
		return domain.Corpus{}, err
	}
	slug := input.Slug
	if strings.TrimSpace(slug) == "" {
		slug = slugify(name)
	}
	slug, err = domain.Slug(slug)
	if err != nil {
		return domain.Corpus{}, err
	}
	if reservedSlug(slug) {
		return domain.Corpus{}, fmt.Errorf("slugs beginning %q are kept for the corpora rag builds from connected apps: %w", reservedSlugPrefix, domain.ErrInvalid)
	}
	scope := input.Scope
	if scope == "" {
		scope = domain.ScopeMember
	}
	if !scope.Valid() {
		return domain.Corpus{}, fmt.Errorf("scope is not allowed: %w", domain.ErrInvalid)
	}
	teamID, memberID, err := domain.Place(scope, input.TeamID, member.UserID)
	if err != nil {
		return domain.Corpus{}, err
	}
	if err := service.mayCreate(ctx, credentials, member, scope, teamID); err != nil {
		return domain.Corpus{}, err
	}

	provider := input.Provider
	if provider == "" {
		provider = service.defaults.Provider
	}
	model := input.Model
	if strings.TrimSpace(model) == "" && provider == service.defaults.Provider {
		model = service.defaults.Model
	}
	metric := input.Metric
	if metric == "" {
		metric = service.defaults.Metric
	}
	spec, err := domain.ResolveSpec(provider, model, input.Dimensions, metric)
	if err != nil {
		return domain.Corpus{}, err
	}
	backend := input.Backend
	if backend == "" {
		backend = service.defaults.Backend
	}
	if !backend.Valid() {
		return domain.Corpus{}, fmt.Errorf("backend is not allowed: %w", domain.ErrInvalid)
	}
	if err := backend.Supports(spec.Metric); err != nil {
		return domain.Corpus{}, err
	}
	if err := requireBackendSpec(backend, spec); err != nil {
		return domain.Corpus{}, err
	}
	// A corpus on a backend this deployment cannot reach would accept
	// documents and never answer a query.
	if _, err := service.vectors.For(domain.Corpus{Backend: backend}); err != nil {
		return domain.Corpus{}, fmt.Errorf("this deployment cannot serve %q: %w", backend, domain.ErrInvalid)
	}
	chunking, err := input.Chunking.Normalize(spec)
	if err != nil {
		return domain.Corpus{}, err
	}
	if err := input.Metadata.ValidateCallerCorpus(); err != nil {
		return domain.Corpus{}, err
	}

	id, err := domain.NewID(domain.CorpusIDPrefix)
	if err != nil {
		return domain.Corpus{}, err
	}
	now := service.now().UTC()
	corpus := domain.Corpus{
		ID: id, OrganizationID: organizationID, Slug: slug, Name: name,
		Scope: scope, TeamID: teamID, MemberID: memberID,
		Spec: spec, Backend: backend, Chunking: chunking, Metadata: input.Metadata,
		CreatedBy: member.UserID, CreatedAt: now, UpdatedAt: now,
	}
	event, err := service.event(rageventsv1.CorpusCreatedDetailType, rageventsv1.CorpusCreated{
		CorpusID: corpus.ID, OrganizationID: organizationID, Slug: slug, Name: name,
		Scope: string(scope), TeamID: teamID, MemberID: memberID,
		Provider: string(spec.Provider), Model: spec.Model, Dimensions: spec.Dimensions,
		Metric: string(spec.Metric), Backend: string(backend),
		CreatedBy: member.UserID, CreatedAt: now,
	})
	if err != nil {
		return domain.Corpus{}, err
	}
	if err := service.store.CreateCorpus(ctx, corpus, event); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return domain.Corpus{}, fmt.Errorf("a corpus with the slug %q already exists here: %w", slug, domain.ErrConflict)
		}
		return domain.Corpus{}, err
	}
	return corpus, nil
}

// ListFilter narrows a corpus listing.
type ListFilter struct {
	Scope  domain.Scope
	TeamID string
}

// Corpora lists the corpora the filter selects that the caller may read. The
// store narrows by the filter and by the caller's own member corpora; which
// of those come back is decided by mayRead, the same check a single read
// makes, so a listing never shows a corpus Corpus would refuse.
//
// What the filter selects: with no scope, the caller's own member corpora and
// the organization's; with member scope, the caller's own; with team scope,
// that team's; with organization scope, the organization's. An org admin can
// read any member's corpus by id, but a listing of member corpora is still
// their own.
func (service *Service) Corpora(ctx context.Context, credentials authclient.Credentials, organizationID string, filter ListFilter) ([]domain.Corpus, error) {
	member, err := service.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	reads := service.reads(credentials, member)
	query := postgres.CorpusQuery{Scope: filter.Scope, MemberID: member.UserID, IncludeAdminOnly: member.SeesAdminOnlyDocuments()}
	if filter.Scope == domain.ScopeTeam {
		query.TeamID = filter.TeamID
		// A team the caller cannot see is not found, as a single read of
		// one of its corpora would be, rather than an empty list that says
		// the team exists. The answer is kept for mayRead below.
		if !member.ActsAsAdmin() {
			access, err := reads.team(ctx, organizationID, filter.TeamID)
			if err != nil {
				return nil, err
			}
			query.TeamID = access.TeamID
		}
	}
	corpora, err := service.store.Corpora(ctx, organizationID, query)
	if err != nil {
		return nil, err
	}
	return reads.readable(ctx, corpora)
}

// Corpus reads one corpus, with its counts, if the caller may see it.
func (service *Service) Corpus(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string) (domain.Corpus, error) {
	corpus, member, rule, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return domain.Corpus{}, err
	}
	documents, chunks, err := service.store.CorpusCounts(ctx, corpus.ID, member.SeesAdminOnlyDocuments())
	if err != nil {
		return domain.Corpus{}, err
	}
	service.record(ctx, member, corpus, access.ActionRead, resourceCorpus, corpus.ID, rule)
	corpus.DocumentCount, corpus.ChunkCount = documents, chunks
	return corpus, nil
}

func (service *Service) DeleteCorpus(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string) error {
	corpus, member, _, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return err
	}
	rule, err := service.mayDelete(ctx, credentials, member, corpus)
	if err != nil {
		return err
	}
	now := service.now().UTC()
	event, err := service.event(rageventsv1.CorpusDeletedDetailType, rageventsv1.CorpusDeleted{
		CorpusID: corpus.ID, OrganizationID: organizationID, Scope: string(corpus.Scope),
		TeamID: corpus.TeamID, MemberID: corpus.MemberID,
		Reason: rageventsv1.ReasonDeleted, DeletedBy: member.UserID, DeletedAt: now,
	})
	if err != nil {
		return err
	}
	// The vectors go first. If the rows went first and this failed, the
	// vectors would be orphaned with nothing left pointing at them.
	if err := service.dropVectors(ctx, corpus); err != nil {
		return err
	}
	if err := service.store.DeleteCorpus(ctx, organizationID, corpusID, event); err != nil {
		return err
	}
	service.record(ctx, member, corpus, access.ActionDelete, resourceCorpus, corpus.ID, rule)
	return nil
}

// AddDocumentInput is one document offered for indexing.
type AddDocumentInput struct {
	Source      domain.Source
	Title       string
	ContentType string
	Access      domain.DocumentAccess
	// Text is the document body for an inline or external source. A file
	// source leaves it empty and rag fetches the bytes from files as the
	// caller.
	Text     string
	Metadata domain.Metadata
}

// DocumentReader supplies the document once the caller is known to be allowed
// to add it, so a request that is not theirs to make never has its body read.
type DocumentReader func() (AddDocumentInput, error)

// AddDocument accepts a document and queues it for indexing.
//
// It returns as soon as the text is safely stored, rather than waiting on an
// embedding provider. The same bytes offered again are recognised and cost
// nothing, which is what lets an upstream pipeline re-deliver freely.
func (service *Service) AddDocument(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string, read DocumentReader) (domain.Document, error) {
	corpus, member, _, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return domain.Document{}, err
	}
	rule, err := service.mayWrite(ctx, credentials, member, corpus)
	if err != nil {
		return domain.Document{}, err
	}
	service.logAgent(ctx, member, "add_document", corpus.ID)
	// Only now, with the caller known and allowed to write here: a document can
	// be megabytes, and nobody else has theirs read.
	input, err := read()
	if err != nil {
		return domain.Document{}, err
	}
	if err := input.Source.Validate(); err != nil {
		return domain.Document{}, err
	}
	if err := input.Metadata.ValidateCallerDocument(); err != nil {
		return domain.Document{}, err
	}
	// No access named is passed on as none: the store keeps the label a
	// document it already holds has, and gives a new one members. A
	// re-delivery that leaves access out must never open an admin-only
	// document to every member.
	documentAccess := input.Access
	if documentAccess != "" && !documentAccess.Valid() {
		return domain.Document{}, fmt.Errorf("document access is not allowed: %w", domain.ErrInvalid)
	}
	if documentAccess == domain.AccessAdmins && (corpus.Scope != domain.ScopeOrganization || !member.ActsAsAdmin()) {
		return domain.Document{}, domain.ErrForbidden
	}
	title, err := domain.Title(input.Title)
	if err != nil {
		return domain.Document{}, err
	}

	text, contentType := input.Text, input.ContentType
	if input.Source.Kind == domain.SourceFile {
		if service.files == nil {
			return domain.Document{}, fmt.Errorf("this deployment cannot reach the files service: %w", domain.ErrInvalid)
		}
		// Fetched now, while the caller's credentials are to hand. The ingest
		// worker runs later and must never need to act as anyone.
		object, err := service.files.Fetch(ctx, organizationID, input.Source.Ref, credentials)
		if err != nil {
			return domain.Document{}, err
		}
		fileAccess := object.Access
		if fileAccess == "" {
			fileAccess = domain.AccessMembers
		}
		if object.Purpose == "billing.attachment" {
			fileAccess = domain.AccessAdmins
		}
		if !fileAccess.Valid() {
			return domain.Document{}, fmt.Errorf("file access is invalid: %w", domain.ErrConflict)
		}
		// A file the caller may read must never be copied where someone who
		// may not read it could find it.
		if !fileFits(corpus, object, fileAccess, member.UserID) {
			return domain.Document{}, domain.ErrNotFound
		}
		// Files serves an org admin's agent every team's files, as it serves
		// the admin, so for an agent, files having served a team file does not
		// show that its member is on the team. Teams is asked instead.
		if member.Agent && object.Scope == domain.ScopeTeam && corpus.Scope == domain.ScopeMember {
			if _, err := service.teamAccess(ctx, credentials, member, organizationID, object.TeamID); err != nil {
				return domain.Document{}, err
			}
		}
		if input.Access == domain.AccessAdmins && fileAccess == domain.AccessMembers {
			return domain.Document{}, fmt.Errorf("set admin access on the file in Files first: %w", domain.ErrInvalid)
		}
		documentAccess = fileAccess
		text = object.Body
		if strings.TrimSpace(contentType) == "" {
			contentType = object.ContentType
		}
		if title == "" {
			title = object.Filename
		}
	}
	contentType, err = domain.ContentType(contentType)
	if err != nil {
		return domain.Document{}, err
	}
	if text, err = domain.Text(text); err != nil {
		return domain.Document{}, err
	}
	// Normalized before it is stored, so the byte offsets a chunk carries
	// address exactly the text that was kept.
	body := chunking.Normalize(text, contentType)
	if strings.TrimSpace(body) == "" {
		return domain.Document{}, fmt.Errorf("the document has no text once normalized: %w", domain.ErrInvalid)
	}

	id, err := domain.NewID(domain.DocumentIDPrefix)
	if err != nil {
		return domain.Document{}, err
	}
	now := service.now().UTC()
	document := domain.Document{
		ID: id, CorpusID: corpus.ID, OrganizationID: organizationID,
		Source: input.Source, Title: title, ContentType: contentType,
		ContentSHA256: domain.Hash(body), ByteSize: int64(len(body)),
		Metadata: input.Metadata, Access: documentAccess, Status: domain.StatusPending,
		CreatedBy: member.UserID, CreatedAt: now, UpdatedAt: now,
	}
	event, err := service.event(rageventsv1.DocumentReceivedDetailType, rageventsv1.DocumentReceived{
		DocumentID: id, CorpusID: corpus.ID, OrganizationID: organizationID,
		SourceKind: string(input.Source.Kind), SourceRef: input.Source.Ref,
		ContentType: contentType, ContentSHA256: document.ContentSHA256,
		ByteSize: document.ByteSize, CreatedBy: member.UserID, ReceivedAt: now,
	})
	if err != nil {
		return domain.Document{}, err
	}
	stored, outcome, err := service.store.UpsertDocument(ctx, document, body, event)
	if err != nil {
		return domain.Document{}, err
	}
	if outcome == postgres.UpsertUnchanged {
		service.logger.InfoContext(ctx, "document already indexed with the same content",
			"document_id", stored.ID, "corpus_id", corpus.ID)
	}
	service.record(ctx, member, corpus, access.ActionWrite, resourceDocument, stored.ID, rule)
	return stored, nil
}

// fileFits reports whether everyone who can read corpus can already read the
// file in files, so copying it there shows it to nobody new.
//
// Files lets any member read an organization file with members access, only
// org owners and admins read an admin-only one, whoever can see a team (and
// every org admin) read that team's files, and a member (and every org admin)
// read that member's own. mayRead gives a corpus the same readers by its
// placement, org admins included, so an organization file with members access
// fits any corpus, a team file fits its own team's corpus, and a member's file
// fits only that member's own corpus. An admin-only document may live only in
// an organization corpus, whose document ACL keeps it to admins there.
//
// A team file also fits the member corpus of whoever is copying it: files has
// just served it to them, so they can read it. Whether any other member can,
// rag cannot ask, so a team file goes into nobody else's member corpus. For
// an agent, files serving it proves less (see AddDocument).
func fileFits(corpus domain.Corpus, file domain.SourceContent, access domain.DocumentAccess, callerID string) bool {
	if access == domain.AccessAdmins {
		return file.Scope == domain.ScopeOrganization && corpus.Scope == domain.ScopeOrganization
	}
	switch file.Scope {
	case domain.ScopeOrganization:
		return true
	case domain.ScopeTeam:
		switch corpus.Scope {
		case domain.ScopeTeam:
			return file.TeamID != "" && corpus.TeamID == file.TeamID
		case domain.ScopeMember:
			return callerID != "" && corpus.MemberID == callerID
		default:
			return false
		}
	case domain.ScopeMember:
		return corpus.Scope == domain.ScopeMember && file.MemberID != "" && corpus.MemberID == file.MemberID
	default:
		return false
	}
}

// DocumentPage is one page of a corpus's documents, newest first.
type DocumentPage struct {
	Documents []domain.Document `json:"documents"`
	// NextCursor continues the listing from exactly where this page ended.
	// It is empty on the last page, which is what tells a caller to stop.
	NextCursor string `json:"next_cursor,omitempty"`
}

// Documents lists a corpus's documents. With no cursor a caller gets the
// first page, exactly as it does today; passing back the cursor a page
// returned continues from where that page left off.
func (service *Service) Documents(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string, status domain.IngestStatus, cursor string, limit int) (DocumentPage, error) {
	// A listing is not a decision about any one document, so nothing is
	// recorded here.
	corpus, member, _, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return DocumentPage{}, err
	}
	if status != "" && !status.Valid() {
		return DocumentPage{}, fmt.Errorf("status is not allowed: %w", domain.ErrInvalid)
	}
	if limit <= 0 || limit > MaxDocumentsPerPage {
		limit = MaxDocumentsPerPage
	}
	// The store keeps only the access labels VisibleTo allows, so a page
	// is never cut short by the filter below; that filter is the same check
	// Document makes, and is what decides. The cursor is the store's, from
	// the last row it read, so even a row dropped here hides nothing after
	// it.
	documents, nextCursor, err := service.store.Documents(ctx, organizationID, corpus.ID, status, cursor, limit, domain.ReadableAccess(member))
	if err != nil {
		return DocumentPage{}, err
	}
	documents = slices.DeleteFunc(documents, func(document domain.Document) bool { return !document.VisibleTo(member) })
	return DocumentPage{Documents: documents, NextCursor: nextCursor}, nil
}

func (service *Service) Document(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID, documentID string) (domain.Document, error) {
	corpus, member, rule, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return domain.Document{}, err
	}
	document, err := service.store.Document(ctx, organizationID, documentID)
	if err != nil {
		return domain.Document{}, err
	}
	if document.CorpusID != corpus.ID {
		return domain.Document{}, domain.ErrNotFound
	}
	if !document.VisibleTo(member) {
		return domain.Document{}, domain.ErrNotFound
	}
	// An admin-only document takes an org admin, whatever let them into
	// its corpus.
	if document.Access == domain.AccessAdmins {
		rule = access.ReasonOrgAdmin
	}
	service.record(ctx, member, corpus, access.ActionRead, resourceDocument, document.ID, rule)
	return document, nil
}

func (service *Service) DeleteDocument(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID, documentID string) error {
	corpus, member, _, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return err
	}
	rule, err := service.mayWrite(ctx, credentials, member, corpus)
	if err != nil {
		return err
	}
	service.logAgent(ctx, member, "delete_document", corpus.ID)
	document, err := service.store.Document(ctx, organizationID, documentID)
	if err != nil {
		return err
	}
	if document.CorpusID != corpus.ID {
		return domain.ErrNotFound
	}
	now := service.now().UTC()
	event, err := service.event(rageventsv1.DocumentRemovedDetailType, rageventsv1.DocumentRemoved{
		DocumentID: documentID, CorpusID: corpus.ID, OrganizationID: organizationID,
		SourceKind: string(document.Source.Kind), SourceRef: document.Source.Ref,
		Reason: rageventsv1.ReasonDeleted, DeletedBy: member.UserID, RemovedAt: now,
	})
	if err != nil {
		return err
	}
	// The vectors go before the rows, as they do when a corpus is deleted. If
	// the rows went first and this failed, the vectors would be left with
	// nothing pointing at them: unfindable, and still costing money to store.
	if err := service.removeDocumentVectors(ctx, corpus, document); err != nil {
		return err
	}
	if _, err := service.store.DeleteDocument(ctx, organizationID, documentID, event); err != nil {
		return err
	}
	service.record(ctx, member, corpus, access.ActionDelete, resourceDocument, documentID, rule)
	return nil
}

// Search returns the passages a question matches, without writing an answer.
func (service *Service) Search(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string, query domain.Query) (retrieval.Results, error) {
	corpus, member, rule, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return retrieval.Results{}, err
	}
	service.logAgent(ctx, member, "search", corpus.ID)
	service.record(ctx, member, corpus, access.ActionSearch, resourceCorpus, corpus.ID, rule)
	query.IncludeAdminOnly = member.SeesAdminOnlyDocuments()
	return service.retriever.Search(ctx, corpus, query)
}

// Answered is a grounded answer together with what it was built from, so a
// caller can show the passages beside it.
type Answered struct {
	Answer  domain.Answer  `json:"answer"`
	Matches []domain.Match `json:"matches"`
}

// Answer retrieves and then writes a grounded answer.
func (service *Service) Answer(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string, query domain.Query) (Answered, error) {
	if service.generator == nil {
		return Answered{}, fmt.Errorf("this deployment has no answering model configured: %w", domain.ErrInvalid)
	}
	corpus, member, rule, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return Answered{}, err
	}
	service.logAgent(ctx, member, "answer", corpus.ID)
	// Answering searches the corpus once, so it is recorded as one search.
	service.record(ctx, member, corpus, access.ActionSearch, resourceCorpus, corpus.ID, rule)
	query.IncludeAdminOnly = member.SeesAdminOnlyDocuments()
	results, err := service.retriever.Search(ctx, corpus, query)
	if err != nil {
		return Answered{}, err
	}
	answer, err := generation.Answer(ctx, service.generator, query.Text, results.Matches, service.defaults.AnswerInstructions)
	if err != nil {
		return Answered{}, err
	}
	answer.Usage.Add(results.Usage)
	return Answered{Answer: answer, Matches: results.Matches}, nil
}

// logAgent records that an agent was let into a corpus to do operation,
// naming the agent, so the trail says which of a member's agents acted.
func (service *Service) logAgent(ctx context.Context, member domain.OrgMembership, operation, corpusID string) {
	if !member.Agent {
		return
	}
	service.logger.InfoContext(ctx, "an agent used knowledge", "operation", operation,
		"organization_id", member.OrganizationID, "member_id", member.UserID,
		"agent_id", member.AgentID, "agent_role", member.AgentRole, "corpus_id", corpusID)
}

// caller asks auth who this is. A caller outside the organization gets not
// found, because auth conceals which organizations exist.
func (service *Service) caller(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	if strings.TrimSpace(organizationID) == "" {
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	member, err := service.directory.Membership(ctx, organizationID, credentials)
	if err != nil {
		return domain.OrgMembership{}, err
	}
	if member.OrganizationID != organizationID || member.UserID == "" {
		// Never turn an answer for another organization into authority over
		// the organization named in the request.
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	if !member.Role.Valid() {
		return domain.OrgMembership{}, domain.ErrForbidden
	}
	return member, nil
}

// readable loads a corpus the caller may see, or reports it as missing, and
// answers the rule that let them read it.
func (service *Service) readable(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string) (domain.Corpus, domain.OrgMembership, string, error) {
	member, err := service.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Corpus{}, domain.OrgMembership{}, "", err
	}
	corpus, err := service.lookup(ctx, organizationID, corpusID)
	if err != nil {
		return domain.Corpus{}, domain.OrgMembership{}, "", err
	}
	rule, err := service.reads(credentials, member).mayRead(ctx, corpus)
	if err != nil {
		return domain.Corpus{}, domain.OrgMembership{}, "", err
	}
	return corpus, member, rule, nil
}

// lookup resolves a corpus by id or by the slug its creator chose.
func (service *Service) lookup(ctx context.Context, organizationID, reference string) (domain.Corpus, error) {
	if strings.HasPrefix(reference, domain.CorpusIDPrefix) {
		return service.store.Corpus(ctx, organizationID, reference)
	}
	return service.store.CorpusBySlug(ctx, organizationID, reference)
}

// mayWrite decides who can add or remove documents. Being able to see a
// corpus is enough for a team or member corpus; an organization corpus is
// shared by everyone, so writing to it is an admin's job. An org admin writes
// to a team corpus without asking teams, exactly as mayRead lets them read it.
// It answers the rule that allowed the write (see granted).
func (service *Service) mayWrite(ctx context.Context, credentials authclient.Credentials, member domain.OrgMembership, corpus domain.Corpus) (string, error) {
	switch corpus.Scope {
	case domain.ScopeOrganization:
		if member.ActsAsAdmin() {
			return access.ReasonOrgAdmin, nil
		}
		return "", domain.ErrForbidden
	case domain.ScopeMember:
		if corpus.MemberID == member.UserID {
			return granted(member, access.ReasonOwner), nil
		}
		if member.ActsAsAdmin() {
			return access.ReasonOrgAdmin, nil
		}
		return "", domain.ErrNotFound
	case domain.ScopeTeam:
		if member.ActsAsAdmin() {
			return access.ReasonOrgAdmin, nil
		}
		if _, err := service.teamAccess(ctx, credentials, member, corpus.OrganizationID, corpus.TeamID); err != nil {
			return "", err
		}
		return granted(member, access.ReasonTeamMember), nil
	default:
		return "", domain.ErrForbidden
	}
}

// mayDelete decides who can remove a whole corpus, which is a larger act than
// removing one document from it. It answers the rule that allowed it (see
// granted).
func (service *Service) mayDelete(ctx context.Context, credentials authclient.Credentials, member domain.OrgMembership, corpus domain.Corpus) (string, error) {
	if corpus.Scope == domain.ScopeMember && corpus.MemberID == member.UserID {
		return granted(member, access.ReasonOwner), nil
	}
	if member.ActsAsAdmin() {
		return access.ReasonOrgAdmin, nil
	}
	switch corpus.Scope {
	case domain.ScopeMember:
		return "", domain.ErrNotFound
	case domain.ScopeTeam:
		team, err := service.teamAccess(ctx, credentials, member, corpus.OrganizationID, corpus.TeamID)
		if err != nil {
			return "", err
		}
		switch {
		case corpus.CreatedBy == member.UserID:
			return granted(member, access.ReasonOwner), nil
		case team.CanManage:
			return granted(member, access.ReasonTeamAdmin), nil
		}
		return "", domain.ErrForbidden
	default:
		return "", domain.ErrForbidden
	}
}

func (service *Service) mayCreate(ctx context.Context, credentials authclient.Credentials, member domain.OrgMembership, scope domain.Scope, teamID string) error {
	switch scope {
	case domain.ScopeOrganization:
		if !member.ActsAsAdmin() {
			return domain.ErrForbidden
		}
		return nil
	case domain.ScopeTeam:
		_, err := service.teamAccess(ctx, credentials, member, member.OrganizationID, teamID)
		return err
	default:
		return nil
	}
}

// teamAccess asks teams. A team the caller cannot see reads as missing,
// because teams conceals it and so does rag.
//
// Teams answers an agent's token as its member, so for an agent acting for an
// org admin it shows every team and says the agent can manage each one. An
// agent gets a plain member's reach, so for one only its member's own place
// on the team counts: it reaches a team its member is on, and manages one
// its member is a team admin of.
func (service *Service) teamAccess(ctx context.Context, credentials authclient.Credentials, member domain.OrgMembership, organizationID, teamID string) (domain.TeamAccess, error) {
	if service.teams == nil {
		return domain.TeamAccess{}, fmt.Errorf("this deployment cannot reach the teams service, so team corpora are unavailable: %w", domain.ErrInvalid)
	}
	access, err := service.teams.Team(ctx, organizationID, teamID, credentials)
	if err != nil {
		return domain.TeamAccess{}, conceal(err)
	}
	access, visible := member.TeamReach(access)
	if !visible {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	return access, nil
}

// requireBackendSpec refuses a corpus whose spec does not match what its
// backend requires. Today only bedrock-kb requires one: a knowledge base
// embeds with whatever model it was created with, chosen once by OpenTofu
// outside rag, so a corpus there cannot pin a spec of its own the way one on
// pgvector or S3 Vectors can. The database enforces the same rule (see
// migration 007), so this exists to fail with a message naming what a corpus
// on this backend actually uses, rather than a bare constraint violation.
func requireBackendSpec(backend domain.Backend, spec domain.EmbeddingSpec) error {
	required, ok := backend.RequiredEmbeddingSpec()
	if !ok || spec.Equal(required) {
		return nil
	}
	return fmt.Errorf("%s corpora use the knowledge base's own embedding spec (%s), not %s: %w",
		backend, required, spec, domain.ErrInvalid)
}

// dropVectors removes everything a corpus holds, wherever its content lives:
// vectorstore.Store for pgvector and S3 Vectors, or the knowledge base
// itself for a bedrock-kb corpus, which vectorstore.Store never touches.
func (service *Service) dropVectors(ctx context.Context, corpus domain.Corpus) error {
	if corpus.Backend == domain.BackendBedrockKB {
		return service.dropKnowledgeBaseCorpus(ctx, corpus)
	}
	store, err := service.vectors.For(corpus)
	if err != nil {
		return err
	}
	return store.DropCorpus(ctx, corpus)
}

// dropKnowledgeBaseCorpus deletes every document this corpus put into the
// knowledge base. There is no per-corpus index there to drop the way S3
// Vectors has one — one knowledge base serves the whole deployment — so a
// corpus going away means naming every document it owned instead.
func (service *Service) dropKnowledgeBaseCorpus(ctx context.Context, corpus domain.Corpus) error {
	if service.knowledgeBase == nil {
		return fmt.Errorf("this deployment cannot reach its knowledge base: %w", domain.ErrInvalid)
	}
	documentIDs, err := service.store.DocumentIDsForCorpus(ctx, corpus.ID)
	if err != nil {
		return err
	}
	if len(documentIDs) == 0 {
		return nil
	}
	return service.knowledgeBase.Delete(ctx, documentIDs)
}

func (service *Service) deleteVectors(ctx context.Context, corpus domain.Corpus, chunkIDs []string) error {
	if len(chunkIDs) == 0 {
		return nil
	}
	store, err := service.vectors.For(corpus)
	if err != nil {
		return err
	}
	return store.Delete(ctx, corpus, chunkIDs)
}

// removeDocumentVectors removes one document's indexed content, wherever it
// lives. A bedrock-kb corpus has no chunk rows for ChunkIDs to find — Bedrock
// never hands rag any — so it is deleted by its own document id instead, and
// any text staged for it is cleaned up alongside; every other backend keeps
// going through ChunkIDs and Vectors.Delete exactly as before.
func (service *Service) removeDocumentVectors(ctx context.Context, corpus domain.Corpus, document domain.Document) error {
	if corpus.Backend == domain.BackendBedrockKB {
		if service.knowledgeBase == nil {
			return fmt.Errorf("this deployment cannot reach its knowledge base: %w", domain.ErrInvalid)
		}
		if err := service.knowledgeBase.Delete(ctx, []string{document.ID}); err != nil {
			return err
		}
		if err := service.knowledgeBase.DeleteStaged(ctx, corpus, document); err != nil {
			service.logger.WarnContext(ctx, "deleting a document's staged text failed",
				"document_id", document.ID, "corpus_id", corpus.ID, "error", err)
		}
		return nil
	}
	chunkIDs, err := service.store.ChunkIDs(ctx, corpus.OrganizationID, document.ID)
	if err != nil {
		return err
	}
	return service.deleteVectors(ctx, corpus, chunkIDs)
}

func (service *Service) event(detailType string, payload any) (domain.Event, error) {
	id, err := domain.NewID(domain.EventIDPrefix)
	if err != nil {
		return domain.Event{}, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: service.now().UTC(), Data: data}, nil
}

// conceal turns a forbidden into a not found, so an error never reveals that
// something exists which the caller may not see.
func conceal(err error) error {
	if errors.Is(err, domain.ErrForbidden) {
		return domain.ErrNotFound
	}
	return err
}

// reservedSlugPrefix begins the slug of every corpus rag's Knowledge bridge
// makes: knowledge-organization, knowledge-team-<hash>,
// knowledge-member-<hash>, and the readable forms it made before (see
// knowledgeCorpusSlug). Anyone can work those out in advance, and a corpus
// someone else made under one first would leave Knowledge unable to index
// anything for that placement, so no caller may create a corpus under the
// prefix.
const reservedSlugPrefix = "knowledge-"

// derivedSlugPrefix goes in front of a slug made from a corpus's name when it
// would otherwise begin with reservedSlugPrefix, so a corpus named "Knowledge
// base" gets corpus-knowledge-base rather than a refusal.
const derivedSlugPrefix = "corpus-"

// reservedSlug reports whether a slug, as domain.Slug normalizes it, is one
// only rag's Knowledge bridge may use.
func reservedSlug(slug string) bool {
	return strings.HasPrefix(slug, reservedSlugPrefix)
}

// slugify makes a usable slug out of a name, for a caller who did not choose
// one. It never makes a reserved one: a name that would slug under
// reservedSlugPrefix gets derivedSlugPrefix in front.
func slugify(name string) string {
	var builder strings.Builder
	previousHyphen := false
	for _, symbol := range strings.ToLower(name) {
		switch {
		case (symbol >= 'a' && symbol <= 'z') || (symbol >= '0' && symbol <= '9'):
			builder.WriteRune(symbol)
			previousHyphen = false
		case !previousHyphen && builder.Len() > 0:
			builder.WriteByte('-')
			previousHyphen = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if reservedSlug(slug) {
		slug = derivedSlugPrefix + slug
	}
	if len(slug) > 63 {
		slug = strings.Trim(slug[:63], "-")
	}
	return slug
}
