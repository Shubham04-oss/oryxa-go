package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// ErrInvalidValue is what every *SchemaError is, for errors.Is.
var ErrInvalidValue = errors.New("does not match its schema")

// SchemaError is one value a schema refuses, and where. It names the path
// and the rule, and never quotes the value: a value is a member's argument,
// or an app's payload, and neither belongs in a log or in an answer.
type SchemaError struct {
	// Path is where in the value, as a JSON Pointer: /owner. The empty path
	// is the value itself.
	Path string
	// Rule is what the schema requires there.
	Rule string
}

func (e *SchemaError) Error() string {
	if e.Path == "" {
		return e.Rule
	}
	return e.Path + ": " + e.Rule
}

func (e *SchemaError) Unwrap() error { return ErrInvalidValue }

// Check answers whether value is valid against schema.
//
// It judges only what it can be certain about, and allows everything else. A
// tool's arguments, a trigger's config and a trigger's payload are all
// checked here, and refusing a valid one is worse than passing an invalid one
// on: an app refuses invalid arguments itself, with a reason the caller can
// act on, while a refusal here is a guess about a schema neither side wrote.
// So a schema that cannot be read answers no complaint, and a keyword this
// does not model is not a failure.
//
// What it does judge: type, enum, const, required, properties,
// additionalProperties, items, minLength, maxLength, pattern, minimum,
// maximum, exclusiveMinimum, exclusiveMaximum, minItems, maxItems and
// uniqueItems. format annotates and is not judged.
func Check(schema, value json.RawMessage) error {
	parsed, err := parseSchema(schema)
	if err != nil {
		return nil
	}
	if problem := parsed.check("", value); problem != nil {
		return problem
	}
	return nil
}

// CheckBytes is Check over bytes that may be empty, where empty is the value
// that was not sent at all.
func CheckBytes(schema json.RawMessage, value []byte) error {
	if len(bytes.TrimSpace(value)) == 0 {
		value = json.RawMessage(`null`)
	}
	return Check(schema, value)
}

// schema is one node of a schema, as far as Check reads it.
type schema struct {
	// declared is the type as the schema names it, "" when it names none, and
	// several space-separated when it names a union.
	declared string
	enum     []any
	hasEnum  bool
	fixed    any
	hasFixed bool

	required   []string
	properties map[string]*schema
	// additional is the schema every property this one does not name is
	// checked against; nil with additionalSet means additionalProperties is
	// false, one with additionalTrue means it is true, and one without
	// additionalSet means the schema says nothing at all.
	additional     *schema
	additionalSet  bool
	additionalTrue bool
	items          *schema

	minLength, maxLength       *int
	minItems, maxItems         *int
	pattern                    *regexp.Regexp
	minimum, maximum           *float64
	exclusiveMin, exclusiveMax *float64
	uniqueItems                bool
}

var schemaCache sync.Map // a schema's bytes → *schema

// parseSchema reads a schema, answering an error when it cannot be read at
// all, so a schema nobody can parse stops being a check rather than becoming
// a refusal.
func parseSchema(raw json.RawMessage) (*schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("no schema")
	}
	if cached, ok := schemaCache.Load(string(raw)); ok {
		parsed, _ := cached.(*schema)
		if parsed == nil {
			return nil, errors.New("the schema is not JSON")
		}
		return parsed, nil
	}
	parsed, err := readSchema(raw)
	schemaCache.Store(string(raw), parsed)
	return parsed, err
}

