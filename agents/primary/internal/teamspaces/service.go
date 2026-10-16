package teamspaces

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
)

const (
	maxNameRunes         = 100
	maxDescriptionRunes  = 2000
	maxInstructionsRunes = 20_000
	maxRoleRunes         = 500
	maxTargetLabelRunes  = 60
	maxEntryTitleRunes   = 200
	maxEntrySummaryRunes = 20_000
	maxEntryTools        = 100
	maxEntryArtifacts    = 50
)

type Repository interface {
	EnsureOrganizationAgent(context.Context, string, string, string) (OrganizationAgent, error)
	GetOrganizationAgent(context.Context, string) (OrganizationAgent, error)
	UpdateOrganizationAgent(context.Context, string, OrganizationAgentUpdate) (OrganizationAgent, error)
	Create(context.Context, Space) (Space, error)
	List(context.Context, string) ([]Space, error)
	Get(context.Context, string, string) (Space, error)
	// Update changes a teamspace as userID asked, recording them as who
	// paused it when the change disables it.
	Update(ctx context.Context, organizationID, id, userID string, input UpdateInput) (Space, error)
	Delete(context.Context, string, string) error
	AddContribution(context.Context, Actor, string, ContributionInput) (Space, error)
	UpdateContribution(context.Context, Actor, string, string, ContributionUpdate) (Space, error)
	DeleteContribution(context.Context, string, string, string) (Space, error)
	PublishEntry(context.Context, Entry) (Entry, error)
	ListEntries(context.Context, string, string, int) ([]Entry, error)
}

type Directory interface {
	Load(context.Context, string, string) (primaryagents.OrganizationDirectory, error)
}

// Guard checks the texts an agent publishes to a teamspace before the team
// reads them. *answerguard.Guard implements it; it never fails, holding what it
// cannot check.
type Guard interface {
	CheckWith(ctx context.Context, texts []string, input *answerguard.RelevanceInput) answerguard.Verdict
}

// GuardPolicy is the existing member/workspace policy under deployment caps.
type GuardPolicy interface {
	Effective(context.Context, string, string) (guardpolicy.Effective, error)
}

type Service struct {
	repository Repository
	directory  Directory
	// guard, when there is one, reads what an agent publishes through
	// CollaborationPublish. What a member publishes is theirs: PublishEntry
	// never asks it.
	guard       Guard
	guardPolicy GuardPolicy
	logger      *slog.Logger
}

// Option is something a Service may be given beyond what it cannot do without.
type Option func(*Service)

// WithGuard has every publication an agent makes (CollaborationPublish) read
// by guard before the team does. A publication a member makes, and one lifted
// from a room message, carry the member's own words and are not read. Nil, as
// no option, reads nothing; a nil *answerguard.Guard boxed in the interface
// would not read as none, so a caller passes the option only when it has one.
func WithGuard(guard Guard) Option { return func(service *Service) { service.guard = guard } }

// WithGuardPolicy applies the existing effective peer level, workspace
// relevance settings and deployment cap. Without a policy, the legacy strict
// masking check remains and relevance is off, as for the check route.
func WithGuardPolicy(policy GuardPolicy) Option {
	return func(service *Service) { service.guardPolicy = policy }
}

// WithLogger records what the guard found in a publication, by kind and never
// by value. Without it, nothing is logged.
func WithLogger(logger *slog.Logger) Option {
	return func(service *Service) { service.logger = logger }
}

func New(repository Repository, directory Directory, options ...Option) (*Service, error) {
	if repository == nil {
		return nil, errors.New("teamspaces: a repository is required")
	}
	if directory == nil {
		return nil, errors.New("teamspaces: an organization directory is required")
	}
	service := &Service{repository: repository, directory: directory, logger: slog.New(slog.DiscardHandler)}
	for _, option := range options {
		option(service)
	}
	if service.logger == nil {
		service.logger = slog.New(slog.DiscardHandler)
	}
	return service, nil
}

func (service *Service) OrganizationAgent(ctx context.Context, actor Actor) (OrganizationAgent, error) {
	access, err := service.organizationAccess(ctx, actor)
	if err != nil {
		return OrganizationAgent{}, err
	}
	agent, err := service.repository.EnsureOrganizationAgent(ctx, actor.OrganizationID, access.organizationName, actor.UserID)
	if err != nil {
		return OrganizationAgent{}, err
	}
	agent.CanManage = access.canManageOrganization
	return agent, nil
}

