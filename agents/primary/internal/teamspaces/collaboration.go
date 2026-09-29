package teamspaces

import (
	"context"
	"errors"
	"fmt"
	"strings"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
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

// CollaborationPublish is an agent publishing to a teamspace. Everybody on
// the team reads what it publishes, and it is words a model wrote, so when
// there is a guard (WithGuard) it reads them first: what it finds that is
// always masked is masked, what it holds elsewhere for review (an email
// address, a phone number, an address) is replaced by its kind, since a
// teamspace has nobody to review a publication before the team reads it, and a
// publication that could not be checked is not stored at all (ErrUnchecked).
// The entry stored is the masked one; the result says what was found.
//
// Only this path is read: PublishEntry, which a member's own publication and
// a room message lifted to the ledger go through, carries the member's own
// words and never asks the guard.
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
	publication := PublishEntryInput{
		AuthorAgentID: actor.AgentID, Kind: EntryKind(input.Kind), Title: input.Title,
		Summary: input.Summary, ToolsUsed: input.ToolsUsed, Artifacts: artifacts,
	}
	var found *primaryagents.SharedEntryGuard
	if service.guard != nil {
		publication, found, err = service.guarded(ctx, actor, space.ID, publication)
		if err != nil {
			return primaryagents.SharedEntry{}, err
		}
	}
	entry, err := service.PublishEntry(ctx, member, id, publication)
	if err != nil {
		return primaryagents.SharedEntry{}, err
	}
	shared := collaborationEntry(entry)
	shared.Guard = found
	return shared, nil
}

// ErrUnchecked is what an agent's publication is refused with when the answer
// guard could not check it: nothing was stored, and the team reads none of it.
// It is the agent's to be told, in these words, and not a caller's error to
// map to a status: no route publishes an agent's words.
var ErrUnchecked = errors.New("the publication couldn't be checked, so it wasn't shared with the teamspace: nothing was stored")

// The texts of a publication the guard reads, in the order it is handed them:
// the title and the summary as they are, and the artifacts' names, URIs and
// media types and the tools used, each list as one text, one element to a
// line. A publication costs at most six checks whatever it lists (a text with
// nothing in it is not sent on), and every element is one line: names, media
// types and tool names are cleaned to one, and a URI that holds a line break
// is not a URI.
const (
	titleText = iota
	summaryText
	artifactNamesText
	artifactURIsText
	artifactMediaTypesText
	toolsUsedText
	publicationTexts
)