func readSchema(raw json.RawMessage) (*schema, error) {
	var node struct {
		Type    json.RawMessage            `json:"type"`
		Enum    []any                      `json:"enum"`
		Const   json.RawMessage            `json:"const"`
		Require []string                   `json:"required"`
		Props   map[string]json.RawMessage `json:"properties"`
		// Additional is a bool or a schema, which is what the keyword has
		// always been allowed to be.
		Additional   json.RawMessage `json:"additionalProperties"`
		Items        json.RawMessage `json:"items"`
		MinLength    *int            `json:"minLength"`
		MaxLength    *int            `json:"maxLength"`
		MinItems     *int            `json:"minItems"`
		MaxItems     *int            `json:"maxItems"`
		Pattern      string          `json:"pattern"`
		Minimum      *float64        `json:"minimum"`
		Maximum      *float64        `json:"maximum"`
		ExclusiveMin *float64        `json:"exclusiveMinimum"`
		ExclusiveMax *float64        `json:"exclusiveMaximum"`
		UniqueItems  bool            `json:"uniqueItems"`
	}
	if json.Unmarshal(raw, &node) != nil {
		return nil, errors.New("the schema is not JSON")
	}
	parsed := &schema{
		declared: declaredType(node.Type), enum: node.Enum, hasEnum: node.Enum != nil,
		required: node.Require, minLength: node.MinLength, maxLength: node.MaxLength,
		minItems: node.MinItems, maxItems: node.MaxItems, minimum: node.Minimum, maximum: node.Maximum,
		exclusiveMin: node.ExclusiveMin, exclusiveMax: node.ExclusiveMax, uniqueItems: node.UniqueItems,
	}
	if len(node.Const) > 0 {
		var fixed any
		if json.Unmarshal(node.Const, &fixed) == nil {
			parsed.fixed, parsed.hasFixed = fixed, true
		}
	}
	if node.Pattern != "" {
		if expression, err := regexp.Compile(node.Pattern); err == nil {
			parsed.pattern = expression
		}
	}
	for name, property := range node.Props {
		child, err := parseSchema(property)
		if err != nil {
			continue
		}
		if parsed.properties == nil {
			parsed.properties = map[string]*schema{}
		}
		parsed.properties[name] = child
	}
	if len(node.Items) > 0 {
		parsed.items, _ = parseSchema(node.Items)
	}
	if len(node.Additional) > 0 {
		parsed.additionalSet = true
		switch trimmed := bytes.TrimSpace(node.Additional); {
		case bytes.Equal(trimmed, []byte("false")):
			// The one value of the keyword that refuses; check reads the
			// schema's absence beside the flag.
		case bytes.Equal(trimmed, []byte("true")):
			parsed.additionalTrue = true
		default:
			parsed.additional, _ = parseSchema(node.Additional)
		}
	}
	return parsed, nil
}

// declaredType reads a type as one name or a list of them.
func declaredType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return strings.Join(many, " ")
	}
	return ""
}

// check judges one value, at path.
func (s *schema) check(path string, value json.RawMessage) *SchemaError {
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return &SchemaError{Path: path, Rule: "must be JSON"}
	}
	kind := jsonKind(decoded)
	if s.declared != "" && !matchesType(s.declared, kind) {
		return &SchemaError{Path: path, Rule: "must be " + humanType(s.declared)}
	}
	if s.hasEnum && !slices.ContainsFunc(s.enum, func(allowed any) bool { return sameValue(allowed, decoded) }) {
		return &SchemaError{Path: path, Rule: "must be one of the values the schema lists"}
	}
	if s.hasFixed && !sameValue(s.fixed, decoded) {
		return &SchemaError{Path: path, Rule: "must be the one value the schema names"}
	}
	switch typed := decoded.(type) {
	case map[string]any:
		return s.checkObject(path, typed)
	case []any:
		return s.checkArray(path, typed)
	case string:
		return s.checkString(path, typed)
	case float64:
		return s.checkNumber(path, typed)
	}
	return nil
}

func (s *schema) checkObject(path string, value map[string]any) *SchemaError {
	for _, name := range s.required {
		if present, ok := value[name]; !ok || present == nil {
			return &SchemaError{Path: join(path, name), Rule: "is required"}
		}
	}
	names := make([]string, 0, len(value))
	for name := range value {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		child, declared := s.properties[name]
		switch {
		case declared:
		case !s.additionalSet || s.additionalTrue:
			// The schema says nothing about properties it does not name, or
			// says they are all allowed.
			continue
		case s.additional == nil:
			return &SchemaError{Path: join(path, name), Rule: "is not a property this schema takes"}
		default:
			child = s.additional
		}
		encoded, err := json.Marshal(value[name])
		if err != nil {
			continue
		}
		if problem := child.check(join(path, name), encoded); problem != nil {
			return problem
		}
	}
	return nil
}

func (s *schema) checkArray(path string, value []any) *SchemaError {
	switch {
	case s.minItems != nil && len(value) < *s.minItems:
		return &SchemaError{Path: path, Rule: "needs at least " + strconv.Itoa(*s.minItems) + " items"}
	case s.maxItems != nil && len(value) > *s.maxItems:
		return &SchemaError{Path: path, Rule: "takes at most " + strconv.Itoa(*s.maxItems) + " items"}
	}
	if s.uniqueItems {
		for i, member := range value {
			for _, other := range value[i+1:] {
				if sameValue(member, other) {
					return &SchemaError{Path: path, Rule: "takes no item twice"}
				}
			}
		}
	}
	if s.items != nil {
		for index, member := range value {
			encoded, err := json.Marshal(member)
			if err != nil {
				continue
			}
			if problem := s.items.check(join(path, strconv.Itoa(index)), encoded); problem != nil {
				return problem
			}
		}
	}
	return nil
}

