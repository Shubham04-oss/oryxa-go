package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCheckRefusesWhatTheSchemaNames(t *testing.T) {
	const schema = `{
	  "type": "object",
	  "required": ["owner", "repo"],
	  "properties": {
	    "owner":    {"type": "string", "minLength": 1, "pattern": "^[a-z0-9-]+$"},
	    "repo":     {"type": "string", "maxLength": 100},
	    "state":    {"type": "string", "enum": ["open", "closed"]},
	    "per_page": {"type": "integer", "minimum": 1, "maximum": 100},
	    "since":    {"type": "number", "minimum": 0},
	    "labels":   {"type": "array", "items": {"type": "string"}, "uniqueItems": true, "maxItems": 2},
	    "draft":    {"type": "boolean"},
	    "body":     {"type": "string", "minLength": 1}
	  },
	  "additionalProperties": false
	}`

	cases := []struct {
		name    string
		value   string
		refuses string
	}{
		{"what the schema takes", `{"owner":"acme","repo":"api","state":"open","per_page":50}`, ""},
		{"a whole number where a number belongs", `{"owner":"acme","repo":"api","since":3}`, ""},
		{"a fraction where an integer belongs", `{"owner":"acme","repo":"api","per_page":1.5}`, "/per_page: must be a number"},
		{"a missing required field", `{"owner":"acme"}`, "/repo: is required"},
		{"an explicit null for a required field", `{"owner":"acme","repo":null}`, "/repo: is required"},
		{"the wrong type", `{"owner":7,"repo":"api"}`, "/owner: must be text"},
		{"a value outside an enum", `{"owner":"acme","repo":"api","state":"merged"}`, "/state: must be one of the values the schema lists"},
		{"text outside a pattern", `{"owner":"Acme Co","repo":"api"}`, "/owner: is not in the form this schema takes"},
		{"text too short", `{"owner":"","repo":"api"}`, "/owner: is shorter than 1 characters"},
		{"text too long", `{"owner":"acme","repo":"` + strings.Repeat("x", 101) + `"}`, "/repo: is longer than 100 characters"},
		{"a number below the minimum", `{"owner":"acme","repo":"api","per_page":0}`, "/per_page: is below the smallest this takes"},
		{"a number above the maximum", `{"owner":"acme","repo":"api","per_page":101}`, "/per_page: is above the largest this takes"},
		{"a repeated list item", `{"owner":"acme","repo":"api","labels":["bug","bug"]}`, "/labels: takes no item twice"},
		{"a list longer than it takes", `{"owner":"acme","repo":"api","labels":["a","b","c"]}`, "/labels: takes at most 2 items"},
		{"a list item of the wrong type", `{"owner":"acme","repo":"api","labels":[1]}`, "/labels/0: must be text"},
		{"a property the schema does not take", `{"owner":"acme","repo":"api","extra":1}`, "/extra: is not a property this schema takes"},
		{"the wrong shape entirely", `["acme"]`, "must be an object"},
		{"text where a number belongs", `{"owner":"acme","repo":"api","per_page":"50"}`, "/per_page: must be a number"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := Check(json.RawMessage(schema), json.RawMessage(test.value))
			if test.refuses == "" {
				if err != nil {
					t.Fatalf("Check(%s) = %v, want no complaint", test.value, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Check(%s) = nil, want %q", test.value, test.refuses)
			}
			if err.Error() != test.refuses {
				t.Fatalf("Check(%s) says %q, want %q", test.value, err, test.refuses)
			}
			if !errors.Is(err, ErrInvalidValue) {
				t.Fatalf("Check(%s) is not ErrInvalidValue", test.value)
			}
			var problem *SchemaError
			if !errors.As(err, &problem) {
				t.Fatalf("Check(%s) is not a *SchemaError", test.value)
			}
		})
	}
}

// A refusal never quotes the value: an argument is a member's own, and a log
// line outlives the call.
func TestCheckNeverQuotesAValue(t *testing.T) {
	const secret = "ghp_S3cr3tValueNobodyMayLog"
	const schema = `{"type":"object","properties":{"token":{"type":"string","minLength":64}}}`
	err := Check(json.RawMessage(schema), json.RawMessage(`{"token":"`+secret+`"}`))
	if err == nil {
		t.Fatal("a too-short token was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the complaint quotes the value: %v", err)
	}
	if err.Error() != "/token: is shorter than 64 characters" {
		t.Fatalf("Check says %q", err)
	}
}

// A schema this cannot judge is not a refusal: an app refuses invalid
// arguments itself, with a reason the caller can act on, and a guess here
// would be worse than silence.
func TestCheckAllowsWhatItCannotJudge(t *testing.T) {
	cases := map[string]string{
		"no schema at all":               ``,
		"a schema that is not JSON":      `"a string is not a schema"`,
		"a schema it cannot read":        `{"type": 7}`,
		"a keyword it does not model":    `{"type":"object","properties":{"token":{"$ref":"#/definitions/t"}}}`,
		"a format it only annotates":     `{"type":"object","properties":{"id":{"type":"string","format":"uuid"}}}`,
		"a pattern it cannot compile":    `{"type":"object","properties":{"id":{"pattern":"a)|(b"}}}`,
		"additionalProperties true":      `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":true}`,
		"a nested schema it cannot read": `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"enum":"nonsense"}}}}}`,
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Check(json.RawMessage(schema), json.RawMessage(`{"anything":true}`)); err != nil {
				t.Fatalf("Check = %v, want no complaint", err)
			}
		})
	}
}

