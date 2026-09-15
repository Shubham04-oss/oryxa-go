package main

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode"
)

// normalize adjusts a slice so oapi-codegen generates a Go client from it that
// compiles, without changing what the client sends or accepts:
//
//   - a discriminator over inline schemas is dropped;
//   - a parameter whose schema is a oneOf or anyOf takes a single type that
//     still sends every value;
//   - a component whose Go type name would repeat an inline enum's is renamed.
func normalize(slice map[string]any) error {
	dropInapplicableDiscriminators(slice)
	if err := collapseParameterUnions(slice); err != nil {
		return err
	}
	return renameShadowedComponents(slice)
}

// dropInapplicableDiscriminators removes each discriminator whose oneOf or
// anyOf variants are not all references. OpenAPI does not consider inline
// schemas when discriminating, so such a discriminator selects nothing, and
// oapi-codegen refuses it.
func dropInapplicableDiscriminators(node any) {
	switch value := node.(type) {
	case map[string]any:
		if _, ok := value["discriminator"]; ok && !variantsAreReferences(value) {
			delete(value, "discriminator")
		}
		for _, child := range value {
			dropInapplicableDiscriminators(child)
		}
	case []any:
		for _, child := range value {
			dropInapplicableDiscriminators(child)
		}
	}
}

func variantsAreReferences(schema map[string]any) bool {
	for _, key := range []string{"oneOf", "anyOf"} {
		variants, _ := schema[key].([]any)
		for _, variant := range variants {
			object, _ := variant.(map[string]any)
			if _, ok := object["$ref"].(string); !ok {
				return false
			}
		}
	}
	return true
}

// collapseParameterUnions gives each parameter whose schema is a oneOf or anyOf
// a single type: its array variant if it has one, since a scalar is sent
// exactly as a one-item array is, and otherwise a string, since every
// parameter is sent as text. oapi-codegen generates union parameters that do
// not compile. A union with a variant that is neither a scalar nor an array,
// or with two arrays, is refused.
func collapseParameterUnions(slice map[string]any) error {
	for _, parameter := range parameters(slice) {
		schema, _ := parameter["schema"].(map[string]any)
		if schema["oneOf"] == nil && schema["anyOf"] == nil {
			continue
		}
		collapsed, err := collapseUnion(schema)
		if err != nil {
			name, _ := parameter["name"].(string)
			return fmt.Errorf("parameter %q: %w", name, err)
		}
		parameter["schema"] = collapsed
	}
	return nil
}

func collapseUnion(schema map[string]any) (map[string]any, error) {
	chosen := map[string]any{"type": "string"}
	arrays := 0
	for _, key := range []string{"oneOf", "anyOf"} {
		variants, _ := schema[key].([]any)
		for _, rawVariant := range variants {
			variant, _ := rawVariant.(map[string]any)
			switch variant["type"] {
			case "string", "integer", "number", "boolean":
			case "array":
				arrays++
				chosen = variant
			default:
				return nil, errors.New("its schema has a variant that is neither a scalar nor an array")
			}
		}
	}
	if arrays > 1 {
		return nil, errors.New("its schema has more than one array variant")
	}
	collapsed := maps.Clone(schema)
	delete(collapsed, "oneOf")
	delete(collapsed, "anyOf")
	maps.Copy(collapsed, chosen)
	return collapsed, nil
}

// parameters returns the parameter objects a slice defines: in its components,
// and inline on its paths and operations.
func parameters(slice map[string]any) []map[string]any {
	var lists []any
	components, _ := slice["components"].(map[string]any)
	defined, _ := components["parameters"].(map[string]any)
	for _, parameter := range defined {
		lists = append(lists, []any{parameter})
	}
	paths, _ := slice["paths"].(map[string]any)
	for _, rawItem := range paths {
		item, _ := rawItem.(map[string]any)
		lists = append(lists, item["parameters"])
		for _, method := range methods {
			operation, _ := item[method].(map[string]any)
			lists = append(lists, operation["parameters"])
		}
	}
	var found []map[string]any
	for _, list := range lists {
		entries, _ := list.([]any)
		for _, entry := range entries {
			if parameter, ok := entry.(map[string]any); ok {
				found = append(found, parameter)
			}
		}
	}
	return found
}

