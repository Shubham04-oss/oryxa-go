package eventauth

import "testing"

// TestCanonicalJSONUnifiesEquivalentSpellings walks every pair of byte
// spellings that decode to the same JSON value — key order, string escapes,
// number spellings — and asserts the canonical form treats them as one.
// The bus re-encodes on the way through; the signature must not care.
func TestCanonicalJSONUnifiesEquivalentSpellings(t *testing.T) {
	pairs := []struct{ a, b string }{
		{"{\"a\":\"x<b\"}", "{\"a\":\"x\\u003cb\"}"},
		{"{\"a\":1.5}", "{\"a\":1.5e0}"},
		{"{\"a\":1.0}", "{\"a\":1}"},
		{"{\"a\":1}", "{\"a\":1e0}"},
		{"{\"a\":2.50}", "{\"a\":2.5}"},
		{"{\"a\":1,\"b\":2}", "{\"b\":2,\"a\":1}"},
		{"{\"a\":{\"x\":1,\"y\":2}}", "{\"a\":{\"y\":2,\"x\":1}}"},
		{"{\"a\":[1,2,3]}", "[1, 2, 3]"}, // not equal; array stays an array
	}
	// The last pair is deliberately not equal; the loop treats it specially.
	for index, pair := range pairs {
		same := string(canonicalJSON([]byte(pair.a))) == string(canonicalJSON([]byte(pair.b)))
		want := index != 7
		if same != want {
			t.Fatalf("pair %d: %s vs %s unified=%v, want %v", index, pair.a, pair.b, same, want)
		}
	}
}
