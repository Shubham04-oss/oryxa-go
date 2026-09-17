package chunking

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

func policy(maxTokens, overlap int) domain.ChunkPolicy {
	return domain.ChunkPolicy{Strategy: domain.StrategyStructure, MaxTokens: maxTokens, OverlapTokens: overlap}
}

// assertInvariants checks the two promises the package makes about every
// piece it returns, on whatever document a test happens to be using.
func assertInvariants(t *testing.T, text string, pieces []Piece, chunkPolicy domain.ChunkPolicy) {
	t.Helper()
	for index, piece := range pieces {
		if piece.StartByte < 0 || piece.EndByte > len(text) || piece.StartByte >= piece.EndByte {
			t.Fatalf("piece %d has range [%d,%d) over %d bytes", index, piece.StartByte, piece.EndByte, len(text))
		}
		if got := text[piece.StartByte:piece.EndByte]; got != piece.Text {
			t.Fatalf("piece %d text is not its own byte range:\n text = %q\nrange = %q", index, piece.Text, got)
		}
		if piece.TokenCount > chunkPolicy.MaxTokens {
			t.Errorf("piece %d is %d tokens, over the %d budget: %q", index, piece.TokenCount, chunkPolicy.MaxTokens, piece.Text)
		}
		if strings.TrimSpace(piece.Text) != piece.Text {
			t.Errorf("piece %d is not trimmed: %q", index, piece.Text)
		}
	}
	for index := 1; index < len(pieces); index++ {
		if pieces[index].StartByte <= pieces[index-1].StartByte {
			t.Fatalf("piece %d starts at %d, not after piece %d at %d",
				index, pieces[index].StartByte, index-1, pieces[index-1].StartByte)
		}
	}
}

// assertCovers checks that no text was dropped. Heading lines are expected to
// be missing from the body, because they are carried in the heading path.
func assertCovers(t *testing.T, text string, pieces []Piece) {
	t.Helper()
	covered := make([]bool, len(text))
	for _, piece := range pieces {
		for position := piece.StartByte; position < piece.EndByte; position++ {
			covered[position] = true
		}
	}
	lineStart := 0
	for lineStart < len(text) {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart + 1
		}
		line := strings.TrimRight(text[lineStart:lineEnd], "\n")
		if _, _, isHeading := heading(line); !isHeading {
			for position := lineStart; position < lineEnd; position++ {
				if !isBoundary(text[position]) && !covered[position] {
					t.Fatalf("byte %d (%q) of line %q was dropped", position, text[position:position+1], line)
				}
			}
		}
		lineStart = lineEnd
	}
}

const manual = `# Oryxa

Oryxa is an AWS-backed Go monorepo.

## Security

### Credentials

Every credential is held by the connections service. It must be rotated every
ninety days, and a rotation that fails pages the on-call engineer.

Tokens issued to an agent expire in sixty seconds.

## Storage

Files hang on a member, a team or the organization.
`

func TestCutKeepsPiecesAsExactByteRanges(t *testing.T) {
	t.Parallel()
	chunkPolicy := policy(64, 8)
	pieces := Cut(manual, "text/markdown", chunkPolicy)
	if len(pieces) == 0 {
		t.Fatal("Cut returned nothing")
	}
	assertInvariants(t, manual, pieces, chunkPolicy)
	assertCovers(t, manual, pieces)
}

func TestCutCarriesTheHeadingTrail(t *testing.T) {
	t.Parallel()
	pieces := Cut(manual, "text/markdown", policy(64, 0))
	var credentials []Piece
	for _, piece := range pieces {
		if strings.Contains(piece.Text, "rotated every") {
			credentials = append(credentials, piece)
		}
	}
	if len(credentials) != 1 {
		t.Fatalf("expected one piece about rotation, got %d", len(credentials))
	}
	want := []string{"Oryxa", "Security", "Credentials"}
	if !slices.Equal(credentials[0].HeadingPath, want) {
		t.Fatalf("heading path = %v, want %v", credentials[0].HeadingPath, want)
	}
	embedded := domain.Chunk{Text: credentials[0].Text, HeadingPath: credentials[0].HeadingPath}.EmbedText()
	if !strings.HasPrefix(embedded, "Oryxa > Security > Credentials\n\n") {
		t.Fatalf("EmbedText did not lead with the heading trail: %q", embedded)
	}
}

