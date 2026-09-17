// Package chunking cuts a document into the pieces that get embedded.
//
// Chunking is where most retrieval quality is won or lost, and the two
// decisions that matter are made here deliberately. Every piece is an exact
// byte range of the text it came from, so a citation can be checked against
// the original instead of trusted; and every piece carries the trail of
// headings above it, so a passage keeps its subject when it is read alone.
//
// The cut follows the document's own shape. Headings bound a section,
// paragraphs fill a chunk, and a run of text is only broken mid-sentence when
// one sentence is already over budget. Consecutive chunks inside a section
// overlap, because the sentence that answers a question is often the one that
// straddles a boundary.
package chunking

import (
	"strings"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// Piece is one chunk before it has an identity or an embedding. Text is
// always exactly text[StartByte:EndByte] of the document it came from.
type Piece struct {
	Text        string
	TokenCount  int
	HeadingPath []string
	StartByte   int
	EndByte     int
}

// Cut turns a document into pieces under one corpus's policy.
//
// The text must already have been through Normalize for its content type.
// That order is what keeps a piece an exact range of what was stored: if HTML
// were converted here, every offset would point into a string nobody kept.
func Cut(text, contentType string, policy domain.ChunkPolicy) []Piece {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var sections []section
	if policy.Strategy == domain.StrategyFixed {
		sections = []section{{start: 0, end: len(text)}}
	} else {
		// Normalize leaves HTML, CSV and JSON markdown-shaped: headings where
		// the source had them, and a blank line between blocks. So one
		// segmenter serves all of them. Plain text is the exception, where a
		// leading '#' is far more likely to be a comment than a heading.
		sections = segment(text, contentType != "text/plain")
	}
	return pack(text, sections, policy)
}

// section is a run of text under one heading trail. Sections are contiguous
// and in document order.
type section struct {
	start    int
	end      int
	headings []string
	// blocks are the paragraph and code-fence boundaries inside the section,
	// as exclusive end offsets. A chunk boundary prefers to land on one.
	blocks []int
}

// segment walks the document once, tracking the heading stack and fenced code
// blocks, and returns the sections between headings.
//
// Fences matter more than they look: a '#' comment on the first line of a
// shell snippet is not a heading, and treating it as one would reparent every
// chunk after it under a nonsense title.
func segment(text string, markdown bool) []section {
	var (
		sections  []section
		headings  []string
		current   = section{start: 0}
		fence     string
		lineStart int
	)
	flush := func(end int, next []string) {
		current.end = end
		if strings.TrimSpace(text[current.start:current.end]) != "" {
			sections = append(sections, current)
		}
		current = section{start: end, headings: next}
	}
	for lineStart < len(text) {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart + 1
		}
		line := text[lineStart:lineEnd]
		trimmed := strings.TrimRight(line, "\r\n")

		if marker := fenceMarker(trimmed); marker != "" {
			switch {
			case fence == "":
				fence = marker
			case strings.HasPrefix(marker, fence):
				fence = ""
			}
			lineStart = lineEnd
			continue
		}
		if fence == "" {
			if markdown {
				if level, title, ok := heading(trimmed); ok {
					flush(lineStart, headingPath(headings, level, title))
					headings = current.headings
					// The heading line itself is carried in the path, not in
					// the body, so it is not repeated in every chunk beneath.
					current.start = lineEnd
					lineStart = lineEnd
					continue
				}
			}
			if strings.TrimSpace(trimmed) == "" {
				current.blocks = append(current.blocks, lineStart)
			}
		}
		lineStart = lineEnd
	}
	flush(len(text), nil)
	return sections
}

// fenceMarker returns the backtick or tilde run that opens or closes a code
// fence, or empty when the line is not a fence.
func fenceMarker(line string) string {
	indented := strings.TrimLeft(line, " ")
	if len(line)-len(indented) > 3 {
		return ""
	}
	for _, symbol := range []byte{'`', '~'} {
		count := 0
		for count < len(indented) && indented[count] == symbol {
			count++
		}
		if count >= 3 {
			return strings.Repeat(string(symbol), count)
		}
	}
	return ""
}

// heading recognises an ATX heading and returns its level and text.
func heading(line string) (int, string, bool) {
	indented := strings.TrimLeft(line, " ")
	if len(line)-len(indented) > 3 || !strings.HasPrefix(indented, "#") {
		return 0, "", false
	}
	level := 0
	for level < len(indented) && indented[level] == '#' {
		level++
	}
	if level > 6 || level >= len(indented) {
		return 0, "", false
	}
	if indented[level] != ' ' && indented[level] != '\t' {
		return 0, "", false
	}
	title := strings.TrimSpace(strings.TrimRight(indented[level:], " #"))
	if title == "" {
		return 0, "", false
	}
	return level, title, true
}

