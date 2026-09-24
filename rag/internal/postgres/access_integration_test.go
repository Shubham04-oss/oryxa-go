package postgres

import (
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// seedAccessDocument stores one document at the given access label, with one
// chunk, so a count has both to include or leave out.
func seedAccessDocument(t *testing.T, store *Store, corpus domain.Corpus, ref, body string, access domain.DocumentAccess) domain.Document {
	t.Helper()
	ctx := t.Context()
	document := newDocument(corpus, domain.Source{Kind: domain.SourceExternal, Ref: ref}, ref, body)
	document.Access = access
	stored, _, err := store.UpsertDocument(ctx, document, body, testEvent(t, "rag.document.received.v1"))
	if err != nil {
		t.Fatalf("UpsertDocument(%s): %v", ref, err)
	}
	if _, err := store.SyncChunks(ctx, stored, []domain.Chunk{{
		Text: body, TokenCount: 1, SHA256: domain.Hash(body), StartByte: 0, EndByte: len(body),
	}}, time.Now().UTC()); err != nil {
		t.Fatalf("SyncChunks(%s): %v", ref, err)
	}
	return stored
}

// TestCorpusListingCountsFollowIncludeAdminOnly: an agent acting for an org
// admin reaches the corpora the admin does (Admin) but reads only what every
// member may, so the listing it gets must not count admin-only documents or
// their chunks. Only IncludeAdminOnly lets them into the totals.
func TestCorpusListingCountsFollowIncludeAdminOnly(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())
	seedAccessDocument(t, store, corpus, "handbook", "handbook", domain.AccessMembers)
	seedAccessDocument(t, store, corpus, "payroll", "payroll", domain.AccessAdmins)

	for _, test := range []struct {
		name              string
		query             CorpusQuery
		documents, chunks int64
	}{
		{"a member", CorpusQuery{Scope: domain.ScopeOrganization}, 1, 1},
		{"an agent acting for an admin", CorpusQuery{Scope: domain.ScopeOrganization, Admin: true}, 1, 1},
		{"an admin's own session", CorpusQuery{Scope: domain.ScopeOrganization, Admin: true, IncludeAdminOnly: true}, 2, 2},
	} {
		listed, err := store.Corpora(ctx, organizationID, test.query)
		if err != nil || len(listed) != 1 {
			t.Fatalf("%s: Corpora = %+v, %v", test.name, listed, err)
		}
		if listed[0].DocumentCount != test.documents || listed[0].ChunkCount != test.chunks {
			t.Fatalf("%s: counts = (%d, %d), want (%d, %d)", test.name,
				listed[0].DocumentCount, listed[0].ChunkCount, test.documents, test.chunks)
		}
	}
}
