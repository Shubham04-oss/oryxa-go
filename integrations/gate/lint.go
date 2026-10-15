// Package gate is the connector gate (docs/design/integrations/README.md §8.4):
// checks a connector must pass in CI with no live account.
package gate

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
)

const (
	maxTools      = 15
	minDescLen    = 40
	maxDescLen    = 400
	checkToolCap  = "tool budget"
	checkDescribe = "description lint"
)

var firstWordRE = regexp.MustCompile(`^[A-Z][a-z]+\b`)

// notVerbs are words a description must not open with; a description opens with what the tool does.
// ponytail: a stop list, not a parser; a noun outside it passes.
var notVerbs = []string{"A", "An", "The", "This", "These", "It", "Tool", "Tools", "For", "To", "Used"}

// Lint checks the manifest's tools: the budget and each description.
func Lint(m *manifest.Manifest) []Failure {
	var out []Failure
	if len(m.Tools) > maxTools {
		out = append(out, Failure{checkToolCap, fmt.Sprintf("%d tools; at most %d, the rest go through the proxy", len(m.Tools), maxTools)})
	}
	seen := map[string]string{}
	for _, t := range m.Tools {
		d := strings.TrimSpace(t.Description)
		fail := func(format string, args ...any) {
			out = append(out, Failure{checkDescribe, t.Name + ": " + fmt.Sprintf(format, args...)})
		}
		if n := len([]rune(d)); n < minDescLen || n > maxDescLen {
			fail("%d characters; want %d to %d", n, minDescLen, maxDescLen)
		}
		if w := firstWordRE.FindString(d); w == "" || slices.Contains(notVerbs, w) {
			fail("must start with a verb")
		}
		if !strings.Contains(strings.ToLower(d), "when") {
			fail("must say when to use the tool")
		}
		if other, ok := seen[d]; ok {
			fail("same description as %s", other)
		}
		seen[d] = t.Name
	}
	return out
}
