package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// check is what one of the command's rules said, as a test reads it.
func check(run func(*report)) []string {
	found := &report{}
	run(found)
	return found.problems
}

func joined(problems []string) string { return strings.Join(problems, "\n") }

// A manifest whose names carry no toolkit prefix is refused: an agent's skill
// reads tools by the names Composio gave them, so a name that changes is a
// member's breakage rather than a cleanup.
func TestTheManifestCheckWantsTheToolkitPrefix(t *testing.T) {
	t.Parallel()
	manifest := integration.Manifest{
		Toolkit: "acme", Version: 1, Name: "Acme", APIBase: "https://api.acme.test",
		Auth: integration.Auth{
			Methods: []integration.Method{{Kind: integration.KindAPIToken, Place: &integration.Place{
				In: integration.InHeader, Name: "Authorization", Template: "Bearer {token}"}}},
			Test: integration.Test{Path: "/me", Label: "/login"},
		},
		Tools: []integration.ToolSpec{{
			Name: "ACME_LIST_ISSUES", Description: "List issues", ReadOnly: true,
			InputSchema: []byte(`{"type":"object"}`),
		}},
		Triggers: []integration.TriggerSpec{{
			Slug: "ACME_ISSUE_EVENT", Name: "An issue changed", Kind: integration.TriggerPoll,
			ConfigSchema: []byte(`{"type":"object"}`), PayloadSchema: []byte(`{"type":"object"}`),
		}},
	}
	if problems := check(func(r *report) { checkManifest(r, manifest) }); len(problems) != 0 {
		t.Fatalf("a sound manifest was refused: %v", problems)
	}
	// A name without the prefix is not sound, so the registry would have
	// refused it before the check ever saw it.
	unprefixed := manifest
	unprefixed.Tools = append([]integration.ToolSpec(nil), manifest.Tools...)
	unprefixed.Tools[0].Name = "LIST_ISSUES"
	unprefixed.Triggers = append([]integration.TriggerSpec(nil), manifest.Triggers...)
	unprefixed.Triggers[0].Slug = "ISSUE_EVENT"
	problems := check(func(r *report) { checkManifest(r, unprefixed) })
	for _, want := range []string{"invalid manifest", "does not begin with ACME_"} {
		if !strings.Contains(joined(problems), want) {
			t.Errorf("the manifest check said %v, which does not mention %q", problems, want)
		}
	}
	// A read tool marked destructive is sound to the schema and still wrong,
	// which is why the check says it in its own words as well.
	marked := manifest
	marked.Tools = append([]integration.ToolSpec(nil), manifest.Tools...)
	marked.Tools[0].Destructive = true
	problems = check(func(r *report) { checkManifest(r, marked) })
	if !strings.Contains(joined(problems), "read tool ACME_LIST_ISSUES is marked destructive") {
		t.Errorf("the manifest check said %v", problems)
	}
	// A tool declared twice is the other half of the same rule.
	repeat := manifest
	repeat.Tools = append(append([]integration.ToolSpec(nil), manifest.Tools...), manifest.Tools[0])
	if problems = check(func(r *report) { checkManifest(r, repeat) }); !strings.Contains(joined(problems), "declared twice") {
		t.Errorf("a tool declared twice said %v", problems)
	}
	// A manifest with nothing on it is not a toolkit anybody can be moved to.
	empty := manifest
	empty.Tools, empty.Triggers, empty.Readers = nil, nil, nil
	if problems := check(func(r *report) { checkManifest(r, empty) }); len(problems) == 0 {
		t.Error("a manifest with no tools, readers or events passed")
	}
}

