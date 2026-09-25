package answerguard

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// TestTheGuardrailIsSetUpAsTheGuardReadsIt: the guardrail production
// provisions anonymizes exactly the kinds the guard takes as always masked,
// and detects exactly those it holds for review, so a kind the guard would
// deliver masked is never left in the text, nor one it holds left out of
// its findings.
func TestTheGuardrailIsSetUpAsTheGuardReadsIt(t *testing.T) {
	config, err := os.ReadFile("../../../../deploy/production/answer_guard.tf")
	if err != nil {
		t.Fatal(err)
	}
	list := func(name string) []string {
		t.Helper()
		found := regexp.MustCompile(`(?s)` + name + `\s*=\s*\[(.*?)\]`).FindSubmatch(config)
		if found == nil {
			t.Fatalf("answer_guard.tf sets no %s", name)
		}
		var kinds []string
		for _, kind := range regexp.MustCompile(`"([A-Z_]+)"`).FindAllSubmatch(found[1], -1) {
			kinds = append(kinds, string(kind[1]))
		}
		return kinds
	}
	if masked := list("answer_guard_masked"); !slices.Equal(slices.Sorted(slices.Values(masked)), slices.Sorted(slices.Values(MaskedKinds()))) {
		t.Errorf("the guardrail masks %v, the guard takes %v as masked", masked, MaskedKinds())
	}
	if held := list("answer_guard_held"); !slices.Equal(slices.Sorted(slices.Values(held)), slices.Sorted(slices.Values(HeldKinds()))) {
		t.Errorf("the guardrail detects %v, the guard holds %v", held, HeldKinds())
	}
}
