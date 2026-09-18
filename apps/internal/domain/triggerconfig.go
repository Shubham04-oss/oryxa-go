package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// MaxTriggerConfigBytes bounds a subscription's config, which a member
// supplies and apps stores as-is.
const MaxTriggerConfigBytes = 32 << 10

// TriggerKind is how an event reaches Composio: the app pushes it, or
// Composio polls for it. A poll arrives on Composio's own interval rather
// than immediately, which is what a member is told to expect.
const (
	TriggerKindWebhook = "webhook"
	TriggerKindPoll    = "poll"
)

// TriggerDefinition is what Composio publishes about one event an app can
// deliver, as far as subscribing to it needs. It is the catalog's own
// answer, not anything apps decides.
type TriggerDefinition struct {
	Slug         string
	Toolkit      string
	Name         string
	Instructions string
	Kind         string
	// ConfigSchema is the JSON Schema of what a subscription takes. Empty
	// where Composio publishes none, in which case any object is accepted.
	ConfigSchema json.RawMessage
	// ManualSetup says the app itself needs a webhook URL registered by hand
	// before this trigger delivers anything. Subscribing is not enough.
	ManualSetup bool
}

// CheckTriggerConfig checks a subscription's config against the trigger's own
// schema.
//
// It refuses only what is certainly wrong: a required field that is not
// there, a field whose JSON kind contradicts the one declared, a value
// outside a declared enum. Composio's catalog runs to thousands of triggers
// whose schemas use keywords this does not model, and a valid subscription
// refused is worse than an invalid one passed on — Composio checks it again
// anyway. So anything it cannot judge, it allows.
func CheckTriggerConfig(schema, config json.RawMessage) error {
	fields, err := configObject(config)
	if err != nil {
		return err
	}
	parsed, ok := parseConfigSchema(schema)
	if !ok {
		return nil
	}

	var missing []string
	for _, name := range parsed.Required {
		value, present := fields[name]
		if !present || isNull(value) && !parsed.allowsNull(name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("%w: this trigger needs %s in its config, and without %s it would never deliver",
			ErrInvalid, quoteAll(missing), plural(len(missing), "it", "them"))
	}

	for name, property := range parsed.Properties {
		value, present := fields[name]
		if !present || isNull(value) {
			continue
		}
		if err := property.check(name, value); err != nil {
			return err
		}
	}
	return nil
}

// configObject reads a config as a JSON object, which is the one shape every
// trigger's config takes.
func configObject(config json.RawMessage) (map[string]json.RawMessage, error) {
	if len(config) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	if len(config) > MaxTriggerConfigBytes {
		return nil, fmt.Errorf("%w: a trigger's config is at most %d bytes", ErrInvalid, MaxTriggerConfigBytes)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(config, &fields); err != nil {
		return nil, fmt.Errorf("%w: a trigger's config is a JSON object", ErrInvalid)
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	return fields, nil
}

type configSchema struct {
	Properties map[string]propertySchema `json:"properties"`
	Required   []string                  `json:"required"`
}

// allowsNull says a required property may be explicitly null: its declared
// type carries "null", it is marked nullable (OpenAPI's own keyword for it,
// alongside a type rather than folded into it), or one of an anyOf/oneOf's
// branches is exactly {"type":"null"} (how Pydantic v2 writes Optional[X]).
// This is the only place anyOf and oneOf are read at all for a property's
// type - check below still leaves the rest of their shape unjudged, since
// the null branch is the one case answerable without modelling the keyword.
func (schema configSchema) allowsNull(name string) bool {
	property := schema.Properties[name]
	if slices.Contains(property.types(), "null") || property.Nullable {
		return true
	}
	return slices.ContainsFunc(property.AnyOf, isNullSchema) || slices.ContainsFunc(property.OneOf, isNullSchema)
}

// isNullSchema says a sub-schema is exactly {"type":"null"}, which is how a
// nullable union is spelled inside anyOf or oneOf.
func isNullSchema(raw json.RawMessage) bool {
	var branch struct {
		Type json.RawMessage `json:"type"`
	}
	if json.Unmarshal(raw, &branch) != nil {
		return false
	}
	var kind string
	return json.Unmarshal(branch.Type, &kind) == nil && kind == "null"
}

type propertySchema struct {
	Type json.RawMessage   `json:"type"`
	Enum []json.RawMessage `json:"enum"`
	// Nullable is OpenAPI's pre-3.1 way of saying a value may be null beside
	// its declared type, rather than folding "null" into the type itself.
	Nullable bool `json:"nullable"`
	// AnyOf, OneOf, AllOf and Ref each mean the property's shape is decided
	// somewhere this does not follow, so its type is not judged at all -
	// except that allowsNull above also looks inside AnyOf and OneOf for a
	// {"type":"null"} branch.
	AnyOf []json.RawMessage `json:"anyOf"`
	OneOf []json.RawMessage `json:"oneOf"`
	AllOf []json.RawMessage `json:"allOf"`
	Ref   string            `json:"$ref"`
}

// types reads a property's declared type, which JSON Schema writes as one
// name or a list of them. Nothing judgeable answers none.
func (property propertySchema) types() []string {
	if len(property.AnyOf) > 0 || len(property.OneOf) > 0 || len(property.AllOf) > 0 || property.Ref != "" {
		return nil
	}
	if len(property.Type) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(property.Type, &one) == nil {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(property.Type, &many) == nil {
		return many
	}
	return nil
}

func (property propertySchema) check(name string, value json.RawMessage) error {
	if declared := property.types(); len(declared) > 0 {
		actual := jsonKind(value)
		if actual == "" {
			return nil
		}
		if !slices.ContainsFunc(declared, func(want string) bool { return matches(want, actual, value) }) {
			return fmt.Errorf("%w: %q in this trigger's config is %s, and the trigger takes %s",
				ErrInvalid, name, actual, strings.Join(declared, " or "))
		}
	}
	if len(property.Enum) > 0 && !slices.ContainsFunc(property.Enum, func(allowed json.RawMessage) bool {
		return sameJSON(allowed, value)
	}) {
		return fmt.Errorf("%w: %q in this trigger's config is not one of the values it takes: %s",
			ErrInvalid, name, strings.Join(enumValues(property.Enum), ", "))
	}
	return nil
}

// parseConfigSchema reads the part of a trigger's schema this judges by.
// Anything that is not an object schema answers false, and is not judged.
func parseConfigSchema(schema json.RawMessage) (configSchema, bool) {
	if len(schema) == 0 {
		return configSchema{}, false
	}
	var parsed configSchema
	if err := json.Unmarshal(schema, &parsed); err != nil {
		// Composio published something this does not read. Its own check
		// still stands; refusing here would only block a valid subscription.
		return configSchema{}, false
	}
	if len(parsed.Properties) == 0 && len(parsed.Required) == 0 {
		return configSchema{}, false
	}
	return parsed, true
}

// jsonKind names a value's JSON type, as JSON Schema spells it. An
// unreadable value answers none, and is not judged.
func jsonKind(value json.RawMessage) string {
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return ""
	}
	switch decoded.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return ""
	}
}

// matches says whether a value of kind actual satisfies the declared type
// want. An integer is a number without a fraction, as JSON Schema defines it.
func matches(want, actual string, value json.RawMessage) bool {
	if want == actual {
		return true
	}
	if want != "integer" || actual != "number" {
		return false
	}
	var number float64
	return json.Unmarshal(value, &number) == nil && number == math.Trunc(number) && !math.IsInf(number, 0)
}

// sameJSON compares two JSON values by what they mean rather than how they
// are written, so 1 and 1.0 are one value and so are two orderings of an
// object.
func sameJSON(left, right json.RawMessage) bool {
	var first, second any
	if json.Unmarshal(left, &first) != nil || json.Unmarshal(right, &second) != nil {
		return false
	}
	canonical := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
	return canonical(first) == canonical(second)
}

func isNull(value json.RawMessage) bool {
	return string(value) == "null" || len(value) == 0
}

func enumValues(enum []json.RawMessage) []string {
	values := make([]string, 0, len(enum))
	for _, value := range enum {
		values = append(values, string(value))
	}
	return values
}

func quoteAll(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, strconv.Quote(name))
	}
	return strings.Join(quoted, ", ")
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}