// PreviewOrganizationAgent returns effective settings without creating the
// organization agent. Workspace plans use this read-only projection.
func (service *Service) PreviewOrganizationAgent(ctx context.Context, actor Actor) (OrganizationAgent, error) {
	access, err := service.organizationAccess(ctx, actor)
	if err != nil {
		return OrganizationAgent{}, err
	}
	agent, err := service.repository.GetOrganizationAgent(ctx, actor.OrganizationID)
	if errors.Is(err, ErrNotFound) {
		agent = defaultOrganizationAgent(actor.OrganizationID, access.organizationName)
	} else if err != nil {
		return OrganizationAgent{}, err
	}
	agent.CanManage = access.canManageOrganization
	return agent, nil
}

func (service *Service) UpdateOrganizationAgent(ctx context.Context, actor Actor, input OrganizationAgentUpdate) (OrganizationAgent, error) {
	if input.Name == nil && input.Description == nil && input.Instructions == nil && input.Status == nil {
		return OrganizationAgent{}, fmt.Errorf("%w: send at least one organization-agent field to change", ErrInvalid)
	}
	current, err := service.OrganizationAgent(ctx, actor)
	if err != nil {
		return OrganizationAgent{}, err
	}
	if !current.CanManage {
		return OrganizationAgent{}, ErrForbidden
	}
	input = cleanOrganizationAgentUpdate(input)
	merged := current
	if input.Name != nil {
		merged.Name = *input.Name
	}
	if input.Description != nil {
		merged.Description = *input.Description
	}
	if input.Instructions != nil {
		merged.Instructions = *input.Instructions
	}
	if input.Status != nil {
		merged.Status = *input.Status
	}
	if err := validateOrganizationAgent(merged); err != nil {
		return OrganizationAgent{}, err
	}
	updated, err := service.repository.UpdateOrganizationAgent(ctx, actor.OrganizationID, input)
	if err != nil {
		return OrganizationAgent{}, err
	}
	updated.CanManage = true
	return updated, nil
}

func (service *Service) Create(ctx context.Context, actor Actor, input CreateInput) (Space, error) {
	if err := validateActor(actor); err != nil {
		return Space{}, err
	}
	input = cleanCreate(input)
	if err := validateCreate(input); err != nil {
		return Space{}, err
	}
	access, err := service.resolveAccess(ctx, actor, input.TeamID)
	if err != nil {
		return Space{}, err
	}
	if !access.canManage {
		return Space{}, ErrForbidden
	}
	spaceID, err := newID("tsp_")
	if err != nil {
		return Space{}, err
	}
	organizationAgent, err := service.repository.EnsureOrganizationAgent(ctx, actor.OrganizationID, access.organizationName, actor.UserID)
	if err != nil {
		return Space{}, err
	}
	space, err := service.repository.Create(ctx, Space{
		ID: spaceID, OrganizationID: actor.OrganizationID, TeamID: input.TeamID,
		Name: input.Name, Description: input.Description, CreatedByUserID: actor.UserID,
		OrganizationAgentID: organizationAgent.ID, OrganizationAgent: organizationAgent,
		Status: StatusActive, Revision: 1,
	})
	if err != nil {
		return Space{}, err
	}
	return decorate(space, access), nil
}

func (service *Service) List(ctx context.Context, actor Actor) ([]Space, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	directory, err := service.directory.Load(ctx, actor.OrganizationID, actor.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("load organization directory: %w", err)
	}
	accessByTeam := directoryAccess(directory, actor.UserID)
	spaces, err := service.repository.List(ctx, actor.OrganizationID)
	if err != nil {
		return nil, err
	}
	visible := make([]Space, 0, len(spaces))
	for _, space := range spaces {
		access, ok := accessByTeam[space.TeamID]
		if ok && (access.isMember || access.canManage) {
			visible = append(visible, decorate(space, access))
		}
	}
	return visible, nil
}