// The scope rule has two halves, and both cost a member something: a scope
// nothing uses is one they are asked to grant for nothing, and a tool asking
// for a scope the sign-in does not have is a tool that fails at the app with a
// message the caller cannot act on.
func TestTheScopeCheckNamesBothHalves(t *testing.T) {
	t.Parallel()
	manifest := scoped(func(scopes []string) integration.Manifest {
		return integration.Manifest{
			Toolkit: "acme", Version: 1, Name: "Acme", APIBase: "https://api.acme.test",
			Auth: integration.Auth{
				Methods: []integration.Method{{Kind: integration.KindOAuth2Code, OAuth2: &integration.OAuth2{
					Client: "acme", AuthURL: "https://acme.test/a", TokenURL: "https://acme.test/t",
					RevokeURL: "https://acme.test/r", Scopes: scopes, PKCE: true,
					ClientAuth: integration.ClientAuthBody,
				}}},
				Test: integration.Test{Path: "/me", Label: "/login"},
			},
			Tools: []integration.ToolSpec{{
				Name: "ACME_LIST_ISSUES", Description: "List issues", ReadOnly: true,
				Scopes: []string{"read"}, InputSchema: []byte(`{"type":"object"}`),
			}},
		}
	})
	if problems := check(func(r *report) { checkManifest(r, manifest) }); len(problems) != 0 {
		t.Fatalf("the manifest is not sound: %v", problems)
	}
	if problems := check(func(r *report) { checkScopes(r, manifest) }); len(problems) != 0 {
		t.Fatalf("a sound set of scopes was refused: %v", problems)
	}

	// A scope the sign-in asks for and nothing uses.
	unused := manifest
	unused.Auth.Methods[0].OAuth2.Scopes = []string{"read", "read:org"}
	if problems := check(func(r *report) { checkScopes(r, unused) }); !strings.Contains(joined(problems), "used by nothing") {
		t.Errorf("an unused scope was accepted: %v", problems)
	}
	// A scope a tool asks for that the sign-in does not grant.
	extra := manifest
	extra.Auth.Methods[0].OAuth2.Scopes = []string{"read"}
	extra.Tools = []integration.ToolSpec{{
		Name: "ACME_CLOSE_ISSUE", Description: "Close an issue", Destructive: true,
		Scopes: []string{"write"}, InputSchema: []byte(`{"type":"object"}`),
	}}
	problems := check(func(r *report) { checkScopes(r, extra) })
	if !strings.Contains(joined(problems), "no method's sign-in grants") {
		t.Errorf("a scope outside the sign-in was accepted: %v", problems)
	}
	// A method that grants no named scope has nothing to be checked against,
	// which is a key or a password rather than an omission.
	keyed := manifest
	keyed.Auth.Methods = []integration.Method{{Kind: integration.KindAPIToken, Place: &integration.Place{
		In: integration.InHeader, Name: "Authorization", Template: "Bearer {token}"}}}
	if problems := check(func(r *report) { checkScopes(r, keyed) }); len(problems) != 0 {
		t.Errorf("a key-only method was refused: %v", problems)
	}
}

func scoped(build func([]string) integration.Manifest) integration.Manifest {
	return build([]string{"read"})
}

// The imports that would put a credential or a signature where an integration
// can reach it are named, each with why, because a worker who hits the rule
// needs to know what to do rather than only that they may not.
func TestTheImportCheckRefusesTheOnesThatBreakTheBoundary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := `package acme

import (
	"context"
	"encoding/json"

	"golang.org/x/oauth2"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/google/go-github/v92/github"
)

var _ = oauth2.NoContext
var _ = signin.ErrNotActive
var _ = github.NewClient
var _ = context.Background
var _ = json.Marshal
`
	if err := os.WriteFile(filepath.Join(dir, "acme.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	problems := check(func(r *report) { checkImports(r, dir) })
	for _, forbidden := range []string{"golang.org/x/oauth2", "apps/internal/signin"} {
		if !strings.Contains(joined(problems), forbidden) {
			t.Errorf("the import check said %v, which does not mention %s", problems, forbidden)
		}
	}
	// The SDK itself is not forbidden: a toolkit is built on it.
	if strings.Contains(joined(problems), "go-github") {
		t.Errorf("the import check refused the SDK: %v", problems)
	}
	// An empty package is a rule the worker needs told about rather than a
	// silent pass.
	if problems := check(func(r *report) { checkImports(r, t.TempDir()) }); len(problems) == 0 {
		t.Error("a package with no file in it passed")
	}
}

// A cassette is a transcript of a provider's own documented answers, so it
// holds ids and text and nothing else. One that holds a credential is either a
// recording from a live account or a copy of one.
func TestTheTestdataScanNamesTheShapesACredentialTakes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "testdata", "cassettes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "testdata", "cassettes", "ok.json"), []byte(
		`{"interactions":[{"request":{"method":"GET","url":"https://api.acme.test/user"},
		  "response":{"status":200,"body":{"login":"octocat"}}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if problems := check(func(r *report) { checkTestdata(r, dir) }); len(problems) != 0 {
		t.Fatalf("a clean cassette was refused: %v", problems)
	}
	for _, secret := range []string{"ghp_", "xoxb-", "rk_live_", "AIza", "AKIA", "-----BEGIN", "sk_live_"} {
		bare := t.TempDir()
		if err := os.MkdirAll(filepath.Join(bare, "testdata", "hooks"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bare, "testdata", "hooks", "delivery.json"),
			[]byte(`{"authorization":"`+secret+`0123456789"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		problems := check(func(r *report) { checkTestdata(r, bare) })
		if len(problems) == 0 {
			t.Errorf("a cassette holding %s passed", secret)
			continue
		}
		if !strings.Contains(joined(problems), "testdata/hooks/delivery.json") {
			t.Errorf("the scan did not name the file: %v", problems)
		}
	}
	// No recordings at all is a rule worth saying: a toolkit with no cassettes
	// has nothing for a reviewer to read.
	if problems := check(func(r *report) { checkTestdata(r, t.TempDir()) }); len(problems) == 0 {
		t.Error("a toolkit with no testdata passed")
	}
}

