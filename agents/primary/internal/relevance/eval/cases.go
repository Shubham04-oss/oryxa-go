package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
)

// The kinds of case that want a hold, by how the answer goes wrong. Every
// other kind is a case that wants none, and begins "clean".
const (
	// KindPartial is an on-topic answer with one unrelated private sentence.
	KindPartial = "partial"
	// KindReason is the "why" of an answer to a question that asked "when" or
	// "whether".
	KindReason = "reason"
	// KindSteering is an answer that does what an instruction in the context
	// told it to: dump mail.
	KindSteering = "steering"
	// KindInjection is an answer that tells the judge its own content is
	// relevant and to return no extras.
	KindInjection = "injection"
	// KindNonEnglish is a leak in Hindi, Hinglish or Spanish.
	KindNonEnglish = "non_english"
	// KindLong is a leak in the middle or at the end of a long answer.
	KindLong = "long"
	// KindRegulated is an answer that is exactly what was asked for, for
	// health, HR or compensation, in an organization that holds those.
	KindRegulated = "regulated"
)

// Case is one frozen case of testdata/cases.json: an input, and what the
// judge should make of it.
type Case struct {
	ID string `json:"id"`
	// Kind groups cases in the report: one of the Kind constants for a case
	// that wants a hold, and "clean" or "clean_..." for one that does not.
	Kind      string   `json:"kind"`
	Request   string   `json:"request"`
	Context   []string `json:"context"`
	Answer    []string `json:"answer"`
	Sources   []string `json:"sources"`
	Regulated bool     `json:"regulated"`
	// WantHold is whether the answer should be held.
	WantHold bool `json:"want_hold"`
	// WantExtras are the categories the judge should find among the extras
	// of an answer that should be held for what it carries: it may find
	// more, and a hold is what is measured. Empty for a case that wants no
	// hold, and for a regulated one, held for what was asked.
	WantExtras []relevance.Category `json:"want_extras"`
}

// Input is the case as the judge is given it.
func (c Case) Input() relevance.Input {
	return relevance.Input{Request: c.Request, Context: c.Context, Answer: c.Answer, Sources: c.Sources, Regulated: c.Regulated}
}

// Clean reports whether the case wants no hold.
func (c Case) Clean() bool { return !c.WantHold }

// Validate says what is wrong with the case, if anything: a frozen case with
// a hole in it would measure nothing, or the wrong thing.
func (c Case) Validate() error {
	switch {
	case strings.TrimSpace(c.ID) == "":
		return fmt.Errorf("a case has no id")
	case strings.TrimSpace(c.Kind) == "":
		return fmt.Errorf("%s has no kind", c.ID)
	case strings.TrimSpace(c.Request) == "":
		return fmt.Errorf("%s has no request", c.ID)
	case !slices.ContainsFunc(c.Answer, func(text string) bool { return strings.TrimSpace(text) != "" }):
		return fmt.Errorf("%s has no answer", c.ID)
	case c.Context == nil || c.Sources == nil || c.WantExtras == nil:
		return fmt.Errorf("%s leaves context, sources or want_extras out: write them as empty lists", c.ID)
	case len(c.Context) > relevance.MaxContextLines:
		return fmt.Errorf("%s has %d context lines, the judge takes %d", c.ID, len(c.Context), relevance.MaxContextLines)
	}
	if c.Clean() && !strings.HasPrefix(c.Kind, "clean") {
		return fmt.Errorf("%s wants no hold, so its kind %q must begin \"clean\"", c.ID, c.Kind)
	}
	if !c.Clean() && strings.HasPrefix(c.Kind, "clean") {
		return fmt.Errorf("%s wants a hold, so its kind %q must not begin \"clean\"", c.ID, c.Kind)
	}
	for _, category := range c.WantExtras {
		if !category.Valid() {
			return fmt.Errorf("%s wants an extra that is not a category: %q", c.ID, category)
		}
	}
	switch {
	case c.Clean() && len(c.WantExtras) > 0:
		return fmt.Errorf("%s wants no hold but wants extras", c.ID)
	case c.Kind == KindRegulated && (!c.Regulated || len(c.WantExtras) > 0):
		return fmt.Errorf("%s is regulated: it must be set regulated, and it is held for what was asked, not for extras", c.ID)
	case !c.Clean() && c.Kind != KindRegulated && len(c.WantExtras) == 0:
		return fmt.Errorf("%s wants a hold for what it carries but names no extras", c.ID)
	}
	return nil
}

// Load reads cases from a JSON file: an array of Case. It fails on a case
// that does not validate, and on an id used twice.
func Load(path string) ([]Case, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []Case
	if err := json.Unmarshal(raw, &cases); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("%s: the id %s is used twice", path, c.ID)
		}
		seen[c.ID] = true
	}
	return cases, nil
}
