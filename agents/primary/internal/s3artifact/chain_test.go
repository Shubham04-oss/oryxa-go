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

func saveText(t *testing.T, store artifact.Service, name, text string) int64 {
	t.Helper()
	saved, err := store.Save(context.Background(), &artifact.SaveRequest{
		AppName: "krude", UserID: "usr_1", SessionID: "sess_1", FileName: name, Part: genai.NewPartFromText(text),
	})
	if err != nil {
		t.Fatal(err)
	}
	return saved.Version
}

func loadText(t *testing.T, store artifact.Service, name string, version int64) string {
	t.Helper()
	loaded, err := store.Load(context.Background(), &artifact.LoadRequest{AppName: "krude", UserID: "usr_1", SessionID: "sess_1", FileName: name, Version: version})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Part.InlineData != nil {
		return string(loaded.Part.InlineData.Data)
	}
	return loaded.Part.Text
}

// During the move, what was saved on S3 stays readable, new versions go to
// R2 numbered after the old ones, and the newest wins wherever it is.
func TestChainKeepsOldArtifactsReadableWhileWritingNewOnes(t *testing.T) {
	legacy := newTestService(t, newFakeS3())
	r2 := newFakeS3()
	current := newTestService(t, r2)
	current.prefix, current.encrypt = "artifacts/", false
	saveText(t, legacy, "report.csv", "v1 on S3")
	saveText(t, legacy, "report.csv", "v2 on S3")
	saveText(t, legacy, "old.txt", "only on S3")
	chain, err := NewChain(current, legacy)
	if err != nil {
		t.Fatal(err)
	}

	if got := loadText(t, chain, "old.txt", 0); got != "only on S3" {
		t.Errorf("old.txt = %q", got)
	}
	if version := saveText(t, chain, "report.csv", "v3 on R2"); version != 3 {
		t.Fatalf("new version = %d, want 3 after S3's 2", version)
	}
	if got := loadText(t, chain, "report.csv", 0); got != "v3 on R2" {
		t.Errorf("latest = %q", got)
	}
	if got := loadText(t, chain, "report.csv", 1); got != "v1 on S3" {
		t.Errorf("v1 = %q", got)
	}
	if got := loadText(t, current, "report.csv", 3); got != "v3 on R2" {
		t.Errorf("R2 holds %q", got)
	}
	if _, ok := r2.objects["artifacts/krude/usr_1/sess_1/report.csv/3"]; !ok {
		t.Errorf("R2 keys = %v, want the artifact under artifacts/", keysOf(r2))
	}
	if _, err := legacy.Load(context.Background(), &artifact.LoadRequest{AppName: "krude", UserID: "usr_1", SessionID: "sess_1", FileName: "report.csv", Version: 3}); err == nil {
		t.Error("the new version was written to S3")
	}

	versions, err := chain.Versions(context.Background(), &artifact.VersionsRequest{AppName: "krude", UserID: "usr_1", SessionID: "sess_1", FileName: "report.csv"})
	if err != nil || !slices.Equal(versions.Versions, []int64{1, 2, 3}) {
		t.Errorf("versions = %v, %v", versions, err)
	}
	listed, err := chain.List(context.Background(), &artifact.ListRequest{AppName: "krude", UserID: "usr_1", SessionID: "sess_1"})
	if err != nil || !slices.Equal(listed.FileNames, []string{"old.txt", "report.csv"}) {
		t.Errorf("list = %v, %v", listed, err)
	}
	if version := saveText(t, chain, "fresh.txt", "new"); version != 1 {
		t.Errorf("a new artifact starts at %d", version)
	}
}

func TestChainDeletesFromBothAndReportsMissing(t *testing.T) {
	legacy, current := newTestService(t, newFakeS3()), newTestService(t, newFakeS3())
	saveText(t, legacy, "a.txt", "old")
	chain, _ := NewChain(current, legacy)
	saveText(t, chain, "a.txt", "new")
	if err := chain.Delete(context.Background(), &artifact.DeleteRequest{AppName: "krude", UserID: "usr_1", SessionID: "sess_1", FileName: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	_, err := chain.Load(context.Background(), &artifact.LoadRequest{AppName: "krude", UserID: "usr_1", SessionID: "sess_1", FileName: "a.txt"})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("after delete: %v", err)
	}
	if _, err := NewChain(nil, legacy); err == nil {
		t.Error("accepted a chain with no current store")
	}
}

func keysOf(fake *fakeS3) []string {
	keys := make([]string, 0, len(fake.objects))
	for key := range fake.objects {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
