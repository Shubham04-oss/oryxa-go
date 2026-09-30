package guardpolicy

import (
	"context"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// NewWorkspacePolicies adapts the host's workspace provider to answer guard
// policies. A missing provider is the same deliberate absence as policy.Noop;
// a provider error is preserved so Service applies its conservative fallback.
func NewWorkspacePolicies(provider policy.Provider) OrgPolicies {
	if isNil(provider) {
		provider = policy.Noop{}
	}
	return workspacePolicies{provider: provider}
}

type workspacePolicies struct{ provider policy.Provider }

func (source workspacePolicies) Get(ctx context.Context, organizationID string) (OrgPolicy, error) {
	settings, err := source.provider.Get(ctx, organizationID)
	if err != nil {
		return OrgPolicy{}, err
	}
	return OrgPolicy{
		MinimumOwner: Level(settings.PIIMinimumOwner),
		MinimumPeer:  Level(settings.PIIMinimumPeer),
		Relevance:    RelevanceMode(settings.GuardRelevance),
		Regulated:    settings.Regulated,
	}, nil
}
