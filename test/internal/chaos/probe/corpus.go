// Package probe throws hostile and malformed payloads at a running rig and
// judges what comes back.
package probe

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Fragment is a raw JSON value dropped in place of one field of an otherwise
// valid body. It is raw bytes, not a Go value, because json.Marshal would
// quietly repair the very things worth sending: invalid UTF-8, lone
// surrogates, numbers past int64, and depth past what a decoder allows.
type Fragment struct {
	Name string
	JSON []byte
	// Class groups fragments for reporting: "text", "control", "size",
	// "number", "type", "structure", "injection".
	Class string
}

// esc renders runes as JSON \\uXXXX escapes, with surrogate pairs above the
// BMP. Fragments are built with it, not written with literal control or
// invisible characters, so the source stays readable and compiles.
func esc(runes ...rune) string {
	var out strings.Builder
	for _, r := range runes {
		if r > 0xFFFF {
			high, low := utf16.EncodeRune(r)
			fmt.Fprintf(&out, "\\u%04x\\u%04x", high, low)
			continue
		}
		fmt.Fprintf(&out, "\\u%04x", r)
	}
	return out.String()
}

func frag(class, name, raw string) Fragment {
	return Fragment{Name: name, JSON: []byte(raw), Class: class}
}

func repeat(unit string, n int) string { return strings.Repeat(unit, n) }

