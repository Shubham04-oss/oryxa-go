package sources

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/filesclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// fakeFiles answers a fixed listing, recording which files had bytes read.
type fakeFiles struct {
	objects []filesclient.Object
	bodies  map[string]string
	missing map[string]bool
	// served overrides the content type Content answers for a file id; a file
	// not named here answers text/markdown, matching every test written
	// before this field existed.
	served   map[string]string
	listErr  error
	fetched  []string
	listedAs filesclient.Query
}

func (files *fakeFiles) Objects(_ context.Context, _, _ string, query filesclient.Query) ([]filesclient.Object, error) {
	files.listedAs = query
	if files.listErr != nil {
		return nil, files.listErr
	}
	return files.objects, nil
}

func (files *fakeFiles) Content(_ context.Context, _, fileID, _ string) ([]byte, string, error) {
	files.fetched = append(files.fetched, fileID)
	if files.missing[fileID] {
		return nil, "", domain.ErrNotFound
	}
	contentType := "text/markdown"
	if served, ok := files.served[fileID]; ok {
		contentType = served
	}
	return []byte(files.bodies[fileID]), contentType, nil
}

func storedFile(id, filename, contentType string) filesclient.Object {
	return filesclient.Object{
		ID: id, OrganizationID: "org_1", Scope: string(domain.ScopeOrganization),
		Filename: filename, ContentType: contentType,
		Purpose: "upload", SHA256: "abc", CreatedBy: "user-1",
		CreatedAt: time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC),
	}
}

func filesRequest() Request {
	return Request{
		Source:     domain.Source{OrganizationID: "org_1", Provider: "files", Target: map[string]string{}},
		Credential: domain.Credential{AccessToken: "member-token"},
	}
}

func TestFilesMemberSourceFetchesOnlyItsOwnersObjects(t *testing.T) {
	own := storedFile("file_own", "own.md", "text/markdown")
	own.Scope, own.MemberID = string(domain.ScopeMember), "user-1"
	other := storedFile("file_other", "other.md", "text/markdown")
	other.Scope, other.MemberID = string(domain.ScopeMember), "user-2"
	shared := storedFile("file_shared", "shared.md", "text/markdown")
	files := &fakeFiles{
		objects: []filesclient.Object{own, other, shared},
		bodies: map[string]string{
			"file_own": "mine", "file_other": "private to someone else", "file_shared": "shared",
		},
	}
	request := filesRequest()
	request.Source.Scope = domain.ScopeMember
	request.Source.MemberID = "user-1"
	request.Source.Target = map[string]string{"member": "self"}
	page, err := NewFiles(files).Fetch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if files.listedAs.Scope != string(domain.ScopeMember) {
		t.Fatalf("listed scope = %q", files.listedAs.Scope)
	}
	if len(page.Items) != 1 || page.Items[0].ExternalID != own.ID {
		t.Fatalf("member source published %+v", page.Items)
	}
	if len(files.fetched) != 1 || files.fetched[0] != own.ID {
		t.Fatalf("member source fetched %v", files.fetched)
	}
}

func TestFilesMemberTargetCannotNameAnotherMember(t *testing.T) {
	source := NewFiles(&fakeFiles{})
	if err := source.Target(map[string]string{"member": "user-2"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("arbitrary member target = %v", err)
	}
	if err := source.Target(map[string]string{"member": "self", "team": "team-1"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("member and team target = %v", err)
	}
}

func TestFilesMemberSourceWithoutOwnerFailsClosed(t *testing.T) {
	files := &fakeFiles{}
	request := filesRequest()
	request.Source.Scope = domain.ScopeMember
	request.Source.Target = map[string]string{"member": "self"}
	if _, err := NewFiles(files).Fetch(context.Background(), request); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("member source without an owner = %v", err)
	}
	if files.listedAs.Scope != "" {
		t.Fatal("queried Files before validating the member owner")
	}
}

func TestFilesTeamSourceWithMismatchedTargetFailsClosed(t *testing.T) {
	files := &fakeFiles{}
	request := filesRequest()
	request.Source.Scope = domain.ScopeTeam
	request.Source.TeamID = "team_1"
	request.Source.Target = map[string]string{"team": "team_2"}
	if _, err := NewFiles(files).Fetch(context.Background(), request); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("team source with mismatched target = %v", err)
	}
	if files.listedAs.Scope != "" {
		t.Fatal("queried Files before validating the team placement")
	}
}

