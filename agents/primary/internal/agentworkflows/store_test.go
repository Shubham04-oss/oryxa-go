package agentworkflows

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// truncate bounds the text FinishExecution writes to the error column. It
// used to slice by byte offset, which can land inside a multi-byte rune's
// encoding and hand Postgres a string that fails UTF-8 validation on write -
// leaving the execution stuck in "running" with its lease uncleared. Slicing
// by rune keeps every returned string valid UTF-8 regardless of where the
// limit falls.
func TestTruncateDoesNotSplitMultibyteCharacters(t *testing.T) {
	tests := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "empty", value: "", limit: 10, want: ""},
		{name: "trims whitespace before measuring", value: "  short  ", limit: 10, want: "short"},
		{name: "exact limit is kept whole", value: "abcde", limit: 5, want: "abcde"},
		{name: "ascii over limit truncates cleanly", value: "abcdefgh", limit: 5, want: "abcde"},
		{
			// Each "中" encodes as 3 bytes, so byte offset 5 (the old
			// value[:limit] behaviour) lands in the middle of the second
			// rune's encoding and produces invalid UTF-8. A rune-based limit
			// of 5 keeps the first five whole characters instead.
			name:  "multibyte value truncated at a limit that lands mid-character",
			value: strings.Repeat("中", 10),
			limit: 5,
			want:  strings.Repeat("中", 5),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := truncate(test.value, test.limit)
			if got != test.want {
				t.Fatalf("truncate(%q, %d) = %q, want %q", test.value, test.limit, got, test.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncate(%q, %d) = %q is not valid UTF-8", test.value, test.limit, got)
			}
		})
	}
}