// headingPath returns the trail for a heading at level, replacing whatever the
// previous trail held at that depth and below.
func headingPath(stack []string, level int, title string) []string {
	depth := min(level-1, len(stack))
	path := make([]string, 0, depth+1)
	path = append(path, stack[:depth]...)
	return append(path, title)
}

// pack fills chunks section by section. A chunk never spans two sections,
// because the heading trail is part of what a chunk means.
func pack(text string, sections []section, policy domain.ChunkPolicy) []Piece {
	var pieces []Piece
	for _, current := range sections {
		for _, span := range packSection(text, current, policy) {
			start, end := trim(text, span[0], span[1])
			if start >= end {
				continue
			}
			// Trimming whitespace, and nudging an overlap forward to stay
			// inside the budget, can both land a span on the same first
			// non-blank byte as the span before it. Whichever of the two
			// covers the other is the one to keep, so pieces always advance
			// through the document and no text is indexed twice.
			redundant := false
			for len(pieces) > 0 {
				last := pieces[len(pieces)-1]
				if start > last.StartByte {
					break
				}
				if end <= last.EndByte {
					redundant = true
					break
				}
				pieces = pieces[:len(pieces)-1]
			}
			if redundant {
				continue
			}
			body := text[start:end]
			pieces = append(pieces, Piece{
				Text:        body,
				TokenCount:  EstimateTokens(body),
				HeadingPath: current.headings,
				StartByte:   start,
				EndByte:     end,
			})
		}
	}
	return pieces
}

// packSection greedily fills chunks from one section and applies the overlap
// between consecutive chunks.
//
// MaxTokens is a ceiling on the finished chunk, not on its body, because what
// it ultimately guards is the model's input limit. So the body is packed to
// the budget less the overlap, and the overlap is added back on top.
func packSection(text string, current section, policy domain.ChunkPolicy) [][2]int {
	body := policy.MaxTokens - policy.OverlapTokens
	if body < 1 {
		body = policy.MaxTokens
	}
	spans := splitRange(text, current.start, current.end, body, 0)
	// Re-join adjacent spans that fit together, so a section of short
	// paragraphs becomes a few full chunks rather than many thin ones.
	merged := make([][2]int, 0, len(spans))
	for _, span := range spans {
		if len(merged) > 0 {
			last := merged[len(merged)-1]
			if EstimateTokens(text[last[0]:span[1]]) <= body {
				merged[len(merged)-1] = [2]int{last[0], span[1]}
				continue
			}
		}
		merged = append(merged, span)
	}
	if policy.OverlapTokens <= 0 {
		return merged
	}
	for position := 1; position < len(merged); position++ {
		previous := merged[position-1]
		start := overlapStart(text, previous[0], previous[1], policy.OverlapTokens)
		// The overlap may not swallow a whole earlier chunk, or two chunks
		// would carry the same text and the index would count it twice.
		if start > previous[0] && start < merged[position][0] {
			merged[position][0] = fit(text, start, merged[position][1], policy.MaxTokens)
		}
	}
	return merged
}

// fit walks a span's start forward, a word at a time, until the span is inside
// the budget. The token count is an estimate, so adding an estimated overlap
// to an estimated body can land a token or two over; this is the backstop that
// makes the ceiling hold exactly rather than nearly.
func fit(text string, start, end, maxTokens int) int {
	for start < end && EstimateTokens(text[start:end]) > maxTokens {
		next := start
		for next < end && !isBoundary(text[next]) {
			next++
		}
		for next < end && isBoundary(text[next]) {
			next++
		}
		if next == start {
			break
		}
		start = next
	}
	return start
}

// overlapStart walks back from the end of a chunk until about overlapTokens
// worth of text is behind it, then snaps forward to a word boundary so the
// next chunk does not begin mid-word.
func overlapStart(text string, start, end, overlapTokens int) int {
	position := end
	for position > start {
		next := position - 1
		for next > start && !utf8.RuneStart(text[next]) {
			next--
		}
		if EstimateTokens(text[next:end]) > overlapTokens {
			break
		}
		position = next
	}
	for position < end && !isBoundary(text[position]) {
		position++
	}
	for position < end && isBoundary(text[position]) {
		position++
	}
	if position >= end {
		return start
	}
	return position
}

func isBoundary(symbol byte) bool {
	return symbol == ' ' || symbol == '\n' || symbol == '\t' || symbol == '\r'
}

// trim narrows a span past leading and trailing whitespace, keeping it a true
// range of the source.
func trim(text string, start, end int) (int, int) {
	for start < end && isBoundary(text[start]) {
		start++
	}
	for end > start && isBoundary(text[end-1]) {
		end--
	}
	return start, end
}

