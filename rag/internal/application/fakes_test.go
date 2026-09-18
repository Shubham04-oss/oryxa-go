package application

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/generation"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/postgres"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/retrieval"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/vectorstore"
)

// The org and its people, used throughout: an owner and an admin (both org
// admins), two ordinary members who both sit on team_eng at different
// privilege levels, a third ordinary member team_eng never heard of, a member
// whose role auth answered as something this service does not recognise, and
// a token nobody in the organization holds at all.
const testOrganization = "org_1"

func creds(userID string) authclient.Credentials {
	return authclient.Credentials{Authorization: "Bearer " + userID}
}

type fakeDirectory struct{}

func (fakeDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	token := strings.TrimPrefix(credentials.Authorization, "Bearer ")
	people := map[string]domain.OrgMembership{
		"user-owner":    {OrganizationID: testOrganization, OrganizationName: "Acme", UserID: "user-owner", Email: "owner@example.com", Role: domain.OrgRoleOwner},
		"user-admin":    {OrganizationID: testOrganization, OrganizationName: "Acme", UserID: "user-admin", Email: "admin@example.com", Role: domain.OrgRoleAdmin},
		"user-creator":  {OrganizationID: testOrganization, OrganizationName: "Acme", UserID: "user-creator", Email: "creator@example.com", Role: domain.OrgRoleMember},
		"user-teammate": {OrganizationID: testOrganization, OrganizationName: "Acme", UserID: "user-teammate", Email: "teammate@example.com", Role: domain.OrgRoleMember},
		"user-teamlead": {OrganizationID: testOrganization, OrganizationName: "Acme", UserID: "user-teamlead", Email: "teamlead@example.com", Role: domain.OrgRoleMember},
		"user-outsider": {OrganizationID: testOrganization, OrganizationName: "Acme", UserID: "user-outsider", Email: "outsider@example.com", Role: domain.OrgRoleMember},
		"user-badrole":  {OrganizationID: testOrganization, OrganizationName: "Acme", UserID: "user-badrole", Email: "badrole@example.com", Role: domain.OrgRole("pending")},
	}
	membership, ok := people[token]
	if !ok || organizationID != testOrganization {
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	return membership, nil
}

// fakeTeams knows one team, team_eng. user-creator and user-teammate can both
// see it as plain members; user-teamlead can see it and manage it. An org
// owner or admin sees and can manage every team whether or not they are a
// member of it — teams.CanSeeTeam and teams.CanManageTeam grant both
// automatically to org.IsOrgAdmin() — so user-owner and user-admin are here
// too, unlike user-outsider, an organization member in good standing who
// simply never joined team_eng and so cannot see it at all.
type fakeTeams struct {
	access map[string]domain.TeamAccess
	err    error
}

func newFakeTeams() *fakeTeams {
	return &fakeTeams{access: map[string]domain.TeamAccess{
		"user-creator/team_eng":  {TeamID: "team_eng", CanManage: false, Member: true},
		"user-teammate/team_eng": {TeamID: "team_eng", CanManage: false, Member: true},
		"user-teamlead/team_eng": {TeamID: "team_eng", CanManage: true, Member: true},
		"user-owner/team_eng":    {TeamID: "team_eng", CanManage: true, Member: false},
		"user-admin/team_eng":    {TeamID: "team_eng", CanManage: true, Member: false},
	}}
}

func (fake *fakeTeams) Team(_ context.Context, organizationID, teamID string, credentials authclient.Credentials) (domain.TeamAccess, error) {
	if fake.err != nil {
		return domain.TeamAccess{}, fake.err
	}
	if organizationID != testOrganization {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	token := strings.TrimPrefix(credentials.Authorization, "Bearer ")
	access, ok := fake.access[token+"/"+teamID]
	if !ok || access.TeamID == "" {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	return access, nil
}

type documentsCall struct {
	organizationID, corpusID string
	status                   domain.IngestStatus
	cursor                   string
	limit                    int
}

// fakeStore stands in for PostgreSQL. Corpora and documents are held in maps
// keyed by id, which is enough to exercise every access rule without a real
// database; Documents (the paginated listing) is a recording fake instead,
// since keyset pagination itself is proven against a real PostgreSQL by
// store_integration_test.go and all this layer needs to prove is that it asks
// for the right page and passes back what the store answered.
type fakeStore struct {
	mu                sync.Mutex
	corpora           map[string]domain.Corpus
	documents         map[string]domain.Document
	chunkIDs          map[string][]string
	orgs              map[string]string
	events            []domain.Event
	knowledgeMappings map[string]domain.KnowledgeDocumentMapping

	lastDocumentsCall documentsCall
	documentsPage     []domain.Document
	documentsNext     string
	documentsErr      error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		corpora:           map[string]domain.Corpus{},
		documents:         map[string]domain.Document{},
		chunkIDs:          map[string][]string{},
		orgs:              map[string]string{},
		knowledgeMappings: map[string]domain.KnowledgeDocumentMapping{},
	}
}

func (store *fakeStore) CreateCorpus(_ context.Context, corpus domain.Corpus, event domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.corpora {
		if existing.OrganizationID == corpus.OrganizationID && strings.EqualFold(existing.Slug, corpus.Slug) {
			return domain.ErrConflict
		}
	}
	store.corpora[corpus.ID] = corpus
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) Corpus(_ context.Context, organizationID, corpusID string) (domain.Corpus, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	corpus, ok := store.corpora[corpusID]
	if !ok || corpus.OrganizationID != organizationID {
		return domain.Corpus{}, domain.ErrNotFound
	}
	return corpus, nil
}

func (store *fakeStore) CorpusBySlug(_ context.Context, organizationID, slug string) (domain.Corpus, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, corpus := range store.corpora {
		if corpus.OrganizationID == organizationID && strings.EqualFold(corpus.Slug, slug) {
			return corpus, nil
		}
	}
	return domain.Corpus{}, domain.ErrNotFound
}

// matchesCorpusQuery mirrors postgres.Store.Corpora's own filter exactly,
// including the admin-sees-every-member-corpus branch that today's
// application never actually reaches (it always names its own caller as
// MemberID), so a future caller that does leave it empty gets what the real
// store would give it.
func matchesCorpusQuery(corpus domain.Corpus, query postgres.CorpusQuery) bool {
	switch query.Scope {
	case domain.ScopeTeam:
		return corpus.Scope == domain.ScopeTeam && corpus.TeamID == query.TeamID
	case domain.ScopeOrganization:
		return corpus.Scope == domain.ScopeOrganization
	case domain.ScopeMember:
		if corpus.Scope != domain.ScopeMember {
			return false
		}
		if query.Admin && query.MemberID == "" {
			return true
		}
		return corpus.MemberID == query.MemberID
	default:
		switch corpus.Scope {
		case domain.ScopeOrganization:
			return true
		case domain.ScopeMember:
			return corpus.MemberID == query.MemberID
		default:
			return false
		}
	}
}

func (store *fakeStore) Corpora(_ context.Context, organizationID string, query postgres.CorpusQuery) ([]domain.Corpus, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var result []domain.Corpus
	for _, corpus := range store.corpora {
		if corpus.OrganizationID == organizationID && matchesCorpusQuery(corpus, query) {
			result = append(result, corpus)
		}
	}
	return result, nil
}

func (store *fakeStore) CorpusCounts(_ context.Context, corpusID string) (int64, int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var documents int64
	for _, document := range store.documents {
		if document.CorpusID == corpusID {
			documents++
		}
	}
	return documents, 0, nil
}

func (store *fakeStore) CorporaForWipe(_ context.Context, organizationID string, scope domain.Scope, ownerID string) ([]domain.Corpus, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var result []domain.Corpus
	for _, corpus := range store.corpora {
		if corpus.OrganizationID != organizationID {
			continue
		}
		switch scope {
		case domain.ScopeMember:
			if corpus.Scope != domain.ScopeMember || corpus.MemberID != ownerID {
				continue
			}
		case domain.ScopeTeam:
			if corpus.Scope != domain.ScopeTeam || corpus.TeamID != ownerID {
				continue
			}
		}
		result = append(result, corpus)
	}
	return result, nil
}

func (store *fakeStore) DeleteCorpus(_ context.Context, organizationID, corpusID string, event domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	corpus, ok := store.corpora[corpusID]
	if !ok || corpus.OrganizationID != organizationID {
		return domain.ErrNotFound
	}
	delete(store.corpora, corpusID)
	store.events = append(store.events, event)
	return nil
}

func (store *fakeStore) UpsertDocument(_ context.Context, document domain.Document, _ string, event domain.Event) (domain.Document, postgres.Upsert, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.documents {
		if existing.CorpusID != document.CorpusID {
			continue
		}
		sameSource := document.Source.Ref != "" && existing.Source == document.Source
		sameInline := document.Source.Ref == "" && existing.Source.Kind == domain.SourceInline && existing.ContentSHA256 == document.ContentSHA256
		if !sameSource && !sameInline {
			continue
		}
		if existing.ContentSHA256 == document.ContentSHA256 && existing.Status == domain.StatusReady {
			return existing, postgres.UpsertUnchanged, nil
		}
		document.ID = existing.ID
		store.documents[document.ID] = document
		store.events = append(store.events, event)
		return document, postgres.UpsertChanged, nil
	}
	store.documents[document.ID] = document
	store.events = append(store.events, event)
	return document, postgres.UpsertCreated, nil
}

func (store *fakeStore) Document(_ context.Context, organizationID, documentID string) (domain.Document, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	document, ok := store.documents[documentID]
	if !ok || document.OrganizationID != organizationID {
		return domain.Document{}, domain.ErrNotFound
	}
	return document, nil
}

// Documents records what it was asked and answers with whatever a test
// configured. Real keyset pagination — order, exact-first-page-unchanged,
// next_cursor presence — is proven against PostgreSQL in
// store_integration_test.go; what this layer must prove instead is that the
// service applies the access gate first and forwards status, cursor and a
// clamped limit unchanged.
func (store *fakeStore) Documents(_ context.Context, organizationID, corpusID string, status domain.IngestStatus, cursor string, limit int) ([]domain.Document, string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.lastDocumentsCall = documentsCall{organizationID, corpusID, status, cursor, limit}
	if store.documentsErr != nil {
		return nil, "", store.documentsErr
	}
	return store.documentsPage, store.documentsNext, nil
}

func (store *fakeStore) DocumentsBySource(_ context.Context, organizationID string, kind domain.SourceKind, ref string) ([]domain.Document, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var result []domain.Document
	for _, document := range store.documents {
		if document.OrganizationID == organizationID && document.Source.Kind == kind && document.Source.Ref == ref {
			result = append(result, document)
		}
	}
	return result, nil
}

func (store *fakeStore) ChunkIDs(_ context.Context, _, documentID string) ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.chunkIDs[documentID], nil
}

func (store *fakeStore) DeleteDocument(_ context.Context, organizationID, documentID string, event domain.Event) ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	document, ok := store.documents[documentID]
	if !ok || document.OrganizationID != organizationID {
		return nil, domain.ErrNotFound
	}
	ids := store.chunkIDs[documentID]
	delete(store.documents, documentID)
	delete(store.chunkIDs, documentID)
	store.events = append(store.events, event)
	return ids, nil
}