func TestFilesReadsWhatItCanAndSkipsWhatItCannot(t *testing.T) {
	files := &fakeFiles{
		objects: []filesclient.Object{
			storedFile("file_1", "notes.md", "text/markdown"),
			storedFile("file_2", "diagram.png", "image/png"),
			storedFile("file_3", "data.csv", "text/csv"),
		},
		bodies: map[string]string{"file_1": "# notes", "file_3": "a,b"},
	}
	page, err := NewFiles(files).Fetch(context.Background(), filesRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("Fetch() = %d items, want 2: the image holds no text", len(page.Items))
	}
	// The image's bytes were never asked for, since its type already said so.
	for _, fetched := range files.fetched {
		if fetched == "file_2" {
			t.Fatal("read the bytes of a file whose type already said it holds no text")
		}
	}
	if page.Items[0].Kind != knowledgev1.KindFile || page.Items[0].MIME != transform.MIMEMarkdown {
		t.Fatalf("Fetch() item = %+v", page.Items[0])
	}
}

func TestFilesStepsOverAFileThatQuietlyExpired(t *testing.T) {
	// Retention lapsing publishes no event; files just stops answering.
	files := &fakeFiles{
		objects: []filesclient.Object{
			storedFile("file_1", "gone.md", "text/markdown"),
			storedFile("file_2", "here.md", "text/markdown"),
		},
		bodies:  map[string]string{"file_2": "still here"},
		missing: map[string]bool{"file_1": true},
	}
	page, err := NewFiles(files).Fetch(context.Background(), filesRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ExternalID != "file_2" {
		t.Fatalf("Fetch() = %+v, want only the file that is still there", page.Items)
	}
}

func TestFilesPagesItsOwnWalkSinceFilesDoesNot(t *testing.T) {
	objects := []filesclient.Object{}
	bodies := map[string]string{}
	for _, id := range []string{"file_1", "file_2", "file_3", "file_4"} {
		objects = append(objects, storedFile(id, id+".md", "text/markdown"))
		bodies[id] = "body of " + id
	}
	files := &fakeFiles{objects: objects, bodies: bodies}
	source := NewFiles(files)

	request := filesRequest()
	request.Limit = 2
	first, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(first.Items) != 2 || first.Cursor != "file_2" {
		t.Fatalf("Fetch() = %d items, cursor %q, want 2 and file_2", len(first.Items), first.Cursor)
	}

	request.Cursor = domain.Cursor{Value: first.Cursor}
	second, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(second.Items) != 2 || second.Items[0].ExternalID != "file_3" {
		t.Fatalf("Fetch() = %+v, want the walk to carry on at file_3", second.Items)
	}
	if second.Cursor != "" {
		t.Fatalf("Fetch() cursor = %q, want empty: nothing is left", second.Cursor)
	}
}

func TestFilesKeepsItsPlaceWhenAFileIsAddedMidWalk(t *testing.T) {
	// An offset would shift; naming the last id read does not.
	files := &fakeFiles{
		objects: []filesclient.Object{
			storedFile("file_2", "b.md", "text/markdown"),
			storedFile("file_4", "d.md", "text/markdown"),
		},
		bodies: map[string]string{"file_2": "b", "file_4": "d", "file_3": "c"},
	}
	source := NewFiles(files)
	request := filesRequest()
	request.Limit = 1

	first, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if first.Cursor != "file_2" {
		t.Fatalf("Fetch() cursor = %q, want file_2", first.Cursor)
	}

	// Something lands between the two while the walk is in flight.
	files.objects = append(files.objects, storedFile("file_3", "c.md", "text/markdown"))
	request.Cursor = domain.Cursor{Value: first.Cursor}
	second, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ExternalID != "file_3" {
		t.Fatalf("Fetch() = %+v, want the new file rather than a skipped one", second.Items)
	}
}

func TestFilesNarrowsToATeamWhenOneIsNamed(t *testing.T) {
	files := &fakeFiles{}
	request := filesRequest()
	request.Source.Target = map[string]string{"team": "team_1"}

	if _, err := NewFiles(files).Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if files.listedAs.TeamID != "team_1" || files.listedAs.Scope != string(domain.ScopeTeam) {
		t.Fatalf("listed as %+v, want a team listing: files never includes team files otherwise", files.listedAs)
	}
}

// TestFilesAsksForOrganizationFilesAndNeverTheMembersOwn is a privacy rule,
// not a filter. A source here is organization-scoped, so everything it
// publishes is searchable by the whole organization; files asked with no
// scope at all answers the caller's own private files alongside the shared
// ones, which would put one member's private files in front of everybody.
func TestFilesAsksForOrganizationFilesAndNeverTheMembersOwn(t *testing.T) {
	for name, target := range map[string]map[string]string{
		"no target":          nil,
		"narrowed by a kind": {"purpose": "knowledge.document"},
	} {
		t.Run(name, func(t *testing.T) {
			files := &fakeFiles{}
			request := filesRequest()
			request.Source.Target = target

			if _, err := NewFiles(files).Fetch(context.Background(), request); err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if files.listedAs.Scope != string(domain.ScopeOrganization) {
				t.Fatalf("listed as scope %q, want the organization's own files alone", files.listedAs.Scope)
			}
		})
	}
}

// TestFilesCarriesBillingPurpose is a privacy rule: RAG uses the trusted
// Files purpose to keep a billing attachment admin-only after indexing.
func TestFilesCarriesBillingPurpose(t *testing.T) {
	invoice := storedFile("file_1", "invoice.csv", "text/csv")
	invoice.Purpose = "billing.attachment"
	files := &fakeFiles{
		objects: []filesclient.Object{invoice, storedFile("file_2", "handbook.md", "text/markdown")},
		bodies:  map[string]string{"file_1": "amount,due", "file_2": "# handbook"},
	}
	page, err := NewFiles(files).Fetch(context.Background(), filesRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].Metadata["purpose"] != "billing.attachment" {
		t.Fatalf("Fetch() = %+v, want the invoice labeled for admin-only indexing", page.Items)
	}
	if err := NewFiles(files).Target(map[string]string{"purpose": "billing.attachment"}); err != nil {
		t.Fatalf("Target(billing attachments) error = %v", err)
	}
}

// TestFilesPublishesOnlyWhatItsScopeMayCarry is the second guard behind the
// scope the listing asks for: whatever files answers, an object outside the
// source's own scope, or another team's, is never published under it.
func TestFilesPublishesOnlyWhatItsScopeMayCarry(t *testing.T) {
	private := storedFile("file_1", "mine.md", "text/markdown")
	private.Scope, private.MemberID = string(domain.ScopeMember), "user-1"
	theirs := storedFile("file_2", "theirs.md", "text/markdown")
	theirs.Scope, theirs.TeamID = string(domain.ScopeTeam), "team_2"
	ours := storedFile("file_3", "ours.md", "text/markdown")
	ours.Scope, ours.TeamID = string(domain.ScopeTeam), "team_1"
	files := &fakeFiles{
		objects: []filesclient.Object{private, theirs, ours},
		bodies:  map[string]string{"file_1": "mine", "file_2": "theirs", "file_3": "ours"},
	}
	request := filesRequest()
	request.Source.Target = map[string]string{"team": "team_1"}

	page, err := NewFiles(files).Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ExternalID != "file_3" {
		t.Fatalf("Fetch() = %+v, want only team_1's own file", page.Items)
	}
}

func TestFilesTargetRefusesAFieldNamedButLeftEmpty(t *testing.T) {
	source := NewFiles(&fakeFiles{})
	if err := source.Target(map[string]string{"team": "  "}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Target(empty team) error = %v, want ErrInvalid", err)
	}
	if err := source.Target(map[string]string{}); err != nil {
		t.Fatalf("Target(nothing named) error = %v, want nil", err)
	}
}

// TestFilesPassesAPDFThroughOnlyWithOriginals covers the switch WithOriginals
// is: without it, a PDF is skipped exactly as before, and its bytes are
// never even asked for; with it, the same file comes back as an item with
// its real MIME and untouched bytes, ready for keepOriginal to decide what
// to do with.
func TestFilesPassesAPDFThroughOnlyWithOriginals(t *testing.T) {
	files := &fakeFiles{
		objects: []filesclient.Object{storedFile("file_1", "report.pdf", "application/pdf")},
		bodies:  map[string]string{"file_1": "%PDF-1.4 fake"},
		served:  map[string]string{"file_1": "application/pdf"},
	}
	page, err := NewFiles(files).Fetch(context.Background(), filesRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("Fetch() = %+v, want the PDF skipped without WithOriginals", page.Items)
	}
	if len(files.fetched) != 0 {
		t.Fatal("read a PDF's bytes even though nothing can keep them")
	}

	page, err = NewFiles(files, WithOriginals()).Fetch(context.Background(), filesRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].MIME != "application/pdf" || string(page.Items[0].Content) != "%PDF-1.4 fake" {
		t.Fatalf("Fetch() = %+v, want the PDF passed through with its real MIME and bytes untouched", page.Items)
	}
}

// TestFilesSkipsAnOversizeOriginalEvenWithWithOriginals covers the memory
// ceiling: WithOriginals is not a promise to hold anything of any size.
func TestFilesSkipsAnOversizeOriginalEvenWithWithOriginals(t *testing.T) {
	files := &fakeFiles{
		objects: []filesclient.Object{storedFile("file_1", "huge.pdf", "application/pdf")},
		bodies:  map[string]string{"file_1": strings.Repeat("a", MaxOriginalBytes+1)},
		served:  map[string]string{"file_1": "application/pdf"},
	}
	page, err := NewFiles(files, WithOriginals()).Fetch(context.Background(), filesRequest())
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("Fetch() = %d items, want the oversize file skipped", len(page.Items))
	}
}

