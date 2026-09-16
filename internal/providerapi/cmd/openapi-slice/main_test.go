package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunWritesOneReadableSlicePerTag(t *testing.T) {
	directory := t.TempDir()
	spec := filepath.Join(directory, "spec.json")
	if err := os.WriteFile(spec, []byte(description), 0o644); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	out := filepath.Join(directory, "out")
	if err := run([]string{"-spec", spec, "-tags", "pets, admin", "-out", out}, &log); err != nil {
		t.Fatalf("run() error = %v (%s)", err, log.String())
	}
	for _, pkg := range []string{"pets", "admin"} {
		written, err := os.ReadFile(filepath.Join(out, pkg, "openapi.json"))
		if err != nil {
			t.Fatal(err)
		}
		if text := string(written); !strings.HasSuffix(text, "}\n") || !strings.Contains(text, `"openapi": "3.0.3"`) {
			t.Fatalf("%s slice is not readable JSON:\n%s", pkg, text)
		}
	}
	pets, _ := os.ReadFile(filepath.Join(out, "pets", "openapi.json"))
	if text := string(pets); !strings.Contains(text, `"<b>escaped</b> & named"`) || !strings.Contains(text, "9007199254740993") {
		t.Fatalf("pets slice escaped HTML or lost precision:\n%s", text)
	}
	if err := run([]string{"-spec", spec, "-tags", "pets"}, &log); err == nil {
		t.Fatal("run() without -out succeeded")
	}
	if err := run([]string{"-spec", spec, "-tags", "pets,owners", "-out", out}, &log); err == nil {
		t.Fatal("run() with a tag that has no operations succeeded")
	}

	if err := run([]string{"-spec", spec, "-tags", "zoo = pets | admin, pets", "-out", out}, &log); err != nil {
		t.Fatalf("run() with a grouped entry error = %v (%s)", err, log.String())
	}
	zoo, err := os.ReadFile(filepath.Join(out, "zoo", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if text := string(zoo); !strings.Contains(text, `"operationId": "list"`) || !strings.Contains(text, `"operationId": "audit"`) {
		t.Fatalf("zoo slice lacks the operations of pets and admin:\n%s", text)
	}
	for _, tags := range []string{"pets,pets", "pets=admin,pets", "Zoo=pets", "zoo=pets|owners", "zoo=|"} {
		if err := run([]string{"-spec", spec, "-tags", tags, "-out", out}, &log); err == nil {
			t.Errorf("run() with -tags %q succeeded", tags)
		}
	}
}

func TestParseEntry(t *testing.T) {
	for entry, want := range map[string]string{
		"repos":             "repos: [repos]",
		" code-scanning ":   "codescanning: [code-scanning]",
		"Uploads (Chunked)": "uploadschunked: [Uploads (Chunked)]",
		"files=Files|File versions|Trashed files":  "files: [Files File versions Trashed files]",
		" uploads = Uploads | Uploads (Chunked) |": "uploads: [Uploads Uploads (Chunked)]",
	} {
		pkg, tags, err := parseEntry(entry)
		if got := pkg + ": [" + strings.Join(tags, " ") + "]"; err != nil || got != want {
			t.Errorf("parseEntry(%q) = %s %v, want %s", entry, got, err, want)
		}
	}
	for _, entry := range []string{"--", "Files=Files", "2fa=Two factor", "my-files=Files", "files=", "files= | "} {
		if _, _, err := parseEntry(entry); err == nil {
			t.Errorf("parseEntry(%q) succeeded", entry)
		}
	}
}

func TestPackageName(t *testing.T) {
	for tag, want := range map[string]string{"repos": "repos", "code-scanning": "codescanning", "Enterprise Teams": "enterpriseteams"} {
		if got, err := packageName(tag); err != nil || got != want {
			t.Errorf("packageName(%q) = %q %v, want %q", tag, got, err, want)
		}
	}
	for _, tag := range []string{"--", "2fa"} {
		if _, err := packageName(tag); err == nil {
			t.Errorf("packageName(%q) succeeded", tag)
		}
	}
}