func (service *Service) Get(ctx context.Context, actor Actor, id string) (Space, error) {
	if err := validateActor(actor); err != nil {
		return Space{}, err
	}
	space, err := service.repository.Get(ctx, actor.OrganizationID, strings.TrimSpace(id))
	if err != nil {
		return Space{}, err
	}
	access, err := service.resolveAccess(ctx, actor, space.TeamID)
	if err != nil {
		return Space{}, err
	}
	if !access.isMember && !access.canManage {
		return Space{}, ErrNotFound
	}
	return decorate(space, access), nil
}

func (service *Service) Update(ctx context.Context, actor Actor, id string, input UpdateInput) (Space, error) {
	if emptyUpdate(input) {
		return Space{}, fmt.Errorf("%w: send at least one field to change", ErrInvalid)
	}
	current, err := service.Get(ctx, actor, id)
	if err != nil {
		return Space{}, err
	}
	if !current.CanManage {
		return Space{}, ErrForbidden
	}
	input = cleanUpdate(input)
	merged := applyUpdate(current, input)
	if err := validateSpaceFields(merged.Name, merged.Description); err != nil {
		return Space{}, err
	}
	if input.Status != nil && *input.Status != StatusActive && *input.Status != StatusDisabled {
		return Space{}, fmt.Errorf("%w: status must be active or disabled", ErrInvalid)
	}
	if err := validateTarget(input); err != nil {
		return Space{}, err
	}
	updated, err := service.repository.Update(ctx, actor.OrganizationID, current.ID, actor.UserID, input)
	if err != nil {
		return Space{}, err
	}
	return decorate(updated, accessFrom(current)), nil
}

func (service *Service) Delete(ctx context.Context, actor Actor, id string) error {
	current, err := service.Get(ctx, actor, id)
	if err != nil {
		return err
	}
	if !current.CanManage {
		return ErrForbidden
	}
	return service.repository.Delete(ctx, actor.OrganizationID, current.ID)
}

func (service *Service) AddContribution(ctx context.Context, actor Actor, id string, input ContributionInput) (Space, error) {
	current, err := service.Get(ctx, actor, id)
	if err != nil {
		return Space{}, err
	}
	if !current.IsMember {
		return Space{}, fmt.Errorf("%w: only a current team member can contribute an agent", ErrForbidden)
	}
	if current.Status != StatusActive {
		return Space{}, ErrDisabled
	}
	input = cleanContribution(input)
	if err := validateContribution(input.AgentID, input.Role, input.DelegationMode); err != nil {
		return Space{}, err
	}
	updated, err := service.repository.AddContribution(ctx, actor, current.ID, input)
	if err != nil {
		return Space{}, err
	}
	access := accessFrom(current)
	access.isMember = true
	return decorate(updated, access), nil
}

func (service *Service) UpdateContribution(ctx context.Context, actor Actor, id, agentID string, input ContributionUpdate) (Space, error) {
	if input.Role == nil && input.DelegationMode == nil {
		return Space{}, fmt.Errorf("%w: send at least one contribution field to change", ErrInvalid)
	}
	current, err := service.Get(ctx, actor, id)
	if err != nil {
		return Space{}, err
	}
	contribution, ok := findContribution(current.Contributions, agentID)
	if !ok {
		return Space{}, ErrNotFound
	}
	if contribution.OwnerUserID != actor.UserID {
		return Space{}, fmt.Errorf("%w: only the specialist owner can change its standing authority", ErrForbidden)
	}
	input = cleanContributionUpdate(input)
	role, mode := contribution.Role, contribution.DelegationMode
	if input.Role != nil {
		role = *input.Role
	}
	if input.DelegationMode != nil {
		mode = *input.DelegationMode
	}
	if err := validateContribution(contribution.AgentID, role, mode); err != nil {
		return Space{}, err
	}
	updated, err := service.repository.UpdateContribution(ctx, actor, current.ID, contribution.AgentID, input)
	if err != nil {
		return Space{}, err
	}
	return decorate(updated, accessFrom(current)), nil
}

func (service *Service) DeleteContribution(ctx context.Context, actor Actor, id, agentID string) (Space, error) {
	current, err := service.Get(ctx, actor, id)
	if err != nil {
		return Space{}, err
	}
	contribution, ok := findContribution(current.Contributions, agentID)
	if !ok {
		return Space{}, ErrNotFound
	}
	if contribution.OwnerUserID != actor.UserID && !current.CanManage {
		return Space{}, ErrForbidden
	}
	updated, err := service.repository.DeleteContribution(ctx, actor.OrganizationID, current.ID, contribution.AgentID)
	if err != nil {
		return Space{}, err
	}
	return decorate(updated, accessFrom(current)), nil
}