func TestCutDoesNotSpanHeadings(t *testing.T) {
	t.Parallel()
	// A budget large enough to swallow the whole document would still be
	// wrong to use across sections: a chunk means one heading trail.
	for _, piece := range Cut(manual, "text/markdown", policy(2048, 0)) {
		if strings.Contains(piece.Text, "monorepo") && strings.Contains(piece.Text, "rotated") {
			t.Fatalf("a piece spanned two sections: %q", piece.Text)
		}
	}
}

func TestCutOverlapsWithinASection(t *testing.T) {
	t.Parallel()
	body := "# Section\n\n" + strings.Repeat("The quick brown fox jumps over the lazy dog. ", 40)
	chunkPolicy := policy(64, 16)
	pieces := Cut(body, "text/markdown", chunkPolicy)
	if len(pieces) < 3 {
		t.Fatalf("expected several pieces, got %d", len(pieces))
	}
	assertInvariants(t, body, pieces, chunkPolicy)
	overlaps := 0
	for index := 1; index < len(pieces); index++ {
		if pieces[index].StartByte < pieces[index-1].EndByte {
			overlaps++
		}
	}
	if overlaps == 0 {
		t.Fatal("no piece overlapped its predecessor")
	}

	none := Cut(body, "text/markdown", policy(64, 0))
	for index := 1; index < len(none); index++ {
		if none[index].StartByte < none[index-1].EndByte {
			t.Fatalf("piece %d overlapped with overlap_tokens at zero", index)
		}
	}
}

func TestCutTreatsAFencedHashAsCodeNotAHeading(t *testing.T) {
	t.Parallel()
	body := "# Real\n\nBefore.\n\n```sh\n# this is a shell comment\necho hi\n```\n\nAfter.\n"
	for _, piece := range Cut(body, "text/markdown", policy(256, 0)) {
		if slices.Contains(piece.HeadingPath, "this is a shell comment") {
			t.Fatalf("a fenced comment became a heading: %v", piece.HeadingPath)
		}
	}
	pieces := Cut(body, "text/markdown", policy(256, 0))
	assertInvariants(t, body, pieces, policy(256, 0))
	joined := strings.Join(texts(pieces), "\n")
	if !strings.Contains(joined, "echo hi") {
		t.Fatalf("the code fence was lost: %q", joined)
	}
}

func TestCutSplitsTextWithNoBoundaryToUse(t *testing.T) {
	t.Parallel()
	// One unbroken run: no sentence, no word, no line to break on.
	body := strings.Repeat("a", 4000)
	chunkPolicy := policy(64, 0)
	pieces := Cut(body, "text/plain", chunkPolicy)
	if len(pieces) < 2 {
		t.Fatalf("expected the run to be split, got %d pieces", len(pieces))
	}
	assertInvariants(t, body, pieces, chunkPolicy)
	if strings.Join(texts(pieces), "") != body {
		t.Fatal("the pieces do not reassemble the original run")
	}
}

func TestCutSplitsCJKWithoutSpaces(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("認証と認可は別物である。", 200)
	chunkPolicy := policy(128, 0)
	pieces := Cut(body, "text/plain", chunkPolicy)
	if len(pieces) < 2 {
		t.Fatalf("expected several pieces, got %d", len(pieces))
	}
	assertInvariants(t, body, pieces, chunkPolicy)
	for _, piece := range pieces {
		if !strings.HasPrefix(piece.Text, "認") && !strings.HasPrefix(piece.Text, "と") &&
			!strings.HasPrefix(piece.Text, "は") && !strings.HasPrefix(piece.Text, "別") &&
			!strings.HasPrefix(piece.Text, "可") && !strings.HasPrefix(piece.Text, "物") &&
			!strings.HasPrefix(piece.Text, "で") && !strings.HasPrefix(piece.Text, "あ") &&
			!strings.HasPrefix(piece.Text, "る") {
			t.Fatalf("a piece began mid-rune: %q", piece.Text[:min(12, len(piece.Text))])
		}
	}
}

