// Command generate writes testdata/cases.json, the frozen cases the relevance
// eval judges.
//
// The cases are hand-written templates and their variations: a few dozen
// questions a startup's Slack and Gmail would ask, each with the ordinary
// work answer to it, filled with different people, projects, customers,
// days and figures; the same answers again with one private sentence added;
// and cases written out, in Hindi, Hinglish and Spanish. Nothing is random:
// the same source writes the same file, so the file can be reviewed as it
// is, and a change to a template shows as a change to the cases.
//
// Run it from the repository root, and commit the file it writes:
//
//	go run ./agents/primary/internal/relevance/eval/testdata/generate
//
// The file is frozen so that a change to the judge is compared on the same
// answers. Change a template only to add a case or fix a label, and say so in
// the commit: a judge measured on new cases has not been compared with itself.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance/eval"
)

func main() {
	out := flag.String("out", "agents/primary/internal/relevance/eval/testdata/cases.json", "the file to write")
	flag.Parse()

	var cases []eval.Case
	cases = append(cases, cleanCases()...)
	cases = append(cases, askedCases()...)
	cases = append(cases, steerIgnoredCases()...)
	cases = append(cases, nonEnglishCleanCases()...)
	cases = append(cases, longCleanCases()...)
	cases = append(cases, regulatedWorkCases()...)
	cases = append(cases, credentialMentionCases()...)
	cases = append(cases, terseCases()...)
	cases = append(cases, mixedCases()...)
	cases = append(cases, gateCases()...)
	cases = append(cases, nearMissCases()...)
	held, regulatedClean := regulatedCases()
	cases = append(cases, regulatedClean...)

	cases = append(cases, partialCases()...)
	cases = append(cases, reasonCases()...)
	cases = append(cases, steeringCases()...)
	cases = append(cases, injectionCases()...)
	cases = append(cases, nonEnglishLeakCases()...)
	cases = append(cases, longLeakCases()...)
	cases = append(cases, breadthCases()...)
	cases = append(cases, held...)

	if err := check(cases); err != nil {
		log.Fatal(err)
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", " ")
	if err := encoder.Encode(cases); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, encoded.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Print(summary(cases))
	fmt.Printf("wrote %s (%d bytes)\n", *out, encoded.Len())
}

// check refuses cases that do not validate, an id used twice, and a question
// asked the same way with the same answer twice.
func check(cases []eval.Case) error {
	ids, texts := map[string]bool{}, map[string]string{}
	for _, c := range cases {
		if err := c.Validate(); err != nil {
			return err
		}
		if ids[c.ID] {
			return fmt.Errorf("the id %s is used twice", c.ID)
		}
		ids[c.ID] = true
		key := c.Request + "\x00" + strings.Join(c.Context, "\x00") + "\x00" + strings.Join(c.Answer, "\x00")
		if other, seen := texts[key]; seen {
			return fmt.Errorf("%s and %s are the same case", c.ID, other)
		}
		texts[key] = c.ID
		for _, template := range append(append([]string{c.Request}, c.Context...), c.Answer...) {
			if hasPlaceholder(template) {
				return fmt.Errorf("%s has a placeholder that was never filled: %.120s", c.ID, template)
			}
		}
	}
	return nil
}

// placeholder is one of the templates' own {names}. A JSON or code sample in
// an answer has quotes in its braces, and does not look like it.
var placeholder = regexp.MustCompile(`\{[A-Za-z][A-Za-z0-9]*\}`)

// hasPlaceholder reports whether text still holds one.
func hasPlaceholder(text string) bool { return placeholder.MatchString(text) }

// summary counts the cases the way the eval's tests do.
func summary(cases []eval.Case) string {
	var clean, leaks, regulated int
	kinds := map[string]int{}
	for _, c := range cases {
		kinds[c.Kind]++
		switch {
		case c.Clean():
			clean++
		case c.Kind == eval.KindRegulated:
			regulated++
		default:
			leaks++
		}
	}
	names := make([]string, 0, len(kinds))
	for kind := range kinds {
		names = append(names, kind)
	}
	sort.Strings(names)
	var out strings.Builder
	fmt.Fprintf(&out, "%d cases: %d clean, %d leaks, %d regulated holds\n", len(cases), clean, leaks, regulated)
	for _, kind := range names {
		fmt.Fprintf(&out, "  %-28s %d\n", kind, kinds[kind])
	}
	return out.String()
}