// ladder is the order in which an over-budget span is broken up: whole lines
// first, which keeps code and lists intact, then sentences, then words. Below
// that there is nothing to respect and the cut is made on a rune boundary.
var ladder = []func(text string, start, end int) [][2]int{lineUnits, sentenceUnits, wordUnits}

// splitRange breaks [start, end) into spans that each fit the budget, going
// one level down the ladder only for the part that did not fit.
func splitRange(text string, start, end, maxTokens, level int) [][2]int {
	if start >= end {
		return nil
	}
	if EstimateTokens(text[start:end]) <= maxTokens {
		return [][2]int{{start, end}}
	}
	if level >= len(ladder) {
		return hardCut(text, start, end, maxTokens)
	}
	units := ladder[level](text, start, end)
	if len(units) < 2 {
		return splitRange(text, start, end, maxTokens, level+1)
	}
	var (
		spans      [][2]int
		spanStart  = start
		spanEnd    = start
		flushSpan  = func() { spans = append(spans, [2]int{spanStart, spanEnd}) }
		hasContent = func() bool { return spanEnd > spanStart }
	)
	for _, unit := range units {
		if EstimateTokens(text[unit[0]:unit[1]]) > maxTokens {
			if hasContent() {
				flushSpan()
			}
			spans = append(spans, splitRange(text, unit[0], unit[1], maxTokens, level+1)...)
			spanStart, spanEnd = unit[1], unit[1]
			continue
		}
		if hasContent() && EstimateTokens(text[spanStart:unit[1]]) > maxTokens {
			flushSpan()
			spanStart = unit[0]
		}
		spanEnd = unit[1]
	}
	if hasContent() {
		flushSpan()
	}
	return spans
}

// hardCut is the last resort: an unbroken run with no boundary to use, such as
// a base64 blob or a long CJK passage with no spaces.
func hardCut(text string, start, end, maxTokens int) [][2]int {
	var spans [][2]int
	for start < end {
		limit := min(start+budgetBytes(maxTokens), end)
		for limit > start && limit < end && !utf8.RuneStart(text[limit]) {
			limit--
		}
		for limit > start && EstimateTokens(text[start:limit]) > maxTokens {
			limit--
			for limit > start && !utf8.RuneStart(text[limit]) {
				limit--
			}
		}
		if limit <= start {
			_, size := utf8.DecodeRuneInString(text[start:])
			limit = min(start+max(size, 1), end)
		}
		spans = append(spans, [2]int{start, limit})
		start = limit
	}
	return spans
}

// lineUnits splits on newlines. Each unit keeps its own line ending, so the
// units tile the span exactly.
func lineUnits(text string, start, end int) [][2]int {
	var units [][2]int
	for position := start; position < end; {
		breakAt := strings.IndexByte(text[position:end], '\n')
		if breakAt < 0 {
			units = append(units, [2]int{position, end})
			break
		}
		next := position + breakAt + 1
		units = append(units, [2]int{position, next})
		position = next
	}
	return units
}

// sentenceUnits splits after sentence-ending punctuation followed by space,
// including the full-width stops used in CJK text.
func sentenceUnits(text string, start, end int) [][2]int {
	var units [][2]int
	unitStart := start
	for position := start; position < end; {
		symbol, size := utf8.DecodeRuneInString(text[position:end])
		position += size
		if !isSentenceEnd(symbol) {
			continue
		}
		// Let a closing quote or bracket belong to the sentence it ends.
		for position < end && strings.ContainsRune(`"'”’)]`, rune(text[position])) {
			position++
		}
		if position < end && !isBoundary(text[position]) && !isWide(symbol) {
			continue
		}
		for position < end && isBoundary(text[position]) {
			position++
		}
		units = append(units, [2]int{unitStart, position})
		unitStart = position
	}
	if unitStart < end {
		units = append(units, [2]int{unitStart, end})
	}
	return units
}

func isSentenceEnd(symbol rune) bool {
	switch symbol {
	case '.', '!', '?', '。', '！', '？', '…':
		return true
	default:
		return false
	}
}

// wordUnits splits on runs of whitespace, each unit carrying the whitespace
// that follows it.
func wordUnits(text string, start, end int) [][2]int {
	var units [][2]int
	unitStart := start
	for position := start; position < end; position++ {
		if !isBoundary(text[position]) {
			continue
		}
		for position < end && isBoundary(text[position]) {
			position++
		}
		units = append(units, [2]int{unitStart, position})
		unitStart = position
		position--
	}
	if unitStart < end {
		units = append(units, [2]int{unitStart, end})
	}
	return units
}
