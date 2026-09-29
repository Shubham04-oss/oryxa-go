// Package projection keeps the small, version-tolerant operational policy
// stored alongside an applied workspace, independent of strict manifest parsing.
package projection

import (
	"fmt"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"go.yaml.in/yaml/v3"
	"reflect"
	"time"
)

type Settings struct {
	// ExpireAfter is zero when the workspace has no override.
	ExpireAfter   time.Duration
	HoldForReview bool
	Proactive     string
	// SearchMode is empty when the workspace has no override.
	SearchMode string
	// Offer is nil when all Composio toolkits are allowed. An empty list
	// explicitly disables every toolkit for this organization.
	Offer               *[]string
	EmployeeConnections *bool
	AutoSubscribe       *bool
	// WakeUpWithin is zero when the workspace uses the deployment default.
	WakeUpWithin time.Duration
	// PIIMinimumOwner and PIIMinimumPeer are the least strict levels, "secrets"
	// or "strict", the answer guard lets a member choose for an answer they
	// started themselves and for one another member reads. Empty when the
	// workspace has no override.
	PIIMinimumOwner string
	PIIMinimumPeer  string
	// GuardRelevance is how the answer guard treats an answer that does not
	// answer what was asked: "hold", "shadow" or "off". Empty when the
	// workspace has no override.
	GuardRelevance string
	// Regulated is true when the workspace was applied with the regulated
	// preset.
	Regulated bool
}

// FromDocument projects a manifest that the write path already validated.
func FromDocument(document spec.Document) (Settings, error) {
	settings := Settings{Regulated: document.Preset == "regulated"}
	target := reflect.ValueOf(&settings).Elem()
	for _, setting := range spec.SettingsCatalog() {
		if setting.Projection == "" {
			continue
		}
		value, present := setting.Get(document)
		if !present {
			continue
		}
		if setting.Kind == "duration" {
			duration, err := spec.ParseDuration(value.(string))
			if err != nil {
				return Settings{}, err
			}
			value = duration
		}
		field := target.FieldByName(setting.Projection)
		if !field.IsValid() {
			return Settings{}, fmt.Errorf("setting projection is missing %s", setting.Projection)
		}
		next := reflect.ValueOf(value)
		if field.Kind() == reflect.Pointer {
			field.Set(reflect.New(field.Type().Elem()))
			field.Elem().Set(next)
		} else {
			field.Set(next)
		}
	}
	return settings, nil
}

// FromYAML backfills old versions once. Only operational sections are decoded:
// future keys and unrelated directory/people validation cannot disable policy.
func FromYAML(data []byte) (Settings, error) {
	var fields struct {
		Preset string       `yaml:"preset"`
		Rules  *spec.Rules  `yaml:"rules"`
		Safety *spec.Safety `yaml:"safety"`
		Tools  *spec.Tools  `yaml:"tools"`
		Search *spec.Search `yaml:"search"`
		Agents struct {
			Personal *spec.PersonalAgent `yaml:"personal"`
		} `yaml:"agents"`
	}
	if err := yaml.Unmarshal(data, &fields); err != nil {
		return Settings{}, fmt.Errorf("read stored workspace policy: %w", err)
	}
	return FromDocument(spec.Document{Preset: fields.Preset, Rules: fields.Rules, Safety: fields.Safety, Tools: fields.Tools, Search: fields.Search, Agents: &spec.Agents{Personal: fields.Agents.Personal}})
}
