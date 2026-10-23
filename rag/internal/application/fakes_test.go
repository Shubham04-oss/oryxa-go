package application

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/generation"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/knowledgeclient"
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

// agentCreds is a token auth minted for an agent acting for userID. Auth, and
// teams after it, answer it as that member, marked as an agent.
func agentCreds(userID string) authclient.Credentials {
	return authclient.Credentials{Authorization: "Bearer " + agentTokenPrefix + userID}
}

const agentTokenPrefix = "agent:"

// grantedCreds is a specialist's token whose grant names exactly these
// corpora; none at all is a grant of nothing.
func grantedCreds(userID string, corpora ...string) authclient.Credentials {
	return authclient.Credentials{Authorization: "Bearer " + agentTokenPrefix + userID + ";corpora=" + strings.Join(corpora, ",")}
}

// roleCreds is an agent's token naming its role (primary, specialist or
// organization). A nil grant is a token without the field, which is all.
func roleCreds(userID, role string, grant []string) authclient.Credentials {
	token := "Bearer " + agentTokenPrefix + userID + ";role=" + role
	if grant != nil {
		token += ";corpora=" + strings.Join(grant, ",")
	}
	return authclient.Credentials{Authorization: token}
}

// fakeDirectory answers for testOrganization, or for organization when it is
// set, so a test against a shared database can use an organization of its own.
type fakeDirectory struct{ organization string }

