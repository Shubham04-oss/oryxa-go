package consentguard

import (
	"unicode"
)

// CopyRun is how many words in a row, copied word for word from what the run
// read, make an answer a quotation and no longer a summary: 20.
const CopyRun = 20

// words are the words of text, lower-cased, in order.
//
// A word is a run of letters, digits and combining marks. An apostrophe
// between two of them stays inside the word, and is dropped ("don't" and
// "don’t" are both "dont"), and so does the point or comma between two digits
// ("3.14", "1,000"), so that a contraction and a number are each one word, as a
// reader counts them. Everything else ends a word: spaces, punctuation, hyphens
// and markup. So a passage copied into a bullet list, with its quotation marks
// changed or its lines broken somewhere else, is the same run of words. Format
// characters (zero-width spaces and joiners, the soft hyphen, direction marks)
// are invisible and ignored, so that one cannot split a word in two.
func words(text string) []string {
	runes := []rune(text)
	var found []string
	var word []rune
	flush := func() {
		if len(word) > 0 {
			found = append(found, string(word))
			word = word[:0]
		}
	}
	for index := 0; index < len(runes); index++ {
		char := runes[index]
		switch {
		case isFormat(char):
			// Invisible: neither part of a word nor the end of one.
		case isWordRune(char):
			word = append(word, unicode.ToLower(char))
		case len(word) > 0 && isApostrophe(char) && isWordRune(nextVisible(runes, index+1)):
			// Inside a word, and dropped.
		case len(word) > 0 && (char == '.' || char == ',') && unicode.IsDigit(word[len(word)-1]) && unicode.IsDigit(nextVisible(runes, index+1)):
			word = append(word, char)
		default:
			flush()
		}
	}
	flush()
	return found
}

// isFormat reports whether char is a format character: one that draws nothing.
func isFormat(char rune) bool { return unicode.Is(unicode.Cf, char) }

func isWordRune(char rune) bool {
	return unicode.IsLetter(char) || unicode.IsNumber(char) || unicode.IsMark(char)
}

func isApostrophe(char rune) bool { return char == '\'' || char == '’' || char == 'ʼ' }

// nextVisible is the first rune of runes at or after index that draws
// something, or -1, which is no word rune, when there is none.
func nextVisible(runes []rune, index int) rune {
	for ; index < len(runes); index++ {
		if !isFormat(runes[index]) {
			return runes[index]
		}
	}
	return -1
}

// windowKey is CopyRun consecutive words, as the numbers their words were given.
type windowKey [CopyRun]uint32

// copyIndex holds every run of CopyRun consecutive words of what a run read, so
// that whether an answer copies any of them is one look-up for each place in the
// answer it could start. Each value a tool returned is read on its own: a run
// never crosses from one value into the next, since they were never in a
// sentence together.
type copyIndex struct {
	// ids gives each distinct word a number from 1; 0 is a word no source has.
	ids     map[string]uint32
	windows map[windowKey]struct{}
}

func newCopyIndex(sources []Source) *copyIndex {
	index := &copyIndex{ids: map[string]uint32{}, windows: map[windowKey]struct{}{}}
	for _, source := range sources {
		for _, part := range source.Parts {
			index.add(words(part))
		}
	}
	return index
}

func (index *copyIndex) add(found []string) {
	if len(found) < CopyRun {
		return
	}
	numbers := make([]uint32, len(found))
	for position, word := range found {
		number, known := index.ids[word]
		if !known {
			number = uint32(len(index.ids) + 1)
			index.ids[word] = number
		}
		numbers[position] = number
	}
	for start := 0; start+CopyRun <= len(numbers); start++ {
		index.windows[windowKey(numbers[start:start+CopyRun])] = struct{}{}
	}
}

// longestCopy is the length, in words, of the longest run in text that is a
// run of some source's words and is at least CopyRun long, or 0 when there is
// none. A run of 19 words is not one, and a run of 20 is.
func (index *copyIndex) longestCopy(text string) int {
	found := words(text)
	if len(found) < CopyRun {
		return 0
	}
	numbers := make([]uint32, len(found))
	for position, word := range found {
		numbers[position] = index.ids[word]
	}
	longest, run := 0, 0
	for start := 0; start+CopyRun <= len(numbers); start++ {
		if _, copied := index.windows[windowKey(numbers[start:start+CopyRun])]; copied {
			// Each window that follows one that matched adds a word to the run.
			run++
			longest = max(longest, run+CopyRun-1)
			continue
		}
		run = 0
	}
	return longest
}
