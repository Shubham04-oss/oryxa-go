package eval

import (
	"strings"
	"testing"
)

// TestLocate: a knowledge base's passage is placed in its document however
// the knowledge base re-spaced it, and one it rewrote beyond recognition is
// reported as not placed rather than placed somewhere wrong.
func TestLocate(t *testing.T) {
	t.Parallel()
	body := "# Title\n\nIntro line.\n\n## Retrieval\n\nVectors miss an exact identifier,\nerror code or surname.\n\n" +
		strings.Repeat("Full-text search misses a question worded nothing like the passage. ", 6) + "\n\n## Next\n\nThe end."
	section := strings.Index(body, "Vectors miss")
	cases := []struct {
		name      string
		passage   string
		wantStart int
		placed    bool
	}{
		{name: "verbatim", passage: "Vectors miss an exact identifier,\nerror code or surname.", wantStart: section, placed: true},
		{name: "rejoined on one line", passage: "Vectors miss an exact identifier, error code or surname.", wantStart: section, placed: true},
		{name: "re-spaced with a changed middle", passage: "Vectors miss an exact identifier, error code or surname. " +
			strings.Repeat("Full-text search misses a question worded nothing like the passage. ", 2) + "SOMETHING ELSE " +
			strings.Repeat("Full-text search misses a question worded nothing like the passage. ", 2), wantStart: section, placed: true},
		{name: "not in the document", passage: "Nothing like this appears anywhere in the document at all.", placed: false},
	}
	for _, test := range cases {
		start, end, placed := locate(body, test.passage)
		if placed != test.placed {
			t.Fatalf("%s: placed = %v, want %v", test.name, placed, test.placed)
		}
		if !placed {
			continue
		}
		if start != test.wantStart || end <= start || end > len(body) {
			t.Fatalf("%s: span = [%d, %d), want it to start at %d", test.name, start, end, test.wantStart)
		}
	}
	// The verbatim passage's span is exactly the passage.
	passage := cases[0].passage
	if start, end, _ := locate(body, passage); body[start:end] != passage {
		t.Fatalf("verbatim span = %q, want %q", body[start:end], passage)
	}
}