func (directory fakeDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	organization := cmp.Or(directory.organization, testOrganization)
	token, agent := strings.CutPrefix(strings.TrimPrefix(credentials.Authorization, "Bearer "), agentTokenPrefix)
	token, granted, limited := strings.Cut(token, ";corpora=")
	token, role, named := strings.Cut(token, ";role=")
	people := map[string]domain.OrgMembership{
		"user-owner":    {OrganizationID: organization, OrganizationName: "Acme", UserID: "user-owner", Email: "owner@example.com", Role: domain.OrgRoleOwner},
		"user-admin":    {OrganizationID: organization, OrganizationName: "Acme", UserID: "user-admin", Email: "admin@example.com", Role: domain.OrgRoleAdmin},
		"user-creator":  {OrganizationID: organization, OrganizationName: "Acme", UserID: "user-creator", Email: "creator@example.com", Role: domain.OrgRoleMember},
		"user-teammate": {OrganizationID: organization, OrganizationName: "Acme", UserID: "user-teammate", Email: "teammate@example.com", Role: domain.OrgRoleMember},
		"user-teamlead": {OrganizationID: organization, OrganizationName: "Acme", UserID: "user-teamlead", Email: "teamlead@example.com", Role: domain.OrgRoleMember},
		"user-outsider": {OrganizationID: organization, OrganizationName: "Acme", UserID: "user-outsider", Email: "outsider@example.com", Role: domain.OrgRoleMember},
		"user-badrole":  {OrganizationID: organization, OrganizationName: "Acme", UserID: "user-badrole", Email: "badrole@example.com", Role: domain.OrgRole("pending")},
	}
	membership, ok := people[token]
	if !ok || organizationID != organization {
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	membership.Agent = agent
	if agent {
		membership.AgentID, membership.AgentRole = "agt_"+token, "specialist"
		if named {
			membership.AgentRole = role
		}
		if limited {
			membership.AgentCorpora = []string{}
			if granted != "" {
				membership.AgentCorpora = strings.Split(granted, ",")
			}
		}
	}
	return membership, nil
}

// fakeTeams knows two teams. On team_eng, user-creator and user-teammate are
// plain members; user-teamlead is its team admin, so can manage it. An org
// owner or admin sees and can manage every team whether or not they are a
// member of it — teams.CanSeeTeam and teams.CanManageTeam grant both
// automatically to org.IsOrgAdmin() — so user-owner and user-admin are here
// too, unlike user-outsider, an organization member in good standing who
// simply never joined team_eng and so cannot see it at all. user-admin also
// sits on team_ops as a plain member, where teams still says they can manage
// it, only because they are an org admin.
type fakeTeams struct {
	access map[string]domain.TeamAccess
	err    error
	// organization is the one teams answers for; testOrganization if unset.
	organization string
	// calls counts the questions asked about each team.
	mu    sync.Mutex
	calls map[string]int
}

func newFakeTeams() *fakeTeams {
	return &fakeTeams{access: map[string]domain.TeamAccess{
		"user-creator/team_eng":  {TeamID: "team_eng", CanManage: false, Member: true},
		"user-teammate/team_eng": {TeamID: "team_eng", CanManage: false, Member: true},
		"user-teamlead/team_eng": {TeamID: "team_eng", CanManage: true, Member: true, TeamAdmin: true},
		"user-owner/team_eng":    {TeamID: "team_eng", CanManage: true, Member: false},
		"user-admin/team_eng":    {TeamID: "team_eng", CanManage: true, Member: false},
		"user-admin/team_ops":    {TeamID: "team_ops", CanManage: true, Member: true},
	}}
}

func (fake *fakeTeams) Team(_ context.Context, organizationID, teamID string, credentials authclient.Credentials) (domain.TeamAccess, error) {
	fake.mu.Lock()
	if fake.calls == nil {
		fake.calls = map[string]int{}
	}
	fake.calls[teamID]++
	fake.mu.Unlock()
	if fake.err != nil {
		return domain.TeamAccess{}, fake.err
	}
	if organizationID != cmp.Or(fake.organization, testOrganization) {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	// Teams answers an agent's token as its member, whatever else it says.
	token := strings.TrimPrefix(strings.TrimPrefix(credentials.Authorization, "Bearer "), agentTokenPrefix)
	token, _, _ = strings.Cut(token, ";")
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
// store_integration_test.go and all most tests here need to prove is that it
// asks for the right page and passes back what the store answered. With
// pageDocuments set it pages the documents held instead, for the listing
// equivalence tests.
type fakeStore struct {
	mu                sync.Mutex
	corpora           map[string]domain.Corpus
	documents         map[string]domain.Document
	chunkIDs          map[string][]string
	orgs              map[string]string
	events            []domain.Event
	knowledgeMappings map[string]domain.KnowledgeDocumentMapping
	knowledgeRemovals map[string]time.Time
	knowledgeLocks    map[string]*sync.Mutex

	lastDocumentsCall documentsCall
	documentsPage     []domain.Document
	documentsNext     string
	documentsErr      error
	// pageDocuments makes Documents page through the documents held, as
	// the real store does, instead of answering with documentsPage.
	pageDocuments bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		corpora:           map[string]domain.Corpus{},
		documents:         map[string]domain.Document{},
		chunkIDs:          map[string][]string{},
		orgs:              map[string]string{},
		knowledgeMappings: map[string]domain.KnowledgeDocumentMapping{},
		knowledgeRemovals: map[string]time.Time{},
		knowledgeLocks:    map[string]*sync.Mutex{},
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

// matchesCorpusQuery mirrors postgres.Store.Corpora's own filter exactly.
func matchesCorpusQuery(corpus domain.Corpus, query postgres.CorpusQuery) bool {
	switch query.Scope {
	case domain.ScopeTeam:
		return corpus.Scope == domain.ScopeTeam && corpus.TeamID == query.TeamID
	case domain.ScopeOrganization:
		return corpus.Scope == domain.ScopeOrganization
	case domain.ScopeMember:
		return corpus.Scope == domain.ScopeMember && corpus.MemberID == query.MemberID
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

// Corpora lists each corpus with its document count, as the real listing
// does; this store keeps no chunks, so there are none to count.
func (store *fakeStore) Corpora(_ context.Context, organizationID string, query postgres.CorpusQuery) ([]domain.Corpus, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var result []domain.Corpus
	for _, corpus := range store.corpora {
		if corpus.OrganizationID == organizationID && matchesCorpusQuery(corpus, query) {
			corpus.DocumentCount = store.countDocuments(corpus.ID, query.IncludeAdminOnly)
			result = append(result, corpus)
		}
	}
	return result, nil
}

func (store *fakeStore) CorpusCounts(_ context.Context, corpusID string, includeAdminOnly bool) (int64, int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.countDocuments(corpusID, includeAdminOnly), 0, nil
}

// countDocuments counts what a corpus holds, leaving out admin-only documents
// unless asked for them. The caller holds store.mu.
func (store *fakeStore) countDocuments(corpusID string, includeAdminOnly bool) int64 {
	var documents int64
	for _, document := range store.documents {
		if document.CorpusID == corpusID && (includeAdminOnly || document.Access != domain.AccessAdmins) {
			documents++
		}
	}
	return documents
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

// UpsertDocument settles a missing access label as postgres.Store does: the
// document already held keeps its own, and a new one gets members.
func (store *fakeStore) UpsertDocument(_ context.Context, document domain.Document, _ string, event domain.Event) (domain.Document, postgres.Upsert, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	requestedAccess := document.Access
	for _, existing := range store.documents {
		if existing.CorpusID != document.CorpusID {
			continue
		}
		sameSource := document.Source.Ref != "" && existing.Source == document.Source
		sameInline := document.Source.Ref == "" && existing.Source.Kind == domain.SourceInline && existing.ContentSHA256 == document.ContentSHA256
		if !sameSource && !sameInline {
			continue
		}
		document.Access = cmp.Or(requestedAccess, existing.Access, domain.AccessMembers)
		if existing.ContentSHA256 == document.ContentSHA256 && existing.Access == document.Access &&
			existing.Title == document.Title && existing.ContentType == document.ContentType &&
			maps.Equal(existing.Metadata, document.Metadata) && existing.Status == domain.StatusReady {
			return existing, postgres.UpsertUnchanged, nil
		}
		document.ID = existing.ID
		store.documents[document.ID] = document
		store.events = append(store.events, event)
		return document, postgres.UpsertChanged, nil
	}
	document.Access = cmp.Or(requestedAccess, domain.AccessMembers)
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
func (store *fakeStore) Documents(_ context.Context, organizationID, corpusID string, status domain.IngestStatus, cursor string, limit int, access []domain.DocumentAccess) ([]domain.Document, string, error) {
	includeAdminOnly := slices.Contains(access, domain.AccessAdmins)
	store.mu.Lock()
	defer store.mu.Unlock()
	store.lastDocumentsCall = documentsCall{organizationID, corpusID, status, cursor, limit}
	if store.documentsErr != nil {
		return nil, "", store.documentsErr
	}
	if store.pageDocuments {
		return store.pageOfDocuments(organizationID, corpusID, status, cursor, limit, access)
	}
	if includeAdminOnly {
		return store.documentsPage, store.documentsNext, nil
	}
	visible := make([]domain.Document, 0, len(store.documentsPage))
	for _, document := range store.documentsPage {
		if document.Access != domain.AccessAdmins {
			visible = append(visible, document)
		}
	}
	return visible, store.documentsNext, nil
}

// pageOfDocuments is postgres.Store.Documents over the documents held:
// newest first, then by id, with a cursor naming the last row read.
func (store *fakeStore) pageOfDocuments(organizationID, corpusID string, status domain.IngestStatus, cursor string, limit int, access []domain.DocumentAccess) ([]domain.Document, string, error) {
	var after domain.Document
	if cursor != "" {
		at, id, ok := strings.Cut(cursor, "|")
		createdAt, err := time.Parse(time.RFC3339Nano, at)
		if !ok || err != nil {
			return nil, "", domain.ErrInvalid
		}
		after = domain.Document{ID: id, CreatedAt: createdAt}
	}
	var rows []domain.Document
	for _, document := range store.documents {
		if document.OrganizationID != organizationID || document.CorpusID != corpusID ||
			!slices.Contains(access, document.Access) || (status != "" && document.Status != status) {
			continue
		}
		if cursor != "" && !(document.CreatedAt.Before(after.CreatedAt) ||
			(document.CreatedAt.Equal(after.CreatedAt) && document.ID > after.ID)) {
			continue
		}
		rows = append(rows, document)
	}
	slices.SortFunc(rows, func(a, b domain.Document) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	if len(rows) <= limit {
		return rows, "", nil
	}
	rows = rows[:limit]
	last := rows[len(rows)-1]
	return rows, last.CreatedAt.Format(time.RFC3339Nano) + "|" + last.ID, nil
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

func (store *fakeStore) DocumentIDsForCorpus(_ context.Context, corpusID string) ([]string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var ids []string
	for id, document := range store.documents {
		if document.CorpusID == corpusID {
			ids = append(ids, id)
		}
	}
	return ids, nil
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

func (store *fakeStore) KnowledgeDocumentRemovedAt(_ context.Context, organizationID, knowledgeDocumentID string) (time.Time, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	removedAt, ok := store.knowledgeRemovals[organizationID+"/"+knowledgeDocumentID]
	if !ok {
		return time.Time{}, domain.ErrNotFound
	}
	return removedAt, nil
}

func (store *fakeStore) RecordKnowledgeDocumentRemoval(_ context.Context, organizationID, knowledgeDocumentID string, removedAt time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := organizationID + "/" + knowledgeDocumentID
	if removedAt.After(store.knowledgeRemovals[key]) {
		store.knowledgeRemovals[key] = removedAt
	}
	return nil
}

func (store *fakeStore) WithKnowledgeDocumentLock(_ context.Context, organizationID, knowledgeDocumentID string, run func() error) error {
	key := organizationID + "/" + knowledgeDocumentID
	store.mu.Lock()
	lock := store.knowledgeLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		store.knowledgeLocks[key] = lock
	}
	store.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return run()
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

// fakeKnowledgeBase stands in for the Bedrock knowledge base a bedrock-kb
// corpus's documents are removed from, recording what was deleted so a test
// can check it without a real Bedrock behind it.
type fakeKnowledgeBase struct {
	mu              sync.Mutex
	deleted         []string
	deletedStaged   []string
	err             error
	deleteStagedErr error
}

func (fake *fakeKnowledgeBase) Delete(_ context.Context, documentIDs []string) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.err != nil {
		return fake.err
	}
	fake.deleted = append(fake.deleted, documentIDs...)
	return nil
}

func (fake *fakeKnowledgeBase) DeleteStaged(_ context.Context, _ domain.Corpus, document domain.Document) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.deleteStagedErr != nil {
		return fake.deleteStagedErr
	}
	fake.deletedStaged = append(fake.deletedStaged, document.ID)
	return nil
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
// fakeDocuments answers a body knowledge could not carry on the bus.
type fakeDocuments struct {
	body string
	// hash, when set, is what knowledge says the body is now — which differs
	// from the event's when the document changed while it was being read.
	hash  string
	err   error
	calls []string
}

func (documents *fakeDocuments) Body(_ context.Context, organizationID, documentID string) (knowledgeclient.Document, error) {
	documents.calls = append(documents.calls, organizationID+"/"+documentID)
	if documents.err != nil {
		return knowledgeclient.Document{}, documents.err
	}
	return knowledgeclient.Document{ID: documentID, Body: documents.body, ContentHash: documents.hash}, nil
}

type harness struct {
	service       *Service
	store         *fakeStore
	teams         *fakeTeams
	files         *fakeFiles
	documents     *fakeDocuments
	vectors       *fakeVectors
	retriever     *fakeRetriever
	generator     *fakeGenerator
	knowledgeBase *fakeKnowledgeBase
}

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

func newHarness() *harness {
	store := newFakeStore()
	teams := newFakeTeams()
	files := &fakeFiles{}
	documents := &fakeDocuments{}
	vectors := newFakeVectors()
	retriever := &fakeRetriever{}
	generator := &fakeGenerator{model: "test-model"}
	knowledgeBase := &fakeKnowledgeBase{}
	service := New(store, fakeDirectory{}, teams, files, documents, vectors, retriever, generator, knowledgeBase, Defaults{}, nil)
	service.now = func() time.Time { return testClock }
	return &harness{service: service, store: store, teams: teams, files: files, documents: documents,
		vectors: vectors, retriever: retriever, generator: generator, knowledgeBase: knowledgeBase}
}
