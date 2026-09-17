package transform

import (
	"errors"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

func TestNormalizeHashesTheSameBodyTheSameWay(t *testing.T) {
	first, err := Normalize([]byte("hello world"), MIMEPlain)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	second, err := Normalize([]byte("hello world"), MIMEPlain)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if first.Hash != second.Hash {
		t.Fatalf("Normalize() hash = %q and %q, want them equal", first.Hash, second.Hash)
	}
	if len(first.Hash) != 64 {
		t.Fatalf("Normalize() hash = %q, want 64 hex characters", first.Hash)
	}
}

func TestNormalizeSeesThroughReserialization(t *testing.T) {
	// A provider that hands back the same document with different line
	// endings, trailing padding or extra blank lines has not changed it.
	canonical, err := Normalize([]byte("one\n\ntwo\n"), MIMEMarkdown)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	for _, raw := range []string{
		"one\r\n\r\ntwo\r\n",
		"one   \n\n\n\n\ntwo\t\n\n\n",
		bom + "one\n\ntwo",
		"\n\none\n\ntwo\n\n",
	} {
		again, err := Normalize([]byte(raw), MIMEMarkdown)
		if err != nil {
			t.Fatalf("Normalize(%q) error = %v", raw, err)
		}
		if again.Hash != canonical.Hash {
			t.Fatalf("Normalize(%q) body = %q, want the same document as %q", raw, again.Body, canonical.Body)
		}
	}
}

func TestNormalizeHashesAChangedDocumentDifferently(t *testing.T) {
	before, err := Normalize([]byte("one\n\ntwo"), MIMEMarkdown)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	after, err := Normalize([]byte("one\n\nthree"), MIMEMarkdown)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if before.Hash == after.Hash {
		t.Fatal("Normalize() hashed two different documents the same")
	}
}

func TestNormalizeKeepsOneBlankLineBetweenParagraphs(t *testing.T) {
	text, err := Normalize([]byte("first\n\n\n\nsecond"), MIMEMarkdown)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if text.Body != "first\n\nsecond" {
		t.Fatalf("Normalize() body = %q, want %q", text.Body, "first\n\nsecond")
	}
}

func TestNormalizeReplacesBytesThatAreNotText(t *testing.T) {
	text, err := Normalize([]byte{'o', 'k', 0xff, 0xfe}, MIMEPlain)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if !strings.HasPrefix(text.Body, "ok") {
		t.Fatalf("Normalize() body = %q, want it to start with the readable part", text.Body)
	}
	if !strings.Contains(text.Body, "�") {
		t.Fatalf("Normalize() body = %q, want the invalid bytes replaced", text.Body)
	}
}

func TestNormalizeCutsAnEnormousBodyAtARuneBoundary(t *testing.T) {
	// Three-byte runes, so a naive cut at MaxBytes lands mid-rune.
	raw := strings.Repeat("あ", MaxBytes)
	text, err := Normalize([]byte(raw), MIMEPlain)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if !text.Truncated {
		t.Fatal("Normalize() truncated = false, want true")
	}
	if text.Bytes > MaxBytes {
		t.Fatalf("Normalize() kept %d bytes, want at most %d", text.Bytes, MaxBytes)
	}
	if strings.Contains(text.Body, "�") {
		t.Fatal("Normalize() cut a rune in half")
	}
}

func TestNormalizeRefusesWhatHoldsNoText(t *testing.T) {
	for _, declared := range []string{"application/pdf", "image/png", "application/zip", "application/octet-stream"} {
		if _, err := Normalize([]byte("whatever"), declared); !errors.Is(err, domain.ErrUnsupportedType) {
			t.Fatalf("Normalize(%s) error = %v, want ErrUnsupportedType", declared, err)
		}
	}
}

func TestCanonicalMIMEDropsWhatFollowsTheMediaType(t *testing.T) {
	value, err := CanonicalMIME("text/markdown; charset=utf-8")
	if err != nil || value != MIMEMarkdown {
		t.Fatalf("CanonicalMIME() = %q, %v, want %q", value, err, MIMEMarkdown)
	}
	value, err = CanonicalMIME("TEXT/PLAIN")
	if err != nil || value != MIMEPlain {
		t.Fatalf("CanonicalMIME() = %q, %v, want %q", value, err, MIMEPlain)
	}
	value, err = CanonicalMIME("")
	if err != nil || value != MIMEPlain {
		t.Fatalf("CanonicalMIME(unset) = %q, %v, want %q", value, err, MIMEPlain)
	}
}

func TestMIMEForFilenameSaysNothingRatherThanGuessing(t *testing.T) {
	if got := MIMEForFilename("README.md"); got != MIMEMarkdown {
		t.Fatalf("MIMEForFilename(README.md) = %q, want %q", got, MIMEMarkdown)
	}
	if got := MIMEForFilename("data.CSV"); got != MIMECSV {
		t.Fatalf("MIMEForFilename(data.CSV) = %q, want %q", got, MIMECSV)
	}
	if got := MIMEForFilename("photo.png"); got != "" {
		t.Fatalf("MIMEForFilename(photo.png) = %q, want an empty answer", got)
	}
	if got := MIMEForFilename("LICENSE"); got != "" {
		t.Fatalf("MIMEForFilename(LICENSE) = %q, want an empty answer", got)
	}
}
