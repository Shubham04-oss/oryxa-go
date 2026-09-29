package integrations

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The template is a directory the go tool ignores, because its name begins
// with an underscore: nothing is compiled from it and a copy is the first
// thing a worker does with it. That is also how a broken template would sit
// here unnoticed, copying itself into six toolkits that all fail the same
// way, so it is built and run from this test by naming it explicitly.
//
// The go tool only ignores a directory like this for a wildcard pattern:
// naming it outright builds it.
func TestTheTemplateBuildsAndPassesItsOwnConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("the template is compiled with the go tool, which -short asks not to spend on")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go tool is not on PATH, so the template cannot be built here")
	}
	built := exec.Command("go", "test", "./_template")
	output, err := built.CombinedOutput()
	if err != nil {
		t.Fatalf("the template does not build or does not pass its own conformance: %v\n%s",
			err, output)
	}
	if !strings.Contains(string(output), "ok") {
		t.Fatalf("the template said %s", output)
	}
}

// A template is copied, so every file a worker needs has to be in it: the
// code, the test that runs the harness, and the cassettes the harness reads.
// A template that arrives without its recordings fails at the first tool, and
// a worker filling them in from a live account is exactly what the harness
// exists to stop.
func TestTheTemplateCarriesItsOwnCassettes(t *testing.T) {
	t.Parallel()
	root := filepath.Join("_template", "testdata")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("the template has no testdata: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			t.Fatalf("the template's testdata has %s beside its cassettes, which is not a shape the harness reads", entry.Name())
		}
	}
	cassettes := 0
	if walked := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") &&
			!strings.HasSuffix(entry.Name(), ".headers.json") {
			cassettes++
		}
		return nil
	}); walked != nil {
		t.Fatal(walked)
	}
	if cassettes == 0 {
		t.Fatal("the template has no cassettes, so a copy fails at the first tool")
	}
	// Nothing in a template may be a real credential, and nothing may be a
	// placeholder a worker has to remember to replace.
	recorded := readTree(t, root)
	for _, secret := range []string{"ghp_", "gho_", "xox", "sk_live_", "AIza", "-----BEGIN"} {
		if strings.Contains(recorded, secret) {
			t.Fatalf("the template's testdata holds what looks like a %s", secret)
		}
	}
}

// readTree is every file under root, held in one string so a scan reads as
// one question about the whole of it.
func readTree(t *testing.T, root string) string {
	t.Helper()
	held := &strings.Builder{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		held.Write(contents)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return held.String()
}
