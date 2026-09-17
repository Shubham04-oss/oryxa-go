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
	"strings"
	"time"

	rageventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/rag/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/chunking"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/generation"
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
	CorpusCounts(ctx context.Context, corpusID string) (int64, int64, error)
	CorporaForWipe(ctx context.Context, organizationID string, scope domain.Scope, ownerID string) ([]domain.Corpus, error)
	DeleteCorpus(ctx context.Context, organizationID, corpusID string, event domain.Event) error

	UpsertDocument(ctx context.Context, document domain.Document, body string, event domain.Event) (domain.Document, postgres.Upsert, error)
	Document(ctx context.Context, organizationID, documentID string) (domain.Document, error)
	Documents(ctx context.Context, organizationID, corpusID string, status domain.IngestStatus, limit int) ([]domain.Document, error)
	DocumentsBySource(ctx context.Context, organizationID string, kind domain.SourceKind, ref string) ([]domain.Document, error)
	ChunkIDs(ctx context.Context, organizationID, documentID string) ([]string, error)
	DeleteDocument(ctx context.Context, organizationID, documentID string, event domain.Event) ([]string, error)

	UpsertOrganization(ctx context.Context, organizationID, name string, at time.Time) error
	RecordEvents(ctx context.Context, events []domain.Event) error
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

// Retriever finds the passages a question should be answered from.
type Retriever interface {
	Search(ctx context.Context, corpus domain.Corpus, query domain.Query) (retrieval.Results, error)
}

// MaxDocumentsPerPage bounds a listing.
const MaxDocumentsPerPage = 200

type Service struct {
	store     Store
	directory Directory
	teams     Teams
	files     Files
	vectors   Vectors
	retriever Retriever
	generator generation.Generator
	defaults  Defaults
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

func New(store Store, directory Directory, teams Teams, files Files, vectors Vectors,
	retriever Retriever, generator generation.Generator, defaults Defaults, logger *slog.Logger) *Service {
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
		store: store, directory: directory, teams: teams, files: files, vectors: vectors,
		retriever: retriever, generator: generator, defaults: defaults, logger: logger, now: time.Now,
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
	// A corpus on a backend this deployment cannot reach would accept
	// documents and never answer a query.
	if _, err := service.vectors.For(domain.Corpus{Backend: backend}); err != nil {
		return domain.Corpus{}, fmt.Errorf("this deployment cannot serve %q: %w", backend, domain.ErrInvalid)
	}
	chunking, err := input.Chunking.Normalize(spec)
	if err != nil {
		return domain.Corpus{}, err
	}
	if err := input.Metadata.Validate(); err != nil {
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

func (service *Service) Corpora(ctx context.Context, credentials authclient.Credentials, organizationID string, filter ListFilter) ([]domain.Corpus, error) {
	member, err := service.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	query := postgres.CorpusQuery{Scope: filter.Scope, MemberID: member.UserID, Admin: member.Role.IsOrgAdmin()}
	if filter.Scope == domain.ScopeTeam {
		access, err := service.teamAccess(ctx, credentials, organizationID, filter.TeamID)
		if err != nil {
			return nil, err
		}
		query.TeamID = access.TeamID
	}
	return service.store.Corpora(ctx, organizationID, query)
}

// Corpus reads one corpus, with its counts, if the caller may see it.
func (service *Service) Corpus(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string) (domain.Corpus, error) {
	corpus, _, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return domain.Corpus{}, err
	}
	documents, chunks, err := service.store.CorpusCounts(ctx, corpus.ID)
	if err != nil {
		return domain.Corpus{}, err
	}
	corpus.DocumentCount, corpus.ChunkCount = documents, chunks
	return corpus, nil
}

func (service *Service) DeleteCorpus(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string) error {
	corpus, member, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return err
	}
	if err := service.mayDelete(ctx, credentials, member, corpus); err != nil {
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
	return service.store.DeleteCorpus(ctx, organizationID, corpusID, event)
}

// AddDocumentInput is one document offered for indexing.
type AddDocumentInput struct {
	Source      domain.Source
	Title       string
	ContentType string
	// Text is the document body for an inline or external source. A file
	// source leaves it empty and rag fetches the bytes from files as the
	// caller.
	Text     string
	Metadata domain.Metadata
}

// AddDocument accepts a document and queues it for indexing.
//
// It returns as soon as the text is safely stored, rather than waiting on an
// embedding provider. The same bytes offered again are recognised and cost
// nothing, which is what lets an upstream pipeline re-deliver freely.
func (service *Service) AddDocument(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string, input AddDocumentInput) (domain.Document, error) {
	corpus, member, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return domain.Document{}, err
	}
	if err := service.mayWrite(ctx, credentials, member, corpus); err != nil {
		return domain.Document{}, err
	}
	if err := input.Source.Validate(); err != nil {
		return domain.Document{}, err
	}
	if err := input.Metadata.Validate(); err != nil {
		return domain.Document{}, err
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
		Metadata: input.Metadata, Status: domain.StatusPending,
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
	return stored, nil
}

func (service *Service) Documents(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string, status domain.IngestStatus, limit int) ([]domain.Document, error) {
	corpus, _, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return nil, err
	}
	if status != "" && !status.Valid() {
		return nil, fmt.Errorf("status is not allowed: %w", domain.ErrInvalid)
	}
	if limit <= 0 || limit > MaxDocumentsPerPage {
		limit = MaxDocumentsPerPage
	}
	return service.store.Documents(ctx, organizationID, corpus.ID, status, limit)
}

func (service *Service) Document(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID, documentID string) (domain.Document, error) {
	corpus, _, err := service.readable(ctx, credentials, organizationID, corpusID)
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
	return document, nil
}

func (service *Service) DeleteDocument(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID, documentID string) error {
	corpus, member, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return err
	}
	if err := service.mayWrite(ctx, credentials, member, corpus); err != nil {
		return err
	}
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
	chunkIDs, err := service.store.ChunkIDs(ctx, organizationID, documentID)
	if err != nil {
		return err
	}
	if err := service.deleteVectors(ctx, corpus, chunkIDs); err != nil {
		return err
	}
	_, err = service.store.DeleteDocument(ctx, organizationID, documentID, event)
	return err
}

