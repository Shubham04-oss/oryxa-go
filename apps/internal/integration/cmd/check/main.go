// Command check is gate G1 for one of Oryxa's own integrations. It answers
// whether a toolkit is fit to be built on, before a reviewer reads it, and it
// is the same check CI runs:
//
//	go run ./apps/internal/integration/cmd/check github
//
// It reads the toolkit out of the registry, so it checks what this build
// actually ships rather than what a file says, and it exits non-zero on the
// first rule it breaks. Every rule names what to do, because the point is to
// be read by the worker who has to fix it.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations"
)

func main() {
	toolkit, base := readFlags(os.Args[1:])
	if toolkit == "" {
		fmt.Fprintln(os.Stderr, "usage: check <toolkit> [--base <ref>]")
		fmt.Fprintln(os.Stderr, "  the toolkit is a registered integration's own slug, such as github")
		fmt.Fprintln(os.Stderr, "  --base is the commit the diff is measured against, for the scope check")
		os.Exit(2)
	}
	registry, err := integrations.NewRegistry()
	if err != nil {
		fmt.Fprintf(os.Stderr, "the registry does not build: %v\n", err)
		os.Exit(1)
	}
	registered, ok := registry.Lookup(toolkit)
	if !ok {
		fmt.Fprintf(os.Stderr, "%s is not an integration in this build; these are: %s\n",
			toolkit, strings.Join(registry.Toolkits(), ", "))
		os.Exit(2)
	}
	manifest := registered.Manifest()
	broken := false
	for _, check := range []struct {
		what string
		run  func(*report)
	}{
		{"the manifest", func(r *report) { checkManifest(r, manifest) }},
		{"scopes", func(r *report) { checkScopes(r, manifest) }},
		{"imports", func(r *report) { checkPackageImports(r, toolkit) }},
		{"testdata", func(r *report) { checkPackageTestdata(r, toolkit) }},
		{"the diff", func(r *report) { checkChangedScope(r, toolkit, base) }},
	} {
		found := &report{}
		check.run(found)
		if len(found.problems) == 0 {
			fmt.Printf("ok   %s\n", check.what)
			continue
		}
		broken = true
		fmt.Printf("FAIL %s\n", check.what)
		for _, problem := range found.problems {
			fmt.Printf("       %s\n", problem)
		}
	}
	if broken {
		os.Exit(1)
	}
	fmt.Printf("\n%s: every check G1 names passes.\n", toolkit)
}

// report is what a check found.
type report struct{ problems []string }

func (r *report) fail(problem string) { r.problems = append(r.problems, problem) }

// readFlags is the toolkit and the commit the diff is measured against.
func readFlags(args []string) (toolkit, base string) {
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--base", "-base":
			if index+1 < len(args) {
				index++
				base = args[index]
			}
		default:
			if toolkit == "" {
				toolkit = args[index]
			}
		}
	}
	return toolkit, base
}

// checkManifest is the manifest itself, which the registry already validated
// on Register, plus the prefixes Composio's names carry: an agent's skill
// reads tools by those names, so a name that changes is a member's breakage
// rather than a cleanup.
func checkManifest(r *report, manifest integration.Manifest) {
	if err := manifest.Validate(); err != nil {
		r.fail(err.Error())
	}
	prefix := strings.ToUpper(manifest.Toolkit) + "_"
	names := map[string]bool{}
	for _, tool := range manifest.Tools {
		if !strings.HasPrefix(tool.Name, prefix) {
			r.fail(fmt.Sprintf("the tool %s does not begin with %s, as GITHUB_LIST_ISSUES does", tool.Name, prefix))
		}
		if names[tool.Name] {
			r.fail(fmt.Sprintf("the tool %s is declared twice", tool.Name))
		}
		names[tool.Name] = true
		if tool.ReadOnly && tool.Destructive {
			r.fail(fmt.Sprintf("the read tool %s is marked destructive, which changes nothing", tool.Name))
		}
	}
	for _, trigger := range manifest.Triggers {
		if !strings.HasPrefix(trigger.Slug, prefix) {
			r.fail(fmt.Sprintf("the event %s does not begin with %s, as GITHUB_ISSUE_CREATED does", trigger.Slug, prefix))
		}
	}
	if len(manifest.Tools) == 0 && len(manifest.Readers) == 0 && len(manifest.Triggers) == 0 {
		r.fail("the manifest declares no tools, readers or events, so there is nothing to switch on")
	}
}

