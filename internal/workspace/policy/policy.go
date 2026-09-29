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
	// Offer is nil when all Composio toolkits are allowed. An empty list
	// explicitly disables every toolkit for this organization.
	Offer         *[]string
	AutoSubscribe *bool
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
	if document.Agents != nil && document.Agents.Personal != nil && document.Agents.Personal.Proactive != nil {
		settings.Proactive = *document.Agents.Personal.Proactive
	}
	if document.Tools != nil && document.Tools.Offer != nil {
		copyOfOffer := append([]string{}, (*document.Tools.Offer)...)
		settings.Offer = &copyOfOffer
	}
	if document.Tools != nil {
		settings.AutoSubscribe = document.Tools.AutoSubscribe
	}
	return settings, nil
}
