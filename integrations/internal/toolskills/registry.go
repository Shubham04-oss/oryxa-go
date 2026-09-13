// Package toolskills owns call-scoped instructions for integration tools.
// Direct tool adapters inject Skill.Content with the selected tool, while an
// MCP server can expose the same content at Skill.ResourceURI.
package toolskills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

//go:embed registry.json skills/*/SKILL.md
var files embed.FS

type Operation struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

type Skill struct {
	Name                string      `json:"name"`
	Description         string      `json:"description"`
	Path                string      `json:"path"`
	ResourceURI         string      `json:"resource_uri"`
	ConnectionProviders []string    `json:"connection_providers,omitempty"`
	Pieces              []string    `json:"pieces"`
	Actions             []Operation `json:"actions,omitempty"`
	Triggers            []Operation `json:"triggers,omitempty"`
	Content             string      `json:"-"`
	Digest              string      `json:"-"`
}

type registryDocument struct {
	Version    int     `json:"version"`
	LoadPolicy string  `json:"load_policy"`
	Skills     []Skill `json:"skills"`
}

var registry = mustLoad()

// All returns the tool-bound skill catalog. Callers must load only the skill
// selected for the current tool instead of injecting the entire catalog.
func All() []Skill {
	result := make([]Skill, 0, len(registry.Skills))
	for _, skill := range registry.Skills {
		result = append(result, clone(skill))
	}
	return result
}

func ForConnection(provider string) (Skill, bool) {
	provider = normalize(provider)
	return find(func(skill Skill) bool {
		return slices.Contains(skill.ConnectionProviders, provider)
	})
}

func ForPiece(pieceName string) (Skill, bool) {
	pieceName = strings.TrimSpace(pieceName)
	return find(func(skill Skill) bool { return slices.Contains(skill.Pieces, pieceName) })
}

func ForAction(provider, actionName string) (Skill, bool) {
	return forOperation(normalize(provider), normalize(actionName), func(skill Skill) []Operation {
		return skill.Actions
	})
}

func ForTrigger(provider, triggerName string) (Skill, bool) {
	return forOperation(normalize(provider), normalize(triggerName), func(skill Skill) []Operation {
		return skill.Triggers
	})
}

func forOperation(provider, name string, operations func(Skill) []Operation) (Skill, bool) {
	return find(func(skill Skill) bool {
		for _, operation := range operations(skill) {
			if operation.Provider == provider && operation.Name == name {
				return true
			}
		}
		return false
	})
}

func find(matches func(Skill) bool) (Skill, bool) {
	for _, skill := range registry.Skills {
		if matches(skill) {
			return clone(skill), true
		}
	}
	return Skill{}, false
}

func clone(skill Skill) Skill {
	skill.ConnectionProviders = slices.Clone(skill.ConnectionProviders)
	skill.Pieces = slices.Clone(skill.Pieces)
	skill.Actions = slices.Clone(skill.Actions)
	skill.Triggers = slices.Clone(skill.Triggers)
	return skill
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func mustLoad() registryDocument {
	raw, err := files.ReadFile("registry.json")
	if err != nil {
		panic(fmt.Errorf("read integration tool-skill registry: %w", err))
	}
	var document registryDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		panic(fmt.Errorf("parse integration tool-skill registry: %w", err))
	}
	if document.Version != 1 || document.LoadPolicy != "tool-bound" || len(document.Skills) == 0 {
		panic("integration tool-skill registry has an unsupported contract")
	}
	seenNames := map[string]struct{}{}
	seenPaths := map[string]struct{}{}
	seenResources := map[string]struct{}{}
	seenConnections := map[string]struct{}{}
	seenPieces := map[string]struct{}{}
	seenActions := map[string]struct{}{}
	seenTriggers := map[string]struct{}{}
	for index := range document.Skills {
		skill := &document.Skills[index]
		content, err := files.ReadFile(skill.Path)
		if err != nil {
			panic(fmt.Errorf("read integration tool skill %q: %w", skill.Name, err))
		}
		if skill.Name == "" || skill.Description == "" || skill.ResourceURI == "" ||
			!strings.HasPrefix(string(content), "---\n") ||
			!strings.Contains(string(content), "\nname: "+skill.Name+"\n") {
			panic(fmt.Errorf("integration tool skill %q has invalid metadata", skill.Name))
		}
		mustBeUnique("skill name", skill.Name, seenNames)
		mustBeUnique("skill path", skill.Path, seenPaths)
		mustBeUnique("skill resource", skill.ResourceURI, seenResources)
		for connectionIndex := range skill.ConnectionProviders {
			skill.ConnectionProviders[connectionIndex] = normalize(skill.ConnectionProviders[connectionIndex])
			mustBeUnique("connection provider", skill.ConnectionProviders[connectionIndex], seenConnections)
		}
		for pieceIndex := range skill.Pieces {
			skill.Pieces[pieceIndex] = strings.TrimSpace(skill.Pieces[pieceIndex])
			mustBeUnique("piece", skill.Pieces[pieceIndex], seenPieces)
		}
		for actionIndex := range skill.Actions {
			action := &skill.Actions[actionIndex]
			action.Provider, action.Name = normalize(action.Provider), normalize(action.Name)
			mustBeUnique("action", action.Provider+"\x00"+action.Name, seenActions)
		}
		for triggerIndex := range skill.Triggers {
			trigger := &skill.Triggers[triggerIndex]
			trigger.Provider, trigger.Name = normalize(trigger.Provider), normalize(trigger.Name)
			mustBeUnique("trigger", trigger.Provider+"\x00"+trigger.Name, seenTriggers)
		}
		skill.Content = string(content)
		digest := sha256.Sum256(content)
		skill.Digest = "sha256:" + hex.EncodeToString(digest[:])
	}
	return document
}

func mustBeUnique(kind, value string, seen map[string]struct{}) {
	if value == "" {
		panic(fmt.Errorf("integration tool-skill %s is empty", kind))
	}
	if _, exists := seen[value]; exists {
		panic(fmt.Errorf("duplicate integration tool-skill %s %q", kind, value))
	}
	seen[value] = struct{}{}
}
