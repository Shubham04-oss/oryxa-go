package triggercheck

import "encoding/json"

// What the web app can put a form to, mirrored here so the catalog can be
// measured against it from a command rather than guessed at.
//
// This is a copy of `triggerConfigCapability` in
// `web/src/lib/apps/client.ts`, and the two are meant to agree: the rule
// lives there, and this exists to count how much of Composio's catalog it
// reaches. A trigger it reports as needing setup is one a member can
// subscribe to only through the API.

// formFillable says whether every field a trigger requires is one a form can
// collect. Optional fields it cannot collect are left out rather than
// refusing the trigger, exactly as the web app does, so they do not count
// against it.
func formFillable(schema json.RawMessage) bool {
	if len(schema) == 0 {
		return true
	}
	var parsed struct {
		Properties map[string]schemaProperty `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if json.Unmarshal(schema, &parsed) != nil {
		// Unreadable here is unreadable there: the web app finds no
		// properties and asks for nothing.
		return true
	}
	for _, name := range parsed.Required {
		property, described := parsed.Properties[name]
		if !described || !collectable(property) {
			return false
		}
	}
	return true
}

type schemaProperty struct {
	Type  json.RawMessage   `json:"type"`
	Enum  []json.RawMessage `json:"enum"`
	Items *schemaProperty   `json:"items"`
	AnyOf []schemaProperty  `json:"anyOf"`
	OneOf []schemaProperty  `json:"oneOf"`
	AllOf []schemaProperty  `json:"allOf"`
}

// collectable says whether a form can offer a field for this property: a
// scalar to type, an enum to choose, or a list of either. An object, or a
// list of them, is what it cannot.
func collectable(property schemaProperty) bool {
	if scalarEnum(property.Enum) {
		return true
	}
	types := typeNames(property.Type)
	if contains(types, "array") {
		if property.Items == nil {
			// Nothing said what is in it, so it is typed a line at a time.
			return true
		}
		if scalarEnum(property.Items.Enum) {
			return true
		}
		itemTypes := typeNames(property.Items.Type)
		if len(itemTypes) == 0 {
			return true
		}
		for _, name := range itemTypes {
			if scalarType(name) {
				return true
			}
		}
		return false
	}
	for _, name := range types {
		if scalarType(name) {
			return true
		}
	}
	for _, branch := range concat(property.AnyOf, property.OneOf, property.AllOf) {
		if collectable(branch) {
			return true
		}
	}
	return false
}

func scalarType(name string) bool {
	switch name {
	case "boolean", "integer", "number", "string":
		return true
	default:
		return false
	}
}

// scalarEnum says an enum's values are all scalars, so they can be listed as
// choices.
func scalarEnum(values []json.RawMessage) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		var decoded any
		if json.Unmarshal(value, &decoded) != nil {
			return false
		}
		switch decoded.(type) {
		case string, float64, bool:
		default:
			return false
		}
	}
	return true
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func concat(groups ...[]schemaProperty) []schemaProperty {
	var all []schemaProperty
	for _, group := range groups {
		all = append(all, group...)
	}
	return all
}
