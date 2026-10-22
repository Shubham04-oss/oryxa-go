package jobs

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsValidUTF8(t *testing.T) {
	got := truncate(strings.Repeat("é", 300), 500) // é is 2 bytes; 500 cuts no rune, 499 would
	if !utf8.ValidString(got) || len(got) != 500 {
		t.Fatalf("truncate at a rune boundary: valid=%v len=%d", utf8.ValidString(got), len(got))
	}
	got = truncate("a"+strings.Repeat("é", 300), 500)
	if !utf8.ValidString(got) || len(got) != 499 {
		t.Fatalf("truncate mid-rune: valid=%v len=%d, want valid and 499", utf8.ValidString(got), len(got))
	}
}
