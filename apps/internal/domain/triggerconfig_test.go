package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The schemas here are the shapes Composio's catalog actually publishes for
// trigger configs, across apps that differ in how they are written: a plain
// object of required strings, one with defaults and nothing required, enums,
// numbers, arrays, nullable fields, and the ones whose properties are
// described somewhere this deliberately does not follow.
func TestCheckTriggerConfigAcceptsWhatEachTriggerTakes(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct{ schema, config string }{
		"the required fields, and nothing else": {
			schema: `{"type":"object","properties":{"owner":{"type":"string","title":"Owner"},
				"repo":{"type":"string","title":"Repo"}},"required":["owner","repo"],"title":"CommitEventConfig"}`,
			config: `{"owner":"oryxa-labs","repo":"oryxa"}`,
		},
		"a field the trigger does not declare": {
			// Composio may take more than it publishes, and a config refused
			// here is a subscription a member cannot make at all.
			schema: `{"type":"object","properties":{"channel":{"type":"string"}},"required":["channel"]}`,
			config: `{"channel":"C123","include_bot_messages":true}`,
		},
		"nothing required and nothing given": {
			schema: `{"type":"object","properties":{"limit":{"type":"integer","default":10}}}`,
			config: `{}`,
		},
		"an integer written as a whole number": {
			schema: `{"type":"object","properties":{"interval":{"type":"integer"}},"required":["interval"]}`,
			config: `{"interval":15}`,
		},
		"a number where a number is asked for": {
			schema: `{"type":"object","properties":{"threshold":{"type":"number"}},"required":["threshold"]}`,
			config: `{"threshold":0.75}`,
		},
		"an enum's own value": {
			schema: `{"type":"object","properties":{"state":{"type":"string","enum":["open","closed","all"]}},"required":["state"]}`,
			config: `{"state":"closed"}`,
		},
		"an array of labels": {
			schema: `{"type":"object","properties":{"labels":{"type":"array","items":{"type":"string"}}}}`,
			config: `{"labels":["bug","p0"]}`,
		},
		"a nullable field given as null": {
			schema: `{"type":"object","properties":{"since":{"type":["string","null"]}},"required":["since"]}`,
			config: `{"since":null}`,
		},
		"a nullable field via OpenAPI's own keyword": {
			// OpenAPI (pre-3.1) writes nullable beside type rather than
			// folding "null" into it.
			schema: `{"type":"object","properties":{"since":{"type":"string","nullable":true}},"required":["since"]}`,
			config: `{"since":null}`,
		},
		"a nullable field via anyOf with a null branch": {
			// Pydantic v2 writes Optional[str] this way.
			schema: `{"type":"object","properties":{"since":{"anyOf":[{"type":"string"},{"type":"null"}]}},"required":["since"]}`,
			config: `{"since":null}`,
		},
		"a nullable field via oneOf with a null branch": {
			schema: `{"type":"object","properties":{"since":{"oneOf":[{"type":"string"},{"type":"null"}]}},"required":["since"]}`,
			config: `{"since":null}`,
		},
		"a field whose shape is a reference": {
			schema: `{"type":"object","properties":{"filter":{"$ref":"#/$defs/Filter"}},"required":["filter"]}`,
			config: `{"filter":{"any":"shape at all"}}`,
		},
		"a field written as one of several shapes": {
			schema: `{"type":"object","properties":{"target":{"anyOf":[{"type":"string"},{"type":"integer"}]}},"required":["target"]}`,
			config: `{"target":42}`,
		},
		"a nested object": {
			schema: `{"type":"object","properties":{"options":{"type":"object","properties":{"deep":{"type":"string"}}}}}`,
			config: `{"options":{"deep":"value","unmodelled":[1,2,3]}}`,
		},
		"a schema that declares nothing": {
			schema: `{"type":"object"}`,
			config: `{"whatever":"the app takes"}`,
		},
		"no schema at all": {
			schema: ``,
			config: `{"anything":true}`,
		},
		"a schema this cannot read": {
			// Composio published something unexpected. Its own check still
			// stands, and refusing here would block a valid subscription.
			schema: `not json`,
			config: `{"owner":"oryxa-labs"}`,
		},
		"an empty config against a schema requiring nothing": {
			schema: `{"type":"object","properties":{"repo":{"type":"string"}}}`,
			config: ``,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := CheckTriggerConfig(json.RawMessage(testCase.schema), json.RawMessage(testCase.config)); err != nil {
				t.Fatalf("refused a config the trigger takes: %v", err)
			}
		})
	}
}

func TestCheckTriggerConfigRefusesWhatWouldNeverDeliver(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		schema, config string
		// says is a phrase the member is told, so the refusal names the
		// field rather than only its kind.
		says string
	}{
		"a required field missing": {
			schema: `{"type":"object","properties":{"owner":{"type":"string"},"repo":{"type":"string"}},"required":["owner","repo"]}`,
			config: `{"repo":"oryxa"}`,
			says:   `"owner"`,
		},
		"every required field missing": {
			schema: `{"type":"object","properties":{"owner":{"type":"string"},"repo":{"type":"string"}},"required":["owner","repo"]}`,
			config: `{}`,
			says:   `"owner", "repo"`,
		},
		"a required field given as null": {
			schema: `{"type":"object","properties":{"channel":{"type":"string"}},"required":["channel"]}`,
			config: `{"channel":null}`,
			says:   `"channel"`,
		},
		"a required field given as null, anyOf without a null branch": {
			// Neither branch is {"type":"null"}, so allowsNull must not be
			// fooled by the mere presence of anyOf into accepting null.
			schema: `{"type":"object","properties":{"target":{"anyOf":[{"type":"string"},{"type":"integer"}]}},"required":["target"]}`,
			config: `{"target":null}`,
			says:   `"target"`,
		},
		"a string where a number belongs": {
			schema: `{"type":"object","properties":{"interval":{"type":"integer"}},"required":["interval"]}`,
			config: `{"interval":"fifteen"}`,
			says:   "string",
		},
		"a fraction where an integer belongs": {
			schema: `{"type":"object","properties":{"interval":{"type":"integer"}},"required":["interval"]}`,
			config: `{"interval":1.5}`,
			says:   "integer",
		},
		"an object where a string belongs": {
			schema: `{"type":"object","properties":{"repo":{"type":"string"}},"required":["repo"]}`,
			config: `{"repo":{"name":"oryxa"}}`,
			says:   "object",
		},
		"a value outside the enum": {
			schema: `{"type":"object","properties":{"state":{"type":"string","enum":["open","closed"]}},"required":["state"]}`,
			config: `{"state":"merged"}`,
			says:   `"state"`,
		},
		"a config that is not an object": {
			schema: `{"type":"object","properties":{"repo":{"type":"string"}},"required":["repo"]}`,
			config: `[1,2,3]`,
			says:   "JSON object",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := CheckTriggerConfig(json.RawMessage(testCase.schema), json.RawMessage(testCase.config))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want invalid", err)
			}
			if !strings.Contains(err.Error(), testCase.says) {
				t.Fatalf("the member is told %q, which does not name %s", err, testCase.says)
			}
		})
	}
}

func TestCheckTriggerConfigRefusesAConfigTooLargeToStore(t *testing.T) {
	t.Parallel()
	config := `{"repo":"` + strings.Repeat("x", MaxTriggerConfigBytes) + `"}`
	if err := CheckTriggerConfig(json.RawMessage(`{"type":"object"}`), json.RawMessage(config)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want invalid", err)
	}
}