func TestFilesReportsWhatStoppedTheListing(t *testing.T) {
	files := &fakeFiles{listErr: domain.ErrUnauthorized}
	if _, err := NewFiles(files).Fetch(context.Background(), filesRequest()); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Fetch() error = %v, want ErrUnauthorized", err)
	}
}

// TestFilesSaysWhenItsMemberCannotReadThePlacement: Files answering not found
// or forbidden to the listing itself is Files refusing the member a source
// reads as the whole placement, which no retry fixes; anything else is not.
func TestFilesSaysWhenItsMemberCannotReadThePlacement(t *testing.T) {
	for name, testCase := range map[string]struct {
		listErr    error
		unreadable bool
	}{
		"not found":           {listErr: domain.ErrNotFound, unreadable: true},
		"forbidden":           {listErr: domain.ErrForbidden, unreadable: true},
		"a token not taken":   {listErr: domain.ErrUnauthorized},
		"files not answering": {listErr: domain.ErrProviderUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			request := filesRequest()
			request.Source.Scope, request.Source.TeamID = domain.ScopeTeam, "team_1"
			request.Source.Target = map[string]string{"team": "team_1"}
			_, err := NewFiles(&fakeFiles{listErr: testCase.listErr}).Fetch(context.Background(), request)
			if !errors.Is(err, testCase.listErr) {
				t.Fatalf("Fetch() error = %v, want it to keep %v", err, testCase.listErr)
			}
			if errors.Is(err, domain.ErrUnreadable) != testCase.unreadable {
				t.Fatalf("Fetch() error = %v, unreadable = %t, want %t", err, errors.Is(err, domain.ErrUnreadable), testCase.unreadable)
			}
		})
	}
	// A single file gone missing is only that file: it is stepped over, and
	// says nothing about the placement.
	files := &fakeFiles{
		objects: []filesclient.Object{storedFile("file_1", "gone.md", "text/markdown")},
		missing: map[string]bool{"file_1": true},
	}
	if _, err := NewFiles(files).Fetch(context.Background(), filesRequest()); err != nil {
		t.Fatalf("Fetch() with one file gone = %v, want it stepped over", err)
	}
}