// Search returns the passages a question matches, without writing an answer.
func (service *Service) Search(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string, query domain.Query) (retrieval.Results, error) {
	corpus, _, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return retrieval.Results{}, err
	}
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
	corpus, _, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return Answered{}, err
	}
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
	if !member.Role.Valid() {
		return domain.OrgMembership{}, domain.ErrForbidden
	}
	return member, nil
}

// readable loads a corpus the caller may see, or reports it as missing.
func (service *Service) readable(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string) (domain.Corpus, domain.OrgMembership, error) {
	member, err := service.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Corpus{}, domain.OrgMembership{}, err
	}
	corpus, err := service.lookup(ctx, organizationID, corpusID)
	if err != nil {
		return domain.Corpus{}, domain.OrgMembership{}, err
	}
	if err := service.mayRead(ctx, credentials, member, corpus); err != nil {
		return domain.Corpus{}, domain.OrgMembership{}, err
	}
	return corpus, member, nil
}

// lookup resolves a corpus by id or by the slug its creator chose.
func (service *Service) lookup(ctx context.Context, organizationID, reference string) (domain.Corpus, error) {
	if strings.HasPrefix(reference, domain.CorpusIDPrefix) {
		return service.store.Corpus(ctx, organizationID, reference)
	}
	return service.store.CorpusBySlug(ctx, organizationID, reference)
}

// mayRead decides who can search a corpus. Placement mirrors files exactly.
func (service *Service) mayRead(ctx context.Context, credentials authclient.Credentials, member domain.OrgMembership, corpus domain.Corpus) error {
	switch corpus.Scope {
	case domain.ScopeOrganization:
		return nil
	case domain.ScopeMember:
		if corpus.MemberID == member.UserID || member.Role.IsOrgAdmin() {
			return nil
		}
		return domain.ErrNotFound
	case domain.ScopeTeam:
		if _, err := service.teamAccess(ctx, credentials, corpus.OrganizationID, corpus.TeamID); err != nil {
			return err
		}
		return nil
	default:
		return domain.ErrNotFound
	}
}

// mayWrite decides who can add or remove documents. Being able to see a
// corpus is enough for a team or member corpus; an organization corpus is
// shared by everyone, so writing to it is an admin's job.
func (service *Service) mayWrite(ctx context.Context, credentials authclient.Credentials, member domain.OrgMembership, corpus domain.Corpus) error {
	switch corpus.Scope {
	case domain.ScopeOrganization:
		if member.Role.IsOrgAdmin() {
			return nil
		}
		return domain.ErrForbidden
	case domain.ScopeMember:
		if corpus.MemberID == member.UserID || member.Role.IsOrgAdmin() {
			return nil
		}
		return domain.ErrNotFound
	case domain.ScopeTeam:
		if _, err := service.teamAccess(ctx, credentials, corpus.OrganizationID, corpus.TeamID); err != nil {
			return err
		}
		return nil
	default:
		return domain.ErrForbidden
	}
}

// mayDelete decides who can remove a whole corpus, which is a larger act than
// removing one document from it.
func (service *Service) mayDelete(ctx context.Context, credentials authclient.Credentials, member domain.OrgMembership, corpus domain.Corpus) error {
	if member.Role.IsOrgAdmin() {
		return nil
	}
	switch corpus.Scope {
	case domain.ScopeMember:
		if corpus.MemberID == member.UserID {
			return nil
		}
		return domain.ErrNotFound
	case domain.ScopeTeam:
		access, err := service.teamAccess(ctx, credentials, corpus.OrganizationID, corpus.TeamID)
		if err != nil {
			return err
		}
		if access.CanManage || corpus.CreatedBy == member.UserID {
			return nil
		}
		return domain.ErrForbidden
	default:
		return domain.ErrForbidden
	}
}

func (service *Service) mayCreate(ctx context.Context, credentials authclient.Credentials, member domain.OrgMembership, scope domain.Scope, teamID string) error {
	switch scope {
	case domain.ScopeOrganization:
		if !member.Role.IsOrgAdmin() {
			return domain.ErrForbidden
		}
		return nil
	case domain.ScopeTeam:
		_, err := service.teamAccess(ctx, credentials, member.OrganizationID, teamID)
		return err
	default:
		return nil
	}
}

// teamAccess asks teams. A team the caller cannot see reads as missing,
// because teams conceals it and so does rag.
func (service *Service) teamAccess(ctx context.Context, credentials authclient.Credentials, organizationID, teamID string) (domain.TeamAccess, error) {
	if service.teams == nil {
		return domain.TeamAccess{}, fmt.Errorf("this deployment cannot reach the teams service, so team corpora are unavailable: %w", domain.ErrInvalid)
	}
	access, err := service.teams.Team(ctx, organizationID, teamID, credentials)
	if err != nil {
		return domain.TeamAccess{}, conceal(err)
	}
	if !access.CanSee() {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	return access, nil
}

func (service *Service) dropVectors(ctx context.Context, corpus domain.Corpus) error {
	store, err := service.vectors.For(corpus)
	if err != nil {
		return err
	}
	return store.DropCorpus(ctx, corpus)
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

// slugify makes a usable slug out of a name, for a caller who did not choose
// one.
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
	if len(slug) > 63 {
		slug = strings.Trim(slug[:63], "-")
	}
	return slug
}
