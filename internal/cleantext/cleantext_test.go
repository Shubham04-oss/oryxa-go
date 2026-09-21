package cleantext

import (
	"encoding/json"
	"errors"
	"testing"
)

// The offending bytes are built here rather than written as escapes, so the
// source stays readable.
var (
	nul       = string(rune(0))
	badByte   = string([]byte{0xff})
	truncated = "abc" + string([]byte{0xe2, 0x82})
	overlong  = string([]byte{0xc0, 0xaf})
	// jsonNUL is how a JSON document spells a NUL inside a string.
	jsonNUL = `\u` + `0000`
)

type nested struct {
	Name     string
	Tags     []string
	Labels   map[string]string
	Child    *nested
	Any      any
	Content  []byte
	Count    int
	internal string
}

func TestValid(t *testing.T) {
	tests := map[string]struct {
		text string
		want bool
	}{
		"empty":             {"", true},
		"ascii":             {"Platform", true},
		"multibyte":         {"Équipe 平台 🙂", true},
		"the replacement":   {"a" + string(rune(0xFFFD)) + "b", true},
		"control but text":  {"tab\there\nnewline", true},
		"nul":               {"a" + nul + "b", false},
		"only nul":          {nul, false},
		"lone continuation": {"a" + string([]byte{0x80}) + "b", false},
		"invalid lead byte": {badByte, false},
		"truncated":         {truncated, false},
		"overlong":          {overlong, false},
	}
	for name, test := range tests {
		if got := Valid(test.text); got != test.want {
			t.Errorf("%s: Valid(%q) = %v, want %v", name, test.text, got, test.want)
		}
	}
}

func TestCheckFindsTextWhereverItIsHidden(t *testing.T) {
	bad := "a" + nul + "b"
	tests := map[string]any{
		"field":           nested{Name: bad},
		"slice element":   nested{Tags: []string{"ok", bad}},
		"map value":       nested{Labels: map[string]string{"k": bad}},
		"map key":         nested{Labels: map[string]string{bad: "v"}},
		"pointer":         nested{Child: &nested{Name: bad}},
		"deep pointer":    nested{Child: &nested{Child: &nested{Tags: []string{bad}}}},
		"interface":       nested{Any: bad},
		"in an interface": nested{Any: map[string]any{"list": []any{1, bad}}},
		"through a ptr":   &nested{Name: bad},
		"invalid utf-8":   nested{Name: badByte},
	}
	for name, value := range tests {
		if err := Check(value); !errors.Is(err, ErrUnclean) {
			t.Errorf("%s: Check() = %v, want ErrUnclean", name, err)
		}
	}
}

func TestCheckLeavesCleanValuesAndContentAlone(t *testing.T) {
	clean := nested{
		Name: "Platform", Tags: []string{"a", "b"}, Labels: map[string]string{"k": "v"},
		Child: &nested{Name: "Infra"}, Any: map[string]any{"n": 1, "s": "text"},
		// A file's bytes are content, not text: a NUL is ordinary in one.
		Content: []byte("binary" + nul + badByte),
		Count:   3, internal: "not a field a decoder fills",
	}
	if err := Check(clean); err != nil {
		t.Fatalf("Check() = %v, want nil", err)
	}
	for name, value := range map[string]any{"nil": nil, "nil pointer": (*nested)(nil), "number": 7, "empty struct": struct{}{}} {
		if err := Check(value); err != nil {
			t.Errorf("%s: Check() = %v, want nil", name, err)
		}
	}
}

// What a JSON decoder hands over is what Check is for: an escaped NUL becomes a
// real one, whereas invalid UTF-8 is repaired to U+FFFD by the decoder itself.
func TestCheckOnDecodedJSON(t *testing.T) {
	for body, wantErr := range map[string]bool{
		`{"name":"Platform"}`:                        false,
		`{"name":"a` + jsonNUL + `b"}`:               true,
		`{"name":"a\\` + `u0000b"}`:                  false, // an escaped backslash, then plain text
		`{"name":"a` + badByte + `b"}`:               false, // repaired to U+FFFD on the way in
		`{"tags":["ok","` + jsonNUL + `"]}`:          true,
		`{"labels":{"` + jsonNUL + `":"v"}}`:         true,
		`{"any":{"deep":[{"x":"` + jsonNUL + `"}]}}`: true,
	} {
		var decoded struct {
			Name   string
			Tags   []string
			Labels map[string]string
			Any    any
		}
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if got := Check(&decoded) != nil; got != wantErr {
			t.Errorf("%q: unclean = %v, want %v", body, got, wantErr)
		}
	}
}