// TestFilesChecksAPlacementWithTheListingItsPassesWouldMake: registering
// asks Files for exactly what every pass would, as the member it reads as,
// and reads no file's bytes to do it.
func TestFilesChecksAPlacementWithTheListingItsPassesWouldMake(t *testing.T) {
	files := &fakeFiles{objects: []filesclient.Object{storedFile("file_1", "notes.md", "text/markdown")}}
	request := filesRequest()
	request.Source.Scope, request.Source.TeamID = domain.ScopeTeam, "team_1"
	request.Source.Target = map[string]string{"team": "team_1", "purpose": "report"}
	if err := NewFiles(files).Check(context.Background(), request); err != nil {
		t.Fatalf("Check() = %v", err)
	}
	if files.listedAs != (filesclient.Query{Scope: "team", TeamID: "team_1", Purpose: "report"}) || len(files.fetched) != 0 {
		t.Fatalf("Check() listed %+v and read %v, want the team listing and no bytes", files.listedAs, files.fetched)
	}

	files.listErr = domain.ErrNotFound
	if err := NewFiles(files).Check(context.Background(), request); !errors.Is(err, domain.ErrNotFound) || !errors.Is(err, domain.ErrUnreadable) {
		t.Fatalf("Check(refused) = %v, want the member unable to read it", err)
	}

	unowned := filesRequest()
	unowned.Source.Scope, unowned.Source.Target = domain.ScopeMember, map[string]string{"member": "self"}
	files = &fakeFiles{}
	if err := NewFiles(files).Check(context.Background(), unowned); !errors.Is(err, domain.ErrInvalid) || files.listedAs.Scope != "" {
		t.Fatalf("Check(no owner) = %v after listing %+v, want it refused before asking Files", err, files.listedAs)
	}
}

