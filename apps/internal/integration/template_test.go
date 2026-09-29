package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The template a toolkit worker copies lives at
// apps/internal/integrations/_template. Its name begins with an underscore, so
// the go tool ignores it: nothing is compiled from it and a copy is the first
// thing a worker does with it. That is also how a broken template would sit
// there unnoticed, copying itself into six toolkits that all fail the same
// way, so it is built and run from here by naming it explicitly. The go tool
// only ignores a directory like that for a wildcard pattern: naming it
// outright builds it.
//
// This test lives beside the contract the template is written against rather
// than beside the template, because the template's own directory is not a
// package the go tool will look in.
func TestTheTemplateBuildsAndPassesItsOwnConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("the template is compiled with the go tool, which -short asks not to spend on")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go tool is not on PATH, so the template cannot be built here")
	}
	// The go tool resolves the pattern against the working directory, which is
	// this package's, so the path is spelled from here.
	built := exec.Command("go", "test", "../integrations/_template")
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
	root := templatePath(t)
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

// templatePath is where the template sits relative to this package, which is
// the contract's own directory: integrations/_template is two levels down and
// across.
func templatePath(t *testing.T) string {
	t.Helper()
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(working, "..", "integrations", "_template", "testdata")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the template has no testdata at %s: %v", root, err)
	}
	return root
}