// Fragments is every value tried in place of a field.
func Fragments() []Fragment {
	deepArray := func(depth int) string { return repeat("[", depth) + repeat("]", depth) }
	deepObject := func(depth int) string { return repeat(`{"a":`, depth) + "1" + repeat("}", depth) }
	bigArray := "[" + repeat("0,", 100_000) + "0]"

	return []Fragment{
		// Text that is empty or almost.
		frag("text", "empty", `""`),
		frag("text", "space", `" "`),
		frag("text", "tab-newline", `"\t\n"`),
		frag("text", "leading-trailing-space", `"  padded  "`),

		// Control and encoding oddities.
		frag("control", "nul-middle", `"a`+esc(0)+`b"`),
		frag("control", "nul-only", `"`+esc(0)+`"`),
		frag("control", "bad-utf8", "\"\xff\xfe\xfd\""),
		frag("control", "truncated-utf8", "\"abc\xe2\x82\""),
		frag("control", "overlong-utf8", "\"\xc0\xaf\""),
		frag("control", "lone-surrogate", `"\ud800"`),
		frag("control", "bom", `"`+esc(0xfeff)+`abc"`),
		frag("control", "rtl-override", `"`+esc(0x202e)+`evil.txt"`),
		frag("control", "zero-width", `"a`+esc(0x200b)+`b`+esc(0x200d)+`c"`),
		frag("control", "combining", `"e`+esc(0x301, 0x301, 0x301, 0x301, 0x301, 0x301, 0x301, 0x301)+`"`),
		frag("control", "homoglyph", `"`+esc(0x430)+`dmin"`),
		frag("control", "emoji-zwj", `"`+esc(0x1f468, 0x200d, 0x1f469, 0x200d, 0x1f467, 0x200d, 0x1f466)+`"`),
		frag("control", "crlf", `"x\r\nX-Injected: yes\r\n\r\nbody"`),
		frag("control", "esc-chars", `"`+esc(0x1b)+`[31mred`+esc(0x1b)+`[0m`+esc(7, 8, 0xc)+`"`),
		frag("control", "line-separators", `"a`+esc(0x2028)+`b`+esc(0x2029)+`c`+esc(0x85)+`d"`),

		// Injection strings for every downstream that builds a query, a path,
		// a template, a log line or a URL.
		frag("injection", "sql-quote", `"' OR '1'='1' --"`),
		frag("injection", "sql-drop", `"\"; DROP TABLE teams; --"`),
		frag("injection", "sql-sleep", `"1; SELECT pg_sleep(4)--"`),
		frag("injection", "sql-backslash", `"\\"`),
		frag("injection", "like-percent", `"%"`),
		frag("injection", "like-underscore", `"____"`),
		frag("injection", "ltree", `"a.b.c"`),
		frag("injection", "ltree-star", `"*{1,3}.a|b"`),
		frag("injection", "ltree-dots", `"a..b"`),
		frag("injection", "tsquery", `"a & | ! ( ) <-> :*"`),
		frag("injection", "tsquery-unbalanced", `"(((a &"`),
		frag("injection", "regex", `"(a+)+$"`),
		frag("injection", "html", `"<img src=x onerror=alert(1)>"`),
		frag("injection", "script", `"</script><script>alert(1)</script>"`),
		frag("injection", "template", `"{{7*7}} ${7*7} <%= 7*7 %> #{7*7}"`),
		frag("injection", "jndi", `"${jndi:ldap://127.0.0.1:1/a}"`),
		frag("injection", "format", `"%s%s%s%s%n%x%d"`),
		frag("injection", "path-traversal", `"../../../etc/passwd"`),
		frag("injection", "path-windows", `"..\\..\\windows\\system32"`),
		frag("injection", "file-url", `"file:///etc/passwd"`),
		frag("injection", "ssrf-metadata", `"http://169.254.169.254/latest/meta-data/"`),
		frag("injection", "ssrf-stub", `"http://127.0.0.1:18091/_stub/stats"`),
		frag("injection", "ssrf-gopher", `"gopher://127.0.0.1:6379/_INFO"`),
		frag("injection", "json-in-string", `"\",\"admin\":true,\"x\":\""`),
		frag("injection", "id-null-word", `"null"`),
		frag("injection", "id-undefined", `"undefined"`),
		frag("injection", "id-zero-uuid", `"00000000-0000-0000-0000-000000000000"`),
		frag("injection", "id-star", `"*"`),
		frag("injection", "id-dash", `"-"`),
		frag("injection", "id-team-prefix", `"team_"`),
		frag("injection", "email-injection", `"a@b.test\r\nBcc: victim@x.test"`),

		// Size.
		frag("size", "len-300", `"`+repeat("A", 300)+`"`),
		frag("size", "len-5k", `"`+repeat("A", 5_000)+`"`),
		frag("size", "len-70k", `"`+repeat("A", 70_000)+`"`),
		frag("size", "len-300k-multibyte", `"`+repeat("é", 150_000)+`"`),
		frag("size", "len-1mib+", `"`+repeat("A", (1<<20)+64)+`"`),

		// Numbers, alone.
		frag("number", "zero", `0`),
		frag("number", "minus-one", `-1`),
		frag("number", "one", `1`),
		frag("number", "byte-max", `256`),
		frag("number", "int32-max", `2147483647`),
		frag("number", "int32-overflow", `2147483648`),
		frag("number", "int32-underflow", `-2147483649`),
		frag("number", "uint32-overflow", `4294967296`),
		frag("number", "js-unsafe", `9007199254740993`),
		frag("number", "int64-max", `9223372036854775807`),
		frag("number", "int64-overflow", `9223372036854775808`),
		frag("number", "uint64-overflow", `18446744073709551616`),
		frag("number", "float", `1.5`),
		frag("number", "float-small", `0.0000000000000000000001`),
		frag("number", "float-huge", `1e308`),
		frag("number", "float-inf", `1e999`),
		frag("number", "float-neg-inf", `-1e999`),
		frag("number", "float-denormal", `5e-324`),
		frag("number", "neg-zero", `-0`),
		frag("number", "many-digits", `123456789012345678901234567890`),

		// Wrong type for the field.
		frag("type", "true", `true`),
		frag("type", "false", `false`),
		frag("type", "null", `null`),
		frag("type", "empty-array", `[]`),
		frag("type", "array-of-empty", `[""]`),
		frag("type", "array-of-null", `[null]`),
		frag("type", "array-of-numbers", `[1,2,3]`),
		frag("type", "empty-object", `{}`),
		frag("type", "object", `{"a":1}`),
		frag("type", "object-empty-key", `{"":""}`),
		frag("type", "nested-string-json", `"{\"a\":1}"`),

		// Structure inside the field.
		frag("structure", "array-depth-500", deepArray(500)),
		frag("structure", "array-depth-20000", deepArray(20_000)),
		frag("structure", "object-depth-500", deepObject(500)),
		frag("structure", "object-depth-20000", deepObject(20_000)),
		frag("structure", "array-100k", bigArray),
	}
}