func (service *Service) PublishEntry(ctx context.Context, actor Actor, id string, input PublishEntryInput) (Entry, error) {
	space, err := service.Get(ctx, actor, id)
	if err != nil {
		return Entry{}, err
	}
	if !space.IsMember {
		return Entry{}, fmt.Errorf("%w: only a current team member can publish shared work", ErrForbidden)
	}
	if space.Status != StatusActive || space.OrganizationAgent.Status != StatusActive {
		return Entry{}, ErrDisabled
	}
	input = cleanPublishEntry(input)
	if err := validatePublishEntry(input); err != nil {
		return Entry{}, err
	}
	authorName, allowed := publicationAuthor(space, actor.UserID, input.AuthorAgentID)
	if !allowed {
		return Entry{}, fmt.Errorf("%w: author_agent_id must be your primary whisperer or your contributed active specialist", ErrForbidden)
	}
	entryID, err := newID("tse_")
	if err != nil {
		return Entry{}, err
	}
	return service.repository.PublishEntry(ctx, Entry{
		ID: entryID, TeamspaceID: space.ID, OrganizationID: actor.OrganizationID,
		AuthorUserID: actor.UserID, AuthorAgentID: input.AuthorAgentID, AuthorAgentName: authorName,
		Kind: input.Kind, Title: input.Title, Summary: input.Summary,
		ToolsUsed: input.ToolsUsed, Artifacts: input.Artifacts,
	})
}

