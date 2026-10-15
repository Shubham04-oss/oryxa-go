package tools

import (
	"encoding/json"
	"fmt"
)

// project keeps of output only what schema describes, so a connector that
// returns more than its manifest promised can't hand an agent the rest, and
// refuses an output over MaxOutput rather than cutting it (a cut document
// would no longer match the schema).
// ponytail: follows properties and items only; a schema built on $ref, oneOf
// or patternProperties keeps its values whole.
func project(schema json.RawMessage, output string) (json.RawMessage, error) {
	var s map[string]any
	var v any
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, fmt.Errorf("tools: output_schema: %w", err)
	}
	if err := json.Unmarshal([]byte(output), &v); err != nil {
		return nil, fmt.Errorf("tools: output: %w", err)
	}
	out, err := json.Marshal(keep(s, v))
	if err != nil {
		return nil, err
	}
	if len(out) > MaxOutput {
		return nil, ErrOutputTooLarge
	}
	return out, nil
}

func keep(schema map[string]any, v any) any {
	switch v := v.(type) {
	case map[string]any:
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			return v
		}
		kept := make(map[string]any, len(props))
		for name, sub := range props {
			if value, present := v[name]; present {
				subSchema, _ := sub.(map[string]any)
				kept[name] = keep(subSchema, value)
			}
		}
		return kept
	case []any:
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return v
		}
		kept := make([]any, len(v))
		for i, item := range v {
			kept[i] = keep(items, item)
		}
		return kept
	}
	return v
}