// guarded is publication as the team may read it, and what the guard found in
// it. It is read as the ledger would store it, cleaned and valid: what the
// ledger would refuse is refused before the guard is asked. The guard reads
// every text, and what it allows to be shared (answerguard.Verdict.Shareable,
// the rule a room applies too) replaces the text, so nothing but that ever
// reaches the ledger. A publication the guard could not check, or whose lists
// no longer hold the lines they were given, is refused with ErrUnchecked.
func (service *Service) guarded(ctx context.Context, actor primaryagents.TeamspaceActor, teamspaceID string, publication PublishEntryInput) (PublishEntryInput, *primaryagents.SharedEntryGuard, error) {
	publication = cleanPublishEntry(publication)
	if err := validatePublishEntry(publication); err != nil {
		return PublishEntryInput{}, nil, err
	}
	names, uris, mediaTypes := make([]string, len(publication.Artifacts)), make([]string, len(publication.Artifacts)), make([]string, len(publication.Artifacts))
	for index, artifact := range publication.Artifacts {
		names[index], uris[index], mediaTypes[index] = artifact.Name, artifact.URI, artifact.MediaType
	}
	texts := make([]string, publicationTexts)
	texts[titleText], texts[summaryText] = publication.Title, publication.Summary
	texts[artifactNamesText], texts[artifactURIsText] = strings.Join(names, "\n"), strings.Join(uris, "\n")
	texts[artifactMediaTypesText], texts[toolsUsedText] = strings.Join(mediaTypes, "\n"), strings.Join(publication.ToolsUsed, "\n")

	settings := guardpolicy.Effective{Peer: guardpolicy.LevelStrict, Relevance: guardpolicy.RelevanceOff}
	if service.guardPolicy != nil {
		var err error
		settings, err = service.guardPolicy.Effective(ctx, actor.OrganizationID, actor.UserID)
		if err != nil {
			service.logger.WarnContext(ctx, "a teamspace publication could not read answer guard settings, so it uses their conservative fallback",
				"organization_id", actor.OrganizationID, "user_id", actor.UserID)
		}
	}
	intensity := answerguard.IntensityStrict
	if settings.Peer == guardpolicy.LevelSecrets {
		intensity = answerguard.IntensitySecrets
	}
	run := primaryagents.GuardRun(ctx)
	outcome := service.guard.CheckWith(ctx, texts, &answerguard.RelevanceInput{
		Request: run.Request, Context: run.Context, Sources: run.Sources,
		Mode: string(settings.Relevance), Regulated: settings.Regulated,
		OrganizationID: actor.OrganizationID, UserID: actor.UserID,
	}).At(len(texts), intensity)
	if outcome.State != answerguard.StateClear || outcome.Shareable == nil {
		// What was found is logged by kind, never by value.
		service.logger.InfoContext(ctx, "the answer guard read what an agent published to a teamspace",
			"teamspace_id", teamspaceID, "agent_id", publication.AuthorAgentID,
			"state", outcome.State, "kinds", outcome.Kinds, "published", outcome.Shareable != nil)
	}
	shareable := outcome.Shareable
	if shareable == nil {
		return PublishEntryInput{}, nil, ErrUnchecked
	}
	names, namesKept := splitLines(shareable[artifactNamesText], len(publication.Artifacts))
	uris, urisKept := splitLines(shareable[artifactURIsText], len(publication.Artifacts))
	mediaTypes, mediaTypesKept := splitLines(shareable[artifactMediaTypesText], len(publication.Artifacts))
	tools, toolsKept := splitLines(shareable[toolsUsedText], len(publication.ToolsUsed))
	if !namesKept || !urisKept || !mediaTypesKept || !toolsKept {
		return PublishEntryInput{}, nil, ErrUnchecked
	}

	checked := publication
	checked.Title, checked.Summary, checked.ToolsUsed = shareable[titleText], shareable[summaryText], tools
	checked.Artifacts = make([]EntryArtifact, len(publication.Artifacts))
	for index := range publication.Artifacts {
		checked.Artifacts[index] = EntryArtifact{Name: names[index], URI: uris[index], MediaType: mediaTypes[index]}
	}
	return checked, &primaryagents.SharedEntryGuard{State: outcome.State, Kinds: outcome.Kinds}, nil
}

// splitLines is a list the guard read as one text, one element to a line, back
// into its n elements, and false when what it shares no longer holds n lines
// (or, for none, holds anything): a list that came back another length cannot
// be put back element by element.
func splitLines(text string, n int) ([]string, bool) {
	if n == 0 {
		return nil, strings.TrimSpace(text) == ""
	}
	lines := strings.Split(text, "\n")
	return lines, len(lines) == n
}

func collaborationActor(actor primaryagents.TeamspaceActor, token string) Actor {
	return Actor{OrganizationID: actor.OrganizationID, UserID: actor.UserID, AccessToken: token}
}

// collaborationParticipant is whether the acting agent holds a seat of its
// own member's in the space, of its own kind: a primary agent in its
// member's whisperer seat, a specialist as their active contribution.
func collaborationParticipant(space Space, actor primaryagents.TeamspaceActor) bool {
	found, ok := seatOf(space, actor.AgentID)
	return ok && found.OwnerUserID == actor.UserID && found.Kind == actor.AgentKind
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