func (service *Service) ListEntries(ctx context.Context, actor Actor, id string, limit int) ([]Entry, error) {
	space, err := service.Get(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if !space.IsMember {
		return nil, fmt.Errorf("%w: only a current team member can read shared work", ErrForbidden)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	entries, err := service.repository.ListEntries(ctx, actor.OrganizationID, space.ID, limit)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []Entry{}
	}
	return entries, nil
}

type teamAccess struct {
	name                  string
	organizationName      string
	canManage             bool
	canManageOrganization bool
	isMember              bool
	whisperers            []Whisperer
}

func (service *Service) resolveAccess(ctx context.Context, actor Actor, teamID string) (teamAccess, error) {
	directory, err := service.directory.Load(ctx, actor.OrganizationID, actor.AccessToken)
	if err != nil {
		return teamAccess{}, fmt.Errorf("load organization directory: %w", err)
	}
	return accessIn(directory, actor.UserID, teamID)
}

// accessIn is what userID may do with one team, read from a directory
// already loaded: ErrNotFound when the team is not in it.
func accessIn(directory primaryagents.OrganizationDirectory, userID, teamID string) (teamAccess, error) {
	access, ok := directoryAccess(directory, userID)[teamID]
	if !ok {
		return teamAccess{}, ErrNotFound
	}
	return access, nil
}

func (service *Service) organizationAccess(ctx context.Context, actor Actor) (teamAccess, error) {
	if err := validateActor(actor); err != nil {
		return teamAccess{}, err
	}
	directory, err := service.directory.Load(ctx, actor.OrganizationID, actor.AccessToken)
	if err != nil {
		return teamAccess{}, fmt.Errorf("load organization directory: %w", err)
	}
	access := teamAccess{organizationName: directory.Organization.Name}
	for _, person := range directory.People {
		if person.UserID == actor.UserID {
			access.canManageOrganization = person.OrgRole == "owner" || person.OrgRole == "admin"
			return access, nil
		}
	}
	return teamAccess{}, ErrNotFound
}

func directoryAccess(directory primaryagents.OrganizationDirectory, userID string) map[string]teamAccess {
	memberOf := map[string]bool{}
	whisperersByTeam := make(map[string][]Whisperer, len(directory.Teams))
	for _, person := range directory.People {
		for _, team := range person.Teams {
			if person.UserID == userID {
				memberOf[team.ID] = true
			}
			for _, agent := range person.Agents {
				if agent.Kind != "primary" {
					continue
				}
				whisperersByTeam[team.ID] = append(whisperersByTeam[team.ID], Whisperer{
					UserID: person.UserID, UserDisplayName: person.DisplayName,
					AgentID: agent.ID, AgentName: agent.Name, Mode: "whisperer",
				})
				break
			}
		}
	}
	result := make(map[string]teamAccess, len(directory.Teams))
	canManageOrganization := false
	for _, person := range directory.People {
		if person.UserID == userID {
			canManageOrganization = person.OrgRole == "owner" || person.OrgRole == "admin"
			break
		}
	}
	for _, team := range directory.Teams {
		whisperers := whisperersByTeam[team.ID]
		slices.SortFunc(whisperers, func(left, right Whisperer) int {
			if comparison := strings.Compare(strings.ToLower(left.UserDisplayName), strings.ToLower(right.UserDisplayName)); comparison != 0 {
				return comparison
			}
			if comparison := strings.Compare(strings.ToLower(left.AgentName), strings.ToLower(right.AgentName)); comparison != 0 {
				return comparison
			}
			return strings.Compare(left.UserID, right.UserID)
		})
		result[team.ID] = teamAccess{name: team.Name, organizationName: directory.Organization.Name, canManage: team.CanManage,
			canManageOrganization: canManageOrganization, isMember: memberOf[team.ID], whisperers: whisperers}
	}
	return result
}

func decorate(space Space, access teamAccess) Space {
	space.TeamName = access.name
	space.CanManage = access.canManage
	space.IsMember = access.isMember
	space.OrganizationAgent.CanManage = access.canManageOrganization
	space.Whisperers = slices.Clone(access.whisperers)
	if space.Whisperers == nil {
		space.Whisperers = []Whisperer{}
	}
	if space.Contributions == nil {
		space.Contributions = []Contribution{}
	}
	return space
}

func accessFrom(space Space) teamAccess {
	return teamAccess{
		name: space.TeamName, canManage: space.CanManage, isMember: space.IsMember,
		canManageOrganization: space.OrganizationAgent.CanManage,
		whisperers:            slices.Clone(space.Whisperers),
	}
}

func cleanCreate(input CreateInput) CreateInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Name = cleanLine(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	return input
}

func cleanUpdate(input UpdateInput) UpdateInput {
	if input.Name != nil {
		value := cleanLine(*input.Name)
		input.Name = &value
	}
	if input.Description != nil {
		value := strings.TrimSpace(*input.Description)
		input.Description = &value
	}
	// An empty date or label clears it, as null does.
	if input.TargetDate.Value != nil {
		input.TargetDate.Value = nonEmpty(strings.TrimSpace(*input.TargetDate.Value))
	}
	if input.TargetLabel.Value != nil {
		input.TargetLabel.Value = nonEmpty(cleanLine(*input.TargetLabel.Value))
	}
	return input
}

func nonEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func cleanOrganizationAgentUpdate(input OrganizationAgentUpdate) OrganizationAgentUpdate {
	if input.Name != nil {
		value := cleanLine(*input.Name)
		input.Name = &value
	}
	if input.Description != nil {
		value := strings.TrimSpace(*input.Description)
		input.Description = &value
	}
	if input.Instructions != nil {
		value := strings.TrimSpace(*input.Instructions)
		input.Instructions = &value
	}
	return input
}

func cleanContribution(input ContributionInput) ContributionInput {
	input.AgentID = strings.TrimSpace(input.AgentID)
	input.Role = cleanLine(input.Role)
	return input
}

func cleanContributionUpdate(input ContributionUpdate) ContributionUpdate {
	if input.Role != nil {
		value := cleanLine(*input.Role)
		input.Role = &value
	}
	return input
}

func cleanPublishEntry(input PublishEntryInput) PublishEntryInput {
	input.AuthorAgentID = strings.TrimSpace(input.AuthorAgentID)
	input.Title = cleanLine(input.Title)
	input.Summary = strings.TrimSpace(input.Summary)
	tools := make([]string, 0, len(input.ToolsUsed))
	for _, tool := range input.ToolsUsed {
		tool = cleanLine(tool)
		if tool != "" && !slices.Contains(tools, tool) {
			tools = append(tools, tool)
		}
	}
	input.ToolsUsed = tools
	artifacts := make([]EntryArtifact, 0, len(input.Artifacts))
	for _, artifact := range input.Artifacts {
		artifact.Name = cleanLine(artifact.Name)
		artifact.URI = strings.TrimSpace(artifact.URI)
		artifact.MediaType = cleanLine(artifact.MediaType)
		artifacts = append(artifacts, artifact)
	}
	input.Artifacts = artifacts
	return input
}

func validateActor(actor Actor) error {
	if strings.TrimSpace(actor.OrganizationID) == "" || strings.TrimSpace(actor.UserID) == "" || strings.TrimSpace(actor.AccessToken) == "" {
		return fmt.Errorf("%w: organization, user and member token are required", ErrInvalid)
	}
	return nil
}

func validateCreate(input CreateInput) error {
	if input.TeamID == "" {
		return fmt.Errorf("%w: team_id is required", ErrInvalid)
	}
	return validateSpaceFields(input.Name, input.Description)
}

func validateSpaceFields(name, description string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: name is required", ErrInvalid)
	case utf8.RuneCountInString(name) > maxNameRunes:
		return fmt.Errorf("%w: name holds at most %d characters", ErrInvalid, maxNameRunes)
	case utf8.RuneCountInString(description) > maxDescriptionRunes:
		return fmt.Errorf("%w: description holds at most %d characters", ErrInvalid, maxDescriptionRunes)
	}
	return nil
}

