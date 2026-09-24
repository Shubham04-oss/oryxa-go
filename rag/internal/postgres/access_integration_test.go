package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

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
	if marked, err := store.MarkDocumentReady(ctx, restricted, 0, time.Now().UTC(), testEvent(t, "rag.document.indexed.v1")); err != nil || !marked {
		t.Fatal(marked, err)
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