// checkScopes is the scope rule: every scope a method asks for is used by
// something, and nothing asks for a scope its method does not grant.
//
// Both halves matter. A scope nobody uses is one a member is asked to grant
// for nothing, which is the whole reason Google's scopes are restricted; and
// a tool asking for a scope the method does not have is a tool that fails at
// the app with a message a caller cannot act on.
func checkScopes(r *report, manifest integration.Manifest) {
	declared := map[string]bool{}
	for _, method := range manifest.Auth.Methods {
		if method.OAuth2 == nil {
			// A key, a password or client credentials grants no named scope.
			continue
		}
		for _, scope := range method.OAuth2.Scopes {
			declared[scope] = true
		}
	}
	if len(declared) == 0 {
		return
	}
	used := map[string]bool{}
	grant := func(kind string, name string, scopes []string) {
		for _, scope := range scopes {
			used[scope] = true
			if !declared[scope] {
				r.fail(fmt.Sprintf("the %s %s asks for the scope %q, which no method's sign-in grants", kind, name, scope))
			}
		}
	}
	for _, tool := range manifest.Tools {
		grant("tool", tool.Name, tool.Scopes)
	}
	for _, reader := range manifest.Readers {
		grant("reader", reader.Name, reader.Scopes)
	}
	for _, trigger := range manifest.Triggers {
		grant("event", trigger.Slug, trigger.Scopes)
	}
	if len(manifest.Tools) == 0 && len(manifest.Readers) == 0 && len(manifest.Triggers) == 0 {
		// Nothing has been built on this manifest yet, so nothing uses a
		// scope and saying so would only be the rule above said again.
		return
	}
	for _, scope := range sortedKeys(declared) {
		if !used[scope] {
			r.fail(fmt.Sprintf("the scope %q is asked for by the sign-in and used by nothing, so a member grants it for nothing", scope))
		}
	}
}

// forbiddenImports are the ones that would put a credential or a signature
// where an integration can reach it. The core owns both: it places the
// credential in the innermost transport, below anything that can see it, and
// it verifies a delivery before Parse sees one. An integration that imports
// either can put a request somewhere the core never checks.
var forbiddenImports = map[string]string{
	"golang.org/x/oauth2":       "the core holds and refreshes every token; an integration never makes a token request",
	"apps/internal/signin":      "signin is the core's, and the only thing that may open a credential",
	"apps/internal/hooks":       "hooks verifies every delivery; an integration never sees a signature",
	"apps/internal/postgres":    "an integration reaches its app over the Session, not over apps' tables",
	"apps/internal/application": "the application decides whose account a call acts as; an integration never chooses",
	"internal/secrets":          "a sealed value is opened only by the core, and only for the resource it is bound to",
}

// checkPackageImports is checkImports over the toolkit's own package, which
// is a directory beside this command's.
func checkPackageImports(r *report, toolkit string) {
	root := packageDir(toolkit)
	if root == "" {
		r.fail(fmt.Sprintf("the package for %s is not in this checkout", toolkit))
		return
	}
	checkImports(r, root)
}

// checkImports is that rule, read over one package's files. A toolkit worker
// owns one directory, so everything it can reach is here.
func checkImports(r *report, root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		r.fail(err.Error())
		return
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		checked++
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			r.fail(err.Error())
			continue
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, `"`) && !strings.HasPrefix(trimmed, `_ "`) {
				continue
			}
			imported := strings.Trim(strings.TrimPrefix(strings.TrimSpace(trimmed), "_ "), `"`)
			for forbidden, why := range forbiddenImports {
				// A forbidden path is named the way a rule reads best, from
				// the directory. A package inside one is named by its own
				// module path, so it is the tail that is matched.
				if imported == forbidden || strings.HasSuffix(imported, forbidden) ||
					strings.HasPrefix(imported, forbidden+"/") {
					r.fail(fmt.Sprintf("%s:%d imports %s: %s", name, i+1, forbidden, why))
				}
			}
		}
	}
	if checked == 0 {
		r.fail("the package has no Go file in it, so there is nothing to check")
	}
}