func (store *fakeStore) UpsertOrganization(_ context.Context, organizationID, name string, _ time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.orgs[organizationID] = name
	return nil
}

func (store *fakeStore) RecordEvents(_ context.Context, events []domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.events = append(store.events, events...)
	return nil
}

func (store *fakeStore) KnowledgeDocumentMapping(_ context.Context, organizationID, knowledgeDocumentID string) (domain.KnowledgeDocumentMapping, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	mapping, ok := store.knowledgeMappings[knowledgeDocumentID]
	if !ok || mapping.OrganizationID != organizationID {
		return domain.KnowledgeDocumentMapping{}, domain.ErrNotFound
	}
	return mapping, nil
}

func (store *fakeStore) UpsertKnowledgeDocumentMapping(_ context.Context, mapping domain.KnowledgeDocumentMapping) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.knowledgeMappings[mapping.KnowledgeDocumentID] = mapping
	return nil
}

func (store *fakeStore) DeleteKnowledgeDocumentMapping(_ context.Context, organizationID, knowledgeDocumentID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	mapping, ok := store.knowledgeMappings[knowledgeDocumentID]
	if !ok || mapping.OrganizationID != organizationID {
		return domain.ErrNotFound
	}
	delete(store.knowledgeMappings, knowledgeDocumentID)
	return nil
}

