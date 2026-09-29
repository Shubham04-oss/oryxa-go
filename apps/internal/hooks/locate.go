package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// delivery is one request's headers and body, and the body's JSON once
// something has asked for it.
type delivery struct {
	header http.Header
	body   []byte

	parsed   bool
	document any
	isJSON   bool
}

func newDelivery(header http.Header, body []byte) *delivery {
	return &delivery{header: header, body: body}
}

// json decodes the body once. Numbers keep their literal text, so an id or a
// timestamp reads exactly as the app wrote it.
func (d *delivery) json() (any, bool) {
	if !d.parsed {
		d.parsed = true
		decoder := json.NewDecoder(bytes.NewReader(d.body))
		decoder.UseNumber()
		var document any
		if decoder.Decode(&document) == nil {
			if _, err := decoder.Token(); err == io.EOF {
				d.document, d.isJSON = document, true
			}
		}
	}
	return d.document, d.isJSON
}

// values reads everything a locator points at, as text: every value of a
// header, or everything a JSON pointer reaches. A "*" in the pointer reaches
// every element of an array, and every element must hold the rest of the
// pointer. It answers false when the locator finds nothing, or finds
// something that is neither a string nor a number. A "*" over an empty array
// answers true and no values, which no caller accepts as a match.
func (d *delivery) values(locator integration.Locator) ([]string, bool) {
	switch {
	case locator.Header != "":
		values := d.header.Values(locator.Header)
		return values, len(values) > 0
	case locator.JSON != "":
		document, ok := d.json()
		if !ok {
			return nil, false
		}
		found, ok := resolve(document, locator.JSON)
		if !ok {
			return nil, false
		}
		values := make([]string, 0, len(found))
		for _, value := range found {
			text, ok := scalar(value)
			if !ok {
				return nil, false
			}
			values = append(values, text)
		}
		return values, true
	}
	return nil, false
}

// one reads a locator that must find exactly one value, and takes its prefix
// off. A value without the prefix is not the value.
func (d *delivery) one(locator integration.Locator) (string, bool) {
	values, ok := d.values(locator)
	if !ok || len(values) != 1 {
		return "", false
	}
	value, ok := strings.CutPrefix(values[0], locator.Prefix)
	return value, ok && value != ""
}

// bodyHash is the delivery's id for an app that gives none.
func (d *delivery) bodyHash() string {
	sum := sha256.Sum256(d.body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// resolve follows an RFC 6901 pointer through a decoded document. A "*"
// token stands for every element of an array.
func resolve(document any, pointer string) ([]any, bool) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := []any{document}
	for _, raw := range strings.Split(pointer[1:], "/") {
		wildcard := raw == "*"
		token := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		next := make([]any, 0, len(current))
		for _, value := range current {
			switch value := value.(type) {
			case map[string]any:
				child, ok := value[token]
				if wildcard || !ok {
					return nil, false
				}
				next = append(next, child)
			case []any:
				if wildcard {
					next = append(next, value...)
					continue
				}
				index, ok := arrayIndex(token, len(value))
				if !ok {
					return nil, false
				}
				next = append(next, value[index])
			default:
				return nil, false
			}
		}
		current = next
	}
	return current, true
}

// arrayIndex reads an RFC 6901 array index: digits without a leading zero,
// inside the array.
func arrayIndex(token string, length int) (int, bool) {
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return 0, false
	}
	index := 0
	for i := 0; i < len(token); i++ {
		if token[i] < '0' || token[i] > '9' {
			return 0, false
		}
		index = index*10 + int(token[i]-'0')
		if index >= length {
			return 0, false
		}
	}
	return index, true
}

// scalar is a JSON value as text, when it is a string or a number.
func scalar(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case json.Number:
		return value.String(), true
	}
	return "", false
}

// maxKeyPart is the longest id a dedupe key carries as it is.
const maxKeyPart = 200

// keyPart makes an app's id safe to keep in a dedupe key: as it is when it is
// short, printable text, and otherwise as its hash, since an id is the app's
// to choose and a key has to fit an index.
func keyPart(id string) string {
	if len(id) <= maxKeyPart && utf8.ValidString(id) && !strings.ContainsFunc(id, unicode.IsControl) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "sha256:" + hex.EncodeToString(sum[:])
}