// validateTarget checks a cleaned update's target: a calendar date written
// YYYY-MM-DD, and a short label.
func validateTarget(input UpdateInput) error {
	if date := input.TargetDate.Value; date != nil {
		if _, err := time.Parse(time.DateOnly, *date); err != nil {
			return fmt.Errorf("%w: target_date must be a date written YYYY-MM-DD", ErrInvalid)
		}
	}
	if label := input.TargetLabel.Value; label != nil && utf8.RuneCountInString(*label) > maxTargetLabelRunes {
		return fmt.Errorf("%w: target_label holds at most %d characters", ErrInvalid, maxTargetLabelRunes)
	}
	return nil
}

func validateOrganizationAgent(agent OrganizationAgent) error {
	switch {
	case agent.Name == "":
		return fmt.Errorf("%w: organization agent name is required", ErrInvalid)
	case utf8.RuneCountInString(agent.Name) > maxNameRunes:
		return fmt.Errorf("%w: organization agent name holds at most %d characters", ErrInvalid, maxNameRunes)
	case utf8.RuneCountInString(agent.Description) > maxDescriptionRunes:
		return fmt.Errorf("%w: organization agent description holds at most %d characters", ErrInvalid, maxDescriptionRunes)
	case strings.TrimSpace(agent.Instructions) == "":
		return fmt.Errorf("%w: organization agent instructions are required", ErrInvalid)
	case utf8.RuneCountInString(agent.Instructions) > maxInstructionsRunes:
		return fmt.Errorf("%w: organization agent instructions hold at most %d characters", ErrInvalid, maxInstructionsRunes)
	case agent.Status != StatusActive && agent.Status != StatusDisabled:
		return fmt.Errorf("%w: status must be active or disabled", ErrInvalid)
	}
	return nil
}

func validateContribution(agentID, role string, mode DelegationMode) error {
	switch {
	case strings.TrimSpace(agentID) == "":
		return fmt.Errorf("%w: agent_id is required", ErrInvalid)
	case role == "":
		return fmt.Errorf("%w: role is required", ErrInvalid)
	case utf8.RuneCountInString(role) > maxRoleRunes:
		return fmt.Errorf("%w: role holds at most %d characters", ErrInvalid, maxRoleRunes)
	case mode != DelegationRequest && mode != DelegationAutomatic:
		return fmt.Errorf("%w: delegation_mode must be request or automatic", ErrInvalid)
	}
	return nil
}

