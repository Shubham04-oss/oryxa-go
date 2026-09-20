package teamspaces

import (
	"context"
	"fmt"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// CollaborationSpaces exposes only spaces in which the persistent agent may
// actively participate: a member's primary agent as their whisperer, or a
// specialist the member explicitly contributed.
func (service *Service) CollaborationSpaces(ctx context.Context, actor primaryagents.TeamspaceActor, token string) ([]primaryagents.Teamspace, error) {
	spaces, err := service.List(ctx, collaborationActor(actor, token))
	if err != nil {
		return nil, err
	}
	result := make([]primaryagents.Teamspace, 0, len(spaces))
	for _, space := range spaces {
		if collaborationAvailable(space) && collaborationParticipant(space, actor) {
			result = append(result, collaborationSpace(space))
		}
	}
	return result, nil
}

func (service *Service) CollaborationEntries(ctx context.Context, actor primaryagents.TeamspaceActor, token, id string, limit int) ([]primaryagents.SharedEntry, error) {
	member := collaborationActor(actor, token)
	space, err := service.Get(ctx, member, id)
	if err != nil {
		return nil, err
	}
	if !collaborationParticipant(space, actor) {
		return nil, fmt.Errorf("%w: this agent is not a whisperer or contributed specialist in the teamspace", ErrForbidden)
	}
	if !collaborationAvailable(space) {
		return nil, ErrDisabled
	}
	entries, err := service.ListEntries(ctx, member, id, limit)
	if err != nil {
		return nil, err
	}
	result := make([]primaryagents.SharedEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, collaborationEntry(entry))
	}
	return result, nil
}

func (service *Service) CollaborationPublish(ctx context.Context, actor primaryagents.TeamspaceActor, token, id string, input primaryagents.SharedEntryInput) (primaryagents.SharedEntry, error) {
	member := collaborationActor(actor, token)
	space, err := service.Get(ctx, member, id)
	if err != nil {
		return primaryagents.SharedEntry{}, err
	}
	if !collaborationParticipant(space, actor) {
		return primaryagents.SharedEntry{}, fmt.Errorf("%w: this agent is not a whisperer or contributed specialist in the teamspace", ErrForbidden)
	}
	artifacts := make([]EntryArtifact, 0, len(input.Artifacts))
	for _, artifact := range input.Artifacts {
		artifacts = append(artifacts, EntryArtifact{Name: artifact.Name, URI: artifact.URI, MediaType: artifact.MediaType})
	}
	entry, err := service.PublishEntry(ctx, member, id, PublishEntryInput{
		AuthorAgentID: actor.AgentID, Kind: EntryKind(input.Kind), Title: input.Title,
		Summary: input.Summary, ToolsUsed: input.ToolsUsed, Artifacts: artifacts,
	})
	if err != nil {
		return primaryagents.SharedEntry{}, err
	}
	return collaborationEntry(entry), nil
}

func collaborationActor(actor primaryagents.TeamspaceActor, token string) Actor {
	return Actor{OrganizationID: actor.OrganizationID, UserID: actor.UserID, AccessToken: token}
}

func collaborationParticipant(space Space, actor primaryagents.TeamspaceActor) bool {
	switch actor.AgentKind {
	case "primary":
		for _, whisperer := range space.Whisperers {
			if whisperer.UserID == actor.UserID && whisperer.AgentID == actor.AgentID {
				return true
			}
		}
	case "specialist":
		for _, contribution := range space.Contributions {
			if contribution.OwnerUserID == actor.UserID && contribution.AgentID == actor.AgentID && contribution.Status == "active" {
				return true
			}
		}
	}
	return false
}

// collaborationAvailable keeps disabled shared work out of the agent-facing
// toolbox. Human members retain the historical read access exposed by
// Service.List/Get/ListEntries; only persistent agents must stop participating
// as soon as either the space or its organization agent is disabled.
func collaborationAvailable(space Space) bool {
	return space.Status == StatusActive && space.OrganizationAgent.Status == StatusActive
}

func collaborationSpace(space Space) primaryagents.Teamspace {
	whisperers := make([]primaryagents.TeamspaceWhisperer, 0, len(space.Whisperers))
	for _, whisperer := range space.Whisperers {
		whisperers = append(whisperers, primaryagents.TeamspaceWhisperer{
			UserID: whisperer.UserID, UserDisplayName: whisperer.UserDisplayName,
			AgentID: whisperer.AgentID, AgentName: whisperer.AgentName, Mode: whisperer.Mode,
		})
	}
	contributions := make([]primaryagents.TeamspaceContribution, 0, len(space.Contributions))
	for _, contribution := range space.Contributions {
		contributions = append(contributions, primaryagents.TeamspaceContribution{
			AgentID: contribution.AgentID, OwnerUserID: contribution.OwnerUserID,
			Name: contribution.Name, Description: contribution.Description, Status: string(contribution.Status),
			Role: contribution.Role, DelegationMode: string(contribution.DelegationMode),
		})
	}
	return primaryagents.Teamspace{
		ID: space.ID, TeamID: space.TeamID, TeamName: space.TeamName, Name: space.Name, Description: space.Description,
		OrganizationAgent: primaryagents.TeamspaceOrganizationAgent{
			ID: space.OrganizationAgent.ID, Name: space.OrganizationAgent.Name, Status: string(space.OrganizationAgent.Status),
		},
		Whisperers: whisperers, Contributions: contributions, Status: string(space.Status),
	}
}

func collaborationEntry(entry Entry) primaryagents.SharedEntry {
	artifacts := make([]primaryagents.SharedArtifact, 0, len(entry.Artifacts))
	for _, artifact := range entry.Artifacts {
		artifacts = append(artifacts, primaryagents.SharedArtifact{Name: artifact.Name, URI: artifact.URI, MediaType: artifact.MediaType})
	}
	return primaryagents.SharedEntry{
		ID: entry.ID, TeamspaceID: entry.TeamspaceID, AuthorUserID: entry.AuthorUserID,
		AuthorAgentID: entry.AuthorAgentID, AuthorAgentName: entry.AuthorAgentName,
		Kind: string(entry.Kind), Title: entry.Title, Summary: entry.Summary,
		ToolsUsed: entry.ToolsUsed, Artifacts: artifacts, CreatedAt: entry.CreatedAt,
	}
}
