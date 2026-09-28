package secrets

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

// redacted is what a Secret shows in place of its value.
const redacted = "[redacted]"

// Secret is a plaintext credential, such as a token, an API key, a password or
// a client secret. fmt (whatever the verb), slog, JSON and text encoding all
// show "[redacted]". Reveal answers the value, so each use of it is visible.
// Decoding JSON into a Secret reads the value like any other string.
//
// Two gaps remain, because Secret is a string. fmt cannot call methods on a
// value in an unexported struct field, and prints it raw, so keep a Secret in
// an exported field of anything that might be printed. fmt also checks %w and
// %p before any method, and prints the raw value in its complaint. go vet
// flags both of those verbs with a Secret.
type Secret string

// Reveal answers the plaintext. Call it only where the value is used, for
// example to seal it or to place it on a request.
func (s Secret) Reveal() string { return string(s) }

// String redacts %v and %s, and any message a Secret is formatted into.
func (Secret) String() string { return redacted }

// GoString redacts %#v.
func (Secret) GoString() string { return redacted }

// Format redacts every other verb. fmt checks for a Formatter first. Without
// one, a verb that does not suit a string, such as %d, prints the raw value
// in its complaint.
func (s Secret) Format(state fmt.State, verb rune) {
	if verb == 'v' && state.Flag('#') {
		_, _ = io.WriteString(state, s.GoString())
		return
	}
	_, _ = fmt.Fprintf(state, fmt.FormatString(state, verb), redacted)
}

// LogValue redacts a Secret logged with slog.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON redacts a Secret in JSON, such as an API answer or a JSON log
// line of a struct holding one.
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// MarshalText redacts a Secret for encoders that ask for text rather than
// JSON, such as encoding/xml.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }
