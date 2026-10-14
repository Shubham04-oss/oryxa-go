package s3artifact

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"testing"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/genai"
)

func inOrganization(organizationID string) context.Context {
	return WithOrganization(context.Background(), organizationID)
}

func saveIn(t *testing.T, ctx context.Context, store artifact.Service, sessionID, name, text string) int64 {
	t.Helper()
	saved, err := store.Save(ctx, &artifact.SaveRequest{
		AppName: "krude", UserID: "usr_1", SessionID: sessionID, FileName: name, Part: genai.NewPartFromText(text),
	})
	if err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	return saved.Version
}

func listIn(t *testing.T, ctx context.Context, store artifact.Service, sessionID string) []string {
	t.Helper()
	listed, err := store.List(ctx, &artifact.ListRequest{AppName: "krude", UserID: "usr_1", SessionID: sessionID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return listed.FileNames
}

// L6: one member in two organizations. A file they keep for themselves in
// org A is not there when they work in org B, by name, by listing or by
// version.
func TestMemberFilesStayInTheirOrganization(t *testing.T) {
	fake := newFakeS3()
	store := ByOrganization(newTestService(t, fake))
	orgA, orgB := inOrganization("org_a"), inOrganization("org_b")

	saveIn(t, orgA, store, "sess_a", "user:notes.txt", "org A's notes")

	if names := listIn(t, orgB, store, "sess_b"); len(names) != 0 {
		t.Errorf("org B lists %v", names)
	}
	_, err := store.Load(orgB, &artifact.LoadRequest{AppName: "krude", UserID: "usr_1", SessionID: "sess_b", FileName: "user:notes.txt"})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("org B loads org A's file: %v", err)
	}
	if _, err := store.Versions(orgB, &artifact.VersionsRequest{AppName: "krude", UserID: "usr_1", SessionID: "sess_b", FileName: "user:notes.txt"}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("org B sees versions of org A's file: %v", err)
	}
	// Org B's own file of that name starts at version 1 beside org A's.
	if version := saveIn(t, orgB, store, "sess_b", "user:notes.txt", "org B's notes"); version != 1 {
		t.Errorf("org B's first version = %d, want 1", version)
	}
	if got := loadTextIn(t, orgA, store, "sess_a", "user:notes.txt"); got != "org A's notes" {
		t.Errorf("org A reads %q", got)
	}
	if names := listIn(t, orgA, store, "sess_other"); !slices.Equal(names, []string{"user:notes.txt"}) {
		t.Errorf("org A's other session lists %v", names)
	}

	want := []string{
		"orgs/org_a/krude/usr_1/user/user:notes.txt/1",
		"orgs/org_b/krude/usr_1/user/user:notes.txt/1",
	}
	if keys := keysOf(fake); !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

// Before L6 was fixed, keys had no organization. A session belongs to one
// organization, so its old files stay readable, and new versions follow
// them. A member's own old files cannot be placed in an organization, so no
// session serves them until they are moved (see the package doc).
func TestOldUnorganizedKeys(t *testing.T) {
	fake := newFakeS3()
	inner := newTestService(t, fake)
	ctx := context.Background()
	for _, name := range []string{"report.pdf", "user:notes.txt"} {
		if _, err := inner.Save(ctx, &artifact.SaveRequest{
			AppName: "krude", UserID: "usr_1", SessionID: "sess_a", FileName: name, Part: genai.NewPartFromText("old " + name),
		}); err != nil {
			t.Fatal(err)
		}
	}
	store := ByOrganization(inner)
	orgA := inOrganization("org_a")

	if got := loadTextIn(t, orgA, store, "sess_a", "report.pdf"); got != "old report.pdf" {
		t.Errorf("old session file reads %q", got)
	}
	if version := saveIn(t, orgA, store, "sess_a", "report.pdf", "new"); version != 2 {
		t.Errorf("new version after an old one = %d, want 2", version)
	}
	if got := loadTextIn(t, orgA, store, "sess_a", "report.pdf"); got != "new" {
		t.Errorf("latest reads %q", got)
	}
	if names := listIn(t, orgA, store, "sess_a"); !slices.Equal(names, []string{"report.pdf"}) {
		t.Errorf("lists %v, want the session file and no old member file", names)
	}
	_, err := store.Load(orgA, &artifact.LoadRequest{AppName: "krude", UserID: "usr_1", SessionID: "sess_a", FileName: "user:notes.txt"})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an old member file is served: %v", err)
	}
	// Deleting the session's file removes the old versions too, and leaves
	// the old member file for the move.
	if err := store.Delete(orgA, &artifact.DeleteRequest{AppName: "krude", UserID: "usr_1", SessionID: "sess_a", FileName: "report.pdf"}); err != nil {
		t.Fatal(err)
	}
	if keys := keysOf(fake); !slices.Equal(keys, []string{"krude/usr_1/user/user:notes.txt/1"}) {
		t.Errorf("keys after delete = %v", keys)
	}
}

func TestNoOrganizationNoArtifacts(t *testing.T) {
	store := ByOrganization(newTestService(t, newFakeS3()))
	for _, ctx := range []context.Context{context.Background(), inOrganization(""), inOrganization("org_a/../org_b")} {
		_, err := store.Save(ctx, &artifact.SaveRequest{AppName: "krude", UserID: "usr_1", SessionID: "s", FileName: "f", Part: genai.NewPartFromText("x")})
		if !errors.Is(err, ErrNoOrganization) {
			t.Errorf("save: %v, want ErrNoOrganization", err)
		}
		_, err = store.List(ctx, &artifact.ListRequest{AppName: "krude", UserID: "usr_1", SessionID: "s"})
		if !errors.Is(err, ErrNoOrganization) {
			t.Errorf("list: %v, want ErrNoOrganization", err)
		}
	}
}

func loadTextIn(t *testing.T, ctx context.Context, store artifact.Service, sessionID, name string) string {
	t.Helper()
	loaded, err := store.Load(ctx, &artifact.LoadRequest{AppName: "krude", UserID: "usr_1", SessionID: sessionID, FileName: name})
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	if loaded.Part.InlineData != nil {
		return string(loaded.Part.InlineData.Data)
	}
	return loaded.Part.Text
}
