package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
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

func TestAgentHelpNeedsNoSession(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"setup", "--agent-help"}, &stdout, &stderr, func(string) string { return "" })
	if code != 0 || !strings.Contains(stdout.String(), "orgChart.relationships") || !strings.Contains(stdout.String(), "oryxa plan") {
		t.Fatalf("exit=%d output=%q error=%q", code, stdout.String(), stderr.String())
	}
}

func TestCachedSignInKeepsWhoamiOutputJSON(t *testing.T) {
	testSessionStore(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"user":{"id":"owner"}}`) }))
	defer api.Close()
	getenv := func(key string) string {
		if key == "ORYXA_API_URL" {
			return api.URL
		}
		return ""
	}
	if err := saveSession(api.URL, cliSession{AccessToken: "access", ExpiresAt: time.Now().Add(15 * time.Minute)}, getenv); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"whoami", "--no-input"}, &stdout, &stderr, getenv); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout polluted by login: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "saved sign-in") {
		t.Fatalf("missing diagnostic: %s", stderr.String())
	}
}

func TestSensitiveReviewIncludesRoleElevationAndPolicyChanges(t *testing.T) {
	for _, change := range []runtime.Change{{Op: "update", Kind: "member_role", Name: "member@acme.com", Role: "admin"}, {Op: "update", Kind: "policy", Name: "holdForReview"}, {Op: "delete", Kind: "team", Name: "Old"}} {
		if !hasSensitiveChanges(runtime.Plan{Changes: []runtime.Change{change}}) {
			t.Fatalf("sensitive change escaped review: %+v", change)
		}
	}
}
