package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// decodeDescription reads a description written as a JSON object, or else as
// YAML, into the values JSON decoding gives. Numbers stay as written, as
// json.Number, so examples and limits are not rounded.
func decodeDescription(raw []byte) (map[string]any, error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '{' {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		var document map[string]any
		if err := decoder.Decode(&document); err != nil {
			return nil, fmt.Errorf("as JSON: %w", err)
		}
		return document, nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var root, next yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("as YAML: %w", err)
	}
	switch err := decoder.Decode(&next); {
	case err == nil:
		return nil, errors.New("as YAML: a description is one document")
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("as YAML: %w", err)
	}
	value, err := fromYAML(&root)
	if err != nil {
		return nil, fmt.Errorf("as YAML: %w", err)
	}
	document, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("the description is not an object")
	}
	return document, nil
}

// fromYAML turns a YAML node into the value its JSON form decodes to. Keys
// are strings, as in JSON; an alias is the value it names, and a merge key
// (<<) adds the keys of the mappings it names that the mapping lacks. A
// timestamp or other scalar JSON has no type for stays the text it was
// written as.
func fromYAML(node *yaml.Node) (any, error) {
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return nil, errors.New("a description is one YAML document")
		}
		return fromYAML(node.Content[0])
	case yaml.AliasNode:
		return fromYAML(node.Alias)
	case yaml.MappingNode:
		object := make(map[string]any, len(node.Content)/2)
		var merged []map[string]any
		for index := 0; index+1 < len(node.Content); index += 2 {
			key, child := node.Content[index], node.Content[index+1]
			if key.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: a key is not a scalar", key.Line)
			}
			value, err := fromYAML(child)
			if err != nil {
				return nil, err
			}
			if key.ShortTag() != "!!merge" {
				object[key.Value] = value
				continue
			}
			// << names one mapping, or a sequence of them.
			sources, isList := value.([]any)
			if !isList {
				sources = []any{value}
			}
			for _, source := range sources {
				mapping, ok := source.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("line %d: a merge key names something other than a mapping", key.Line)
				}
				merged = append(merged, mapping)
			}
		}
		// Earlier merged mappings win over later ones, and the mapping's own
		// keys over all of them.
		for _, mapping := range merged {
			for key, value := range mapping {
				if _, taken := object[key]; !taken {
					object[key] = value
				}
			}
		}
		return object, nil
	case yaml.SequenceNode:
		list := make([]any, 0, len(node.Content))
		for _, child := range node.Content {
			value, err := fromYAML(child)
			if err != nil {
				return nil, err
			}
			list = append(list, value)
		}
		return list, nil
	case yaml.ScalarNode:
		return yamlScalar(node)
	default:
		return nil, fmt.Errorf("line %d: unexpected YAML node", node.Line)
	}
}

func yamlScalar(node *yaml.Node) (any, error) {
	switch node.ShortTag() {
	case "!!null":
		return nil, nil
	case "!!bool":
		var value bool
		if err := node.Decode(&value); err != nil {
			return nil, fmt.Errorf("line %d: %w", node.Line, err)
		}
		return value, nil
	case "!!int", "!!float":
		if json.Valid([]byte(node.Value)) {
			return json.Number(node.Value), nil
		}
		// YAML also writes numbers JSON cannot, such as 0x1F or +1; they are
		// written again in JSON's form. JSON has no infinity or NaN.
		var value float64
		if err := node.Decode(&value); err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return nil, fmt.Errorf("line %d: %q is not a number JSON can hold", node.Line, node.Value)
		}
		return json.Number(strconv.FormatFloat(value, 'f', -1, 64)), nil
	default:
		return node.Value, nil
	}
}
