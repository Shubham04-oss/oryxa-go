package chunking

import "unicode"

// EstimateTokens approximates how many tokens a provider will charge for a
// string, without shipping a tokenizer for every model.
//
// It has to be an estimate. Titan, Cohere and Gemini tokenize differently, and
// the exact count only matters for two decisions here: staying under the
// model's input limit, and sizing a chunk. Both are safe in the same
// direction, so this deliberately runs 10 to 20 per cent high on ordinary
// prose. Over-counting makes chunks slightly smaller than the budget;
// under-counting would let a provider silently truncate a chunk, and a
// half-embedded chunk is a retrieval bug that never announces itself.
//
// The weights come from how byte-pair encodings behave: English averages
// around four characters per token, a space usually merges into the word
// after it rather than standing alone, and CJK characters cost about a token
// each.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	var narrow, wide, other, spaces int
	for _, symbol := range text {
		switch {
		case symbol == ' ' || symbol == '\t' || symbol == '\n' || symbol == '\r':
			spaces++
		case symbol < unicode.MaxASCII:
			narrow++
		case isWide(symbol):
			wide++
		default:
			other++
		}
	}
	estimate := float64(narrow)/4.0 + float64(spaces)*0.2 + float64(wide)*1.2 + float64(other)*0.5
	if estimate < 1 {
		return 1
	}
	return int(estimate + 0.5)
}

// wideScripts are the scripts whose characters carry roughly one token each
// rather than roughly a quarter of one.
var wideScripts = []*unicode.RangeTable{
	unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul,
}

func isWide(symbol rune) bool {
	return unicode.IsOneOf(wideScripts, symbol)
}

// budgetBytes converts a token budget into a byte length to cut at, for the
// last resort where a single unbroken run of characters is over budget and
// there is no word or sentence boundary to use. It inverts the narrow-rune
// weight above, so it errs short for the same reason.
func budgetBytes(tokens int) int {
	if tokens < 1 {
		return 1
	}
	return tokens * 4
}