// A toolkit worker owns one directory, and every file its diff names has to be
// inside it, or beside it where the plan says: the registry line, and go.mod
// for an SDK it adds.
func TestTheScopeCheckKeepsAWorkerInsideItsDirectory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		changed []string
		says    string
	}{
		{
			name: "only its own files",
			changed: []string{
				"apps/internal/integrations/github/github.go",
				"apps/internal/integrations/github/testdata/cassettes/list.ok.json",
				"apps/internal/integrations/registry.go", "go.mod", "go.sum",
			},
		},
		{
			name:    "another lane's file",
			changed: []string{"apps/internal/integrations/github/github.go", "apps/mount.go"},
			says:    "apps/mount.go is outside",
		},
		{
			name:    "another toolkit's directory",
			changed: []string{"apps/internal/integrations/slack/slack.go"},
			says:    "apps/internal/integrations/slack/slack.go is outside",
		},
		{
			name:    "a directory whose name merely contains the toolkit's",
			changed: []string{"apps/internal/integrations/githubbot/githubbot.go"},
			says:    "is outside",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			problems := check(func(r *report) { checkScope(r, "github", test.changed) })
			if test.says == "" {
				if len(problems) != 0 {
					t.Fatalf("a worker inside its own directory was refused: %v", problems)
				}
				return
			}
			if !strings.Contains(joined(problems), test.says) {
				t.Fatalf("the scope check said %v, which does not mention %q", problems, test.says)
			}
		})
	}
	// A diff that cannot be taken is said rather than passed, because saying
	// so is what tells a reviewer the rule did not run.
	problems := check(func(r *report) { checkChangedScope(r, "github", "no-such-ref") })
	if !strings.Contains(joined(problems), "the diff could not be measured") {
		t.Fatalf("a missing base said %v", problems)
	}
}

func TestReadFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args    []string
		toolkit string
		base    string
	}{
		{args: []string{"github"}, toolkit: "github"},
		{args: []string{"github", "--base", "slack/base"}, toolkit: "github", base: "slack/base"},
		{args: []string{"--base", "main", "notion"}, toolkit: "notion", base: "main"},
		{args: nil},
	}
	for _, test := range cases {
		toolkit, base := readFlags(test.args)
		if toolkit != test.toolkit || base != test.base {
			t.Errorf("readFlags(%v) = %q, %q, want %q, %q", test.args, toolkit, base, test.toolkit, test.base)
		}
	}
}

// changedFiles reads a porcelain line, which names a path after its status
// and, for a rename, after the arrow.
func TestChangedFilesReadsAPorcelainLine(t *testing.T) {
	dir := t.TempDir()
	write := func(t *testing.T) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(t)
	// The working tree is what a worker has while it is still building, so a
	// missing base is not a reason to check nothing.
	t.Chdir(dir)
	if err := initRepository(t, dir); err != nil {
		t.Fatal(err)
	}
	write(t)
	files, err := changedFiles("")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "a.go" {
		t.Fatalf("changedFiles = %v, want [a.go]", files)
	}
}

// initRepository makes a checkout git will answer for, without a commit: the
// working tree is enough, since that is what a worker is measured on before
// it has one.
func initRepository(t *testing.T, dir string) error {
	t.Helper()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "check@example.invalid"},
		{"config", "user.name", "check"},
	} {
		if _, err := gitIn(dir, args...); err != nil {
			return err
		}
	}
	return nil
}
