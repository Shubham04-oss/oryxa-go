package slackrouter

import (
	"context"
	"errors"
	"fmt"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

// AskerDirectory answers Directory from the organization directory, the one
// agents read through directory_load, read as the asker: with the asker's own
// agent token, which is what Teams authorizes the read with, by the same token
// path ChartAuthority reads the organization chart with.
//
// What it returns is therefore what the asker's own agent could see, and no
// more. Teams shows a member the teams they are on and no others, so an
// owner's teams are the ones they share with the asker, and an owner who
// shares none has none. The router takes a department that is not listed for
// one that is not known, and asks the person; nothing is ever read of a team
// the asker could not have read of themself.
//
// An asker who has never given agents a standing grant has no token, and the
// read fails with ErrNoGrant, which is the asker's to fix in Settings.
type AskerDirectory struct {
	directory agents.Directory
	tokens    memberauth.TokenSource
}

// NewAskerDirectory reads the directory with directory and mints the asker's
// token with tokens. Either may be nil, as when the deployment has no Teams
// URL or no agent-token exchange; nothing is then known of anyone's teams, and
// no grant is asked for.
func NewAskerDirectory(directory agents.Directory, tokens memberauth.TokenSource) *AskerDirectory {
	return &AskerDirectory{directory: directory, tokens: tokens}
}

var _ Directory = (*AskerDirectory)(nil)

// Teams implements Directory.
func (reader *AskerDirectory) Teams(ctx context.Context, organizationID string, asker Person, userIDs []string) (map[string][]string, error) {
	if reader == nil || reader.directory == nil || reader.tokens == nil || asker.UserID == "" {
		return nil, nil
	}
	token, _, err := reader.tokens.AgentToken(ctx, asker.UserID, memberauth.Agent{})
	switch {
	case errors.Is(err, memberauth.ErrGrantRequired):
		return nil, ErrNoGrant
	case err != nil:
		return nil, fmt.Errorf("mint the asker's agent token: %w", err)
	}
	directory, err := reader.directory.Load(ctx, organizationID, token)
	if err != nil {
		return nil, fmt.Errorf("read the organization directory: %w", err)
	}
	wanted := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		wanted[id] = true
	}
	teams := make(map[string][]string, len(userIDs))
	for _, member := range directory.People {
		if !wanted[member.UserID] {
			continue
		}
		names := make([]string, 0, len(member.Teams))
		for _, team := range member.Teams {
			names = append(names, team.Name)
		}
		teams[member.UserID] = names
	}
	return teams, nil
}