func (s *schema) checkString(path, value string) *SchemaError {
	length := len([]rune(value))
	switch {
	case s.minLength != nil && length < *s.minLength:
		return &SchemaError{Path: path, Rule: "is shorter than " + strconv.Itoa(*s.minLength) + " characters"}
	case s.maxLength != nil && length > *s.maxLength:
		return &SchemaError{Path: path, Rule: "is longer than " + strconv.Itoa(*s.maxLength) + " characters"}
	case s.pattern != nil && !s.pattern.MatchString(value):
		// The pattern is the schema's, not a member's, but naming it here
		// would make every refusal read as a regular expression to learn.
		return &SchemaError{Path: path, Rule: "is not in the form this schema takes"}
	}
	return nil
}

func (s *schema) checkNumber(path string, value float64) *SchemaError {
	switch {
	case s.minimum != nil && value < *s.minimum:
		return &SchemaError{Path: path, Rule: "is below the smallest this takes"}
	case s.maximum != nil && value > *s.maximum:
		return &SchemaError{Path: path, Rule: "is above the largest this takes"}
	case s.exclusiveMin != nil && value <= *s.exclusiveMin:
		return &SchemaError{Path: path, Rule: "is not above the smallest this takes"}
	case s.exclusiveMax != nil && value >= *s.exclusiveMax:
		return &SchemaError{Path: path, Rule: "is not below the largest this takes"}
	}
	return nil
}

// join is a JSON Pointer's next token.
func join(path, token string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// JSONKind names a value's JSON type as a schema spells it: "integer" for a
// whole number, so a schema's "number" accepts one. It answers "" for a
// value that is not JSON.
func JSONKind(value json.RawMessage) string {
	var decoded any
	if json.Unmarshal(value, &decoded) != nil {
		return ""
	}
	return jsonKind(decoded)
}

func jsonKind(decoded any) string {
	switch value := decoded.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		if value == math.Trunc(value) && !math.IsInf(value, 0) {
			return "integer"
		}
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

// matchesType says whether a value of kind actual satisfies the declared
// type, which may name a union.
func matchesType(declared, actual string) bool {
	for _, want := range strings.Fields(declared) {
		if want == actual || want == "number" && actual == "integer" {
			return true
		}
	}
	return false
}

// humanType reads a declared type for whoever is refused, in words rather
// than as the schema spells it.
func humanType(declared string) string {
	names := strings.Fields(declared)
	switch len(names) {
	case 0:
		return "a value"
	case 1:
		switch names[0] {
		case "string":
			return "text"
		case "object":
			return "an object"
		case "array":
			return "a list"
		case "boolean":
			return "true or false"
		case "integer", "number":
			return "a number"
		}
		return names[0]
	}
	return strings.Join(names, " or ")
}

// sameValue compares two decoded JSON values by what they mean, so 1 and 1.0
// are one value and so are two orderings of an object.
func sameValue(left, right any) bool {
	leftEncoded, leftErr := json.Marshal(left)
	rightEncoded, rightErr := json.Marshal(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return bytes.Equal(leftEncoded, rightEncoded)
}

// Example builds a value a schema can be satisfied with, for the conformance
// harness to run a tool with when its cassette does not name its own
// arguments.
//
// It fills every required property with the smallest value its declared type
// allows and nothing else: a tool is run with what the schema says it needs,
// and no more.
func Example(raw json.RawMessage) json.RawMessage {
	parsed, err := parseSchema(raw)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	encoded, err := json.Marshal(parsed.example())
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func (s *schema) example() any {
	switch {
	case s.hasFixed:
		return s.fixed
	case s.hasEnum && len(s.enum) > 0:
		return s.enum[0]
	}
	if s.declared == "array" {
		if s.items != nil {
			return []any{s.items.example()}
		}
		return []any{}
	}
	if s.declared == "string" {
		return "x"
	}
	if s.declared == "integer" || s.declared == "number" {
		return float64(0)
	}
	if s.declared == "boolean" {
		return false
	}
	// An object, and the only shape every arguments document and every
	// payload is.
	object := map[string]any{}
	for _, name := range s.required {
		child, declared := s.properties[name]
		if !declared {
			child = s.additional
			if child == nil {
				continue
			}
		}
		object[name] = child.example()
	}
	return object
}

// CheckDescription is what a schema problem says in one line, for a log or
// a command's output. It is the error's own wording, which never quotes a
// value.
func CheckDescription(err error) string {
	var problem *SchemaError
	if errors.As(err, &problem) {
		return problem.Error()
	}
	return fmt.Sprint(err)
}
