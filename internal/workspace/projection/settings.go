// Package projection keeps the small, version-tolerant operational policy
// stored alongside an applied workspace, independent of strict manifest parsing.
package projection

import (
	"fmt"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"go.yaml.in/yaml/v3"
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
	var err error
	settings := Settings{}
	if document.Rules != nil && document.Rules.Consent != nil && document.Rules.Consent.ExpireAfter != nil {
		settings.ExpireAfter, err = spec.ParseDuration(*document.Rules.Consent.ExpireAfter)
		if err != nil {
			return Settings{}, err
		}
	}
	if document.Safety != nil && document.Safety.Guard != nil && document.Safety.Guard.HoldForReview != nil {
		settings.HoldForReview = *document.Safety.Guard.HoldForReview
	}
	if document.Safety != nil && document.Safety.Guard != nil {
		guard := document.Safety.Guard
		if guard.PII != nil && guard.PII.Minimum != nil {
			if guard.PII.Minimum.Owner != nil {
				settings.PIIMinimumOwner = *guard.PII.Minimum.Owner
			}
			if guard.PII.Minimum.Peer != nil {
				settings.PIIMinimumPeer = *guard.PII.Minimum.Peer
			}
		}
		if guard.Relevance != nil {
			settings.GuardRelevance = *guard.Relevance
		}
	}
	settings.Regulated = document.Preset == "regulated"
	if document.Agents != nil && document.Agents.Personal != nil && document.Agents.Personal.Proactive != nil {
		settings.Proactive = *document.Agents.Personal.Proactive
	}
	if document.Tools != nil && document.Tools.Offer != nil {
		copyOfOffer := append([]string{}, (*document.Tools.Offer)...)
		settings.Offer = &copyOfOffer
	}
	if document.Tools != nil {
		settings.EmployeeConnections = document.Tools.EmployeeConnections
		settings.AutoSubscribe = document.Tools.AutoSubscribe
		if document.Tools.WakeUpWithin != nil {
			settings.WakeUpWithin, err = spec.ParseDuration(*document.Tools.WakeUpWithin)
			if err != nil {
				return Settings{}, err
			}
		}
	}
	if document.Search != nil {
		settings.SearchMode = document.Search.Mode
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