// PathValues are strings dropped into a URL path segment, already in the form
// they travel: some are escaped, some deliberately not.
func PathValues() []Fragment {
	return []Fragment{
		frag("path", "empty", ""),
		frag("path", "dot", "."),
		frag("path", "dotdot", ".."),
		frag("path", "dotdot-slash", "..%2f..%2f..%2fetc%2fpasswd"),
		frag("path", "double-encoded-dotdot", "%252e%252e%252f"),
		frag("path", "encoded-slash", "a%2fb"),
		frag("path", "backslash", "a%5cb"),
		frag("path", "nul", "a%00b"),
		frag("path", "newline", "a%0d%0aX-Injected:1"),
		frag("path", "percent", "%25"),
		frag("path", "percent-only", "%"),
		frag("path", "bad-escape", "%zz"),
		frag("path", "unicode", "%E2%80%AEevil"),
		frag("path", "bad-utf8", "%ff%fe"),
		frag("path", "emoji", "%F0%9F%99%82"),
		frag("path", "space", "a%20b"),
		frag("path", "plus", "a+b"),
		frag("path", "semicolon", "a;b=c"),
		frag("path", "question", "a%3fb"),
		frag("path", "hash", "a%23b"),
		frag("path", "star", "*"),
		frag("path", "ltree", "a.b.c"),
		frag("path", "sql", "%27%20OR%20%271%27%3D%271"),
		frag("path", "null-word", "null"),
		frag("path", "zero-uuid", "00000000-0000-0000-0000-000000000000"),
		frag("path", "long-2k", repeat("a", 2_000)),
		frag("path", "long-9k", repeat("a", 9_000)),
		frag("path", "long-70k", repeat("a", 70_000)),
		frag("path", "team-like", "team_AAAAAAAAAAAAAAAA"),
		frag("path", "org-like", "org_AAAAAAAAAAAAAAAA"),
		frag("path", "curly", "%7BorganizationID%7D"),
		frag("path", "crlf-body", "a%0d%0a%0d%0a<html>"),
	}
}

// QueryValues are strings dropped into one query parameter, unescaped: the
// sender escapes them, except where a name says otherwise.
func QueryValues() []Fragment {
	return []Fragment{
		frag("query", "empty", ""),
		frag("query", "zero", "0"),
		frag("query", "one", "1"),
		frag("query", "minus-one", "-1"),
		frag("query", "int32-overflow", "2147483648"),
		frag("query", "int64-max", "9223372036854775807"),
		frag("query", "int64-overflow", "9223372036854775808"),
		frag("query", "huge", "99999999999999999999999999"),
		frag("query", "float", "1.5"),
		frag("query", "exponent", "1e3"),
		frag("query", "hex", "0x10"),
		frag("query", "plus-sign", "+5"),
		frag("query", "space-number", " 5 "),
		frag("query", "arabic-digits", "١٢٣"),
		frag("query", "nan", "NaN"),
		frag("query", "inf", "Infinity"),
		frag("query", "true", "true"),
		frag("query", "null", "null"),
		frag("query", "nul", "a\x00b"),
		frag("query", "bad-utf8", "\xff\xfe"),
		frag("query", "percent", "%"),
		frag("query", "like", "%_%"),
		frag("query", "backslash", "\\"),
		frag("query", "quote", "'\""),
		frag("query", "ltree", "a.b.*"),
		frag("query", "tsquery", "a & | ! ( <-> :*"),
		frag("query", "tsquery-unbalanced", "((a &"),
		frag("query", "regex", "(a+)+$"),
		frag("query", "sql-sleep", "1; SELECT pg_sleep(4)--"),
		frag("query", "unicode-rtl", string(rune(0x202e))+"evil"),
		frag("query", "emoji", "🙂🙂🙂"),
		frag("query", "crlf", "a\r\nX-Injected: 1"),
		frag("query", "array-brackets", "[1,2,3]"),
		frag("query", "comma-list", "a,b,c,d"),
		frag("query", "long-1k", repeat("q", 1_000)),
		frag("query", "long-60k", repeat("q", 60_000)),
		frag("query", "path-traversal", "../../etc/passwd"),
	}
}

// NamedBody is a whole body, malformed in one way.
type NamedBody struct {
	Name        string
	Body        []byte
	ContentType string // "" keeps application/json; "-" sends none
	Encoding    string // Content-Encoding
}

