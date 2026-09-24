package postgres

import (
	"errors"
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

// TestUpsertDocumentWithoutAccessKeepsTheStoredAccess: a document offered
// again with no access label keeps the one it has, whether its content
// changed or not, so a re-delivery can never open an admin-only document to
// every member. A document never held before gets members, and a label that
// is named is written.
func TestUpsertDocumentWithoutAccessKeepsTheStoredAccess(t *testing.T) {
	store, ctx := openTestStore(t)
	organizationID := uniqueOrganization()
	corpus := testCorpus(t, store, ctx, organizationID, titanSpec())
	offer := func(source domain.Source, body string, access domain.DocumentAccess) (domain.Document, Upsert) {
		t.Helper()
		document := newDocument(corpus, source, "Payroll", body)
		document.Access = access
		stored, outcome, err := store.UpsertDocument(ctx, document, body, testEvent(t, "rag.document.received.v1"))
		if err != nil {
			t.Fatalf("UpsertDocument(%+v, access %q): %v", source, access, err)
		}
		return stored, outcome
	}

	fresh, outcome := offer(domain.Source{Kind: domain.SourceExternal, Ref: "handbook"}, "handbook", "")
	if outcome != UpsertCreated || fresh.Access != domain.AccessMembers {
		t.Fatalf("new document without access = %v %q, want created as members", outcome, fresh.Access)
	}

	payroll := domain.Source{Kind: domain.SourceExternal, Ref: "payroll"}
	restricted, _ := offer(payroll, "salary bands", domain.AccessAdmins)
	if err := store.MarkDocumentReady(ctx, restricted.ID, 0, time.Now().UTC(), testEvent(t, "rag.document.indexed.v1")); err != nil {
		t.Fatal(err)
	}
	same, outcome := offer(payroll, "salary bands", "")
	if outcome != UpsertUnchanged || same.ID != restricted.ID || same.Access != domain.AccessAdmins {
		t.Fatalf("same bytes without access = %v %s %q, want unchanged and still admins", outcome, same.ID, same.Access)
	}
	changed, outcome := offer(payroll, "salary bands, revised", "")
	if outcome != UpsertChanged || changed.ID != restricted.ID || changed.Access != domain.AccessAdmins {
		t.Fatalf("new bytes without access = %v %s %q, want changed and still admins", outcome, changed.ID, changed.Access)
	}
	read, err := store.Document(ctx, organizationID, restricted.ID)
	if err != nil || read.Access != domain.AccessAdmins {
		t.Fatalf("stored access = %q, %v, want admins", read.Access, err)
	}

	inline := domain.Source{Kind: domain.SourceInline}
	note, _ := offer(inline, "board minutes", domain.AccessAdmins)
	if again, _ := offer(inline, "board minutes", ""); again.ID != note.ID || again.Access != domain.AccessAdmins {
		t.Fatalf("inline re-offered without access = %s %q, want %s still admins", again.ID, again.Access, note.ID)
	}

	if opened, _ := offer(payroll, "salary bands, revised", domain.AccessMembers); opened.ID != restricted.ID || opened.Access != domain.AccessMembers {
		t.Fatalf("named members = %s %q, want %s opened to members", opened.ID, opened.Access, restricted.ID)
	}
	bad := newDocument(corpus, domain.Source{Kind: domain.SourceExternal, Ref: "bad"}, "Bad", "bad")
	bad.Access = domain.DocumentAccess("owners")
	if _, _, err := store.UpsertDocument(ctx, bad, "bad", testEvent(t, "rag.document.received.v1")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an unknown access label = %v, want ErrInvalid", err)
	}
}
