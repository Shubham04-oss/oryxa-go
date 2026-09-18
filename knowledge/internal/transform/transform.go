// Package transform turns what a provider gave back into text a retrieval
// pipeline can index: one known media type, normalized whitespace, valid
// UTF-8, and a hash over the result that is the document's version.
//
// Normalizing before hashing is what makes the hash mean something. A
// provider that re-serializes a document, changes its line endings or pads it
// differently has not changed the document, and must not look as though it
// did: an unchanged hash is how a run of thousands of documents publishes
// nothing.
package transform

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// The media types knowledge can read text out of. Anything else is refused
// rather than indexed as bytes.
const (
	MIMEPlain    = "text/plain"
	MIMEMarkdown = "text/markdown"
	MIMECSV      = "text/csv"
	MIMEJSON     = "application/json"
	// MIMEHTML is read, not stored: its text is taken out and kept as
	// MIMEPlain, since that is what the body then is.
	MIMEHTML  = "text/html"
	MIMEXHTML = "application/xhtml+xml"
)

// MaxBytes is the most text knowledge keeps for one document. Past it the
// body is cut at a rune boundary and marked truncated, so an enormous file
// still yields something to retrieve rather than nothing.
const MaxBytes = 4 << 20

// Text is a document's content, ready to index.
type Text struct {
	Body string
	MIME string
	// Hash is sha256 over Body, hex encoded. It is the document's version.
	Hash  string
	Bytes int
	// Truncated says the body was longer than MaxBytes and was cut.
	Truncated bool
}

// Normalize reads text out of what a provider sent. It refuses a media type
// it cannot read rather than indexing bytes as though they were words.
func Normalize(content []byte, declared string) (Text, error) {
	mediaType, err := CanonicalMIME(declared)
	if err != nil {
		return Text{}, err
	}
	// HTML is the one type whose body is not what arrived: its text is taken
	// out first, and what is stored is plain text, which is what it then is.
	// Hashing the text rather than the markup is also what keeps a page
	// re-serialized by its provider from looking changed.
	if mediaType == MIMEHTML {
		text, err := htmlText(content)
		if err != nil {
			return Text{}, err
		}
		content, mediaType = []byte(text), MIMEPlain
	}
	body, truncated := clean(string(content))
	sum := sha256.Sum256([]byte(body))
	return Text{
		Body:      body,
		MIME:      mediaType,
		Hash:      hex.EncodeToString(sum[:]),
		Bytes:     len(body),
		Truncated: truncated,
	}, nil
}

// CanonicalMIME reduces a Content-Type to the media type knowledge stores,
// dropping any parameters, or refuses it.
func CanonicalMIME(declared string) (string, error) {
	value := strings.TrimSpace(declared)
	if value == "" {
		return MIMEPlain, nil
	}
	if parsed, _, err := mime.ParseMediaType(value); err == nil {
		value = parsed
	} else {
		value = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	}
	switch value {
	case MIMEPlain, MIMEMarkdown, MIMECSV, MIMEJSON, MIMEHTML:
		return value, nil
	case "text/x-markdown", "text/md":
		return MIMEMarkdown, nil
	case MIMEXHTML:
		return MIMEHTML, nil
	default:
		return "", fmt.Errorf("%q holds no text knowledge can read: %w", value, domain.ErrUnsupportedType)
	}
}

// MIMEForFilename guesses a media type from a name, for a provider that
// stores a file without saying what it is. It answers an empty string when
// the name says nothing, so the caller can fall back to what it was told.
func MIMEForFilename(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".mdx":
		return MIMEMarkdown
	case ".csv":
		return MIMECSV
	case ".json":
		return MIMEJSON
	case ".html", ".htm", ".xhtml":
		return MIMEHTML
	case ".txt", ".text", ".log":
		return MIMEPlain
	default:
		return ""
	}
}

// bom is the byte order mark some providers prefix a file with. It is not
// part of the document, and a hash must not depend on it.
const bom = "\xef\xbb\xbf"

// clean makes one body out of whatever line endings, padding and byte
// sequences a provider used.
func clean(raw string) (string, bool) {
	raw = strings.TrimPrefix(raw, bom)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	if !utf8.ValidString(raw) {
		raw = strings.ToValidUTF8(raw, "�")
	}

	lines := strings.Split(raw, "\n")
	var builder strings.Builder
	builder.Grow(len(raw))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t\v\f\xc2\xa0")
		if line == "" {
			blank++
			// One blank line separates paragraphs; more is padding.
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	body := strings.TrimSpace(builder.String())

	if len(body) <= MaxBytes {
		return body, false
	}
	cut := MaxBytes
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	return strings.TrimSpace(body[:cut]), true
}