// BodyMutations breaks a valid JSON body in ways that have nothing to do with
// any one field: its framing, its type, its size, its encoding.
func BodyMutations(valid []byte) []NamedBody {
	half := valid[:len(valid)/2]
	pad := func(n int) []byte {
		return []byte(`{"pad":"` + repeat("A", n) + `"}`)
	}
	bom := append([]byte{0xEF, 0xBB, 0xBF}, valid...)
	utf16 := []byte{0xFF, 0xFE}
	for _, r := range string(valid) {
		utf16 = append(utf16, byte(r), 0)
	}
	return []NamedBody{
		{Name: "empty", Body: nil},
		{Name: "null", Body: []byte(`null`)},
		{Name: "array", Body: []byte(`[]`)},
		{Name: "array-of-object", Body: append(append([]byte("["), valid...), ']')},
		{Name: "string", Body: []byte(`"text"`)},
		{Name: "number", Body: []byte(`1`)},
		{Name: "true", Body: []byte(`true`)},
		{Name: "empty-object", Body: []byte(`{}`)},
		{Name: "whitespace-only", Body: []byte("  \n\t ")},
		{Name: "truncated-half", Body: half},
		{Name: "truncated-one-byte-short", Body: valid[:len(valid)-1]},
		{Name: "two-documents", Body: append(append(append([]byte{}, valid...), '\n'), valid...)},
		{Name: "trailing-garbage", Body: append(append([]byte{}, valid...), []byte("garbage")...)},
		{Name: "trailing-nul", Body: append(append([]byte{}, valid...), 0)},
		{Name: "trailing-comma-object", Body: bytes.Replace(valid, []byte("}"), []byte(",}"), 1)},
		{Name: "leading-bom", Body: bom},
		{Name: "leading-whitespace", Body: append([]byte("   \n\t"), valid...)},
		{Name: "utf16-bom", Body: utf16},
		{Name: "unknown-field", Body: bytes.Replace(valid, []byte("{"), []byte(`{"zz_unknown":1,`), 1)},
		{Name: "duplicate-first-key", Body: duplicateFirstKey(valid)},
		{Name: "upper-case-keys", Body: upperKeys(valid)},
		{Name: "proto-pollution", Body: bytes.Replace(valid, []byte("{"), []byte(`{"__proto__":{"admin":true},"constructor":{"prototype":{"admin":true}},`), 1)},
		{Name: "pad-100k", Body: pad(100_000)},
		{Name: "pad-1mib+", Body: pad((1 << 20) + 128)},
		{Name: "pad-9mib+", Body: pad((9 << 20) + 128)},
		{Name: "deep-array-20000", Body: []byte(repeat("[", 20_000) + repeat("]", 20_000))},
		{Name: "deep-object-20000", Body: []byte(repeat(`{"a":`, 20_000) + "1" + repeat("}", 20_000))},
		{Name: "wide-object-50k-keys", Body: wideObject(50_000)},
		{Name: "type-text-plain", Body: valid, ContentType: "text/plain"},
		{Name: "type-form", Body: valid, ContentType: "application/x-www-form-urlencoded"},
		{Name: "type-multipart-no-boundary", Body: valid, ContentType: "multipart/form-data"},
		{Name: "type-json-utf7", Body: valid, ContentType: "application/json; charset=utf-7"},
		{Name: "type-xml", Body: []byte(`<?xml version="1.0"?><!DOCTYPE a [<!ENTITY x SYSTEM "file:///etc/passwd">]><a>&x;</a>`), ContentType: "application/xml"},
		{Name: "type-none", Body: valid, ContentType: "-"},
		{Name: "type-garbage", Body: valid, ContentType: "\x00;;;=="},
		{Name: "encoding-gzip-not-gzipped", Body: valid, Encoding: "gzip"},
		{Name: "encoding-deflate-not", Body: valid, Encoding: "deflate"},
		{Name: "encoding-unknown", Body: valid, Encoding: "br, zstd, x-nothing"},
	}
}

func duplicateFirstKey(valid []byte) []byte {
	text := string(valid)
	start := strings.Index(text, `"`)
	if start < 0 {
		return valid
	}
	end := strings.Index(text[start+1:], `"`)
	if end < 0 {
		return valid
	}
	key := text[start : start+end+2]
	return []byte(fmt.Sprintf("{%s:null,%s", key, text[1:]))
}

func upperKeys(valid []byte) []byte {
	out := append([]byte{}, valid...)
	inKey, prev := false, byte(0)
	for i, c := range out {
		if c == '"' && prev != '\\' {
			inKey = !inKey
		} else if inKey && c >= 'a' && c <= 'z' && i > 0 && (out[i-1] == '"' || out[i-1] == '_') {
			out[i] = c - 32
		}
		prev = c
	}
	return out
}

func wideObject(keys int) []byte {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for i := 0; i < keys; i++ {
		if i > 0 {
			buffer.WriteByte(',')
		}
		fmt.Fprintf(&buffer, `"k%d":%d`, i, i)
	}
	buffer.WriteByte('}')
	return buffer.Bytes()
}
