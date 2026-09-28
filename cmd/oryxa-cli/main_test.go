package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func TestInitProducesValidWorkspaceWithoutCredentials(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "--org", "acme", "--domains", "acme.com, example.com"}, &stdout, &stderr, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	document, err := spec.Parse(stdout.Bytes())
	if err != nil {
		t.Fatalf("generated file is invalid: %v", err)
	}
	if document.Metadata.Organization != "acme" || len(document.Metadata.Domains) != 2 || document.Metadata.Domains[1] != "example.com" {
		t.Fatalf("generated file = %+v", document)
	}
}

func TestValidateRefusesUnknownSetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oryxa.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\norganization: {unknown: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"validate", "-f", path}, &stdout, &stderr, func(string) string { return "" })
	if code != 3 || !strings.Contains(stderr.String(), "unknown") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestRemoteCommandNeedsSignInWithoutInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"status", "--org", "acme", "--no-input"}, &stdout, &stderr, func(string) string { return "" })
	if code != 3 || !strings.Contains(stderr.String(), "browser sign-in is required") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}
