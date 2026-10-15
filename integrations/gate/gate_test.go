package gate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fakeappPkg = "github.com/Shubham04-oss/oryxa-go/integrations/connectors/fakeapp"

func build(t *testing.T, dir, pkg string, flags ...string) string {
	t.Helper()
	bin := filepath.Join(dir, strings.NewReplacer("/", "_", " ", "_", "=", "_").Replace(pkg+strings.Join(flags, "")))
	args := append(append([]string{"build"}, flags...), "-o", bin, pkg)
	if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
	return bin
}

func run(t *testing.T, bin string) []Failure {
	t.Helper()
	fs, err := Run(context.Background(), Options{App: "fakeapp", Binary: bin, Dir: "../connectors/fakeapp"})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestFakeappPasses(t *testing.T) {
	bin := build(t, t.TempDir(), fakeappPkg)
	if fs := run(t, bin); len(fs) > 0 {
		t.Fatalf("failures: %v", fs)
	}
}

// Each broken fixture must fail the check that is meant to catch it.
func TestBrokenFixturesFail(t *testing.T) {
	dir := t.TempDir()
	for defect, want := range map[string]string{
		"effect":      "effects: tool fakeapp.add_note",
		"description": "description lint: fakeapp.echo",
		"budget":      "tool budget:",
		"pagination":  "pagination: stream notes case full",
		"resume":      "resume: stream notes case resume: upserts across the crash",
		"delete":      "tombstone: stream notes case delete",
	} {
		t.Run(defect, func(t *testing.T) {
			bin := build(t, dir, "./testdata/brokenapp", "-ldflags=-X main.defect="+defect)
			fs := run(t, bin)
			for _, f := range fs {
				if strings.Contains(f.String(), want) {
					return
				}
			}
			t.Fatalf("no failure containing %q in %v", want, fs)
		})
	}
}

func TestMissingCoverageFails(t *testing.T) {
	// A copy of the fakeapp scenario without its delete case.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(dir, "testdata", "cassettes"), os.DirFS("../connectors/fakeapp/testdata/cassettes")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile("../connectors/fakeapp/testdata/conformance.json")
	cut := strings.Replace(string(raw), `"kind": "delete"`, `"kind": "empty"`, 1)
	if err := os.WriteFile(filepath.Join(dir, "testdata", "conformance.json"), []byte(cut), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := build(t, t.TempDir(), fakeappPkg)
	fs, err := Run(context.Background(), Options{App: "fakeapp", Binary: bin, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failures(fs), "has no delete case") {
		t.Fatalf("got %v", fs)
	}
}

func failures(fs []Failure) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.String() + "\n")
	}
	return b.String()
}