// seedCorpus writes a corpus directly into the store, bypassing CreateCorpus
// and the access rule that guards it, so a test can set up a corpus at
// whatever scope and ownership it needs to check who may then read, search,
// write to, or delete it.
func seedCorpus(store *fakeStore, corpus domain.Corpus) domain.Corpus {
	if corpus.OrganizationID == "" {
		corpus.OrganizationID = testOrganization
	}
	if corpus.ID == "" {
		corpus.ID = domain.CorpusIDPrefix + corpus.Slug
	}
	if corpus.Slug == "" {
		corpus.Slug = corpus.ID
	}
	if corpus.Spec.Provider == "" {
		corpus.Spec = domain.EmbeddingSpec{Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v2:0", Dimensions: 1024, Metric: domain.MetricCosine}
	}
	if corpus.Backend == "" {
		corpus.Backend = domain.BackendPgvector
	}
	store.corpora[corpus.ID] = corpus
	return corpus
}

// seedDocument writes a document directly into the store, the same way
// seedCorpus does for corpora.
func seedDocument(store *fakeStore, document domain.Document) domain.Document {
	if document.ID == "" {
		// A real, random id: two documents seeded under the same corpus (an
		// ordinary thing for a test to do) must never collide the way a
		// corpus-derived id would.
		id, err := domain.NewID(domain.DocumentIDPrefix)
		if err != nil {
			panic(err)
		}
		document.ID = id
	}
	if document.OrganizationID == "" {
		document.OrganizationID = testOrganization
	}
	store.documents[document.ID] = document
	return document
}

type fakeFiles struct {
	content domain.SourceContent
	err     error
	seen    struct {
		organizationID string
		fileID         string
		credentials    authclient.Credentials
	}
}

func (fake *fakeFiles) Fetch(_ context.Context, organizationID, fileID string, credentials authclient.Credentials) (domain.SourceContent, error) {
	fake.seen.organizationID, fake.seen.fileID, fake.seen.credentials = organizationID, fileID, credentials
	return fake.content, fake.err
}

// fakeVectorStore stands in for one backend's vectorstore.Store, recording
// what was dropped or deleted so a test can check the vectors-before-rows
// order without a real pgvector or S3 Vectors behind it.
type fakeVectorStore struct {
	mu      sync.Mutex
	backend domain.Backend
	dropped []string
	deleted map[string][]string
	err     error
}

func (fake *fakeVectorStore) Upsert(context.Context, domain.Corpus, []domain.Vector) error {
	return fake.err
}

func (fake *fakeVectorStore) Search(context.Context, domain.Corpus, vectorstore.Search) ([]vectorstore.Hit, error) {
	return nil, fake.err
}

func (fake *fakeVectorStore) Delete(_ context.Context, corpus domain.Corpus, chunkIDs []string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.err != nil {
		return fake.err
	}
	if fake.deleted == nil {
		fake.deleted = map[string][]string{}
	}
	fake.deleted[corpus.ID] = append(fake.deleted[corpus.ID], chunkIDs...)
	return nil
}

func (fake *fakeVectorStore) DropCorpus(_ context.Context, corpus domain.Corpus) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.err != nil {
		return fake.err
	}
	fake.dropped = append(fake.dropped, corpus.ID)
	return nil
}

