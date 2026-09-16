package providers

import (
	"encoding/json"
	"slices"
)

// TargetField is one field a subscription's target takes.
type TargetField struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Example     string `json:"example,omitempty"`
}

// EventsDescription is what a subscription may ask a provider for.
type EventsDescription struct {
	// Names are the events the provider sends. Open says a subscription may
	// also ask for events this list does not carry, as GitHub's event names
	// are open-ended.
	Names []string `json:"names"`
	Open  bool     `json:"open"`
	// Default says what a subscription that asks for no events receives.
	Default string `json:"default"`
}

// ActionDescription is an action as a caller offering it sees it.
type ActionDescription struct {
	Name    string          `json:"name"`
	Summary string          `json:"summary"`
	Input   json.RawMessage `json:"input"`
	Writes  bool            `json:"writes"`
}

// Description is one provider read aloud: what a subscription watches, what it
// delivers, what may be done with a connection, and how to use any of it. It
// is the same for every organization and holds nothing of one, so a caller
// needs no credentials for it.
type Description struct {
	Name     string              `json:"name"`
	Delivery Delivery            `json:"delivery"`
	Target   []TargetField       `json:"target"`
	Events   EventsDescription   `json:"events"`
	Actions  []ActionDescription `json:"actions"`
	Skill    string              `json:"skill"`
}

// Describe reads a registry aloud, in provider order, so a provider added to
// Builtin appears at once.
func (registry Registry) Describe() []Description {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	slices.Sort(names)
	described := make([]Description, 0, len(names))
	for _, name := range names {
		provider := registry[name]
		description := Description{
			Name: provider.Name, Delivery: provider.Delivery, Target: provider.TargetFields,
			Events: provider.Events, Actions: []ActionDescription{}, Skill: provider.Skill,
		}
		if description.Target == nil {
			description.Target = []TargetField{}
		}
		if description.Events.Names == nil {
			description.Events.Names = []string{}
		}
		for _, action := range provider.Actions {
			description.Actions = append(description.Actions, ActionDescription{
				Name: action.Name, Summary: action.Summary, Input: action.Input, Writes: action.Writes,
			})
		}
		described = append(described, description)
	}
	return described
}
