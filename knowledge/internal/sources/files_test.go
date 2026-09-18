package sources

import (
	"context"
	"errors"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/filesclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// fakeFiles answers a fixed listing, recording which files had bytes read.
type fakeFiles struct {
	objects  []filesclient.Object
	bodies   map[string]string
	missing  map[string]bool
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
	return []byte(files.bodies[fileID]), "text/markdown", nil
}

func storedFile(id, filename, contentType string) filesclient.Object {
	return filesclient.Object{
		ID: id, OrganizationID: "org_1", Filename: filename, ContentType: contentType,
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

func TestFilesTargetRefusesAFieldNamedButLeftEmpty(t *testing.T) {
	source := NewFiles(&fakeFiles{})
	if err := source.Target(map[string]string{"team": "  "}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Target(empty team) error = %v, want ErrInvalid", err)
	}
	if err := source.Target(map[string]string{}); err != nil {
		t.Fatalf("Target(nothing named) error = %v, want nil", err)
	}
}

func TestFilesReportsWhatStoppedTheListing(t *testing.T) {
	files := &fakeFiles{listErr: domain.ErrUnauthorized}
	if _, err := NewFiles(files).Fetch(context.Background(), filesRequest()); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("Fetch() error = %v, want ErrUnauthorized", err)
	}
}