// sectionSuffixes are the component sections whose names become Go type names,
// each with the word oapi-codegen adds to a name that collides across them.
var sectionSuffixes = map[string]string{
	"schemas":       "schema",
	"parameters":    "parameter",
	"requestBodies": "request-body",
	"responses":     "response",
	"headers":       "header",
}

// renameShadowedComponents renames each component whose Go type name would be
// the one oapi-codegen gives an enum written inline in a schema. Parameter
// team-type and the enum of property type in schema team would both be
// TeamType, and oapi-codegen refuses the pair. The component takes its section
// as a suffix, becoming team-type-parameter, as oapi-codegen does for names
// that collide across sections, and every reference to it follows.
func renameShadowedComponents(slice map[string]any) error {
	components, _ := slice["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	enums := map[string]bool{}
	for name, schema := range schemas {
		inlineEnums(schema, name, true, enums)
	}
	renamed := map[string]string{}
	for section, suffix := range sectionSuffixes {
		defined, _ := components[section].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(defined)) {
			if !enums[typeKey(name)] {
				continue
			}
			newName := name + "-" + suffix
			if _, taken := defined[newName]; taken {
				return fmt.Errorf("components/%s/%s shares its Go type name with an inline enum, and %s is taken", section, name, newName)
			}
			defined[newName] = defined[name]
			delete(defined, name)
			renamed[componentReference(section, name)] = componentReference(section, newName)
		}
	}
	if len(renamed) > 0 {
		rewriteReferences(slice, renamed)
	}
	return nil
}

// inlineEnums adds the type key of each enum written inline in a schema.
// oapi-codegen names such an enum after the schema and the properties leading
// to it; array items and allOf members go by the name of the schema holding
// them, and a component's own enum goes by the component's name.
func inlineEnums(node any, name string, top bool, found map[string]bool) {
	schema, ok := node.(map[string]any)
	if !ok || schema["$ref"] != nil {
		return
	}
	if _, ok := schema["enum"]; ok && !top {
		found[typeKey(name)] = true
	}
	inlineEnums(schema["items"], name, top, found)
	allOf, _ := schema["allOf"].([]any)
	for _, member := range allOf {
		inlineEnums(member, name, top, found)
	}
	properties, _ := schema["properties"].(map[string]any)
	for property, child := range properties {
		inlineEnums(child, name+"-"+property, false, found)
	}
}

// typeKey is what oapi-codegen keeps of a name when it makes a Go type name:
// the letters and digits, whose case it may change. Names whose keys match may
// become the same type name.
func typeKey(name string) string {
	var key strings.Builder
	for _, character := range name {
		if unicode.IsUpper(character) || unicode.IsLower(character) || unicode.IsDigit(character) {
			key.WriteRune(unicode.ToLower(character))
		}
	}
	return key.String()
}

// rewriteReferences points each reference and discriminator mapping to a
// renamed component, or into one, at its new name. renamed maps each renamed
// component's old reference to its new one.
func rewriteReferences(node any, renamed map[string]string) {
	switch value := node.(type) {
	case map[string]any:
		if ref, ok := value["$ref"].(string); ok {
			value["$ref"] = renamedReference(ref, renamed)
		}
		mapping := discriminatorMapping(value)
		for key, target := range mapping {
			if target, ok := target.(string); ok {
				if ref := mappingReference(target); renamedReference(ref, renamed) != ref {
					mapping[key] = renamedReference(ref, renamed)
				}
			}
		}
		for _, child := range value {
			rewriteReferences(child, renamed)
		}
	case []any:
		for _, child := range value {
			rewriteReferences(child, renamed)
		}
	}
}

func renamedReference(ref string, renamed map[string]string) string {
	for old, updated := range renamed {
		if ref == old || strings.HasPrefix(ref, old+"/") {
			return updated + strings.TrimPrefix(ref, old)
		}
	}
	return ref
}