func validatePublishEntry(input PublishEntryInput) error {
	switch {
	case input.AuthorAgentID == "":
		return fmt.Errorf("%w: author_agent_id is required", ErrInvalid)
	case input.Kind != EntryUpdate && input.Kind != EntryResult && input.Kind != EntryKindArtifact && input.Kind != EntryHandoff && input.Kind != EntryDecision:
		return fmt.Errorf("%w: kind must be update, result, artifact, handoff, or decision", ErrInvalid)
	case input.Title == "":
		return fmt.Errorf("%w: title is required", ErrInvalid)
	case utf8.RuneCountInString(input.Title) > maxEntryTitleRunes:
		return fmt.Errorf("%w: title holds at most %d characters", ErrInvalid, maxEntryTitleRunes)
	case input.Summary == "":
		return fmt.Errorf("%w: summary is required", ErrInvalid)
	case utf8.RuneCountInString(input.Summary) > maxEntrySummaryRunes:
		return fmt.Errorf("%w: summary holds at most %d characters", ErrInvalid, maxEntrySummaryRunes)
	case len(input.ToolsUsed) > maxEntryTools:
		return fmt.Errorf("%w: tools_used holds at most %d entries", ErrInvalid, maxEntryTools)
	case len(input.Artifacts) > maxEntryArtifacts:
		return fmt.Errorf("%w: artifacts holds at most %d entries", ErrInvalid, maxEntryArtifacts)
	}
	for _, tool := range input.ToolsUsed {
		if utf8.RuneCountInString(tool) > maxEntryTitleRunes {
			return fmt.Errorf("%w: each tool name holds at most %d characters", ErrInvalid, maxEntryTitleRunes)
		}
	}
	for _, artifact := range input.Artifacts {
		if artifact.Name == "" || artifact.URI == "" {
			return fmt.Errorf("%w: every artifact requires name and uri", ErrInvalid)
		}
		if utf8.RuneCountInString(artifact.Name) > maxEntryTitleRunes || utf8.RuneCountInString(artifact.URI) > 4000 || utf8.RuneCountInString(artifact.MediaType) > maxEntryTitleRunes {
			return fmt.Errorf("%w: artifact fields exceed their maximum length", ErrInvalid)
		}
		if !safeArtifactURI(artifact.URI) {
			return fmt.Errorf("%w: artifact uri must be HTTPS or a first-party absolute path", ErrInvalid)
		}
	}
	return nil
}

func safeArtifactURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil {
		return false
	}
	if parsed.IsAbs() {
		return parsed.Scheme == "https" && parsed.Host != ""
	}
	return parsed.Host == "" && strings.HasPrefix(parsed.Path, "/") && !strings.HasPrefix(raw, "//")
}

// publicationAuthor is the name the space shows for an author agent that
// holds a seat of userID's there.
func publicationAuthor(space Space, userID, agentID string) (string, bool) {
	found, ok := seatOf(space, agentID)
	if !ok || found.OwnerUserID != userID {
		return "", false
	}
	return found.name, true
}

func emptyUpdate(input UpdateInput) bool {
	return input.Name == nil && input.Description == nil && input.Status == nil &&
		!input.TargetDate.Set && !input.TargetLabel.Set
}

// applyUpdate is space with input's fields changed, all but its status,
// which changeStatus moves.
func applyUpdate(space Space, input UpdateInput) Space {
	if input.Name != nil {
		space.Name = *input.Name
	}
	if input.Description != nil {
		space.Description = *input.Description
	}
	if input.TargetDate.Set {
		space.TargetDate = input.TargetDate.Value
	}
	if input.TargetLabel.Set {
		space.TargetLabel = input.TargetLabel.Value
	}
	return space
}

// changeStatus moves space to status as userID asked, at at. Disabling an
// active teamspace records who paused it and when; making it active again
// clears both. Asking for the status it already has changes nothing, so a
// second pause keeps the first one's facts.
func changeStatus(space Space, status Status, userID string, at time.Time) Space {
	if space.Status == status {
		return space
	}
	space.Status = status
	if status == StatusDisabled {
		space.PausedByUserID, space.PausedAt = &userID, &at
	} else {
		space.PausedByUserID, space.PausedAt = nil, nil
	}
	return space
}

func findContribution(contributions []Contribution, agentID string) (Contribution, bool) {
	index := slices.IndexFunc(contributions, func(value Contribution) bool { return value.AgentID == strings.TrimSpace(agentID) })
	if index < 0 {
		return Contribution{}, false
	}
	return contributions[index], true
}

func cleanLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func newID(prefix string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("create teamspace id: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(bytes), nil
}
