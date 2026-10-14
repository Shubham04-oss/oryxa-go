package slackrouter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
	workspaceruntime "github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
)

// ChartAuthority answers Authority from the organization chart in the
// workspace file, the same chart agents read through directory_is_above.
//
// The chart is read as the asker. Their agent token is what the workspace
// service authorizes the read with, so what an asker outranks is what the
// chart shows them and nothing broader, and an asker who has never given
// agents a standing grant has no token: Outranks is then false, with no
// error. No grant, no bypass.
type ChartAuthority struct {
	chart  agents.ContextDirectory
	tokens memberauth.TokenSource
}

// NewChartAuthority reads charts with chart and mints the asker's token with
// tokens. Either may be nil, as when the deployment has no workspace service
// or no agent-token exchange; nobody then outranks anybody.
func NewChartAuthority(chart agents.ContextDirectory, tokens memberauth.TokenSource) *ChartAuthority {
	return &ChartAuthority{chart: chart, tokens: tokens}
}

// Outranks implements Authority.
func (authority *ChartAuthority) Outranks(ctx context.Context, organizationID string, asker, owner Person) (bool, error) {
	if authority == nil || authority.chart == nil || authority.tokens == nil {
		return false, nil
	}
	// The chart is written in lower-case addresses, and Above compares them
	// as they are; a person the chart does not name is above nobody.
	askerEmail, ownerEmail := strings.ToLower(strings.TrimSpace(asker.Email)), strings.ToLower(strings.TrimSpace(owner.Email))
	if asker.UserID == "" || askerEmail == "" || ownerEmail == "" || askerEmail == ownerEmail {
		return false, nil
	}
	token, _, err := authority.tokens.AgentToken(ctx, asker.UserID, memberauth.Agent{})
	switch {
	case errors.Is(err, memberauth.ErrGrantRequired):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("mint the asker's agent token: %w", err)
	}
	directory, err := authority.chart.Load(ctx, organizationID, token)
	if err != nil {
		return false, fmt.Errorf("read the organization chart: %w", err)
	}
	return workspaceruntime.Above(directory.OrgChart, askerEmail, ownerEmail), nil
}

var _ Authority = (*ChartAuthority)(nil)