// TestFilesReadsNoBytesItAlreadyHolds: a file's id names the same bytes for
// as long as it exists, so one the source already holds live, with the same
// sha256 and details, is reported unchanged without its bytes being read.
// Anything else, or a pass that reads everything, reads them.
func TestFilesReadsNoBytesItAlreadyHolds(t *testing.T) {
	held, differs, removed, fresh := storedFile("file_1", "held.md", "text/markdown"), storedFile("file_2", "differs.md", "text/markdown"),
		storedFile("file_3", "removed.md", "text/markdown"), storedFile("file_4", "new.md", "text/markdown")
	files := &fakeFiles{
		objects: []filesclient.Object{held, differs, removed, fresh},
		bodies:  map[string]string{"file_1": "one", "file_2": "two", "file_3": "three", "file_4": "four"},
	}
	document := func(object filesclient.Object) domain.Document {
		return domain.Document{
			ExternalID: object.ID, Kind: knowledgev1.KindFile, Title: object.Filename,
			Metadata: map[string]string{
				"filename": object.Filename, "purpose": object.Purpose, "access": object.Access,
				"sha256": object.SHA256, "created_by": object.CreatedBy,
			},
		}
	}
	stored := map[string]domain.Document{
		"file_1": document(held),
		"file_2": document(differs),
		"file_3": document(removed),
	}
	stale := stored["file_2"]
	stale.Metadata = map[string]string{"filename": differs.Filename, "purpose": differs.Purpose, "sha256": differs.SHA256, "created_by": differs.CreatedBy}
	stored["file_2"] = stale // held from before files carried an access label
	gone := stored["file_3"]
	gone.RemovedAt = &gone.ExtractedAt
	stored["file_3"] = gone
	request := filesRequest()
	request.Held = func(_ context.Context, externalID string) (domain.Document, bool, error) {
		document, ok := stored[externalID]
		return document, ok, nil
	}

	page, err := NewFiles(files).Fetch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 || !page.Items[0].Unchanged || page.Items[0].Content != nil {
		t.Fatalf("Fetch() = %+v, want the held file reported unchanged, without its bytes", page.Items)
	}
	for _, item := range page.Items[1:] {
		if item.Unchanged || len(item.Content) == 0 {
			t.Fatalf("Fetch() item %s = %+v, want it read", item.ExternalID, item)
		}
	}
	if !slices.Equal(files.fetched, []string{"file_2", "file_3", "file_4"}) {
		t.Fatalf("read the bytes of %v, want only what the source does not hold unchanged", files.fetched)
	}

	files.fetched = nil
	request.Held = nil
	if _, err := NewFiles(files).Fetch(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(files.fetched) != 4 {
		t.Fatalf("a pass that reads everything read %v", files.fetched)
	}
}