func TestCutIsDeterministic(t *testing.T) {
	t.Parallel()
	chunkPolicy := policy(96, 12)
	first := Cut(manual, "text/markdown", chunkPolicy)
	for attempt := 0; attempt < 5; attempt++ {
		again := Cut(manual, "text/markdown", chunkPolicy)
		if len(again) != len(first) {
			t.Fatalf("piece count changed between runs: %d then %d", len(first), len(again))
		}
		for index := range first {
			if first[index].Text != again[index].Text || first[index].StartByte != again[index].StartByte {
				t.Fatalf("piece %d changed between runs", index)
			}
		}
	}
}

func TestCutFixedStrategyIgnoresStructure(t *testing.T) {
	t.Parallel()
	pieces := Cut(manual, "text/markdown", domain.ChunkPolicy{Strategy: domain.StrategyFixed, MaxTokens: 2048})
	if len(pieces) != 1 {
		t.Fatalf("fixed strategy over a small document should give one piece, got %d", len(pieces))
	}
	if len(pieces[0].HeadingPath) != 0 {
		t.Fatalf("fixed strategy should not build heading paths, got %v", pieces[0].HeadingPath)
	}
}

func TestCutEmptyAndWhitespace(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"", "   ", "\n\n\t\n"} {
		if pieces := Cut(body, "text/plain", policy(64, 8)); pieces != nil {
			t.Fatalf("Cut(%q) = %v, want nil", body, pieces)
		}
	}
}

func TestCutPlainTextLeavesHashesAlone(t *testing.T) {
	t.Parallel()
	body := "# not a heading\n\nsome text\n"
	for _, piece := range Cut(body, "text/plain", policy(256, 0)) {
		if len(piece.HeadingPath) != 0 {
			t.Fatalf("plain text produced a heading path: %v", piece.HeadingPath)
		}
	}
}

func TestEstimateTokens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		text      string
		low, high int
	}{
		{"empty", "", 0, 0},
		{"one word", "hello", 1, 3},
		{"a sentence", "The quick brown fox jumps over the lazy dog.", 8, 14},
		{"cjk", "認証と認可は別物である。", 10, 18},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := EstimateTokens(testCase.text)
			if got < testCase.low || got > testCase.high {
				t.Fatalf("EstimateTokens(%q) = %d, want between %d and %d", testCase.text, got, testCase.low, testCase.high)
			}
		})
	}
}

func TestEstimateTokensGrowsWithText(t *testing.T) {
	t.Parallel()
	previous := 0
	for count := 1; count <= 20; count++ {
		current := EstimateTokens(strings.Repeat("word ", count))
		if current < previous {
			t.Fatalf("estimate fell from %d to %d at %d words", previous, current, count)
		}
		previous = current
	}
}

func texts(pieces []Piece) []string {
	out := make([]string, 0, len(pieces))
	for _, piece := range pieces {
		out = append(out, piece.Text)
	}
	return out
}

// TestCutHoldsInvariantsAcrossShapes runs the invariants over a spread of
// documents and budgets, which is where boundary bugs actually turn up.
func TestCutHoldsInvariantsAcrossShapes(t *testing.T) {
	t.Parallel()
	documents := map[string]string{
		"manual":        manual,
		"no headings":   strings.Repeat("A plain paragraph with several words in it.\n\n", 30),
		"one long line": strings.Repeat("word ", 2000),
		"list":          "# Title\n\n" + strings.Repeat("- an item in a list\n", 200),
		"code":          "# Code\n\n```go\n" + strings.Repeat("\tfmt.Println(\"x\")\n", 200) + "```\n",
		"mixed":         manual + strings.Repeat("Trailing prose. ", 300),
	}
	budgets := []domain.ChunkPolicy{policy(32, 0), policy(64, 8), policy(128, 24), policy(512, 64)}
	for name, document := range documents {
		for _, chunkPolicy := range budgets {
			t.Run(fmt.Sprintf("%s/%d-%d", name, chunkPolicy.MaxTokens, chunkPolicy.OverlapTokens), func(t *testing.T) {
				t.Parallel()
				pieces := Cut(document, "text/markdown", chunkPolicy)
				if len(pieces) == 0 {
					t.Fatal("Cut returned nothing")
				}
				assertInvariants(t, document, pieces, chunkPolicy)
				assertCovers(t, document, pieces)
			})
		}
	}
}