// secrets are the shapes a real credential takes. A cassette is a transcript
// of an app's own answers, so it holds ids and text and nothing else: one
// that holds a credential is either a recording from a live account or a copy
// of one, and both are exactly what must not be in a repository.
var secrets = []struct{ name, prefix string }{
	{"GitHub personal access token", "ghp_"},
	{"GitHub OAuth token", "gho_"},
	{"GitHub user token", "ghu_"},
	{"GitHub server token", "ghs_"},
	{"GitHub refresh token", "ghr_"},
	{"GitHub fine-grained token", "github_pat_"},
	{"Slack token", "xox"},
	{"Stripe live key", "rk_live_"},
	{"Stripe secret key", "sk_live_"},
	{"Google API key", "AIza"},
	{"Google OAuth client secret", "GOCSPX-"},
	{"AWS access key", "AKIA"},
	{"PEM private key", "-----BEGIN"},
	{"Notion integration token", "secret_"},
	{"JSON web token", "eyJ"},
}

// checkPackageTestdata is checkTestdata over the toolkit's own package.
func checkPackageTestdata(r *report, toolkit string) {
	root := packageDir(toolkit)
	if root == "" {
		r.fail(fmt.Sprintf("the package for %s is not in this checkout", toolkit))
		return
	}
	checkTestdata(r, root)
}

// checkTestdata is the secret scan over a toolkit's recordings. A sample
// payload is published on the bus, so a credential in one reaches every
// consumer of the event, and a cassette holds ids and text and nothing else.
func checkTestdata(r *report, root string) {
	scanned := 0
	err := filepath.WalkDir(filepath.Join(root, "testdata"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		scanned++
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		for _, secret := range secrets {
			if strings.Contains(string(raw), secret.prefix) {
				r.fail(fmt.Sprintf("testdata/%s holds what looks like a %s; a cassette is built from the provider's own documented answers and must hold none",
					relative, secret.name))
			}
		}
		return nil
	})
	if err != nil {
		r.fail(err.Error())
	}
	if scanned == 0 {
		r.fail("there is no testdata, so there is nothing to check and nothing to review: the harness needs the cassettes")
	}
}

// checkChangedScope is checkScope over the diff this checkout has.
func checkChangedScope(r *report, toolkit, base string) {
	changed, err := changedFiles(base)
	if err != nil {
		// The base cannot be found: a worktree on its first commit, or a
		// checkout with no git. It is reported rather than failed, because
		// saying so is what a reviewer needs, and a lane rule is not a
		// property of the code.
		r.fail(fmt.Sprintf("the diff could not be measured (%v): pass --base <ref> to check the scope", err))
		return
	}
	checkScope(r, toolkit, changed)
}

// checkScope is the rule that a toolkit worker stays inside its own
// directory: every file the diff names has to be one it owns.
func checkScope(r *report, toolkit string, changed []string) {
	if len(changed) == 0 {
		return
	}
	owned := "apps/internal/integrations/" + toolkit + "/"
	allowed := map[string]bool{
		// The registry line, which G4 adds, and the go.mod a new SDK needs.
		"apps/internal/integrations/registry.go": true,
		"go.mod":                                 true, "go.sum": true,
	}
	for _, name := range changed {
		switch {
		case strings.HasPrefix(name, owned):
		case allowed[name]:
		default:
			r.fail(fmt.Sprintf("%s is outside %s, which is all a toolkit worker owns", name, owned))
		}
	}
}

// changedFiles is what a diff against base touches. With no base it is the
// working tree, which is what a worker has while it is still building.
func changedFiles(base string) ([]string, error) {
	// The output is a file list, and a failure is the caller saying which
	// base it could not find, so nothing is printed to stderr here.
	var output []byte
	var err error
	if base == "" {
		output, err = git("status", "--porcelain")
	} else {
		output, err = git("diff", "--name-only", base+"...")
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(string(output), "\n") {
		// A porcelain line is "XY path", and a rename names both.
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			line = fields[len(fields)-1]
		}
		if name := strings.Trim(line, `"`); name != "" {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	return files, nil
}

// packageDir is where a toolkit's package is in this checkout, beside the
// command itself rather than under apps/internal, so the walk finds it
// without the module path being repeated.
func packageDir(toolkit string) string {
	for _, root := range []string{"../integrations", "apps/internal/integrations", "../../integrations"} {
		dir := filepath.Join(root, toolkit)
		if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
			return dir
		}
	}
	return ""
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