func (fake *fakeVectorStore) Backend() domain.Backend { return fake.backend }

// fakeVectors is the router: one store for whatever backend a corpus names,
// which is all these tests need since none of them mix backends.
type fakeVectors struct {
	store *fakeVectorStore
	err   error
}

func newFakeVectors() *fakeVectors {
	return &fakeVectors{store: &fakeVectorStore{backend: domain.BackendPgvector}}
}

func (fake *fakeVectors) For(domain.Corpus) (vectorstore.Store, error) {
	if fake.err != nil {
		return nil, fake.err
	}
	return fake.store, nil
}

func (fake *fakeVectors) Backends() []domain.Backend { return []domain.Backend{domain.BackendPgvector} }

type fakeRetriever struct {
	results retrieval.Results
	err     error
	seen    struct {
		corpus domain.Corpus
		query  domain.Query
	}
}

func (fake *fakeRetriever) Search(_ context.Context, corpus domain.Corpus, query domain.Query) (retrieval.Results, error) {
	fake.seen.corpus, fake.seen.query = corpus, query
	return fake.results, fake.err
}

type fakeGenerator struct {
	model    string
	response generation.Response
	err      error
}

func (fake *fakeGenerator) Generate(context.Context, generation.Request) (generation.Response, error) {
	return fake.response, fake.err
}

func (fake *fakeGenerator) Model() string { return fake.model }

// harness bundles a Service with the concrete fakes behind it, so a test can
// both call the service and configure or inspect what it did to its
// dependencies.
type harness struct {
	service   *Service
	store     *fakeStore
	teams     *fakeTeams
	files     *fakeFiles
	vectors   *fakeVectors
	retriever *fakeRetriever
	generator *fakeGenerator
}

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

func newHarness() *harness {
	store := newFakeStore()
	teams := newFakeTeams()
	files := &fakeFiles{}
	vectors := newFakeVectors()
	retriever := &fakeRetriever{}
	generator := &fakeGenerator{model: "test-model"}
	service := New(store, fakeDirectory{}, teams, files, vectors, retriever, generator, Defaults{}, nil)
	service.now = func() time.Time { return testClock }
	return &harness{service: service, store: store, teams: teams, files: files, vectors: vectors, retriever: retriever, generator: generator}
}
