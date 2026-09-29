// Package policy reads operational organization policies from the last
// applied workspace version. It never reads a proposed or partial file.
package policy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

type Repository interface {
	Latest(context.Context, string) (store.Version, error)
}

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

type Loader struct{ repository Repository }

func New(repository Repository) *Loader { return &Loader{repository: repository} }

func (loader *Loader) Get(ctx context.Context, organizationID string) (Settings, error) {
	version, err := loader.repository.Latest(ctx, organizationID)
	if errors.Is(err, store.ErrVersionNotFound) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	document, err := spec.Parse(version.Document)
	if err != nil {
		return Settings{}, fmt.Errorf("applied workspace policy is invalid: %w", err)
	}
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
