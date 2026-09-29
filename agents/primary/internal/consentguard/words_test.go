package consentguard

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// numbered is n distinct words, "<prefix>1 <prefix>2 ...", so that a run of them
// is never found by chance somewhere else.
func numbered(prefix string, n int) string {
	found := make([]string, n)
	for index := range found {
		found[index] = fmt.Sprintf("%s%d", prefix, index+1)
	}
	return strings.Join(found, " ")
}

// firstWords is the first n words of text.
func firstWords(text string, n int) string { return strings.Join(strings.Fields(text)[:n], " ") }

func TestWordsAreRunsOfLettersAndDigits(t *testing.T) {
	for _, testCase := range []struct {
		name string
		text string
		want []string
	}{
		{"plain", "Raya shipped the release", []string{"raya", "shipped", "the", "release"}},
		{"capitals fold", "SHIPPED Shipped shipped", []string{"shipped", "shipped", "shipped"}},
		{"punctuation ends a word", "Yes, (really)! It shipped; ok?", []string{"yes", "really", "it", "shipped", "ok"}},
		{"a straight apostrophe stays inside a word", "don't stop", []string{"dont", "stop"}},
		{"and so does a typographic one", "don’t stop, Raya’s call", []string{"dont", "stop", "rayas", "call"}},
		{"both spellings are the same word", "it's", []string{"its"}},
		{"a quotation mark is no apostrophe", "'quoted' text", []string{"quoted", "text"}},
		{"a number with a point is one word", "pi is 3.14 today", []string{"pi", "is", "3.14", "today"}},
		{"and with a comma", "we paid 1,000 dollars", []string{"we", "paid", "1,000", "dollars"}},
		{"a sentence's point ends the word before it", "shipped in 2020.Then it broke", []string{"shipped", "in", "2020", "then", "it", "broke"}},
		{"a trailing comma is no part of a number", "1, 2 and 3", []string{"1", "2", "and", "3"}},
		{"a version is one word", "v1.2.3 is out", []string{"v1.2.3", "is", "out"}},
		{"hyphens end a word", "state-of-the-art tooling", []string{"state", "of", "the", "art", "tooling"}},
		{"markdown and bullets are no words", "- **Bold** and _italic_\n* item", []string{"bold", "and", "italic", "item"}},
		{"line breaks are spaces", "one\ntwo\r\nthree\tfour", []string{"one", "two", "three", "four"}},
		{"letters of every script", "Zoë ate the café crème 日本語 Привет", []string{"zoë", "ate", "the", "café", "crème", "日本語", "привет"}},
		{"a zero-width space splits nothing", "ship​ped", []string{"shipped"}},
		{"nor do a joiner, a soft hyphen or a mark", "sh‍ip­ped‎", []string{"shipped"}},
		{"a format character is no space either", "one​ two", []string{"one", "two"}},
		{"an apostrophe with a format character between still joins", "don'​t", []string{"dont"}},
		{"nothing", "", nil},
		{"only punctuation", "... --- !!!", nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := words(testCase.text); !slices.Equal(got, testCase.want) {
				t.Errorf("words(%q) = %q, want %q", testCase.text, got, testCase.want)
			}
		})
	}
}

// sourceOf is one source with one value: a tool that returned it.
func sourceOf(parts ...string) []Source { return []Source{{Tool: "knowledge_search", Parts: parts}} }