func TestExampleFillsOnlyWhatIsRequired(t *testing.T) {
	const schema = `{
	  "type": "object",
	  "required": ["owner", "repo", "per_page", "labels"],
	  "properties": {
	    "owner":    {"type": "string"},
	    "repo":     {"type": "string"},
	    "per_page": {"type": "integer"},
	    "labels":   {"type": "array", "items": {"type": "string"}},
	    "optional": {"type": "string"},
	    "state":    {"type": "string", "enum": ["open", "closed"]}
	  }
	}`
	example := Example(json.RawMessage(schema))
	if err := Check(json.RawMessage(schema), example); err != nil {
		t.Fatalf("the example does not satisfy its own schema: %v (%s)", err, example)
	}
	var fields map[string]any
	if err := json.Unmarshal(example, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"owner", "repo", "per_page", "labels"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("the example left out %q: %s", name, example)
		}
	}
	if _, ok := fields["optional"]; ok {
		t.Fatalf("the example filled a property nothing requires: %s", example)
	}
}

func TestExamplePrefersAConstAndAnEnum(t *testing.T) {
	const schema = `{"type":"object","required":["state","kind"],
	  "properties":{"state":{"enum":["closed","open"]},"kind":{"const":"issue"}}}`
	example := string(Example(json.RawMessage(schema)))
	if example != `{"kind":"issue","state":"closed"}` {
		t.Fatalf("Example = %s", example)
	}
	if err := Check(json.RawMessage(schema), json.RawMessage(example)); err != nil {
		t.Fatalf("the example does not satisfy its own schema: %v", err)
	}
}

// CheckBytes answers for a body that was not sent at all, which is a null
// rather than an error about reading.
func TestCheckBytes(t *testing.T) {
	const schema = `{"type":"object","required":["owner"]}`
	if err := CheckBytes(json.RawMessage(schema), nil); err == nil {
		t.Fatal("an absent body was accepted where one is required")
	}
	if err := CheckBytes(json.RawMessage(schema), []byte("  ")); err == nil {
		t.Fatal("a blank body was accepted where one is required")
	}
	if err := CheckBytes(json.RawMessage(schema), []byte(`{"owner":"acme"}`)); err != nil {
		t.Fatalf("CheckBytes = %v, want no complaint", err)
	}
}

func TestJSONKind(t *testing.T) {
	cases := map[string]string{
		`null`: "null", `true`: "boolean", `"text"`: "string", `[]`: "array",
		`{}`: "object", `1`: "integer", `1.5`: "number", `not json`: "",
	}
	for value, want := range cases {
		if got := JSONKind(json.RawMessage(value)); got != want {
			t.Errorf("JSONKind(%s) = %q, want %q", value, got, want)
		}
	}
}
