package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

const componentsRef = "#/components/"

// methods are the path item fields that hold operations.
var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// Slice returns a description holding only the operations tagged tag, the
// path-level fields of their paths, the tag's definition, the security schemes
// the operations use, and every component they reference, directly or through
// other components, adjusted by normalize. The slice shares nothing with
// document, which is not changed.
func Slice(document map[string]any, tag string) (map[string]any, error) {
	version, _ := document["openapi"].(string)
	if !strings.HasPrefix(version, "3.") {
		return nil, fmt.Errorf("only OpenAPI 3 descriptions are supported, not %q", version)
	}

	paths, _ := document["paths"].(map[string]any)
	keptPaths := map[string]any{}
	var keptOperations []map[string]any
	named := map[string]string{}
	for path, rawItem := range paths {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		kept := map[string]any{}
		for _, method := range methods {
			operation, ok := item[method].(map[string]any)
			if !ok || !hasTag(operation, tag) {
				continue
			}
			operation = maps.Clone(operation)
			if id, ok := operation["operationId"].(string); ok {
				renamed := operationID(id, tag)
				if other, taken := named[renamed]; taken {
					return nil, fmt.Errorf("tag %q: operations %q and %q would both be named %q", tag, other, id, renamed)
				}
				named[renamed] = id
				if renamed != id {
					operation["operationId"] = renamed
					operation["x-original-operation-id"] = id
				}
			}
			kept[method] = operation
			keptOperations = append(keptOperations, operation)
		}
		if len(kept) == 0 {
			continue
		}
		for key, value := range item {
			if !slices.Contains(methods, key) {
				kept[key] = value
			}
		}
		keptPaths[path] = kept
	}
	if len(keptOperations) == 0 {
		return nil, fmt.Errorf("tag %q has no operations", tag)
	}

	slice := map[string]any{"openapi": document["openapi"], "info": document["info"], "paths": keptPaths}
	for _, key := range []string{"servers", "security", "externalDocs"} {
		if value, ok := document[key]; ok {
			slice[key] = value
		}
	}
	definitions, _ := document["tags"].([]any)
	for _, definition := range definitions {
		if object, _ := definition.(map[string]any); object["name"] == tag {
			slice["tags"] = []any{definition}
		}
	}
	components, err := referencedComponents(document, slice, keptOperations)
	if err != nil {
		return nil, fmt.Errorf("tag %q: %w", tag, err)
	}
	if len(components) > 0 {
		slice["components"] = components
	}
	// The slice still shares its paths and components with document, so it is
	// copied before normalize changes it.
	copied := deepCopy(slice).(map[string]any)
	if err := normalize(copied); err != nil {
		return nil, fmt.Errorf("tag %q: %w", tag, err)
	}
	return copied, nil
}

// deepCopy copies decoded JSON, so a slice can change without changing the
// description it came from.
func deepCopy(node any) any {
	switch value := node.(type) {
	case map[string]any:
		copied := make(map[string]any, len(value))
		for key, child := range value {
			copied[key] = deepCopy(child)
		}
		return copied
	case []any:
		copied := make([]any, len(value))
		for index, child := range value {
			copied[index] = deepCopy(child)
		}
		return copied
	default:
		return value
	}
}

func hasTag(operation map[string]any, tag string) bool {
	tags, _ := operation["tags"].([]any)
	return slices.Contains(tags, any(tag))
}

// operationID names an operation within its tag's slice: repos/get becomes
// get, and api-insights/get-stats in orgs becomes api-insights-get-stats.
func operationID(id, tag string) string {
	id = strings.TrimPrefix(id, tag+"/")
	return strings.ReplaceAll(id, "/", "-")
}

// referencedComponents follows every reference reachable from the kept paths
// and top-level fields, and the security schemes named by security
// requirements.
func referencedComponents(document, slice map[string]any, operations []map[string]any) (map[string]any, error) {
	components, _ := document["components"].(map[string]any)
	kept := map[string]any{}
	keep := func(kind, name string) (any, bool, error) {
		keptKind, _ := kept[kind].(map[string]any)
		if _, ok := keptKind[name]; ok {
			return nil, false, nil
		}
		source, _ := components[kind].(map[string]any)
		value, ok := source[name]
		if !ok {
			return nil, false, fmt.Errorf("components/%s/%s is referenced but not defined", kind, name)
		}
		if keptKind == nil {
			keptKind = map[string]any{}
			kept[kind] = keptKind
		}
		keptKind[name] = value
		return value, true, nil
	}

	// Security requirements name schemes directly rather than with $ref.
	requirements := []any{slice["security"]}
	for _, operation := range operations {
		requirements = append(requirements, operation["security"])
	}
	for _, requirement := range requirements {
		list, _ := requirement.([]any)
		for _, entry := range list {
			names, _ := entry.(map[string]any)
			for name := range names {
				if _, _, err := keep("securitySchemes", name); err != nil {
					return nil, err
				}
			}
		}
	}

	pending := []any{slice["paths"], slice["servers"]}
	for len(pending) > 0 {
		node := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, ref := range references(node, nil) {
			kind, name, err := componentOf(ref)
			if err != nil {
				return nil, err
			}
			value, added, err := keep(kind, name)
			if err != nil {
				return nil, err
			}
			if added {
				pending = append(pending, value)
			}
		}
	}
	return kept, nil
}

// references appends every reference found anywhere in node: each $ref, and
// each schema a discriminator maps a value to.
func references(node any, found []string) []string {
	switch value := node.(type) {
	case map[string]any:
		if ref, ok := value["$ref"].(string); ok {
			found = append(found, ref)
		}
		for _, target := range discriminatorMapping(value) {
			if target, ok := target.(string); ok {
				found = append(found, mappingReference(target))
			}
		}
		for _, child := range value {
			found = references(child, found)
		}
	case []any:
		for _, child := range value {
			found = references(child, found)
		}
	}
	return found
}

// discriminatorMapping returns the mapping of a schema's discriminator, or nil.
func discriminatorMapping(schema map[string]any) map[string]any {
	discriminator, _ := schema["discriminator"].(map[string]any)
	mapping, _ := discriminator["mapping"].(map[string]any)
	return mapping
}

// mappingReference is the reference a discriminator mapping value stands for.
// A value that could be a component name names a schema; any other value is
// already a reference.
func mappingReference(value string) string {
	isName := value != "" && strings.IndexFunc(value, func(character rune) bool {
		return !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune(".-_", character))
	}) < 0
	if isName {
		return componentReference("schemas", value)
	}
	return value
}

// componentOf names the component a local reference points into. A pointer
// deeper into a component keeps the whole component.
func componentOf(ref string) (kind, name string, err error) {
	if !strings.HasPrefix(ref, componentsRef) {
		return "", "", fmt.Errorf("only local component references are supported, not %q", ref)
	}
	parts := strings.Split(strings.TrimPrefix(ref, componentsRef), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("reference %q does not name a component", ref)
	}
	// JSON Pointer escapes, RFC 6901.
	unescape := strings.NewReplacer("~1", "/", "~0", "~")
	return unescape.Replace(parts[0]), unescape.Replace(parts[1]), nil
}

// componentReference is the local reference to a component, the inverse of
// componentOf.
func componentReference(kind, name string) string {
	escape := strings.NewReplacer("~", "~0", "/", "~1")
	return componentsRef + escape.Replace(kind) + "/" + escape.Replace(name)
}