// TestTwentyWordsInARowAreACopyAndNineteenAreNot is the rule's edge: the owner
// allows a summary, and a summary has no run of 20 words taken word for word
// from what the run read.
func TestTwentyWordsInARowAreACopyAndNineteenAreNot(t *testing.T) {
	passage := numbered("w", 60)
	index := newCopyIndex(sourceOf(passage))
	for _, testCase := range []struct {
		name   string
		answer string
		want   int
	}{
		{"nineteen words", firstWords(passage, 19), 0},
		{"twenty words", firstWords(passage, 20), 20},
		{"twenty-one words", firstWords(passage, 21), 21},
		{"the whole passage", passage, 60},
		{"nineteen words twice, with another word between", firstWords(passage, 19) + " other " + firstWords(passage, 19), 0},
		{"nineteen words and then a different twentieth", firstWords(passage, 19) + " other", 0},
		{"twenty words from the middle, inside other words", "so " + strings.Join(strings.Fields(passage)[10:30], " ") + " and that is all", 20},
		{"nineteen words from the middle", "so " + strings.Join(strings.Fields(passage)[10:29], " ") + " and that is all", 0},
		{"a run that is broken by one word in its middle", firstWords(passage, 10) + " other " + strings.Join(strings.Fields(passage)[11:30], " "), 0},
		{"another passage altogether", numbered("z", 60), 0},
		{"no words", "", 0},
		{"a short answer", "a few words", 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := index.longestCopy(testCase.answer); got != testCase.want {
				t.Errorf("longestCopy = %d, want %d", got, testCase.want)
			}
		})
	}
}

// A copy is the same words, however they are written: another case, other
// punctuation, bullets in place of line breaks, a pasted quotation mark.
func TestACopyIsTheSameWordsHoweverTheyAreWritten(t *testing.T) {
	source := "Deployments run every Tuesday at nine, unless the release manager, Priya, calls a freeze; " +
		"in that case they wait for the next window and the on-call engineer's sign-off."
	index := newCopyIndex(sourceOf(source))
	for name, answer := range map[string]string{
		"as it was":                  source,
		"in capitals":                strings.ToUpper(source),
		"without commas":             strings.NewReplacer(",", "", ";", "", ".", "").Replace(source),
		"as bullets":                 "- " + strings.ReplaceAll(source, ", ", "\n- "),
		"in one quote":               "They said: “" + source + "”",
		"with one hyphen":            strings.ReplaceAll(source, "sign-off", "sign off"),
		"with a joiner":              strings.Replace(source, "Tuesday", "Tues​day", 1),
		"with an apostrophe changed": strings.ReplaceAll(source, "'", "’"),
	} {
		if got := index.longestCopy(answer); got < CopyRun {
			t.Errorf("%s: longestCopy = %d, want a copy (at least %d)", name, got, CopyRun)
		}
	}
}

// A run never crosses from one value a tool returned into the next, or from one
// tool's result into another's: they were never in a sentence together.
func TestARunNeverCrossesFromOneValueIntoTheNext(t *testing.T) {
	first, second := numbered("a", 12), numbered("b", 12)
	index := newCopyIndex([]Source{{Tool: "one", Parts: []string{first, second}}, {Tool: "two", Parts: []string{numbered("c", 12)}}})
	if got := index.longestCopy(first + " " + second); got != 0 {
		t.Errorf("a run over two values = %d, want none: each is 12 words", got)
	}
	if got := index.longestCopy(second + " " + numbered("c", 12)); got != 0 {
		t.Errorf("a run over two results = %d, want none", got)
	}
	// Twenty words in one value are a copy wherever the value's words come from.
	whole := newCopyIndex([]Source{{Parts: []string{first + " " + second}}})
	if got := whole.longestCopy(first + " " + second); got != 24 {
		t.Errorf("a run inside one value = %d, want 24", got)
	}
}

// The words of a source are compared with the answer whatever the tool, and
// the longest run is reported, not the first.
func TestTheLongestCopyIsReported(t *testing.T) {
	long, short := numbered("l", 45), numbered("s", 22)
	index := newCopyIndex([]Source{{Parts: []string{short}}, {Parts: []string{long}}})
	answer := short + " between " + long
	if got := index.longestCopy(answer); got != 45 {
		t.Errorf("longestCopy = %d, want 45", got)
	}
}

func TestNothingIsCopiedFromNothing(t *testing.T) {
	answer := numbered("w", 40)
	for name, sources := range map[string][]Source{
		"no sources":           nil,
		"a source of no parts": {{Tool: "knowledge_search"}},
		"blank parts":          sourceOf("", "   "),
		"a short source":       sourceOf("only five words in this source"),
	} {
		if got := newCopyIndex(sources).longestCopy(answer); got != 0 {
			t.Errorf("%s: longestCopy = %d, want 0", name, got)
		}
	}
}
